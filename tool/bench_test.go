package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tool-spec §60: measurements before touching the hot paths named by the external review (item 4).
// A large legacy run: 96 norms, 48 code files of ~100 KB, both roles delivered with four code citations per norm.
const (
	benchNorms     = 96
	benchCodeFiles = 48
	benchFileLines = 2000
	benchCitations = 4
	benchScopeSize = 24
)

func benchLine(i int) string {
	return fmt.Sprintf("func handler%04d(input string) string { return strings.TrimSpace(input) + %q } // padding to make the line long", i, strings.Repeat("x", 16))
}

func benchSource() []byte {
	var b strings.Builder
	for i := 0; i < benchFileLines; i++ {
		b.WriteString(benchLine(i))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func benchRun(b *testing.B) (config, runID string) {
	b.Helper()
	base := b.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0600); err != nil {
			b.Fatal(err)
		}
	}
	var spec strings.Builder
	spec.WriteString("# Нормы\n\n")
	for i := 1; i <= benchNorms; i++ {
		fmt.Fprintf(&spec, "### REQ-BENCH-%03d — Норма %d\n\nУсловие: вызван обработчик %d.\nТребование: результат должен быть равен ожидаемому значению %d.\nПроверка: сравнить результат с %d.\n\n", i, i, i, i, i)
	}
	write("source/rules.md", []byte(spec.String()))
	source := benchSource()
	for i := 0; i < benchCodeFiles; i++ {
		write(fmt.Sprintf("source/src/file%02d.go", i), source)
	}
	write("source/src_test.go", []byte("package src\n\nfunc TestNothing(t *testing.T) {}\n"))
	config = filepath.Join(base, "config.yaml")
	var scopes strings.Builder
	for s := 0; s < benchNorms/benchScopeSize; s++ {
		fmt.Fprintf(&scopes, "  - {id: scope-%d, focus: часть %d, requirements: [", s, s)
		for i := s*benchScopeSize + 1; i <= (s+1)*benchScopeSize; i++ {
			fmt.Fprintf(&scopes, "REQ-BENCH-%03d, ", i)
		}
		scopes.WriteString("]}\n")
	}
	write("config.yaml", []byte("version: 1\nproject_root: source\nspecs: {paths: [rules.md]}\ncode: {paths: [src]}\ntests: {paths: [src_test.go]}\nreports_dir: runs\nruntime: {kind: none}\nscopes:\n"+scopes.String()))
	runID = "bench"
	value, err := execute([]string{"prepare", config, runID})
	if err != nil {
		b.Fatal(err)
	}
	batch := value.(TaskBatch)
	for _, task := range batch.Tasks {
		result := Result{TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope,
			Summary: "Синтетический большой run", Assessments: []Assessment{}, Limitations: []string{}}
		for n, req := range task.Requirements {
			a := Assessment{RequirementID: req.ID, Specification: "clear", Implementation: "supported", Assertion: "missing",
				Statement: "Реализация найдена", Spec: []Citation{req.Source}, Code: []Citation{}, Tests: []TestCitation{}, Limitations: []string{}}
			for c := 0; c < benchCitations; c++ {
				file := (n*benchCitations + c) % benchCodeFiles
				line := 1 + (n*37+c*11)%(benchFileLines-8)
				quote, err := lineQuote(source, line, line+7)
				if err != nil {
					b.Fatal(err)
				}
				a.Code = append(a.Code, Citation{fmt.Sprintf("src/file%02d.go", file), line, line + 7, quote})
			}
			result.Assessments = append(result.Assessments, a)
		}
		raw, _ := json.Marshal(result)
		path := filepath.Join(base, task.TaskID+".json")
		write(task.TaskID+".json", raw)
		if _, err := execute([]string{"submit", config, runID, task.TaskID, path}); err != nil {
			b.Fatal(err)
		}
	}
	return config, runID
}

func BenchmarkLineQuote(b *testing.B) {
	source := benchSource()
	b.SetBytes(int64(len(source)))
	for b.Loop() {
		if _, err := lineQuote(source, benchFileLines/2, benchFileLines/2+7); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckCitations(b *testing.B) {
	base := b.TempDir()
	source := benchSource()
	m := Manifest{}
	var code []Citation
	for i := 0; i < benchCodeFiles; i++ {
		rel := fmt.Sprintf("src/file%02d.go", i)
		if err := os.MkdirAll(filepath.Join(base, "src"), 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, rel), source, 0600); err != nil {
			b.Fatal(err)
		}
		m.Files = append(m.Files, SourceFile{Path: rel, Kind: "code", SHA256: digest(source), Bytes: len(source)})
	}
	for c := 0; c < 128; c++ {
		line := 1 + (c * 13 % (benchFileLines - 8))
		quote, _ := lineQuote(source, line, line+7)
		code = append(code, Citation{fmt.Sprintf("src/file%02d.go", c%benchCodeFiles), line, line + 7, quote})
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()
	req := Requirement{ID: "REQ-1"}
	for b.Loop() {
		if err := checkCitations(nil, code, nil, req, m, newSourceCache(root)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStatusRun(b *testing.B) {
	config, runID := benchRun(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := execute([]string{"status", config, runID}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReportRun(b *testing.B) {
	config, runID := benchRun(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := execute([]string{"report", config, runID}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReviewSummaryRun(b *testing.B) {
	config, runID := benchRun(b)
	b.ResetTimer()
	for b.Loop() {
		if _, err := execute([]string{"review", config, runID, "summary"}); err != nil {
			b.Fatal(err)
		}
	}
}

// The cache hit path keeps every refusal of the uncached path (tool-spec §60).
func TestSourceCache(t *testing.T) {
	base := t.TempDir()
	source := []byte("alpha\nbeta\ngamma\n")
	if err := os.WriteFile(filepath.Join(base, "a.go"), source, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	m := Manifest{Files: []SourceFile{{Path: "a.go", Kind: "code", SHA256: digest(source), Bytes: len(source)}}}
	req := Requirement{ID: "REQ-1"}
	cache := newSourceCache(root)
	good := Citation{"a.go", 2, 3, "beta\ngamma"}
	if err := checkCitations(nil, []Citation{good, good}, nil, req, m, cache); err != nil {
		t.Fatal(err)
	}
	if len(cache.lines) != 1 {
		t.Fatal("файл должен быть прочитан один раз")
	}
	for name, bad := range map[string]Citation{"quote": {"a.go", 2, 3, "beta\ngamm"}, "range": {"a.go", 3, 4, "gamma\n"}} {
		if err := checkCitations(nil, []Citation{good, bad}, nil, req, m, cache); err == nil || !strings.Contains(err.Error(), "цитата не совпадает с полными строками a.go") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	m.Files[0].SHA256 = digest([]byte("other"))
	if err := checkCitations(nil, []Citation{good}, nil, req, m, newSourceCache(root)); err == nil || !strings.Contains(err.Error(), "снимок источников изменился") {
		t.Fatal(err)
	}
}

// tool-spec §64: a run keeps the task form it was prepared with — the §50 per-scope form before 0.1.39, incremental-N after.
func TestIncrementalLegacyGrouping(t *testing.T) {
	reqs := []Requirement{}
	for i := 1; i <= 30; i++ {
		reqs = append(reqs, Requirement{ID: fmt.Sprintf("REQ-%03d", i)})
	}
	m := Manifest{SnapshotID: "snap", Requirements: reqs, Config: Config{Scopes: []Scope{
		{ID: "a", Requirements: []string{"REQ-001", "REQ-002", "REQ-003"}},
		{ID: "b", Requirements: []string{"REQ-004", "REQ-005"}},
		{ID: "c", Requirements: []string{"REQ-006"}},
	}}, Incremental: &IncrementalPlan{SinceRun: "prev", Assessed: []string{"REQ-001", "REQ-003", "REQ-005"}}}
	legacy := legacyIncrementalState(m)
	if len(legacy.Entries) != 4 || legacy.Entries[0].Task.TaskID != "a-mapper" || len(legacy.Entries[0].Task.Requirements) != 2 || legacy.Entries[2].Task.TaskID != "b-mapper" || len(legacy.Entries[2].Task.Requirements) != 1 {
		t.Fatalf("форма §50: задание на scope, только переоцениваемые нормы: %+v", legacy.Entries)
	}
	current := newState(m)
	if len(current.Entries) != 2 || current.Entries[0].Task.TaskID != "incremental-1-mapper" || len(current.Entries[0].Task.Requirements) != 3 {
		t.Fatalf("форма §51.5 по умолчанию (run без маркера, подготовленные 0.1.39–0.1.55): %+v", current.Entries)
	}
	if sameTaskIDs(legacy, current) || !sameTaskIDs(current, newState(m)) {
		t.Fatal("sameTaskIDs различает формы")
	}
}

// tool-spec §70: moving the TZ text of a decided norm (a line inserted above it) does not send the norm back to the
// roles — its spec quote is found verbatim in the norm's current source and carried at the new lines, and the host's
// decision on the incremental run accepts that carried verdict.
func TestIncrementalSpecShift(t *testing.T) {
	config, base := fixture(t)
	source := filepath.Join(base, "source")
	deliver := func(runID string, extra ...string) TaskBatch {
		batch := runOK(t, append([]string{"prepare", config, runID}, extra...)...).(TaskBatch)
		for _, task := range batch.Tasks {
			path := filepath.Join(base, runID+"-"+task.TaskID+".json")
			writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, source)))
			runOK(t, "submit", config, runID, task.TaskID, path)
		}
		return batch
	}
	deliver("r1")
	view := runOK(t, "review", config, "r1").(ReviewContext)
	rows := append([]Assessment{}, view.Entries[0].Result.Assessments...)
	decision := filepath.Join(base, "host-r1.json")
	writeFixture(t, decision, legacyMarshal(t, ReviewDecision{1, "decision-r1", "r1", view.SnapshotID, view.BasisSHA256, "host", "s", rows, []string{}}))
	runOK(t, "review", config, "r1", decision)
	planFrom := func() *IncrementalPlan {
		t.Helper()
		cfg, err := loadConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		m, err := snapshot(cfg)
		if err != nil {
			t.Fatal(err)
		}
		reports, err := os.OpenRoot(cfg.ReportsDir)
		if err != nil {
			t.Fatal(err)
		}
		defer reports.Close()
		plan, err := incrementalPlan(reports, "r1", m)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	before := planFrom()
	rules := filepath.Join(source, "rules.md")
	writeFixture(t, rules, append([]byte("<!-- строка выше всех норм -->\n"), readFixture(t, rules)...))
	after := planFrom()
	for id, reason := range after.Reasons {
		if reason == "changed" {
			t.Fatalf("%s: сдвиг текста ТЗ не меняет норму — changed недопустим", id)
		}
	}
	if len(after.Carried) != len(before.Carried) || len(after.Carried) == 0 {
		t.Fatalf("перенос не должен уменьшиться: было %d, стало %d", len(before.Carried), len(after.Carried))
	}
	for i, carried := range after.Carried {
		if carried.Spec[0].LineStart != before.Carried[i].Spec[0].LineStart+1 || carried.Spec[0].Quote != before.Carried[i].Spec[0].Quote {
			t.Fatalf("%s: spec-цитата должна переехать на строку ниже с той же quote", carried.RequirementID)
		}
	}
	deliver("r2", "since", "r1")
	draft := runOK(t, "draft", config, "r2").(ReviewDecisionV3)
	full := runOK(t, "review", config, "r2").(ReviewContext)
	mapper := map[string]Assessment{}
	for _, entry := range full.Entries {
		if entry.Task.Role == "mapper" && entry.Result != nil {
			for _, a := range entry.Result.Assessments {
				mapper[a.RequirementID] = a
			}
		}
	}
	verdicts := []ReviewVerdict{}
	for i, v := range draft.Verdicts {
		if v.Concur != "carried" {
			a := mapper[v.RequirementID]
			draft.Verdicts[i] = ReviewVerdictV3{v.RequirementID, a.Specification, a.Implementation, a.Assertion, "mapper", "по mapper", []string{}, []Citation{}, []Citation{}, []TestCitation{}}
		}
		verdicts = append(verdicts, draft.Verdicts[i].base())
	}
	draft.Reviewer, draft.Summary, draft.Counts = "host", "сдвиг ТЗ", countVerdicts(verdicts)
	path := filepath.Join(base, "host-r2.json")
	raw, _ := json.Marshal(draft)
	writeFixture(t, path, raw)
	runOK(t, "review", config, "r2", path)
	runOK(t, "report", config, "r2")
}
