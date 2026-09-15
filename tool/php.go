package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed sdk.php
var sdk []byte

type SDKFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type SDKFact struct {
	File         string `json:"file"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Start        int    `json:"start"`
	Stop         int    `json:"stop"`
	Line         int    `json:"line"`
	Quote        string `json:"quote"`
	SourceSHA256 string `json:"source_sha256"`
}

type SDKEnvelope struct {
	Version      string `json:"version"`
	EvidenceKind string `json:"evidence_kind"`
	Runtime      struct {
		PHP                string `json:"php"`
		OS                 string `json:"os"`
		Arch               string `json:"arch"`
		ParserVersion      string `json:"parser_version"`
		ParserReference    string `json:"parser_reference"`
		ComposerLockSHA256 string `json:"composer_lock_sha256"`
		SDKSHA256          string `json:"sdk_sha256"`
	} `json:"runtime"`
	Files []SDKFile `json:"files"`
	Facts []SDKFact `json:"facts"`
}

func verifiedContainer(ctx context.Context, cfg Config) (string, error) {
	ids, err := runProcess(ctx, cfg, nil, nil, "docker", "compose", "ps", "--status", "running", "-q", cfg.Runtime.Service)
	if err := runtimeFailure("поиск контейнера", ids, err); err != nil {
		return "", err
	}
	id := strings.TrimSpace(ids.stdout.data.String())
	if !regexp.MustCompile(`^[a-f0-9]{12,64}$`).MatchString(id) {
		return "", errors.New("нужен ровно один работающий контейнер")
	}
	inspect, err := runProcess(ctx, cfg, nil, nil, "docker", "inspect", id, "--format", "{{json .}}")
	if err := runtimeFailure("проверка контейнера", inspect, err); err != nil {
		return "", err
	}
	var container struct {
		State  struct{ Running bool }
		Mounts []struct{ Source, Destination string }
	}
	if err := json.Unmarshal(inspect.stdout.data.Bytes(), &container); err != nil || !container.State.Running {
		return "", errors.New("контейнер не работает или inspect некорректен")
	}
	found := false
	for _, mount := range container.Mounts {
		actual, _ := filepath.EvalSymlinks(mount.Source)
		if actual == cfg.ProjectRoot && mount.Destination == "/var/www/html" {
			found = true
		}
		if strings.HasPrefix(mount.Destination, "/var/www/html/") {
			rel := strings.TrimPrefix(mount.Destination, "/var/www/html/")
			for _, group := range []Sources{cfg.Specs, cfg.Code, cfg.Tests} {
				for _, input := range group.Paths {
					if within(rel, input) || within(input, rel) {
						return "", errors.New("дополнительный mount подменяет выбранное входное дерево")
					}
				}
			}
		}
	}
	if !found {
		return "", errors.New("контейнер не монтирует выбранный project_root в /var/www/html")
	}
	return id, nil
}

func dockerPHP(ctx context.Context, cfg Config, id string, script []byte, request string) (processResult, error) {
	args := append(containerCommand(id), "timeout", "-s", "TERM", "-k", "1", strconv.Itoa(cfg.Runtime.TimeoutSeconds), "php", "-r", "eval(substr(stream_get_contents(STDIN), 5));", "--", request)
	return runProcess(ctx, cfg, script, nil, "docker", args...)
}

func containerCommand(id string) []string {
	return []string{"exec", "-i", "--workdir", "/var/www/html", id}
}

func phpFacts(cfg Config, m Manifest, paths []string) (any, error) {
	if cfg.Runtime.Kind != "docker-php" {
		return nil, errors.New("PHP facts требуют runtime.kind=docker-php")
	}
	if len(paths) == 0 || len(paths) > 64 {
		return nil, errors.New("PHP facts: 1–64 файла")
	}
	paths = append([]string{}, paths...)
	sort.Strings(paths)
	for i, path := range paths {
		file, ok := findSource(m, path)
		if !ok || !oneOf(file.Kind, "code", "tests") || filepath.Ext(path) != ".php" || (i > 0 && path == paths[i-1]) {
			return nil, errors.New("PHP facts: уникальные PHP-файлы выбранного snapshot")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Runtime.TimeoutSeconds+5)*time.Second)
	defer cancel()
	id, err := verifiedContainer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	request, _ := json.Marshal(map[string]any{"files": paths, "sdk_sha256": digest(sdk)})
	process, err := dockerPHP(ctx, cfg, id, sdk, string(request))
	if err := runtimeFailure("PHP SDK", process, err); err != nil {
		return nil, err
	}
	envelope, err := verifySDK(process.stdout.data.Bytes(), m, paths)
	if err != nil {
		return nil, err
	}
	current, err := snapshot(cfg)
	if err != nil || current.SnapshotID != m.SnapshotID {
		return nil, errors.New("снимок изменился во время SDK")
	}
	return map[string]any{"snapshot_id": m.SnapshotID, "container_id": id, "envelope": envelope}, nil
}

func verifySDK(body []byte, m Manifest, paths []string) (SDKEnvelope, error) {
	var envelope SDKEnvelope
	if err := strictJSON(body, &envelope); err != nil {
		return envelope, err
	}
	if err := requiredJSON(body, reflect.TypeOf(envelope)); err != nil {
		return envelope, errors.New("неполная схема SDK")
	}
	runtime := envelope.Runtime
	if envelope.Version != "sdk/2" || envelope.EvidenceKind != "syntax_only" || runtime.OS != "Linux" || runtime.PHP == "" || runtime.Arch == "" || runtime.ParserVersion == "" || runtime.SDKSHA256 != digest(sdk) || len(envelope.Files) != len(paths) {
		return envelope, errors.New("неверная схема/происхождение SDK")
	}
	root, err := os.OpenRoot(m.Config.ProjectRoot)
	if err != nil {
		return envelope, err
	}
	defer root.Close()
	lock, err := readRoot(root, "composer.lock", maxFile)
	if err != nil || digest(lock) != runtime.ComposerLockSHA256 {
		return envelope, errors.New("SDK использует другой composer.lock")
	}
	expected := map[string]bool{}
	for _, path := range paths {
		expected[path] = true
	}
	sources := map[string][]byte{}
	for _, file := range envelope.Files {
		if !expected[file.Path] || sources[file.Path] != nil {
			return envelope, errors.New("SDK вернул другое множество файлов")
		}
		source, err := readRoot(root, file.Path, maxFile)
		selected, ok := findSource(m, file.Path)
		if err != nil || !ok || file.SHA256 != selected.SHA256 || digest(source) != file.SHA256 || len(source) != file.Bytes {
			return envelope, errors.New("SDK source hash не совпадает")
		}
		sources[file.Path] = source
	}
	for _, fact := range envelope.Facts {
		source, ok := sources[fact.File]
		if !ok || fact.SourceSHA256 != digest(source) || !oneOf(fact.Kind, "Stmt_ClassMethod", "Expr_MethodCall", "Expr_StaticCall") || fact.Name == "" || fact.Start < 0 || fact.Stop <= fact.Start || fact.Stop > len(source) {
			return envelope, errors.New("неверный диапазон/тип SDK fact")
		}
		if !utf8.Valid(source[fact.Start:fact.Stop]) || string(source[fact.Start:fact.Stop]) != fact.Quote || bytes.Count(source[:fact.Start], []byte("\n"))+1 != fact.Line {
			return envelope, errors.New("цитата SDK не совпадает с исходником")
		}
	}
	return envelope, nil
}

func phpTests(ctx context.Context, cfg Config) (processResult, string, []string, []ExecutedTest, bool, error) {
	var empty processResult
	fail := func(err error) (processResult, string, []string, []ExecutedTest, bool, error) {
		return empty, "", nil, nil, false, err
	}
	if len(cfg.Runtime.Paths) == 0 {
		return fail(errors.New("PHPUnit требует явный runtime.paths"))
	}
	preflight, stopPreflight := context.WithTimeout(ctx, 5*time.Second)
	defer stopPreflight()
	id, err := verifiedContainer(preflight, cfg)
	if err != nil {
		return fail(err)
	}
	version, err := runProcess(preflight, cfg, nil, nil, "docker", append(containerCommand(id), "timeout", "5", "php", "-r", "echo PHP_VERSION, ' ', PHP_OS, ' ', php_uname('m');")...)
	if err := runtimeFailure("версия PHP", version, err); err != nil {
		return fail(err)
	}
	junit := "/tmp/spec-audit-" + rand.Text() + ".xml"
	args := append(containerCommand(id), "timeout", "-s", "TERM", "-k", "1", strconv.Itoa(cfg.Runtime.TimeoutSeconds), "php", "vendor/bin/phpunit", "--do-not-cache-result", "--no-coverage", "--log-junit", junit)
	if len(cfg.Runtime.Tests) > 0 {
		names := []string{}
		for _, name := range cfg.Runtime.Tests {
			names = append(names, strings.ReplaceAll(regexp.QuoteMeta(name), "~", "\\~"))
		}
		args = append(args, "--filter", "~^(?:"+strings.Join(names, "|")+")$~")
	}
	args = append(args, cfg.Runtime.Paths...)
	testCtx, stopTest := context.WithTimeout(ctx, time.Duration(cfg.Runtime.TimeoutSeconds+3)*time.Second)
	defer stopTest()
	process, err := runProcess(testCtx, cfg, nil, nil, "docker", args...)
	if err != nil {
		return fail(err)
	}
	if process.exitCode == 124 || process.exitCode == 137 {
		process.incomplete = true
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Only this generated /tmp file is read/unlinked; no project report/cache is requested.
	read, readErr := runProcess(cleanupCtx, cfg, nil, nil, "docker", append(containerCommand(id), "timeout", "4", "php", "-r", "if (!is_file($argv[1])) { exit(1); } try { echo file_get_contents($argv[1]); } finally { unlink($argv[1]); }", "--", junit)...)
	tests, valid := junitResults(read.stdout.data.Bytes())
	valid = valid && readErr == nil && read.exitCode == 0 && !read.incomplete
	return process, "PHP " + strings.TrimSpace(version.stdout.data.String()) + "; container=" + id, append([]string{"docker"}, args...), tests, valid, nil
}
