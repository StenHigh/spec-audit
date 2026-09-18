//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

type ExecutedTest struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type OutputDigest struct {
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

type Receipt struct {
	ID          string         `json:"id"`
	SnapshotID  string         `json:"snapshot_id"`
	StartedAt   string         `json:"started_at"`
	FinishedAt  string         `json:"finished_at"`
	Runtime     string         `json:"runtime"`
	Command     []string       `json:"command"`
	ExitCode    int            `json:"exit_code"`
	State       string         `json:"state"`
	Tests       []ExecutedTest `json:"tests"`
	Stdout      OutputDigest   `json:"stdout"`
	Stderr      OutputDigest   `json:"stderr"`
	Limitations []string       `json:"limitations"`
}

type boundedOutput struct {
	data      bytes.Buffer
	hash      hash.Hash
	size      int
	truncated bool
}

var goTestNameRE = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.hash == nil {
		b.hash = sha256.New()
	}
	b.hash.Write(p)
	b.size += len(p)
	room := maxResult - b.data.Len()
	if len(p) > room {
		b.truncated = true
	}
	b.data.Write(p[:min(room, len(p))])
	return len(p), nil
}

func (b *boundedOutput) summary() OutputDigest {
	sha := digest(nil)
	if b.hash != nil {
		sha = hex.EncodeToString(b.hash.Sum(nil))
	}
	return OutputDigest{sha, b.size, b.truncated}
}

type processResult struct {
	stdout, stderr boundedOutput
	exitCode       int
	incomplete     bool
}

func runProcess(ctx context.Context, cfg Config, stdin []byte, env []string, name string, args ...string) (processResult, error) {
	result := processResult{exitCode: -1}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdin = cfg.ProjectRoot, bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = &result.stdout, &result.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	slog.Debug("запуск разрешённого runtime", "program", name)
	if err := cmd.Start(); err != nil {
		return result, errors.New("не удалось запустить разрешённый runtime")
	}
	err := cmd.Wait()
	if cmd.ProcessState != nil {
		result.exitCode = cmd.ProcessState.ExitCode()
	}
	result.incomplete = ctx.Err() != nil || result.exitCode < 0 || result.stdout.truncated || result.stderr.truncated || errors.Is(err, exec.ErrWaitDelay)
	return result, nil
}

func validateRuntime(rt *Runtime) error {
	if rt.Kind == "" {
		rt.Kind = "none"
	}
	if !oneOf(rt.Kind, "none", "go", "docker-php") {
		return errors.New("runtime.kind: none/go/docker-php")
	}
	if rt.TimeoutSeconds == 0 {
		rt.TimeoutSeconds = 30
	}
	if rt.TimeoutSeconds < 1 || rt.TimeoutSeconds > 120 {
		return errors.New("timeout_seconds: 1–120")
	}
	if rt.Kind == "docker-php" && rt.Service == "" {
		rt.Service = "app"
	}
	if rt.Kind != "docker-php" && rt.Service != "" {
		return errors.New("service допустим только для docker-php")
	}
	if rt.Service != "" && (!slugRE.MatchString(rt.Service) || strings.HasPrefix(rt.Service, "-")) {
		return errors.New("недопустимый service")
	}
	if rt.Paths == nil {
		rt.Paths = []string{}
	}
	if rt.Tests == nil {
		rt.Tests = []string{}
	}
	if rt.Kind == "none" && (len(rt.Paths) != 0 || len(rt.Tests) != 0) {
		return errors.New("runtime none не принимает paths/tests")
	}
	if len(rt.Paths) > 64 || len(rt.Tests) > 256 {
		return errors.New("слишком большой выбор тестов")
	}
	for _, path := range rt.Paths {
		if path != "." && !localPath(path) || strings.HasPrefix(path, "-") || strings.Contains(path, "...") {
			return errors.New("runtime.paths: только локальные пути без флагов/шаблонов")
		}
	}
	for _, test := range rt.Tests {
		if test == "" || len(test) > 512 || strings.ContainsAny(test, "\x00\r\n") {
			return errors.New("недопустимое имя теста")
		}
		if rt.Kind == "go" && !goTestNameRE.MatchString(test) {
			return errors.New("Go tests: точные имена Test функций")
		}
	}
	sort.Strings(rt.Paths)
	sort.Strings(rt.Tests)
	return nil
}

// validateSDK нормализует блок sdk по контракту docs/php-sdk-contract.md.
func validateSDK(cfg *Config) error {
	sdk := cfg.SDK
	if sdk == nil {
		return nil
	}
	if cfg.Runtime.Kind != "docker-php" {
		return errors.New("sdk допустим только при runtime.kind docker-php")
	}
	if !oneOf(sdk.Profile, "php", "laravel") {
		return errors.New("sdk.profile: php/laravel")
	}
	if sdk.TimeoutSeconds == 0 {
		sdk.TimeoutSeconds = sdkDefaultTimeout
	}
	if sdk.TimeoutSeconds < 1 || sdk.TimeoutSeconds > sdkMaxTimeout {
		return fmt.Errorf("sdk.timeout_seconds: 1–%d", sdkMaxTimeout)
	}
	return nil
}

func executeTests(cfg Config, m Manifest, id string) (Receipt, error) {
	receipt := Receipt{ID: id, SnapshotID: m.SnapshotID, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Tests: []ExecutedTest{},
		Limitations: []string{"Процесс исполняет доверенный код проекта; выбранный snapshot не фиксирует всё состояние внешних сервисов и зависимостей.", "passed не доказывает релевантность assertion или соответствие продукта."}}
	if cfg.Runtime.Kind == "none" {
		return receipt, errors.New("исполнение отключено runtime.kind=none")
	}
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	for _, path := range cfg.Runtime.Paths {
		if path != "." {
			if err := noSymlinks(root, path); err != nil {
				return receipt, err
			}
		}
	}
	deadline := cfg.Runtime.TimeoutSeconds
	if cfg.Runtime.Kind == "docker-php" {
		deadline += 15
	} // Preflight/cleanup must not shorten the container-side test deadline.
	ctx, cancel := context.WithTimeout(processContext, time.Duration(deadline)*time.Second)
	defer cancel()
	var process processResult
	valid := false
	if cfg.Runtime.Kind == "go" {
		version, err := runProcess(ctx, cfg, nil, []string{"GOTOOLCHAIN=local", "GOWORK=off"}, "go", "version")
		if err != nil || version.exitCode != 0 || version.incomplete {
			return receipt, errors.New("не удалось получить версию Go runtime")
		}
		receipt.Runtime = strings.TrimSpace(version.stdout.data.String())
		if len(receipt.Runtime) > 256 {
			return receipt, errors.New("неверная версия Go runtime")
		}
		args := []string{"test", "-json", "-count=1"}
		if len(cfg.Runtime.Tests) > 0 {
			names := []string{}
			for _, name := range cfg.Runtime.Tests {
				names = append(names, regexp.QuoteMeta(name))
			}
			args = append(args, "-run", "^("+strings.Join(names, "|")+")$")
		}
		if len(cfg.Runtime.Paths) == 0 {
			args = append(args, ".")
		}
		for _, path := range cfg.Runtime.Paths {
			if path == "." {
				args = append(args, ".")
			} else {
				args = append(args, "./"+path)
			}
		}
		receipt.Command = append([]string{"go"}, args...)
		cache := filepath.Join(cfg.ReportsDir, "runtime", "go-build")
		modules := filepath.Join(cfg.ReportsDir, "runtime", "go-mod")
		temporary := filepath.Join(cfg.ReportsDir, "runtime", "go-tmp")
		if err := makeRuntimeDirs(cfg.ReportsDir); err != nil {
			return receipt, err
		}
		process, err = runProcess(ctx, cfg, nil, []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOCACHE=" + cache, "GOMODCACHE=" + modules, "GOTMPDIR=" + temporary, "TMPDIR=" + temporary}, "go", args...)
		if err != nil {
			return receipt, err
		}
		receipt.Tests, valid = goTestResults(process.stdout.data.Bytes())
	} else {
		process, receipt.Runtime, receipt.Command, receipt.Tests, valid, err = phpTests(ctx, cfg)
		if err != nil {
			return receipt, err
		}
	}
	receipt.ExitCode, receipt.Stdout, receipt.Stderr = process.exitCode, process.stdout.summary(), process.stderr.summary()
	receipt.State = "no_tests"
	passed, failed := 0, false
	for _, test := range receipt.Tests {
		if test.State == "passed" {
			passed++
		}
		if test.State == "failed" {
			failed = true
		}
	}
	if passed > 0 {
		receipt.State = "passed"
	}
	if failed || process.exitCode != 0 {
		receipt.State = "failed"
	}
	if !valid || process.incomplete {
		receipt.State = "incomplete"
	}
	current, snapErr := snapshot(cfg)
	if snapErr != nil || current.SnapshotID != m.SnapshotID {
		receipt.State = "stale"
	}
	receipt.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return receipt, nil
}

func makeRuntimeDirs(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, dir := range []string{"runtime", "runtime/go-build", "runtime/go-mod", "runtime/go-tmp"} {
		if err := root.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if err := noSymlinks(root, dir); err != nil {
			return err
		}
	}
	return nil
}

func lockPHPTests(reports *os.Root) (func(), error) {
	// RUN_ID starts with alphanumeric: this reserved name cannot equal a per-run lock.
	lock, err := reports.OpenFile("._phpunit.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	// ponytail: serialize PHPUnit within one reports_dir; shared DBs across other
	// configurations or external runners still need the operator's coordination.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }, nil
}

func goTestResults(data []byte) ([]ExecutedTest, bool) {
	results := []ExecutedTest{}
	started, done, packages := map[string]bool{}, map[string]string{}, map[string]bool{}
	dec := json.NewDecoder(bytes.NewReader(data))
	valid := true
	for {
		var event struct{ Action, Package, Test string }
		err := dec.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil || event.Package == "" {
			return results, false
		}
		key := event.Package + "::" + event.Test
		if event.Test == "" {
			if event.Action == "start" {
				packages[event.Package] = false
			}
			if oneOf(event.Action, "pass", "fail", "skip") {
				packages[event.Package] = true
			}
			continue
		}
		if event.Action == "run" {
			if started[key] {
				valid = false
			}
			started[key] = true
		}
		if oneOf(event.Action, "pass", "fail", "skip") {
			if !started[key] || done[key] != "" {
				valid = false
			}
			done[key] = map[string]string{"pass": "passed", "fail": "failed", "skip": "skipped"}[event.Action]
		}
	}
	for key := range started {
		if done[key] == "" {
			valid = false
		}
	}
	for key, state := range done {
		results = append(results, ExecutedTest{key, state})
	}
	for _, completed := range packages {
		if !completed {
			valid = false
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results, valid && len(packages) > 0
}

type junitSuite struct {
	XMLName xml.Name
	Suites  []junitSuite `xml:"testsuite"`
	Cases   []struct {
		Name      string    `xml:"name,attr"`
		Class     string    `xml:"class,attr"`
		Classname string    `xml:"classname,attr"`
		Failure   *struct{} `xml:"failure"`
		Error     *struct{} `xml:"error"`
		Skipped   *struct{} `xml:"skipped"`
	} `xml:"testcase"`
}

func junitResults(data []byte) ([]ExecutedTest, bool) {
	var suite junitSuite
	if err := xml.Unmarshal(data, &suite); err != nil || !oneOf(suite.XMLName.Local, "testsuite", "testsuites") {
		return []ExecutedTest{}, false
	}
	results, seen, valid := []ExecutedTest{}, map[string]bool{}, true
	var visit func(junitSuite)
	visit = func(s junitSuite) {
		for _, tc := range s.Cases {
			class := tc.Classname
			if class == "" {
				class = tc.Class
			}
			id := class + "::" + tc.Name
			if class == "" || tc.Name == "" || seen[id] {
				valid = false
			}
			seen[id] = true
			state := "passed"
			if tc.Skipped != nil {
				state = "skipped"
			}
			if tc.Error != nil || tc.Failure != nil {
				state = "failed"
			}
			results = append(results, ExecutedTest{id, state})
		}
		for _, child := range s.Suites {
			visit(child)
		}
	}
	visit(suite)
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results, valid
}

func runtimeFailure(operation string, process processResult, err error) error {
	if err != nil || process.exitCode != 0 || process.incomplete {
		return fmt.Errorf("%s: runtime не завершился корректно", operation)
	}
	return nil
}
