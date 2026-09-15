//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	runOK(t, "submit", config, "normal", first.TaskID, firstPath)
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
				runFail(t, "submit", config, "normal", task.TaskID, resultPath(name, r))
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
			runFail(t, "submit", config, "normal", task.TaskID, path)
		}
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
	runOK(t, "submit", config, "normal", first.TaskID, resultPath("retry", sampleResult(t, task, source)))
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
		runFail(t, "test", config, "normal", "stale-run")
		writeFixture(t, path, prior)
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
