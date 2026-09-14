//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// One integration check exercises the real command boundary and process-level locking.
func TestPilot(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--pilot-child" {
			value, err := execute(os.Args[i+1:])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			_ = json.NewEncoder(os.Stdout).Encode(value)
			os.Exit(0)
		}
	}
	base := t.TempDir()
	source, reports := filepath.Join(base, "source"), filepath.Join(base, "runs")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	files := map[string]string{
		"TZ/rules.md":         "Owned numbers must respect the cap.\n\nFloor changes the probe.\n",
		"app/guard.go":        "package inventory\nfunc Owned() bool { return true }\n",
		"tests/guard_test.go": "package inventory\n// TestOwned checks the cap.\n",
	}
	for path, content := range files {
		write(filepath.Join(source, path), []byte(content))
	}
	yamlText := fmt.Sprintf("version: 1\nproject_root: %q\nspecs: [TZ]\ncode: [app]\ntests: [tests]\nreports_dir: %q\nruntime: {kind: docker, service: app}\nscopes:\n  - id: owned\n    focus: '<script>alert(1)</script>'\n    spec_refs: ['TZ/rules.md:1-1']\n  - id: floor\n    focus: floor\n    spec_refs: ['TZ/rules.md:3-3']\n", source, reports)
	config := filepath.Join(base, "config.yaml")
	write(config, []byte(yamlText))
	run := func(args ...string) any {
		t.Helper()
		value, err := execute(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return value
	}
	fail := func(args ...string) {
		t.Helper()
		if _, err := execute(args); err == nil {
			t.Fatalf("команда должна быть отклонена: %v", args)
		}
	}
	resultFor := func(task Task) Result {
		return Result{
			TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope,
			Summary: "<script>alert(2)</script>", Limitations: []string{"Тесты не запускались."},
			Observations: []Observation{{Key: "cap", Verdict: "supported", Statement: "<img src=x onerror=alert(3)>",
				Spec:  []Citation{{"TZ/rules.md", 1, 1, "Owned numbers must respect the cap."}},
				Code:  []Citation{{"app/guard.go", 2, 2, "func Owned() bool { return true }"}},
				Tests: []Citation{{"tests/guard_test.go", 2, 2, "// TestOwned checks the cap."}},
			}},
		}
	}
	resultPath := func(name string, result Result) string {
		t.Helper()
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(base, name+".json")
		write(path, data)
		return path
	}
	batch := run("prepare", config, "normal").(TaskBatch)
	if len(batch.Tasks) != 4 || batch.SnapshotID == "" {
		t.Fatal("prepare не выдал четыре задания")
	}
	manifestPath := filepath.Join(reports, "normal", "manifest.json")
	manifestBytes := read(manifestPath)
	fail("prepare", config, "normal")
	if !bytes.Equal(manifestBytes, read(manifestPath)) {
		t.Fatal("prepare переписал manifest")
	}
	getReport := func() Report {
		t.Helper()
		run("report", config, "normal")
		var report Report
		if err := json.Unmarshal(read(filepath.Join(reports, "normal", "report.json")), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	if report := getReport(); report.Complete || !report.HostReconciliationRequired || len(report.Pending) != 4 {
		t.Fatal("пустой run ошибочно завершён")
	}
	first := resultFor(batch.Tasks[0])
	firstPath := resultPath("first", first)
	run("submit", config, "normal", first.TaskID, firstPath)
	if report := getReport(); report.Complete || report.Scopes[0].Complete || report.Scopes[0].Mapper == nil || report.Scopes[0].Redteam != nil {
		t.Fatal("scope без redteam не должен быть завершён")
	}
	duplicate := run("submit", config, "normal", first.TaskID, firstPath).(map[string]any)
	if duplicate["duplicate"] != true {
		t.Fatal("одинаковая отправка не признана идемпотентной")
	}
	conflicting := first
	conflicting.Summary = "Другой результат"
	fail("submit", config, "normal", first.TaskID, resultPath("conflict", conflicting))
	t.Run("reject_invalid_results", func(t *testing.T) {
		task := batch.Tasks[1]
		for name, mutate := range map[string]func(*Result){
			"bad_quote":                        func(r *Result) { r.Observations[0].Code[0].Quote = "invented" },
			"bad_line":                         func(r *Result) { r.Observations[0].Spec[0].LineEnd = 100 },
			"traversal":                        func(r *Result) { r.Observations[0].Code[0].Path = "../secret" },
			"absolute_path":                    func(r *Result) { r.Observations[0].Code[0].Path = "/etc/passwd" },
			"wrong_group":                      func(r *Result) { r.Observations[0].Code = r.Observations[0].Tests },
			"enum":                             func(r *Result) { r.Observations[0].Verdict = "PASS" },
			"attempt":                          func(r *Result) { r.Attempt++ },
			"task":                             func(r *Result) { r.TaskID = "other" },
			"role":                             func(r *Result) { r.Role = "mapper" },
			"scope":                            func(r *Result) { r.Scope = "other" },
			"snapshot":                         func(r *Result) { r.SnapshotID = "old" },
			"gap_without_code":                 func(r *Result) { r.Observations[0].Verdict = "gap"; r.Observations[0].Code = []Citation{} },
			"supported_without_spec":           func(r *Result) { r.Observations[0].Spec = []Citation{} },
			"empty_redteam_without_limitation": func(r *Result) { r.Observations = []Observation{}; r.Limitations = []string{} },
		} {
			r := resultFor(task)
			mutate(&r)
			fail("submit", config, "normal", task.TaskID, resultPath(name, r))
		}
		valid, _ := json.Marshal(resultFor(task))
		for name, raw := range map[string][]byte{
			"unknown":      bytes.Replace(valid, []byte(`"summary":`), []byte(`"unknown":0,"summary":`), 1),
			"duplicate":    bytes.Replace(valid, []byte(`"task_id":`), []byte(`"task_id":"wrong","task_id":`), 1),
			"case":         bytes.Replace(valid, []byte(`"task_id":`), []byte(`"TASK_ID":`), 1),
			"missing":      bytes.Replace(valid, []byte(`"key":"cap",`), nil, 1),
			"null":         bytes.Replace(valid, []byte(`"key":"cap"`), []byte(`"key":null`), 1),
			"multiple":     append(append([]byte{}, valid...), []byte("{}")...),
			"malformed":    valid[:len(valid)-1],
			"invalid_utf8": bytes.Replace(valid, []byte(`"key":"cap"`), []byte{'"', 'k', 'e', 'y', '"', ':', '"', 255, '"'}, 1),
			"oversized":    bytes.Repeat([]byte(" "), maxResult+1),
		} {
			path := filepath.Join(base, "invalid-"+name+".json")
			write(path, raw)
			fail("submit", config, "normal", task.TaskID, path)
		}
	})
	for _, task := range batch.Tasks[1:] {
		r := resultFor(task)
		if task.Role == "redteam" {
			r.Observations[0].Verdict = "gap"
		}
		run("submit", config, "normal", task.TaskID, resultPath(task.TaskID, r))
	}
	if report := getReport(); !report.Complete || report.Counts["supported"] != 2 || report.Counts["gap"] != 2 || !report.HostReconciliationRequired {
		t.Fatal("полный отчёт потерял роли или выводы")
	}
	html := string(read(filepath.Join(reports, "normal", "report.html")))
	if strings.Contains(html, "<script>") || strings.Contains(html, "<img src=x") || !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, "выполнение не подтверждено") {
		t.Fatal("HTML не экранирован или заявляет выполнение тестов")
	}
	t.Run("retry_invalidates_report", func(t *testing.T) {
		task := run("retry", config, "normal", first.TaskID).(Task)
		if task.Attempt != 2 {
			t.Fatal("retry не повысил attempt")
		}
		for _, name := range []string{"report.json", "report.html"} {
			if _, err := os.Stat(filepath.Join(reports, "normal", name)); !os.IsNotExist(err) {
				t.Fatal("устаревшая проекция сохранилась", name)
			}
		}
		if _, err := os.Stat(filepath.Join(reports, "normal", "results", first.TaskID+"-attempt-1.json")); err != nil {
			t.Fatal("retry удалил старый результат", err)
		}
		if report := getReport(); report.Complete || len(report.Pending) != 1 {
			t.Fatal("report после retry должен быть incomplete")
		}
		fail("submit", config, "normal", first.TaskID, firstPath)
		empty := resultFor(task)
		empty.Observations = []Observation{}
		fail("submit", config, "normal", task.TaskID, resultPath("empty-mapper", empty))
		run("submit", config, "normal", task.TaskID, resultPath("retried", resultFor(task)))
		if report := getReport(); !report.Complete {
			t.Fatal("повторная попытка не завершилась")
		}
		if !bytes.Equal(manifestBytes, read(manifestPath)) {
			t.Fatal("retry изменил manifest")
		}
	})
	t.Run("stale_and_symlink", func(t *testing.T) {
		path := filepath.Join(source, "app/guard.go")
		write(path, []byte("changed\n"))
		fail("submit", config, "normal", first.TaskID, firstPath)
		fail("report", config, "normal")
		write(path, []byte(files["app/guard.go"]))
		write(config, []byte(strings.Replace(yamlText, "focus: floor", "focus: changed", 1)))
		fail("report", config, "normal")
		write(config, []byte(yamlText))
		outside := filepath.Join(base, "outside.txt")
		write(outside, []byte("not a source"))
		link := filepath.Join(source, "app", "outside")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		fail("report", config, "normal")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("yaml_and_lines", func(t *testing.T) {
		for _, invalid := range []string{
			strings.Replace(yamlText, "version: 1", "version: 2", 1),
			yamlText + "unknown: true\n", yamlText + "version: 1\n", yamlText + "---\nversion: 1\n",
			strings.Replace(yamlText, "specs: [TZ]", "specs: [../TZ]", 1),
			strings.Replace(yamlText, "service: app", "service: other", 1),
			strings.Replace(yamlText, "code: [app]", "code: [TZ]", 1),
			strings.Replace(yamlText, fmt.Sprintf("reports_dir: %q", reports), fmt.Sprintf("reports_dir: %q", filepath.Join(source, "reports")), 1),
		} {
			path := filepath.Join(base, "invalid.yaml")
			write(path, []byte(invalid))
			if _, err := loadConfig(path); err == nil {
				t.Fatal("недопустимый YAML принят")
			}
		}
		for _, check := range []struct {
			data, want string
			start, end int
		}{
			{"a\n\nb\n", "", 2, 2}, {"a\n\nb\n", "a\n", 1, 2}, {"a\r\nb\r\n", "a\r\nb", 1, 2},
		} {
			got, err := lineQuote([]byte(check.data), check.start, check.end)
			if err != nil || got != check.want {
				t.Fatalf("строки: got %q, want %q, err %v", got, check.want, err)
			}
		}
	})
	t.Run("concurrent_processes", func(t *testing.T) {
		parallel := run("prepare", config, "parallel").(TaskBatch)
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		var commands []*exec.Cmd
		var outputs []*bytes.Buffer
		for _, task := range parallel.Tasks[:2] {
			result := resultFor(task)
			if task.Role == "redteam" {
				result.Observations = []Observation{}
			}
			path := resultPath("parallel-"+task.TaskID, result)
			cmd := exec.Command(exe, "-test.run=^TestPilot$", "--", "--pilot-child", "submit", config, "parallel", task.TaskID, path)
			output := new(bytes.Buffer)
			cmd.Stdout, cmd.Stderr = output, output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			commands, outputs = append(commands, cmd), append(outputs, output)
		}
		for i, cmd := range commands {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("concurrent submit: %v: %s", err, outputs[i])
			}
		}
		status := run("status", config, "parallel").(Status)
		if status.Submitted != 2 || status.Complete || len(status.Pending) != 2 {
			t.Fatalf("потеряно конкурентное обновление: %+v", status)
		}
	})
}
