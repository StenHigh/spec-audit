package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
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

// Пределы типизированного SDK (docs/php-sdk-contract.md).
const (
	sdkDefaultTimeout = 300
	sdkMaxTimeout     = 900
	sdkMemoryLimit    = "2G"
	sdkServiceTimeout = 5 * time.Second
	maxFactLines      = 8
	maxTypedFacts     = 20000
	maxTypedTargets   = 16
	maxBasisFiles     = 4096
	maxSDKString      = 512
	maxReceiverType   = 1024
)

//go:embed sdk.php
var sdk []byte

//go:embed sdk-typed.php
var sdkTyped []byte

// typedNeon — обёртка конфигурации PHPStan для php-typed (docs/php-sdk-contract.md, «Neon-обёртка»).
// Плейсхолдеры подставляет renderNeon; скаляры обёртки переопределяют включённые файлы.
const typedNeon = `{{includes}}parameters:
    level: 0
    tmpDir: '{{tmpdir}}/tmp'
    reportUnmatchedIgnoredErrors: false
    parallel:
        maximumNumberOfProcesses: 1
    specAudit:
        profile: '{{profile}}'
        sdkSha256: '{{sdk_sha256}}'
parametersSchema:
    specAudit: structure([profile: string(), sdkSha256: string()])
services:
    -
        class: SpecAudit\FactCollector
        tags: [phpstan.collector]
    -
        class: SpecAudit\FactSink
        arguments:
            allConfigFiles: %allConfigFiles%
            bootstrapFiles: %bootstrapFiles%
            scanFiles: %scanFiles%
            scanDirectories: %scanDirectories%
            migrationPaths: {{migrations}}
            schemaPaths: {{schema}}
            profile: %specAudit.profile%
            sdkSha256: %specAudit.sdkSha256%
        tags: [phpstan.rules.rule]
`

const (
	containerMount  = "/var/www/html"
	larastanNeon    = containerMount + "/vendor/larastan/larastan/extension.neon"
	larastanBoot    = "vendor/larastan/larastan/bootstrap.php"
	typedVersion    = "sdk/3"
	typedEvidence   = "typed"
	typedIdentifier = "specAudit.envelope"
)

// typedSHA256 связывает envelope с точной редакцией экспортёра и обёртки.
var typedSHA256 = digest(append(append([]byte{}, sdkTyped...), typedNeon...))

// renderNeon строит обёртку для профиля; при extension-installer Larastan подключается
// установщиком, а повторный include PHPStan отклоняет как дубликат.
func renderNeon(workdir, profile string, extensionInstaller bool) []byte {
	includes, migrations, schema := "", "[]", "[]"
	if profile == "laravel" {
		migrations, schema = "%databaseMigrationsPath%", "%squashedMigrationsPath%"
		if !extensionInstaller {
			includes = "includes:\n    - " + larastanNeon + "\n"
		}
	}
	return []byte(strings.NewReplacer(
		"{{includes}}", includes,
		"{{tmpdir}}", workdir,
		"{{profile}}", profile,
		"{{sdk_sha256}}", typedSHA256,
		"{{migrations}}", migrations,
		"{{schema}}", schema,
	).Replace(typedNeon))
}

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

func containerCommand(id string, env ...string) []string {
	args := []string{"exec", "-i", "--workdir", containerMount}
	for _, pair := range env {
		args = append(args, "-e", pair)
	}
	return append(args, id)
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
	sources, err := sdkSources(root, m, paths, envelope.Files)
	if err != nil {
		return envelope, err
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

// sdkSources сверяет файлы envelope с запрошенным множеством и текущими исходниками snapshot.
func sdkSources(root *os.Root, m Manifest, paths []string, files []SDKFile) (map[string][]byte, error) {
	if len(files) != len(paths) {
		return nil, errors.New("SDK вернул другое множество файлов")
	}
	expected := map[string]bool{}
	for _, path := range paths {
		expected[path] = true
	}
	sources := map[string][]byte{}
	for _, file := range files {
		if !expected[file.Path] || sources[file.Path] != nil {
			return nil, errors.New("SDK вернул другое множество файлов")
		}
		source, err := readRoot(root, file.Path, maxFile)
		selected, ok := findSource(m, file.Path)
		if err != nil || !ok || file.SHA256 != selected.SHA256 || digest(source) != file.SHA256 || len(source) != file.Bytes {
			return nil, errors.New("SDK source hash не совпадает")
		}
		sources[file.Path] = source
	}
	return sources, nil
}

// Типизированные факты sdk/3 (docs/php-sdk-contract.md).
type TypedEnvelope struct {
	Version        string       `json:"version"`
	EvidenceKind   string       `json:"evidence_kind"`
	Profile        string       `json:"profile"`
	Runtime        TypedRuntime `json:"runtime"`
	Files          []SDKFile    `json:"files"`
	BootstrapFiles []string     `json:"bootstrap_files"`
	Basis          []SDKFile    `json:"basis"`
	Facts          []TypedFact  `json:"facts"`
}

type TypedRuntime struct {
	PHP                string `json:"php"`
	OS                 string `json:"os"`
	Arch               string `json:"arch"`
	ComposerLockSHA256 string `json:"composer_lock_sha256"`
	SDKSHA256          string `json:"sdk_sha256"`
}

type TypedFact struct {
	Citation     Citation      `json:"citation"`
	Syntax       string        `json:"syntax"`
	Name         string        `json:"name"`
	Origin       string        `json:"origin"`
	Resolution   string        `json:"resolution"`
	ReceiverType string        `json:"receiver_type"`
	Targets      []TypedTarget `json:"targets"`
}

type TypedTarget struct {
	Class     string `json:"class"`
	Method    string `json:"method"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Interface bool   `json:"interface"`
	Native    bool   `json:"native"`
}

// SDKRecord — запись импорта в state; факты остаются в артефакте.
type SDKRecord struct {
	Artifact        string `json:"artifact"`
	SHA256          string `json:"sha256"`
	PHPStanVersion  string `json:"phpstan_version"`
	LarastanVersion string `json:"larastan_version"`
	RecordedAt      string `json:"recorded_at"`
}

type typedResult struct {
	raw         []byte
	envelope    TypedEnvelope
	phpstan     string
	larastan    string
	diagnostics int
}

// laravelIsolationEnv отводит bootstrap Laravel от рабочих сервисов: любое обращение к ним падает сразу.
var laravelIsolationEnv = []string{
	"DB_CONNECTION=spec_audit_disabled", "DB_URL=", "DATABASE_URL=", "REDIS_URL=",
	"DB_HOST=127.0.0.1", "DB_PORT=1", "REDIS_HOST=127.0.0.1", "REDIS_PORT=1",
	"CACHE_STORE=array", "CACHE_DRIVER=array", "QUEUE_CONNECTION=sync", "SESSION_DRIVER=array",
	"MAIL_MAILER=array", "BROADCAST_CONNECTION=null", "BROADCAST_DRIVER=null", "LOG_CHANNEL=stderr",
}

const (
	preflightPHP = `/*spec-audit:preflight*/ if (!is_file('vendor/bin/phpstan')) exit(10); if ($argv[1] === 'laravel') { if (!is_file('vendor/larastan/larastan/extension.neon')) exit(11); if (!is_file('bootstrap/app.php')) exit(12); if (is_file('bootstrap/cache/config.php')) exit(13); } echo 'ok';`
	setupPHP     = `/*spec-audit:setup*/ @mkdir(dirname($argv[1]), 0700, true); if (file_put_contents($argv[1], stream_get_contents(STDIN)) === false) exit(20);`
	cleanupPHP   = `/*spec-audit:cleanup*/ $it = new RecursiveIteratorIterator(new RecursiveDirectoryIterator($argv[1], FilesystemIterator::SKIP_DOTS), RecursiveIteratorIterator::CHILD_FIRST); foreach ($it as $f) { $f->isDir() ? rmdir($f->getPathname()) : unlink($f->getPathname()); } rmdir($argv[1]);`
)

var preflightErrors = map[int]string{
	10: "PHPStan не установлен в контейнере",
	11: "Larastan не установлен для профиля laravel",
	12: "bootstrap/app.php отсутствует в контейнере",
	13: "закешированный config (bootstrap/cache/config.php) блокирует изоляцию",
}

// analyzerVersions читает версии анализаторов из composer.lock; иные major или отсутствие пакета — отказ.
func analyzerVersions(lock []byte, profile string) (phpstan, larastan string, extensionInstaller bool, err error) {
	var parsed struct {
		Packages    []struct{ Name, Version string } `json:"packages"`
		PackagesDev []struct{ Name, Version string } `json:"packages-dev"`
	}
	if err := json.Unmarshal(lock, &parsed); err != nil {
		return "", "", false, errors.New("composer.lock не разбирается")
	}
	versions := map[string]string{}
	for _, pkg := range append(parsed.Packages, parsed.PackagesDev...) {
		versions[pkg.Name] = pkg.Version
	}
	incompatible := errors.New("несовместимая версия анализатора")
	phpstan = versions["phpstan/phpstan"]
	if !regexp.MustCompile(`^v?2\.`).MatchString(phpstan) {
		return "", "", false, incompatible
	}
	if profile == "laravel" {
		larastan = versions["larastan/larastan"]
		if !regexp.MustCompile(`^v?3\.`).MatchString(larastan) {
			return "", "", false, incompatible
		}
	}
	_, extensionInstaller = versions["phpstan/extension-installer"]
	return phpstan, larastan, extensionInstaller, nil
}

func phpAtLeast(version string, major, minor int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	gotMajor, err1 := strconv.Atoi(parts[0])
	gotMinor, err2 := strconv.Atoi(leadingDigits(parts[1]))
	return err1 == nil && err2 == nil && (gotMajor > major || gotMajor == major && gotMinor >= minor)
}

func leadingDigits(value string) string {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	return value[:end]
}

// serviceExec запускает служебный шаг внутри контейнера с собственным коротким таймаутом.
func serviceExec(parent context.Context, cfg Config, id string, env []string, stdin []byte, script string, arg string) (processResult, error) {
	ctx, cancel := context.WithTimeout(parent, sdkServiceTimeout+2*time.Second)
	defer cancel()
	args := append(containerCommand(id, env...), "timeout", strconv.Itoa(int(sdkServiceTimeout/time.Second)), "php", "-r", script, "--", arg)
	return runProcess(ctx, cfg, stdin, nil, "docker", args...)
}

// phpTyped выполняет php-typed по контракту: preflight → setup → analyse → cleanup → повторный snapshot.
func phpTyped(cfg Config, m Manifest, paths []string) (typedResult, error) {
	var result typedResult
	if cfg.SDK == nil || cfg.Runtime.Kind != "docker-php" {
		return result, errors.New("php-typed требует блок sdk и runtime.kind=docker-php")
	}
	if len(paths) == 0 || len(paths) > 64 {
		return result, errors.New("php-typed: 1–64 файла")
	}
	paths = append([]string{}, paths...)
	sort.Strings(paths)
	for i, path := range paths {
		file, ok := findSource(m, path)
		if !ok || !oneOf(file.Kind, "code", "tests") || filepath.Ext(path) != ".php" || (i > 0 && path == paths[i-1]) {
			return result, errors.New("php-typed: уникальные PHP-файлы выбранного snapshot")
		}
	}
	profile := cfg.SDK.Profile
	lockFile, ok := findSource(m, "composer.lock")
	if !ok {
		return result, errors.New("composer.lock не входит в snapshot")
	}
	if profile == "laravel" {
		if _, ok := findSource(m, "bootstrap/app.php"); !ok {
			return result, errors.New("bootstrap/app.php не входит в snapshot")
		}
	}
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return result, err
	}
	defer root.Close()
	lock, err := readRoot(root, "composer.lock", maxFile)
	if err != nil || digest(lock) != lockFile.SHA256 {
		return result, errors.New("composer.lock изменился после snapshot")
	}
	phpstan, larastan, extensionInstaller, err := analyzerVersions(lock, profile)
	if err != nil {
		return result, err
	}
	result.phpstan, result.larastan = phpstan, larastan
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.SDK.TimeoutSeconds+20)*time.Second)
	defer cancel()
	id, err := verifiedContainer(ctx, cfg)
	if err != nil {
		return result, err
	}
	slog.Info("php-typed: контейнер проверен", "profile", profile)
	env := []string{"XDEBUG_MODE=off"}
	if profile == "laravel" {
		env = append(env, laravelIsolationEnv...)
	}
	slog.Debug("php-typed: env", "keys", len(env))
	preflight, err := serviceExec(ctx, cfg, id, env, nil, preflightPHP, profile)
	if err != nil || preflight.incomplete || preflight.exitCode != 0 {
		if message, known := preflightErrors[preflight.exitCode]; known && err == nil && !preflight.incomplete {
			return result, errors.New(message)
		}
		return result, errors.New("php-typed: preflight не завершился корректно")
	}
	slog.Info("php-typed: preflight пройден", "profile", profile)
	workdir := "/tmp/spec-audit-" + rand.Text()
	slog.Debug("php-typed: workspace", "suffix", strings.TrimPrefix(workdir, "/tmp/spec-audit-"))
	cleanup := func() {
		process, err := serviceExec(context.Background(), cfg, id, nil, nil, cleanupPHP, workdir) // очистка выполняется и после таймаута analyse
		if err != nil || process.incomplete || process.exitCode != 0 {
			slog.Warn("php-typed: очистка workspace не удалась", "suffix", strings.TrimPrefix(workdir, "/tmp/spec-audit-"))
		}
	}
	defer cleanup()
	for name, content := range map[string][]byte{"sdk-typed.php": sdkTyped, "phpstan.neon": renderNeon(workdir, profile, extensionInstaller)} {
		process, err := serviceExec(ctx, cfg, id, nil, content, setupPHP, workdir+"/"+name)
		if err != nil || process.incomplete || process.exitCode != 0 {
			return result, errors.New("php-typed: не удалось подготовить рабочую область")
		}
	}
	args := append(containerCommand(id, env...), "timeout", "-s", "TERM", "-k", "1", strconv.Itoa(cfg.SDK.TimeoutSeconds),
		"php", "-d", "display_errors=stderr", "vendor/bin/phpstan", "analyse", "--error-format=json", "--no-progress", "--no-interaction", "--no-ansi",
		"--memory-limit="+sdkMemoryLimit, "--autoload-file", workdir+"/sdk-typed.php", "-c", workdir+"/phpstan.neon", "--")
	started := time.Now()
	process, err := runProcess(ctx, cfg, nil, nil, "docker", append(args, paths...)...)
	if err != nil {
		return result, errors.New("экспорт SDK не завершён")
	}
	if process.stdout.truncated || process.stderr.truncated {
		return result, errors.New("вывод SDK превышает лимит; сократите выбор файлов")
	}
	if ctx.Err() != nil || process.exitCode < 0 || process.exitCode == 124 || process.exitCode == 137 {
		return result, errors.New("таймаут SDK")
	}
	if process.exitCode != 1 {
		return result, errors.New("экспорт SDK не завершён")
	}
	raw, diagnostics, err := extractEnvelope(process.stdout.data.Bytes())
	if err != nil {
		return result, err
	}
	slog.Info("php-typed: анализ завершён", "files", len(paths), "exit_code", process.exitCode, "duration_ms", time.Since(started).Milliseconds(), "stdout_bytes", process.stdout.size, "diagnostics", diagnostics)
	envelope, err := verifyTyped(raw, m, cfg, paths, lock)
	if err != nil {
		return result, err
	}
	current, err := snapshot(cfg)
	if err != nil || current.SnapshotID != m.SnapshotID {
		return result, errors.New("снимок изменился во время SDK")
	}
	result.raw, result.envelope, result.diagnostics = raw, envelope, diagnostics
	return result, nil
}

// extractEnvelope находит сообщение экспортёра в JSON PHPStan и считает прочую диагностику.
func extractEnvelope(stdout []byte) ([]byte, int, error) {
	var output struct {
		Files map[string]struct {
			Messages []struct {
				Message    string `json:"message"`
				Identifier string `json:"identifier"`
			} `json:"messages"`
		} `json:"files"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(stdout, &output); err != nil || len(output.Errors) != 0 {
		return nil, 0, errors.New("экспорт SDK не завершён")
	}
	var raw []byte
	diagnostics := 0
	for _, file := range output.Files {
		for _, message := range file.Messages {
			if message.Identifier == typedIdentifier {
				if raw != nil {
					return nil, 0, errors.New("экспорт SDK не завершён")
				}
				raw = []byte(message.Message)
				continue
			}
			diagnostics++
		}
	}
	if raw == nil {
		return nil, 0, errors.New("экспорт SDK не завершён")
	}
	return raw, diagnostics, nil
}

// verifyTyped проверяет envelope sdk/3 против manifest, исходников и инвариантов контракта.
func verifyTyped(body []byte, m Manifest, cfg Config, paths []string, lock []byte) (TypedEnvelope, error) {
	var envelope TypedEnvelope
	if err := strictJSON(body, &envelope); err != nil {
		return envelope, err
	}
	if err := requiredJSON(body, reflect.TypeOf(envelope)); err != nil {
		return envelope, errors.New("неполная схема SDK")
	}
	runtime := envelope.Runtime
	if envelope.Version != typedVersion || envelope.EvidenceKind != typedEvidence || cfg.SDK == nil || envelope.Profile != cfg.SDK.Profile || runtime.OS != "Linux" || runtime.Arch == "" || !phpAtLeast(runtime.PHP, 8, 2) || runtime.SDKSHA256 != typedSHA256 {
		return envelope, errors.New("неверная схема/происхождение SDK")
	}
	if runtime.ComposerLockSHA256 != digest(lock) {
		return envelope, errors.New("SDK использует другой composer.lock")
	}
	root, err := os.OpenRoot(m.Config.ProjectRoot)
	if err != nil {
		return envelope, err
	}
	defer root.Close()
	analysed := map[string]bool{}
	for _, file := range envelope.Files {
		analysed[file.Path] = true
	}
	for _, path := range paths {
		if !analysed[path] {
			return envelope, errors.New("SDK не проанализировал часть выбранных файлов")
		}
	}
	sources, err := sdkSources(root, m, paths, envelope.Files)
	if err != nil {
		return envelope, err
	}
	hasLarastan := false
	for _, path := range envelope.BootstrapFiles {
		if !localPath(path) || !strings.HasPrefix(path, "vendor/") {
			return envelope, errors.New("конфигурация подключает bootstrap вне профиля")
		}
		hasLarastan = hasLarastan || path == larastanBoot
	}
	if (envelope.Profile == "php" && len(envelope.BootstrapFiles) != 0) || (envelope.Profile == "laravel" && !hasLarastan) {
		return envelope, errors.New("конфигурация подключает bootstrap вне профиля")
	}
	if len(envelope.Basis) > maxBasisFiles {
		return envelope, errors.New("вывод SDK превышает лимит; сократите выбор файлов")
	}
	for _, file := range envelope.Basis {
		if !localPath(file.Path) || !validDigest(file.SHA256) || file.Bytes < 0 {
			return envelope, errors.New("основание анализа вне snapshot; добавьте файлы в code.paths")
		}
		if strings.HasPrefix(file.Path, "vendor/") {
			continue
		}
		selected, ok := findSource(m, file.Path)
		if !ok || selected.SHA256 != file.SHA256 || selected.Bytes != file.Bytes {
			return envelope, errors.New("основание анализа вне snapshot; добавьте файлы в code.paths")
		}
	}
	if len(envelope.Facts) > maxTypedFacts {
		return envelope, errors.New("вывод SDK превышает лимит; сократите выбор файлов")
	}
	for _, fact := range envelope.Facts {
		if err := verifyFact(fact, sources); err != nil {
			return envelope, err
		}
	}
	return envelope, nil
}

func verifyFact(fact TypedFact, sources map[string][]byte) error {
	c := fact.Citation
	source, ok := sources[c.Path]
	if !ok || c.LineStart < 1 || c.LineEnd < c.LineStart || c.LineEnd-c.LineStart >= maxFactLines {
		return errors.New("неверный диапазон цитаты SDK")
	}
	quote, err := lineQuote(source, c.LineStart, c.LineEnd)
	if err != nil || quote != c.Quote {
		return errors.New("цитата SDK не совпадает с исходником")
	}
	if !oneOf(fact.Syntax, "Stmt_ClassMethod", "Expr_MethodCall", "Expr_StaticCall", "Expr_NullsafeMethodCall") || !oneOf(fact.Origin, "phpstan", "larastan") || !oneOf(fact.Resolution, "declared", "resolved", "ambiguous", "unresolved", "virtual", "dynamic") {
		return errors.New("неверный тип SDK fact")
	}
	if fact.Name == "" || len(fact.Name) > maxSDKString || len(fact.ReceiverType) > maxReceiverType || !utf8.ValidString(fact.Name) || !utf8.ValidString(fact.ReceiverType) || (fact.Resolution == "dynamic") != (fact.Name == "{dynamic}") || (fact.ReceiverType == "") != (fact.Resolution == "declared") {
		return errors.New("неверное имя или тип получателя SDK fact")
	}
	if len(fact.Targets) > maxTypedTargets {
		return errors.New("вывод SDK превышает лимит; сократите выбор файлов")
	}
	located, virtual := 0, true
	for _, target := range fact.Targets {
		if target.Class == "" || target.Method == "" || len(target.Class) > maxSDKString || len(target.Method) > maxSDKString || len(target.File) > maxSDKString || target.Line < 0 || (target.Line > 0) != (target.File != "") {
			return errors.New("неверная цель SDK fact")
		}
		outside := strings.HasPrefix(target.File, "/") && !strings.HasPrefix(target.File, containerMount+"/") && !strings.Contains(target.File, "..")
		if target.File != "" && !localPath(target.File) && !outside {
			return errors.New("неверная цель SDK fact")
		}
		if target.File != "" || target.Native {
			virtual = false
		}
		if target.File != "" && target.Line > 0 || target.Native {
			located++
		}
	}
	count := len(fact.Targets)
	valid := true
	switch fact.Resolution {
	case "dynamic", "unresolved":
		valid = count == 0
	case "virtual":
		valid = count >= 1 && virtual
	case "resolved":
		valid = count == 1 && located == 1
	case "ambiguous":
		valid = count >= 2
	case "declared":
		valid = count <= 1
	}
	if !valid {
		return errors.New("цели SDK fact противоречат его resolution")
	}
	return nil
}

var sdkArtifactRE = regexp.MustCompile(`^sdk-typed-[A-Za-z0-9]+\.json$`)

// verifySDKRecords сверяет записи state с артефактами, как raw ролей: расхождение блокирует чтение run.
func verifySDKRecords(run *os.Root, records []SDKRecord) error {
	for _, record := range records {
		if !sdkArtifactRE.MatchString(record.Artifact) || !validDigest(record.SHA256) {
			return errors.New("повреждена запись SDK")
		}
		raw, err := readRoot(run, record.Artifact, maxResult)
		if err != nil || digest(raw) != record.SHA256 {
			return errors.New("повреждена запись SDK")
		}
	}
	return nil
}

// sdkRecordsFor отдаёт хосту записи с абсолютными путями артефактов.
func sdkRecordsFor(reportsDir, runID string, records []SDKRecord) []SDKRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]SDKRecord, len(records))
	for i, record := range records {
		out[i] = record
		out[i].Artifact = filepath.Join(reportsDir, runID, record.Artifact)
	}
	return out
}
