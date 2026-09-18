//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicFailurePreservesTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := atomicWrite(root, "probe.json", []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	// Write+execute permits create/rename through the open Root, but not a new directory read.
	if err := os.Chmod(directory, 0300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
	writeErr := atomicWrite(root, "probe.json", []byte("after"), 0600)
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	data := readFixture(t, filepath.Join(directory, "probe.json"))
	if writeErr != nil && string(data) != "before" {
		t.Fatalf("reported refusal after publication: err=%v target=%q", writeErr, data)
	}
	if writeErr == nil && string(data) != "after" {
		t.Fatal("success without publication")
	}
}

func TestPublishedSyncWarning(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writeFixture(t, filepath.Join(directory, "prepared"), []byte("private-payload"))
	dir, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(logger)
	if err := publishPreparedFile(root, "prepared", "published", dir); err != nil {
		t.Fatalf("false refusal after rename: %v", err)
	}
	if string(readFixture(t, filepath.Join(directory, "published"))) != "private-payload" {
		t.Fatal("publication lost")
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) || strings.Contains(logs.String(), "private-payload") {
		t.Fatal("missing or unsafe durability warning")
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	for _, name := range []string{"rules.md", "source.go", "source_test.go", "go.mod"} {
		writeFixture(t, filepath.Join(base, "source", name), readFixture(t, "../acceptance/corpus/"+name))
	}
	config := filepath.Join(base, "config.yaml")
	writeFixture(t, config, []byte("version: 1\nproject_root: source\nspecs: {paths: [rules.md]}\ncode: {paths: [source.go, go.mod]}\ntests: {paths: [source_test.go]}\nreports_dir: runs\nruntime: {kind: none}\nscopes: []\n"))
	return config, base
}

func runOK(t *testing.T, args ...string) any {
	t.Helper()
	value, err := execute(args)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return value
}

func runFail(t *testing.T, args ...string) {
	t.Helper()
	if _, err := execute(args); err == nil {
		t.Fatalf("должно быть отклонено: %v", args)
	}
}

func sampleResult(t *testing.T, task Task, source string) Result {
	t.Helper()
	// Read the frozen expected states, not guessed mappings from the new implementation.
	var raw struct {
		Requirements []map[string]string `json:"requirements"`
	}
	if err := json.Unmarshal(readFixture(t, "../acceptance/expected.json"), &raw); err != nil {
		t.Fatal(err)
	}
	whole := func(path string) Citation {
		data := readFixture(t, filepath.Join(source, path))
		end := len(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"))
		quote, err := lineQuote(data, 1, end)
		if err != nil {
			t.Fatal(err)
		}
		return Citation{path, 1, end, quote}
	}
	r := Result{TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope,
		Summary: "<script>alert(1)</script>", Assessments: []Assessment{}, Limitations: []string{"Смысл задан frozen gold, это проверка механики, не интеллект агента."}}
	for _, req := range task.Requirements {
		for _, expected := range raw.Requirements {
			if expected["requirement_id"] != req.ID {
				continue
			}
			a := Assessment{RequirementID: req.ID, Specification: expected["specification"], Implementation: expected["implementation"], Assertion: expected["assertion"],
				Statement: "<img src=x onerror=alert(2)>", Spec: []Citation{req.Source}, Code: []Citation{whole("source.go")}, Tests: []TestCitation{}, Limitations: []string{}}
			if expected["test_id"] != "" {
				a.Tests = append(a.Tests, TestCitation{expected["test_id"], whole("source_test.go")})
			}
			r.Assessments = append(r.Assessments, a)
		}
	}
	return r
}

// The original pilot integration guard is extended, not replaced by semantic claims.
func TestContract(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--audit-child" {
			value, err := execute(os.Args[i+1:])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			_ = json.NewEncoder(os.Stdout).Encode(value)
			os.Exit(0)
		}
	}
	config, base := fixture(t)
	originalConfig := readFixture(t, config)
	source, reports := filepath.Join(base, "source"), filepath.Join(base, "runs", "normal")
	resultPath := func(name string, result Result) string {
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(base, name+".json")
		writeFixture(t, path, body)
		return path
	}
	batch := runOK(t, "prepare", config, "normal").(TaskBatch)
	if len(batch.Tasks) != 2 || len(batch.Tasks[0].Requirements) != 5 {
		t.Fatal("потеряны нормы/роли")
	}
	manifest := readFixture(t, filepath.Join(reports, "manifest.json"))
	runFail(t, "prepare", config, "normal")
	getReport := func() Report {
		runOK(t, "report", config, "normal")
		var report Report
		if err := json.Unmarshal(readFixture(t, filepath.Join(reports, "report.json")), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	if report := getReport(); report.DeliveryComplete || len(report.Requirements) != 5 || !report.HostReconciliationRequired {
		t.Fatal("ложная полнота")
	}
	first := sampleResult(t, batch.Tasks[0], source)
	firstPath := resultPath("first", first)
	runSnapshot := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		if err := filepath.WalkDir(reports, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				out[p] = digest(readFixture(t, p))
			} else {
				out[p] = "dir"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	t.Run("validate_ok", func(t *testing.T) {
		getReport() // report.json must survive validate untouched
		before := runSnapshot()
		got := runOK(t, "validate", config, "normal", first.TaskID, firstPath).(map[string]any)
		if got["valid"] != true || got["already_submitted"] != false || got["task_id"] != first.TaskID || got["attempt"] != first.Attempt {
			t.Fatalf("%v", got)
		}
		if !reflect.DeepEqual(before, runSnapshot()) {
			t.Fatal("validate изменил каталог run")
		}
	})
	runOK(t, "submit", config, "normal", first.TaskID, firstPath)
	t.Run("validate_submitted", func(t *testing.T) {
		before := runSnapshot()
		got := runOK(t, "validate", config, "normal", first.TaskID, firstPath).(map[string]any)
		if got["valid"] != true || got["already_submitted"] != true {
			t.Fatalf("%v", got)
		}
		conflict := first
		conflict.Summary = "other"
		// validate does not judge duplicates or conflicts; only submit does.
		if got := runOK(t, "validate", config, "normal", first.TaskID, resultPath("validate-conflict", conflict)).(map[string]any); got["valid"] != true {
			t.Fatalf("%v", got)
		}
		if !reflect.DeepEqual(before, runSnapshot()) {
			t.Fatal("validate изменил каталог run")
		}
	})
	if getReport().DeliveryComplete {
		t.Fatal("нет второй роли")
	}
	if runOK(t, "submit", config, "normal", first.TaskID, firstPath).(map[string]any)["duplicate"] != true {
		t.Fatal("нет идемпотентности")
	}
	conflict := first
	conflict.Summary = "other"
	runFail(t, "submit", config, "normal", first.TaskID, resultPath("conflict", conflict))
	t.Run("reject_invalid_results", func(t *testing.T) {
		task := batch.Tasks[1]
		for name, mutate := range map[string]func(*Result){
			"bad_quote":             func(r *Result) { r.Assessments[0].Code[0].Quote = "invented" },
			"bad_line":              func(r *Result) { r.Assessments[0].Spec[0].LineEnd = 999 },
			"traversal":             func(r *Result) { r.Assessments[0].Code[0].Path = "../secret" },
			"absolute":              func(r *Result) { r.Assessments[0].Code[0].Path = "/etc/passwd" },
			"wrong_group":           func(r *Result) { r.Assessments[0].Code[0] = r.Assessments[0].Tests[0].Citation },
			"enum":                  func(r *Result) { r.Assessments[0].Implementation = "PASS" },
			"empty_statement":       func(r *Result) { r.Assessments[0].Statement = " " },
			"attempt":               func(r *Result) { r.Attempt++ },
			"task":                  func(r *Result) { r.TaskID = "other" },
			"role":                  func(r *Result) { r.Role = "mapper" },
			"scope":                 func(r *Result) { r.Scope = "other" },
			"snapshot":              func(r *Result) { r.SnapshotID = "old" },
			"no_code":               func(r *Result) { r.Assessments[0].Code = []Citation{} },
			"no_spec":               func(r *Result) { r.Assessments[0].Spec = []Citation{} },
			"no_assertion_source":   func(r *Result) { r.Assessments[0].Tests = []TestCitation{} },
			"missing_requirement":   func(r *Result) { r.Assessments = r.Assessments[1:] },
			"duplicate_requirement": func(r *Result) { r.Assessments[0] = r.Assessments[1] },
			"foreign_requirement":   func(r *Result) { r.Assessments[0].RequirementID = "REQ-OTHER-001" },
			"foreign_real_quote":    func(r *Result) { r.Assessments[0].Spec = r.Assessments[1].Spec },
		} {
			t.Run(name, func(t *testing.T) {
				r := sampleResult(t, task, source)
				mutate(&r)
				path := resultPath(name, r)
				before := runSnapshot()
				_, errValidate := execute([]string{"validate", config, "normal", task.TaskID, path})
				_, errSubmit := execute([]string{"submit", config, "normal", task.TaskID, path})
				if errValidate == nil || errSubmit == nil || errValidate.Error() != errSubmit.Error() {
					t.Fatalf("validate=%v submit=%v", errValidate, errSubmit)
				}
				if oneOf(name, "enum", "empty_statement", "duplicate_requirement") && !strings.Contains(errValidate.Error(), "REQ-DEMO-") {
					t.Fatalf("ошибка без ID нормы: %v", errValidate)
				}
				if name == "foreign_requirement" && strings.Contains(errValidate.Error(), "REQ-OTHER") {
					t.Fatalf("ошибка раскрывает чужой requirement_id: %v", errValidate)
				}
				if strings.Contains(errValidate.Error(), "invented") {
					t.Fatalf("ошибка раскрывает содержимое ответа: %v", errValidate)
				}
				if !reflect.DeepEqual(before, runSnapshot()) {
					t.Fatal("отказ изменил каталог run")
				}
			})
		}
		valid, _ := json.Marshal(sampleResult(t, task, source))
		for name, body := range map[string][]byte{
			"unknown":   bytes.Replace(valid, []byte(`"summary":`), []byte(`"unknown":0,"summary":`), 1),
			"duplicate": bytes.Replace(valid, []byte(`"task_id":`), []byte(`"task_id":"x","task_id":`), 1),
			"case":      bytes.Replace(valid, []byte(`"task_id":`), []byte(`"TASK_ID":`), 1),
			"missing":   bytes.Replace(valid, []byte(`"assertion":"relevant",`), nil, 1),
			"null":      bytes.Replace(valid, []byte(`"assertion":"relevant"`), []byte(`"assertion":null`), 1),
			"multiple":  append(append([]byte{}, valid...), []byte("{}")...),
			"malformed": valid[:len(valid)-1],
			"utf8":      []byte{255},
			"oversized": bytes.Repeat([]byte(" "), maxResult+1),
		} {
			path := filepath.Join(base, name+".json")
			writeFixture(t, path, body)
			_, errValidate := execute([]string{"validate", config, "normal", task.TaskID, path})
			_, errSubmit := execute([]string{"submit", config, "normal", task.TaskID, path})
			if errValidate == nil || errSubmit == nil || errValidate.Error() != errSubmit.Error() {
				t.Fatalf("%s: validate=%v submit=%v", name, errValidate, errSubmit)
			}
		}
		runFail(t, "validate", config, "normal", "unknown-task", resultPath("validate-unknown", sampleResult(t, task, source)))
	})
	second := sampleResult(t, batch.Tasks[1], source)
	runOK(t, "submit", config, "normal", second.TaskID, resultPath("second", second))
	report := getReport()
	if !report.DeliveryComplete || report.Requirements[1].Roles[0].Assessment.Assertion != "weak" || report.Requirements[2].Roles[0].Assessment.Implementation != "contradicted" || report.Requirements[3].Roles[0].Assessment.Implementation != "unknown" {
		t.Fatal("потеряны раздельные состояния")
	}
	if report.Requirements[0].Roles[0].Executions[0].State != "not_recorded" {
		t.Fatal("выдумано исполнение")
	}
	html := string(readFixture(t, filepath.Join(reports, "report.html")))
	if strings.Contains(html, "<img src=x") || !strings.Contains(html, "&lt;img") {
		t.Fatal("HTML injection")
	}
	task := runOK(t, "retry", config, "normal", first.TaskID).(Task)
	if task.Attempt != 2 || getReport().DeliveryComplete {
		t.Fatal("неверный retry")
	}
	if _, err := os.Stat(filepath.Join(reports, "results", first.TaskID+"-attempt-1.json")); err != nil {
		t.Fatal("потеряна история")
	}
	runFail(t, "submit", config, "normal", first.TaskID, firstPath)
	retryPath := resultPath("retry", sampleResult(t, task, source))
	runOK(t, "submit", config, "normal", first.TaskID, retryPath)
	if !bytes.Equal(manifest, readFixture(t, filepath.Join(reports, "manifest.json"))) {
		t.Fatal("переписан manifest")
	}
	t.Run("stale_is_readable", func(t *testing.T) {
		path := filepath.Join(source, "source.go")
		prior := readFixture(t, path)
		writeFixture(t, path, append(prior, []byte("\n// changed\n")...))
		if getReport().Freshness != "stale" {
			t.Fatal("скрыто устаревание")
		}
		runFail(t, "submit", config, "normal", first.TaskID, firstPath)
		if _, err := execute([]string{"validate", config, "normal", first.TaskID, retryPath}); err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("validate на stale run: %v", err)
		}
		runFail(t, "test", config, "normal", "stale-run")
		writeFixture(t, path, prior)
		if got := runOK(t, "validate", config, "normal", first.TaskID, retryPath).(map[string]any); got["valid"] != true || got["already_submitted"] != true {
			t.Fatalf("validate после восстановления: %v", got)
		}
		writeFixture(t, config, append(originalConfig, []byte("# physical comment\n")...))
		if getReport().Freshness != "fresh" {
			t.Fatal("комментарий изменил identity")
		}
		writeFixture(t, config, originalConfig)
		link := filepath.Join(source, "link.go")
		if err := os.Symlink("/etc/passwd", link); err != nil {
			t.Fatal(err)
		}
		bad := strings.Replace(string(originalConfig), "[source.go, go.mod]", "[source.go, go.mod, link.go]", 1)
		writeFixture(t, config, []byte(bad))
		runFail(t, "prepare", config, "linked")
		writeFixture(t, config, originalConfig)
	})
	t.Run("concurrent_processes", func(t *testing.T) {
		parallel := runOK(t, "prepare", config, "parallel").(TaskBatch)
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		var commands []*exec.Cmd
		var outputs []*bytes.Buffer
		for _, task := range parallel.Tasks {
			path := resultPath(task.TaskID, sampleResult(t, task, source))
			cmd := exec.Command(exe, "-test.run=^TestContract$", "--", "--audit-child", "submit", config, "parallel", task.TaskID, path)
			output := new(bytes.Buffer)
			cmd.Stdout, cmd.Stderr = output, output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			commands = append(commands, cmd)
			outputs = append(outputs, output)
		}
		for i, cmd := range commands {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("submit: %v: %s", err, outputs[i])
			}
		}
		if status := runOK(t, "status", config, "parallel").(Status); status.Submitted != 2 || !status.DeliveryComplete {
			t.Fatal("потеряно обновление")
		}
	})
}

func TestFrozenAcceptance(t *testing.T) {
	for _, line := range strings.Split(strings.TrimSpace(string(readFixture(t, "../acceptance/baseline.sha256"))), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || digest(readFixture(t, "../"+parts[1])) != parts[0] {
			t.Fatal("приёмка изменена после baseline", line)
		}
	}
}

func TestReportProjection(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "large").(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		result.Summary = "summary-regression-" + strings.Repeat("x", 7<<19)
		body, _ := json.Marshal(result)
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, body)
		runOK(t, "submit", config, "large", task.TaskID, path)
	}
	runOK(t, "report", config, "large")
	body := readFixture(t, filepath.Join(base, "runs/large/report.json"))
	html := readFixture(t, filepath.Join(base, "runs/large/report.html"))
	if len(body) > 10<<20 || len(html) > 10<<20 || bytes.Count(html, []byte("summary-regression-")) != 2 {
		t.Fatal("итоги ролей размножены по числу требований")
	}
	var m Manifest
	var state State
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/large/manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/large/state.json")), &state); err != nil {
		t.Fatal(err)
	}
	id := state.Entries[0].Result.Assessments[0].Tests[0].TestID
	// In-memory projection only: these synthetic receipts are not published as observed runs.
	state.Executions = []Receipt{{ID: "first", SnapshotID: m.SnapshotID, State: "failed", Tests: []ExecutedTest{{id, "failed"}}},
		{ID: "second", SnapshotID: m.SnapshotID, State: "passed", Tests: []ExecutedTest{{id, "passed"}}}}
	report := makeReport("large", m, state, true)
	latest := report.Requirements[0].Roles[0].Executions
	if len(report.Summaries) != 2 || len(report.Executions) != 2 || report.Executions[0].State != "failed" || len(latest) != 1 || latest[0].ReceiptID != "second" || latest[0].State != "passed" {
		t.Fatal("история потеряна или проекция размножает executions")
	}
	if stale := makeReport("large", m, state, false); stale.Requirements[0].Roles[0].Executions[0].State != "stale" {
		t.Fatal("проекция скрыла stale")
	}
}

func TestNativeCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("нативная сборка и процессы")
	}
	config, base := fixture(t)
	binary := filepath.Join(base, "spec-audit")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("сборка: %v: %s", err, output)
	}
	before := map[string][]byte{}
	for _, name := range []string{"rules.md", "source.go", "source_test.go", "go.mod"} {
		before[name] = readFixture(t, filepath.Join(base, "source", name))
	}
	invoke := func(level string, success bool, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir, cmd.Env = base, []string{"PATH=", "LOG_LEVEL=" + level}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if (err == nil) != success {
			t.Fatalf("%v: %v: %s", args, err, &stderr)
		}
		if success && !json.Valid(stdout.Bytes()) {
			t.Fatal("stdout не один JSON", &stdout)
		}
		if level == "error" && success && stderr.Len() != 0 {
			t.Fatal("LOG_LEVEL не соблюдён", &stderr)
		}
		if !success && (!json.Valid(stderr.Bytes()) || strings.Contains(stderr.String(), "private-token")) {
			t.Fatal("диагностика не JSON или содержит секрет", &stderr)
		}
		if level == "debug" && !strings.Contains(stderr.String(), `"level":"DEBUG"`) {
			t.Fatal("нет DEBUG")
		}
		return stdout.Bytes()
	}
	invoke("error", true, "init", filepath.Join(base, "new.yaml"))
	invoke("debug", true, "index", config)
	invoke("error", true, "prepare", config, "native")
	var batch TaskBatch
	if err := json.Unmarshal(invoke("error", true, "tasks", config, "native"), &batch); err != nil {
		t.Fatal(err)
	}
	for _, task := range batch.Tasks {
		body, _ := json.Marshal(sampleResult(t, task, filepath.Join(base, "source")))
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, body)
		invoke("error", true, "validate", config, "native", task.TaskID, path)
		invoke("error", false, "validate", config, "native", "missing-task", path)
		invoke("error", true, "submit", config, "native", task.TaskID, path)
	}
	invoke("error", true, "status", config, "native")
	invoke("error", true, "report", config, "native")
	invoke("error", true, "retry", config, "native", batch.Tasks[0].TaskID)
	bad := filepath.Join(base, "bad.yaml")
	writeFixture(t, bad, []byte("unknown: private-token\n"))
	invoke("error", false, "index", bad)
	for name, data := range before {
		if !bytes.Equal(data, readFixture(t, filepath.Join(base, "source", name))) {
			t.Fatal("изменён входной файл", name)
		}
	}
}

func TestConfigurationAndIndex(t *testing.T) {
	config, base := fixture(t)
	original := readFixture(t, config)
	for _, invalid := range []string{
		string(original) + "version: 1\n", string(original) + "unknown: secret\n", string(original) + "---\nversion: 1\n",
		strings.Replace(string(original), "version: 1", "version: 2", 1),
		strings.Replace(string(original), "paths: [rules.md]", "paths: [../rules.md]", 1),
		strings.Replace(string(original), "reports_dir: runs", "reports_dir: source", 1),
		strings.Replace(string(original), "kind: none", "kind: shell", 1),
	} {
		writeFixture(t, config, []byte(invalid))
		runFail(t, "prepare", config, "bad")
	}
	writeFixture(t, config, original)
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := fixture(t)
	cfg2, _ := loadConfig(second)
	m2, err := snapshot(cfg2)
	if err != nil || m2.SnapshotID != m.SnapshotID {
		t.Fatal("identity не переносима", err)
	}
	before := m.Requirements[0].ContentHash
	path := filepath.Join(base, "source", "rules.md")
	writeFixture(t, path, append([]byte("\n\n"), readFixture(t, path)...))
	moved, err := snapshot(cfg)
	if err != nil || moved.Requirements[0].ContentHash != before || moved.SnapshotID == m.SnapshotID {
		t.Fatal("неверный hash нормы/файла")
	}
	init := filepath.Join(base, "new.yaml")
	runOK(t, "init", init)
	prior := readFixture(t, init)
	runFail(t, "init", init)
	if !bytes.Equal(prior, readFixture(t, init)) {
		t.Fatal("init перезаписал файл")
	}
	link := filepath.Join(base, "dangling.yaml")
	if err := os.Symlink("missing", link); err != nil {
		t.Fatal(err)
	}
	runFail(t, "init", link)
	for _, check := range []struct {
		data, want string
		start, end int
	}{{"a\n\nb\n", "", 2, 2}, {"a\r\nb\r\n", "a\r\nb", 1, 2}} {
		got, err := lineQuote([]byte(check.data), check.start, check.end)
		if err != nil || got != check.want {
			t.Fatal("CRLF/empty quote")
		}
	}
	if err := checkStateSize(State{Entries: []Entry{{Result: &Result{Summary: strings.Repeat("x", maxState)}}}}); err == nil {
		t.Fatal("нет write-side лимита")
	}
}

// Provenance journal (tool-spec §19, REQ-SA-040): appended only after a successful publication, never on refusal.
func TestToolVersions(t *testing.T) {
	journalPath := func(base, run string) string { return filepath.Join(base, "runs", run, toolVersionsFile) }
	readJournal := func(t *testing.T, path string) toolVersionJournal {
		t.Helper()
		var journal toolVersionJournal
		if err := json.Unmarshal(readFixture(t, path), &journal); err != nil {
			t.Fatal(err)
		}
		return journal
	}
	captureLogs := func(t *testing.T) *bytes.Buffer {
		t.Helper()
		var buf bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		t.Cleanup(func() { slog.SetDefault(previous) })
		return &buf
	}
	t.Run("ordered", func(t *testing.T) {
		config, base, _ := reviewedFixture(t, false)
		manifest := readFixture(t, filepath.Join(base, "runs/review/manifest.json"))
		runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
		journal := readJournal(t, journalPath(base, "review"))
		want := []string{"prepare", "submit", "submit", "review"}
		if journal.Version != 1 || len(journal.Records) != len(want) {
			t.Fatalf("журнал: %+v", journal)
		}
		for i, record := range journal.Records {
			if _, err := time.Parse(time.RFC3339, record.RecordedAt); record.Command != want[i] || record.ToolVersion != version || err != nil || !strings.HasSuffix(record.RecordedAt, "Z") {
				t.Fatalf("запись %d: %+v", i, record)
			}
		}
		runOK(t, "report", config, "review")
		var report Report
		if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/report.json")), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.ToolVersions) != 4 || report.ToolVersions[3].Command != "review" {
			t.Fatalf("report.tool_versions: %+v", report.ToolVersions)
		}
		if !strings.Contains(string(readFixture(t, filepath.Join(base, "runs/review/report.html"))), "Версии бинарника") {
			t.Fatal("HTML без версий")
		}
		if !bytes.Equal(manifest, readFixture(t, filepath.Join(base, "runs/review/manifest.json"))) {
			t.Fatal("журнал изменил manifest")
		}
		if runOK(t, "status", config, "review").(Status).Freshness != "fresh" {
			t.Fatal("журнал изменил freshness")
		}
	})
	t.Run("no_record_on_failure_and_read_only", func(t *testing.T) {
		config, base := fixture(t)
		batch := runOK(t, "prepare", config, "journal").(TaskBatch)
		path := journalPath(base, "journal")
		before := readFixture(t, path)
		if journal := readJournal(t, path); len(journal.Records) != 1 || journal.Records[0].Command != "prepare" {
			t.Fatalf("после prepare: %+v", journal)
		}
		entries, _ := os.ReadDir(filepath.Join(base, "runs", "journal"))
		bad := sampleResult(t, batch.Tasks[0], filepath.Join(base, "source"))
		bad.Assessments[0].Implementation = "PASS"
		body, _ := json.Marshal(bad)
		badPath := filepath.Join(base, "bad.json")
		writeFixture(t, badPath, body)
		runFail(t, "submit", config, "journal", batch.Tasks[0].TaskID, badPath)
		runOK(t, "status", config, "journal")
		runOK(t, "report", config, "journal")
		runOK(t, "tasks", config, "journal")
		good, _ := json.Marshal(sampleResult(t, batch.Tasks[0], filepath.Join(base, "source")))
		goodPath := filepath.Join(base, "good.json")
		writeFixture(t, goodPath, good)
		runOK(t, "validate", config, "journal", batch.Tasks[0].TaskID, goodPath)
		after, _ := os.ReadDir(filepath.Join(base, "runs", "journal"))
		if !bytes.Equal(before, readFixture(t, path)) || len(after) != len(entries)+2 { // + report.json + report.html only
			t.Fatalf("отказ, read-only или validate изменили журнал: %d → %d записей каталога", len(entries), len(after))
		}
	})
	t.Run("legacy_run", func(t *testing.T) {
		config, base := fixture(t)
		writeFixture(t, config, bytes.Replace(readFixture(t, config), []byte("runtime: {kind: none}"), []byte("runtime: {kind: go, paths: ['.'], tests: [], timeout_seconds: 30}"), 1))
		for _, file := range []string{"manifest.json", "state.json"} {
			writeFixture(t, filepath.Join(base, "runs/blind-v1", file), readFixture(t, "../acceptance/declared-v01/"+file))
		}
		runOK(t, "status", config, "blind-v1")
		runOK(t, "report", config, "blind-v1")
		if _, err := os.Stat(journalPath(base, "blind-v1")); !os.IsNotExist(err) {
			t.Fatal("read-only команды создали журнал")
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/blind-v1/report.json")), &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["tool_versions"]; ok {
			t.Fatal("tool_versions у исторического run без журнала")
		}
		view := runOK(t, "review", config, "blind-v1").(ReviewContext)
		decision := ReviewDecision{1, "legacy-review", "blind-v1", view.SnapshotID, view.BasisSHA256, "host", "Historical canonical evidence only", view.Entries[0].Result.Assessments, []string{"Original raw formatting unavailable"}}
		runOK(t, "review", config, "blind-v1", writeReviewInput(t, base, decision))
		if journal := readJournal(t, journalPath(base, "blind-v1")); len(journal.Records) != 1 || journal.Records[0].Command != "review" {
			t.Fatalf("первая запись исторического run: %+v", journal)
		}
	})
	t.Run("corrupt_journal", func(t *testing.T) {
		config, base := fixture(t)
		batch := runOK(t, "prepare", config, "corrupt").(TaskBatch)
		path := journalPath(base, "corrupt")
		writeFixture(t, path, []byte(`{"version":1}`))
		state := readFixture(t, filepath.Join(base, "runs/corrupt/state.json"))
		good, _ := json.Marshal(sampleResult(t, batch.Tasks[0], filepath.Join(base, "source")))
		goodPath := filepath.Join(base, "good.json")
		writeFixture(t, goodPath, good)
		runFail(t, "submit", config, "corrupt", batch.Tasks[0].TaskID, goodPath)
		runFail(t, "validate", config, "corrupt", batch.Tasks[0].TaskID, goodPath)
		runFail(t, "tasks", config, "corrupt")
		runFail(t, "report", config, "corrupt")
		runFail(t, "status", config, "corrupt")
		if !bytes.Equal(state, readFixture(t, filepath.Join(base, "runs/corrupt/state.json"))) || string(readFixture(t, path)) != `{"version":1}` {
			t.Fatal("повреждённый журнал не остановил запись")
		}
		if entries, _ := os.ReadDir(filepath.Join(base, "runs/corrupt/results")); len(entries) != 0 {
			t.Fatal("результат записан при повреждённом журнале")
		}
	})
	t.Run("version_change", func(t *testing.T) {
		config, base := fixture(t)
		runOK(t, "prepare", config, "versions")
		root, err := os.OpenRoot(filepath.Join(base, "runs", "versions"))
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		logs := captureLogs(t)
		data, err := prepareToolVersion(root, "submit", "9.9.9")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(logs.String(), "другой версией") {
			t.Fatalf("нет WARN о смене версии: %s", logs.String())
		}
		publishToolVersion(root, data)
		runOK(t, "report", config, "versions")
		var report Report
		if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/versions/report.json")), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.ToolVersions) != 2 || report.ToolVersions[0].ToolVersion != version || report.ToolVersions[1].ToolVersion != "9.9.9" {
			t.Fatalf("%+v", report.ToolVersions)
		}
	})
	t.Run("append_failure_is_warning", func(t *testing.T) {
		config, base := fixture(t)
		runOK(t, "prepare", config, "blocked")
		root, err := os.OpenRoot(filepath.Join(base, "runs", "blocked"))
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		data, err := prepareToolVersion(root, "submit", version)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Remove(toolVersionsFile); err != nil {
			t.Fatal(err)
		}
		if err := root.Mkdir(toolVersionsFile, 0700); err != nil { // rename onto a directory fails after the commit point
			t.Fatal(err)
		}
		logs := captureLogs(t)
		publishToolVersion(root, data)
		if !strings.Contains(logs.String(), "журнал версий не записан") {
			t.Fatalf("сбой публикации журнала не предупредил: %s", logs.String())
		}
	})
}

// tool-spec §24.2: prepare materializes each role's directory; retry rewrites only task.json.
func TestPrepareDispatch(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		dir := filepath.Join(base, "runs/review/dispatch", task.TaskID)
		var stored Task
		if err := json.Unmarshal(readFixture(t, filepath.Join(dir, "task.json")), &stored); err != nil || !reflect.DeepEqual(stored, task) {
			t.Fatalf("task.json должен равняться заданию prepare: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "files.json")); !os.IsNotExist(err) {
			t.Fatal("files.json не копируется в каталог задания (§26.3)")
		}
	}
	// tool-spec §28.1: sdk is an empty list, not a missing key, when no facts were imported.
	if raw, err := json.Marshal(batch); err != nil || !bytes.Contains(raw, []byte(`"sdk":[]`)) {
		t.Fatal("prepare должен отдавать sdk: []", err)
	}
	sharedPath := filepath.Join(base, "runs/review/dispatch/files.json")
	var files []SourceFile
	if err := json.Unmarshal(readFixture(t, sharedPath), &files); err != nil || !reflect.DeepEqual(files, batch.Files) {
		t.Fatalf("dispatch/files.json должен равняться списку файлов prepare: %v", err)
	}
	// tool-spec §30.1: the protocol and each role's prompt come from the binary, with absolute paths and no state/manifest paths.
	if !bytes.Equal(readFixture(t, filepath.Join(base, "runs/review/dispatch/protocol.txt")), readFixture(t, "../skills/spec-audit/references/protocol.txt")) {
		t.Fatal("dispatch/protocol.txt должен побайтно равняться протоколу skill")
	}
	absConfig, _ := filepath.Abs(config)
	for _, task := range batch.Tasks {
		dir := filepath.Join(base, "runs/review/dispatch", task.TaskID)
		prompt := string(readFixture(t, filepath.Join(dir, "prompt.md")))
		for _, want := range []string{"Задание роли " + task.Role, "SOURCE_ROOT: " + batch.ProjectRoot, filepath.Join(dir, "task.json"), filepath.Join(base, "runs/review/dispatch/files.json"),
			filepath.Join(base, "runs/review/dispatch/protocol.txt"), filepath.Join(dir, "result.json"), " validate " + absConfig + " review " + task.TaskID + " ", filepath.Join(dir, "sdk_hints.json"),
			" cite " + absConfig + " PATH A B", filepath.Join(dir, "validate.log"), "AGENTS.md, CLAUDE.md", "scratchpad", `Tests\Feature\ExampleTest::test_name`, "supported|contradicted|unknown", "ambiguous-норму нельзя объявлять clear", dir + "/\n"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("prompt.md роли %s не содержит %q", task.TaskID, want)
			}
		}
		if strings.Contains(prompt, "manifest.json") || strings.Contains(prompt, "state.json") || task.Role == "redteam" && !strings.Contains(prompt, "восстановление/incident, повтор, откат") {
			t.Fatal("prompt.md не должен называть manifest/state")
		}
	}
	// tool-spec §30.3: counters as in status.
	if batch.Expected != len(batch.Tasks) || batch.Submitted != 0 || batch.DeliveryComplete || len(batch.PendingIDs) != len(batch.Tasks) || batch.PendingIDs[0] != batch.Tasks[0].TaskID {
		t.Fatal("prepare: счётчики доставки", batch.Expected, batch.Submitted, batch.DeliveryComplete)
	}
	first := batch.Tasks[0]
	dir := filepath.Join(base, "runs/review/dispatch", first.TaskID)
	promptBefore := readFixture(t, filepath.Join(dir, "prompt.md"))
	writeFixture(t, filepath.Join(dir, "prompt.md"), []byte("host edit\n"))
	writeFixture(t, filepath.Join(dir, "notes.txt"), []byte("role notes\n"))
	filesBefore := readFixture(t, sharedPath)
	retried := runOK(t, "retry", config, "review", first.TaskID).(Task)
	var stored Task
	if err := json.Unmarshal(readFixture(t, filepath.Join(dir, "task.json")), &stored); err != nil || stored.Attempt != 2 || !reflect.DeepEqual(stored, retried) {
		t.Fatal("retry должен перезаписать task.json новой попыткой", stored.Attempt)
	}
	if !bytes.Equal(promptBefore, readFixture(t, filepath.Join(dir, "prompt.md"))) {
		t.Fatal("retry должен восстановить prompt.md бинарника (§30.1)")
	}
	if string(readFixture(t, filepath.Join(dir, "notes.txt"))) != "role notes\n" || !bytes.Equal(filesBefore, readFixture(t, sharedPath)) {
		t.Fatal("retry не должен трогать остальные файлы каталога")
	}
	tasks := runOK(t, "tasks", config, "review").(TaskBatch)
	if !reflect.DeepEqual(tasks.Tasks[0], stored) {
		t.Fatal("tasks после retry должен совпадать с task.json", tasks.Tasks[0])
	}
	for _, task := range tasks.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	if done := runOK(t, "tasks", config, "review").(TaskBatch); len(done.Tasks) != 0 || done.Expected != len(batch.Tasks) || done.Submitted != done.Expected || !done.DeliveryComplete || len(done.PendingIDs) != 0 {
		t.Fatal("tasks после полной доставки: tasks [] и счётчики", done.Expected, done.Submitted, done.DeliveryComplete)
	}
	if raw, err := json.Marshal(TaskBatch{Tasks: []Task{}}); err != nil || !bytes.Contains(raw, []byte(`"delivery_complete":false`)) {
		t.Fatal("счётчики — обязательные ключи ответа", string(raw))
	}
}

// tool-spec §24.4: help and version synonyms answer on stdout; unknown flags still refuse.
func TestHelpVersion(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		got := runOK(t, arg).(map[string]any)
		text, _ := got["usage"].(string)
		if !strings.Contains(text, "draft CONFIG RUN_ID") || !strings.Contains(text, "skill install|update") || !strings.Contains(text, "help") {
			t.Fatalf("%s: %v", arg, got)
		}
	}
	for _, arg := range []string{"--version", "-V"} {
		if got := runOK(t, arg).(map[string]any); !reflect.DeepEqual(got, runVersion()) {
			t.Fatalf("%s: %v", arg, got)
		}
	}
	runFail(t, "--bogus")
	runFail(t, "help", "extra")
}

// tool-spec §31.1: cite returns the exact quote validate expects; bad ranges and paths refuse; nothing is created.
func TestCite(t *testing.T) {
	config, base := fixture(t)
	source := readFixture(t, filepath.Join(base, "source/source.go"))
	want, err := lineQuote(source, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	got := runOK(t, "cite", config, "source.go", "2", "4").(Citation)
	if !reflect.DeepEqual(got, Citation{"source.go", 2, 4, want}) {
		t.Fatal("cite должен вернуть точную цитату", got)
	}
	if _, err := os.Stat(filepath.Join(base, "runs")); !os.IsNotExist(err) {
		t.Fatal("cite не создаёт каталог отчётов")
	}
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	task := batch.Tasks[0]
	result := sampleResult(t, task, filepath.Join(base, "source"))
	result.Assessments[0].Code = []Citation{got}
	path := filepath.Join(base, "cited.json")
	writeFixture(t, path, legacyMarshal(t, result))
	runOK(t, "validate", config, "review", task.TaskID, path)
	if err := os.Symlink(filepath.Join(base, "source/source.go"), filepath.Join(base, "source/link.go")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"source.go", "4", "2"}, {"source.go", "0", "1"}, {"source.go", "1", "100000"}, {"source.go", "x", "2"}, {".", "1", "1"}, {"../config.yaml", "1", "1"}, {filepath.Join(base, "source/source.go"), "1", "1"}, {"link.go", "1", "1"}, {"missing.go", "1", "1"}} {
		runFail(t, append([]string{"cite", config}, args...)...)
	}
}

// tool-spec §33.3: a publishing command on a run prepared by another binary version warns; the journal records both.
func TestVersionDriftWarning(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	versionsPath := filepath.Join(base, "runs/review/tool-versions.json")
	var journal toolVersionJournal
	if err := json.Unmarshal(readFixture(t, versionsPath), &journal); err != nil {
		t.Fatal(err)
	}
	journal.Records[0].ToolVersion = "0.0.1"
	data, _ := json.MarshalIndent(journal, "", "  ")
	writeFixture(t, versionsPath, append(data, '\n'))
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	task := batch.Tasks[0]
	path := filepath.Join(base, task.TaskID+".json")
	writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
	runOK(t, "tasks", config, "review")
	if strings.Contains(logs.String(), "другой версией") {
		t.Fatal("read-only команда не предупреждает")
	}
	runOK(t, "submit", config, "review", task.TaskID, path)
	if !strings.Contains(logs.String(), "другой версией") || !strings.Contains(logs.String(), `"prepared":"0.0.1"`) {
		t.Fatal("submit должен предупредить о версии prepare", logs.String())
	}
	if err := json.Unmarshal(readFixture(t, versionsPath), &journal); err != nil || len(journal.Records) != 2 || journal.Records[1].ToolVersion == "0.0.1" {
		t.Fatal("журнал версий пишет обе версии", journal.Records)
	}
}

// tool-spec §34: overview maps several scopes read-only; a stale scope is reported, not fatal; totals come from decided runs.
func TestOverview(t *testing.T) {
	config, base := fixture(t)
	if view := runOK(t, "overview", config).(Overview); view.Totals.Scopes != 1 || view.Scopes[0].Runs != 0 || view.Scopes[0].Latest != nil || view.Scopes[0].IndexMode != "declared" || view.Scopes[0].Freshness != "n/a" || view.Scopes[0].Requirements != 5 {
		t.Fatal("scope без run", view.Scopes[0])
	}
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	if view := runOK(t, "overview", config).(Overview); view.Scopes[0].Runs != 1 || view.Scopes[0].Latest == nil || view.Scopes[0].Latest.DeliveryComplete || view.Scopes[0].Decided != nil || len(view.Scopes[0].Latest.Disagree) != 0 || view.Scopes[0].Latest.ToolVersion != version {
		t.Fatal("run без доставки", view.Scopes[0].Latest)
	}
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			result.Assessments[0].Assertion = "weak"
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
	accepted, acceptedBase := acceptedFixture(t)
	c := candidateAt(t, acceptedBase, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, accepted, "initial", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", accepted, raw, decision)
	versionsPath := filepath.Join(base, "runs/review/tool-versions.json")
	before := readFixture(t, versionsPath)
	view := runOK(t, "overview", config, accepted).(Overview)
	if view.Totals.Scopes != 2 || view.Totals.Requirements != 6 || view.Totals.Runs != 1 || view.Totals.Implementation["supported"] == 0 || view.Totals.Assertion["weak"] != 1 {
		t.Fatal("итоги по двум scope", view.Totals)
	}
	first := view.Scopes[0]
	if first.Decided == nil || first.Decided.RunID != "review" || first.Decided.HostReviewState != "current" || first.Decided.Form != "assessments" || !reflect.DeepEqual(first.Decided.Disagree, []string{"REQ-DEMO-001"}) || !first.Decided.SnapshotCurrent || first.Decided.Submitted != 2 || len(first.Decided.Contradicted) != first.Decided.Implementation["contradicted"] {
		t.Fatal("решённый run", first.Decided)
	}
	if second := view.Scopes[1]; second.IndexMode != "accepted" || second.Freshness != "fresh" || second.Requirements != 1 || second.Head == "" || second.Runs != 0 {
		t.Fatal("accepted scope без run", second)
	}
	// tool-spec §42.2: the code lines cited under contradicted verdicts are listed across scopes with their origin.
	if len(view.ContradictedCode) == 0 || view.ContradictedCode[0].Config != config || view.ContradictedCode[0].RunID != "review" || view.ContradictedCode[0].Path == "" {
		t.Fatal("contradicted_code по решённому run", view.ContradictedCode)
	}
	for _, c := range view.ContradictedCode {
		found := false
		for _, id := range first.Decided.Contradicted {
			found = found || id == c.RequirementID
		}
		if !found {
			t.Fatal("строка приписана норме без вердикта contradicted", c)
		}
	}
	if !bytes.Equal(before, readFixture(t, versionsPath)) {
		t.Fatal("overview не пишет провенанс")
	}
	rules := filepath.Join(acceptedBase, "source/rules.md")
	writeFixture(t, rules, append(readFixture(t, rules), []byte("ещё строка\n")...))
	if stale := runOK(t, "overview", accepted).(Overview).Scopes[0]; stale.Freshness != "stale" || stale.Error == "" || stale.Requirements != 1 {
		t.Fatal("stale scope сообщается, а не роняет карту", stale)
	}
	runFail(t, "overview")
}

// tool-spec §41.1: a field-set mismatch names the missing and unknown keys.
func TestRequiredJSONNamesFields(t *testing.T) {
	err := requiredJSON(json.RawMessage(`{"candidate":"C001","extra":1}`), reflect.TypeOf(AcceptedTarget{}))
	if err == nil || !strings.Contains(err.Error(), "нет [title verification]") || !strings.Contains(err.Error(), "лишние [extra]") {
		t.Fatal("отказ должен назвать поля", err)
	}
}
