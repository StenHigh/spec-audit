package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func acceptedFixture(t *testing.T) (string, string) {
	t.Helper()
	config, base := fixture(t)
	writeFixture(t, config, append(readFixture(t, config), []byte("index_mode: accepted\n")...))
	writeFixture(t, filepath.Join(base, "source/rules.md"), []byte("# Нормы\nЛимит одного файла — 8 МиБ включительно.\nНазвание карточки обязательно.\nУведомить при задержке; срок не согласован.\nНапример: две минуты.\n"))
	return config, base
}

func candidateAt(t *testing.T, base, path, id, statement string, start, end int) legacyCandidate {
	t.Helper()
	quote, err := lineQuote(readFixture(t, filepath.Join(base, "source", path)), start, end)
	if err != nil {
		t.Fatal(err)
	}
	return legacyCandidate{id, "В указанной области", statement, []string{}, "clear", []string{}, []Citation{{path, start, end, quote}}}
}

func acceptOperation(action string, previous []string, candidates ...string) AcceptedOperation {
	targets := []AcceptedTarget{}
	for _, id := range candidates {
		target := AcceptedTarget{id, "Норма", "Проверить точный результат"}
		if oneOf(action, "reject", "defer") {
			target.Title, target.Verification = "", ""
		}
		targets = append(targets, target)
	}
	return AcceptedOperation{action, previous, targets, "Решение хоста по источникам"}
}

func acceptedInputs(t *testing.T, config, id string, candidates []legacyCandidate, ops ...AcceptedOperation) (string, string) {
	t.Helper()
	if ops == nil {
		ops = []AcceptedOperation{}
	}
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	set, _, err := acceptedSources(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := readAccepted(cfg.ReportsDir)
	if err != nil {
		t.Fatal(err)
	}
	raw := legacyMarshal(t, legacyRaw{1, set, candidates, []string{}})
	decision := AcceptedDecision{1, id, state.Head, digest(raw), ops}
	rawPath, decisionPath := filepath.Join(filepath.Dir(config), id+"-raw.json"), filepath.Join(filepath.Dir(config), id+"-decision.json")
	writeFixture(t, rawPath, raw)
	writeFixture(t, decisionPath, legacyMarshal(t, decision))
	return rawPath, decisionPath
}

func acceptedRead(t *testing.T, config string) acceptedState {
	t.Helper()
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := readAccepted(cfg.ReportsDir)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAcceptedBaseline(t *testing.T) {
	for _, line := range strings.Split(strings.TrimSpace(string(readFixture(t, "../acceptance/accepted-index.sha256"))), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || digest(readFixture(t, "../"+parts[1])) != parts[0] {
			t.Fatal("изменён контракт/ожидания после фиксации", line)
		}
	}
}

func TestAcceptedLifecycle(t *testing.T) {
	config, base := acceptedFixture(t)
	view := runOK(t, "reconcile", config).(map[string]any)
	if view["freshness"] != "uninitialized" || view["semantic_completeness_proven"] != false {
		t.Fatal("ложная готовность индекса")
	}
	if _, err := os.Stat(filepath.Join(base, "runs")); !os.IsNotExist(err) {
		t.Fatal("чтение создало артефакты")
	}
	runFail(t, "index", config)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	example := candidateAt(t, base, "rules.md", "C002", "Две минуты", 5, 5)
	unclear := candidateAt(t, base, "rules.md", "C003", "Уведомить", 4, 4)
	unclear.Clarity, unclear.Unresolved = "ambiguous", []string{"Какой срок?"}
	raw, decision := acceptedInputs(t, config, "initial", []legacyCandidate{c, example, unclear},
		acceptOperation("accept", []string{}, "C001"), acceptOperation("reject", []string{}, "C002"), acceptOperation("defer", []string{}, "C003"))
	runOK(t, "reconcile", config, raw, decision)
	state := acceptedRead(t, config)
	if len(state.Records) != 1 || state.Records[0].Requirement.ID != "REQ-AI-001" || state.Records[0].Requirement.Accepted.Revision != 1 || len(state.History[0].Decision.Operations) != 3 {
		t.Fatal("A01: неправильная приёмка/учёт")
	}
	journal := filepath.Join(base, "runs", acceptedFile)
	original := readFixture(t, journal)
	if runOK(t, "reconcile", config, raw, decision).(map[string]any)["duplicate"] != true || !bytes.Equal(original, readFixture(t, journal)) {
		t.Fatal("A08: replay изменил журнал")
	}
	writeFixture(t, decision, append(readFixture(t, decision), '\n'))
	runFail(t, "reconcile", config, raw, decision)
	batch := runOK(t, "prepare", config, "before").(TaskBatch)
	first := batch.Tasks[0].Requirements[0]
	// A02: both path and line change; the selected source set changes explicitly.
	if err := os.Rename(filepath.Join(base, "source/rules.md"), filepath.Join(base, "source/moved.md")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(base, "source/moved.md"), append([]byte("\n\n"), readFixture(t, filepath.Join(base, "source/moved.md"))...))
	writeFixture(t, config, bytes.ReplaceAll(readFixture(t, config), []byte("rules.md"), []byte("moved.md")))
	runFail(t, "index", config)
	c = candidateAt(t, base, "moved.md", "C001", c.Statement, 4, 4)
	raw, decision = acceptedInputs(t, config, "move", []legacyCandidate{c}, acceptOperation("rebind", []string{first.ID}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	next := runOK(t, "prepare", config, "after").(TaskBatch)
	rebound := next.Tasks[0].Requirements[0]
	if rebound.ID != first.ID || rebound.ContentHash != first.ContentHash || rebound.Accepted.Revision != 1 || rebound.Source.Path != "moved.md" || next.SnapshotID == batch.SnapshotID {
		t.Fatal("A02: потеряна идентичность или скрыт snapshot drift")
	}
	if runOK(t, "status", config, "before").(Status).Freshness != "stale" {
		t.Fatal("A02: старые свидетельства выглядят свежими")
	}
	// A03/A07: semantic edit needs revise, not rebind; failed transaction leaves bytes intact.
	path := filepath.Join(base, "source/moved.md")
	writeFixture(t, path, bytes.ReplaceAll(readFixture(t, path), []byte("8 МиБ"), []byte("16 МиБ")))
	c = candidateAt(t, base, "moved.md", "C001", "Лимит 16 МиБ", 4, 4)
	raw, decision = acceptedInputs(t, config, "change", []legacyCandidate{c}, acceptOperation("rebind", []string{first.ID}, "C001"))
	original = readFixture(t, journal)
	runFail(t, "reconcile", config, raw, decision)
	if !bytes.Equal(original, readFixture(t, journal)) {
		t.Fatal("A03: частичная запись отказа")
	}
	raw, decision = acceptedInputs(t, config, "change", []legacyCandidate{c}, acceptOperation("revise", []string{first.ID}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	state = acceptedRead(t, config)
	if state.Records[0].Requirement.ID != first.ID || state.Records[0].Requirement.Accepted.Revision != 2 || state.Records[0].Requirement.ContentHash == first.ContentHash || len(state.History) != 3 {
		t.Fatal("A03: нет новой редакции/истории")
	}
	// A06: disappearing text alone cannot retire; explicit retirement and new acceptance never reuse IDs.
	writeFixture(t, path, []byte("# Норм больше не указано\n"))
	if runOK(t, "reconcile", config).(map[string]any)["freshness"] != "stale" || acceptedRead(t, config).Records[0].Status != "active" {
		t.Fatal("A06: автоматическая отмена")
	}
	raw, decision = acceptedInputs(t, config, "retire", []legacyCandidate{}, acceptOperation("retire", []string{first.ID}))
	runOK(t, "reconcile", config, raw, decision)
	runFail(t, "prepare", config, "empty")
	state = acceptedRead(t, config)
	if state.Records[0].Status != "retired" || state.Records[0].Reason == "" {
		t.Fatal("A06: потеряна отмена/причина")
	}
	writeFixture(t, path, []byte("Название обязательно.\n"))
	c = candidateAt(t, base, "moved.md", "C001", "Название обязательно", 1, 1)
	raw, decision = acceptedInputs(t, config, "new", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	if acceptedRead(t, config).Records[1].Requirement.ID != "REQ-AI-002" {
		t.Fatal("A06/A08: повторно выдан ID")
	}
	// History remains readable even if every source file is unavailable.
	if err := os.Rename(filepath.Join(base, "source"), filepath.Join(base, "offline")); err != nil {
		t.Fatal(err)
	}
	if runOK(t, "reconcile", config).(map[string]any)["freshness"] != "unavailable" {
		t.Fatal("история зависит от текущих файлов")
	}
	runOK(t, "report", config, "before")
}

func TestAcceptedSplitMerge(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Состояние и журнал при общем условии", 2, 3)
	raw, decision := acceptedInputs(t, config, "combined", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	a, b := c, c
	a.Statement, b.ID, b.Statement = "Состояние при общем условии", "C002", "Журнал при общем условии"
	raw, decision = acceptedInputs(t, config, "split", []legacyCandidate{a, b}, acceptOperation("split", []string{"REQ-AI-001"}, "C001", "C002"))
	valid := readFixture(t, decision)
	writeFixture(t, decision, bytes.Replace(valid, []byte(`"candidate":"C002"`), []byte(`"candidate":"C099"`), 1))
	before := readFixture(t, filepath.Join(base, "runs", acceptedFile))
	runFail(t, "reconcile", config, raw, decision)
	if !bytes.Equal(before, readFixture(t, filepath.Join(base, "runs", acceptedFile))) {
		t.Fatal("A04: плохой потомок частично применён")
	}
	writeFixture(t, decision, valid)
	runOK(t, "reconcile", config, raw, decision)
	state := acceptedRead(t, config)
	if len(state.Records) != 3 || state.Records[0].Status != "retired" {
		t.Fatal("A04: потеряны предшественники")
	}
	for i, id := range []string{"REQ-AI-002", "REQ-AI-003"} {
		if state.Records[i+1].Requirement.ID != id || !reflect.DeepEqual(state.Records[i+1].Requirement.Accepted.Parents, []string{"REQ-AI-001"}) {
			t.Fatal("A04: неверная lineage")
		}
	}
	raw, decision = acceptedInputs(t, config, "merge", []legacyCandidate{c}, acceptOperation("merge", []string{"REQ-AI-002", "REQ-AI-003"}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	state = acceptedRead(t, config)
	if len(state.Records) != 4 || state.Records[1].Status != "retired" || state.Records[2].Status != "retired" || state.Records[3].Requirement.ID != "REQ-AI-004" || !reflect.DeepEqual(state.Records[3].Requirement.Accepted.Parents, []string{"REQ-AI-002", "REQ-AI-003"}) {
		t.Fatal("A05: неверное объединение")
	}
	// Semantics of the synthetic split are not proven by these mechanical checks.
}

func TestAcceptedRejections(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит", 2, 2)
	raw, decision := acceptedInputs(t, config, "initial", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	raw, decision = acceptedInputs(t, config, "next", []legacyCandidate{c}, acceptOperation("rebind", []string{"REQ-AI-001"}, "C001"))
	valid := readFixture(t, decision)
	before := readFixture(t, filepath.Join(base, "runs", acceptedFile))
	for _, pair := range [][2]string{
		{`"version":1`, `"version":1,"extra":1`}, {`"version":1`, `"version":1,"version":1`}, {`"version":1`, `"Version":1`},
		{`"previous":["REQ-AI-001"]`, `"previous":null`}, {`"previous":["REQ-AI-001"],`, ``},
		{`"previous":["REQ-AI-001"]`, `"previous":["REQ-AI-999"]`}, {`"action":"rebind"`, `"action":"revise"`},
		{`"action":"rebind"`, `"action":"unknown"`}, {`"candidate":"C001"`, `"candidate":"C002"`},
		{`"reason":"Решение хоста по источникам"`, `"reason":" "`},
		{`"title":"Норма"`, `"title":" "`},
	} {
		writeFixture(t, decision, bytes.Replace(valid, []byte(pair[0]), []byte(pair[1]), 1))
		runFail(t, "reconcile", config, raw, decision)
	}
	for _, data := range [][]byte{[]byte("{"), append(append([]byte{}, valid...), valid...), bytes.Repeat([]byte(" "), maxResult+1)} {
		writeFixture(t, decision, data)
		runFail(t, "reconcile", config, raw, decision)
	}
	writeFixture(t, decision, valid)
	writeFixture(t, raw, append(readFixture(t, raw), '\n'))
	runFail(t, "reconcile", config, raw, decision)
	// Re-signing a forged quotation cannot bypass provenance validation.
	raw, decision = acceptedInputs(t, config, "forged", []legacyCandidate{c}, acceptOperation("rebind", []string{"REQ-AI-001"}, "C001"))
	var r legacyRaw
	_ = json.Unmarshal(readFixture(t, raw), &r)
	r.Candidates[0].Citations[0].Quote = "Придуманная цитата"
	forged := legacyMarshal(t, r)
	writeFixture(t, raw, forged)
	var d AcceptedDecision
	_ = json.Unmarshal(readFixture(t, decision), &d)
	d.RawSHA256 = digest(forged)
	writeFixture(t, decision, legacyMarshal(t, d))
	runFail(t, "reconcile", config, raw, decision)
	// Missing previous ID is not an implicit retirement, even with a perfectly valid candidate.
	raw, decision = acceptedInputs(t, config, "missing", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runFail(t, "reconcile", config, raw, decision)
	if !bytes.Equal(before, readFixture(t, filepath.Join(base, "runs", acceptedFile))) {
		t.Fatal("A07: отказ изменил прежний журнал")
	}
	raw, decision = acceptedInputs(t, config, "stale", []legacyCandidate{c}, acceptOperation("rebind", []string{"REQ-AI-001"}, "C001"))
	writeFixture(t, filepath.Join(base, "source/rules.md"), append([]byte("Вводная изменилась.\n"), readFixture(t, filepath.Join(base, "source/rules.md"))...))
	runFail(t, "reconcile", config, raw, decision)
	if !bytes.Equal(before, readFixture(t, filepath.Join(base, "runs", acceptedFile))) {
		t.Fatal("A07: stale изменил журнал")
	}
}

func TestAcceptedConcurrency(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит", 2, 2)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := []*exec.Cmd{}
	for i, action := range []string{"accept", "reject"} {
		raw, decision := acceptedInputs(t, config, fmt.Sprintf("race-%d", i), []legacyCandidate{c}, acceptOperation(action, []string{}, "C001"))
		commands = append(commands, exec.Command(exe, "-test.run=^TestContract$", "--", "--audit-child", "reconcile", config, raw, decision))
	}
	for _, cmd := range commands {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	successes := 0
	for _, cmd := range commands {
		if cmd.Wait() == nil {
			successes++
		}
	}
	if successes != 1 || len(acceptedRead(t, config).History) != 1 {
		t.Fatal("A09: потеря обновления или два успешных stale-пакета")
	}
}

func TestAcceptedSourcesAndAudit(t *testing.T) {
	config, base := fixture(t)
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	declared, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, append(readFixture(t, config), []byte("index_mode: accepted\n")...))
	// Existing five-case corpus and gold go through the primary accepted pipeline.
	candidates := []legacyCandidate{}
	ops := []AcceptedOperation{}
	for i, req := range declared.Requirements {
		id := fmt.Sprintf("C%03d", i+1)
		candidate := legacyCandidate{id, req.Condition, req.Statement, []string{}, "clear", []string{}, []Citation{req.Source}}
		if i == 4 {
			candidate.Clarity, candidate.Unresolved = "ambiguous", []string{"Стандарт не задан"}
		}
		candidates = append(candidates, candidate)
		op := acceptOperation("accept", []string{}, id)
		op.Targets[0].Title, op.Targets[0].Verification = req.Title, req.Verification
		ops = append(ops, op)
	}
	// A10/A11: one requirement has several nonadjacent fragments and a second file.
	writeFixture(t, filepath.Join(base, "source/clarification.md"), []byte("# Уточнение\nУсловия исходной нормы сохраняются.\n"))
	writeFixture(t, config, bytes.Replace(readFixture(t, config), []byte("[rules.md]"), []byte("[rules.md, clarification.md]"), 1))
	candidates[0].Citations = append(candidates[0].Citations,
		candidateAt(t, base, "rules.md", "C099", "Вводная", 1, 1).Citations[0],
		candidateAt(t, base, "clarification.md", "C099", "Уточнение", 2, 2).Citations[0])
	candidates[0].Exceptions = []string{"Исключения явно не установлены"}
	raw, decision := acceptedInputs(t, config, "corpus", candidates, ops...)
	runOK(t, "reconcile", config, raw, decision)
	batch := runOK(t, "prepare", config, "accepted-corpus").(TaskBatch)
	if len(batch.Tasks) != 2 || len(batch.Tasks[0].Requirements) != 5 {
		t.Fatal("A12: потеряны назначения")
	}
	for _, task := range batch.Tasks {
		oldTask := task
		oldTask.Requirements = declared.Requirements
		result := sampleResult(t, oldTask, filepath.Join(base, "source"))
		for i := range result.Assessments {
			result.Assessments[i].RequirementID = task.Requirements[i].ID
			result.Assessments[i].Spec = task.Requirements[i].Accepted.Citations
		}
		path := filepath.Join(base, task.TaskID+".json")
		valid := legacyMarshal(t, result)
		result.Assessments[0].Spec = []Citation{task.Requirements[1].Source}
		writeFixture(t, path, legacyMarshal(t, result))
		runFail(t, "submit", config, "accepted-corpus", task.TaskID, path)
		_ = json.Unmarshal(valid, &result)
		result.Assessments[4].Specification = "clear"
		writeFixture(t, path, legacyMarshal(t, result))
		runFail(t, "submit", config, "accepted-corpus", task.TaskID, path)
		_ = json.Unmarshal(valid, &result)
		result.SnapshotID = declared.SnapshotID
		writeFixture(t, path, legacyMarshal(t, result))
		runFail(t, "submit", config, "accepted-corpus", task.TaskID, path)
		writeFixture(t, path, valid)
		runOK(t, "submit", config, "accepted-corpus", task.TaskID, path)
	}
	runOK(t, "report", config, "accepted-corpus")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/accepted-corpus/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if !report.DeliveryComplete || report.Accepted == nil || !report.HostReconciliationRequired || report.CompletenessBasis != "accepted_requirements_only" || report.SemanticCompletenessProven || report.Requirements[1].Roles[0].Assessment.Assertion != "weak" || report.Requirements[2].Roles[0].Assessment.Implementation != "contradicted" || report.Requirements[3].Roles[0].Assessment.Implementation != "unknown" {
		t.Fatal("A12: принятый индекс усилил выводы")
	}
	html := string(readFixture(t, filepath.Join(base, "runs/accepted-corpus/report.html")))
	for _, fragment := range []string{"accepted_requirements_only", "clarification.md", "Условия исходной нормы сохраняются.", "Стандарт не задан", "Редакция: 1"} {
		if !strings.Contains(html, fragment) {
			t.Fatal("A10/A11/A12: неполный HTML", fragment)
		}
	}
	if report.Requirements[0].Roles[0].Executions[0].State != "not_recorded" {
		t.Fatal("A12: импортировано несуществующее исполнение")
	}
	// Modification outside the main citation still invalidates the whole accepted source set.
	writeFixture(t, filepath.Join(base, "source/clarification.md"), []byte("Приоритет изменён.\n"))
	runFail(t, "index", config)
	if runOK(t, "status", config, "accepted-corpus").(Status).Freshness != "stale" {
		t.Fatal("A11: изменение второго файла не учтено")
	}
}

func TestAcceptedStorageGuards(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит", 2, 2)
	raw, decision := acceptedInputs(t, config, "initial", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	path := filepath.Join(base, "runs", acceptedFile)
	valid := readFixture(t, path)
	for _, content := range [][]byte{
		[]byte(`{"version":1,"commits":null}`), []byte(`{"version":1,"version":1,"commits":[]}`),
		[]byte(`{"version":1,"commits":[],"extra":0}`), bytes.Repeat([]byte(" "), maxState+1),
	} {
		writeFixture(t, path, content)
		runFail(t, "reconcile", config)
		runFail(t, "index", config)
	}
	writeFixture(t, path, valid)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	runFail(t, "reconcile", config)
	runFail(t, "reconcile", config, raw, decision)
	if !bytes.Equal(valid, readFixture(t, path+".saved")) {
		t.Fatal("symlink позволил изменить чужой файл")
	}
	// Input bounds are checked independently of the source provenance and before publication.
	cfg, _ := loadConfig(config)
	set, _, err := acceptedSources(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := legacyRaw{1, set, []legacyCandidate{}, []string{}}
	for i := 1; i <= 65; i++ {
		item := c
		item.ID = fmt.Sprintf("C%03d", i)
		r.Candidates = append(r.Candidates, item)
	}
	if legacyShape(r) == nil {
		t.Fatal("превышение 64 кандидатов принято")
	}
	d := AcceptedDecision{Version: 1, DecisionID: "limit", BaseIndex: emptyAccepted().Head, RawSHA256: digest(nil), Operations: make([]AcceptedOperation, 129)}
	if _, err := applyAccepted(emptyAccepted(), legacyRaw{}, d, nil, nil); err == nil {
		t.Fatal("превышение 128 операций принято")
	}
}

func TestAcceptedExactHistory(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
	c.Clarity, c.Unresolved, c.Exceptions = "ambiguous", []string{"Какой срок?"}, []string{"Исключения не определены"}
	c.Citations = append(c.Citations, candidateAt(t, base, "rules.md", "C099", "Заголовок", 1, 1).Citations[0])
	example := candidateAt(t, base, "rules.md", "C002", "Две минуты", 5, 5)
	deferred := candidateAt(t, base, "rules.md", "C003", "Название", 3, 3)
	ops := []AcceptedOperation{acceptOperation("accept", []string{}, c.ID), acceptOperation("reject", []string{}, example.ID), acceptOperation("defer", []string{}, deferred.ID)}
	ops[0].Targets[0] = AcceptedTarget{c.ID, "Уведомление", "Проверить <срок>"}
	ops[0].Reason, ops[1].Reason, ops[2].Reason = "Принята известная часть", "Это пример", "Нужен отдельный разбор"
	rawPath, decisionPath := acceptedInputs(t, config, "exact", []legacyCandidate{c, example, deferred}, ops...)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, readFixture(t, rawPath), "", "\t"); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(" \r\n"), bytes.ReplaceAll(pretty.Bytes(), []byte("\n"), []byte("\r\n"))...)
	raw = append(raw, '\t', '\r', '\n')
	var decision AcceptedDecision
	if err := json.Unmarshal(readFixture(t, decisionPath), &decision); err != nil {
		t.Fatal(err)
	}
	decision.RawSHA256 = digest(raw)
	decisionBytes := append(legacyMarshal(t, decision), '\r', '\n', '\t')
	writeFixture(t, rawPath, raw)
	writeFixture(t, decisionPath, decisionBytes)
	runOK(t, "reconcile", config, rawPath, decisionPath)
	view := runOK(t, "reconcile", config).(map[string]any)
	ledger := view["journal"].(acceptedLedger)
	if len(ledger.Commits) != 1 || ledger.Commits[0].Raw != string(raw) || ledger.Commits[0].Decision != string(decisionBytes) {
		t.Fatal("первая запись нормализовала исходные bytes")
	}
	state := acceptedRead(t, config)
	if !reflect.DeepEqual(state.History[0].Decision, decision) || len(state.Records) != 1 || state.Records[0].Reason != ops[0].Reason {
		t.Fatal("потеряны действия, причины или история")
	}
	want := Requirement{ID: "REQ-AI-001", Title: "Уведомление", Condition: c.Condition, Statement: c.Statement, Verification: "Проверить <срок>",
		Source: c.Citations[0], ContentHash: state.Records[0].Requirement.ContentHash,
		Accepted: &AcceptedDetails{1, c.Exceptions, c.Clarity, c.Unresolved, c.Citations, []string{}}}
	batch := runOK(t, "prepare", config, "exact").(TaskBatch)
	for _, task := range batch.Tasks {
		if len(task.Requirements) != 1 || !reflect.DeepEqual(task.Requirements[0], want) {
			t.Fatal("задание потеряло принятые поля")
		}
	}
	runOK(t, "report", config, "exact")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/exact/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Requirements[0].Requirement, want) || !reflect.DeepEqual(report.Accepted.History[0].Decision, decision) {
		t.Fatal("JSON потерял принятые поля или причины")
	}
	html := string(readFixture(t, filepath.Join(base, "runs/exact/report.html")))
	for _, part := range []string{"Способ проверки (хост): Проверить &lt;срок&gt;", "reject · previous: [] · Это пример", "defer · previous: [] · Нужен отдельный разбор", "Вопрос: Какой срок?", "Исключение: Исключения не определены"} {
		if !strings.Contains(html, part) {
			t.Fatal("HTML не сохраняет различие нормы, метода и решения", part)
		}
	}
	retire := acceptOperation("retire", []string{"REQ-AI-001"})
	retire.Reason = "Явная отмена после пересмотра"
	rawPath, decisionPath = acceptedInputs(t, config, "retire-exact", []legacyCandidate{}, retire)
	runOK(t, "reconcile", config, rawPath, decisionPath)
	if got := acceptedRead(t, config); got.Records[0].Reason != retire.Reason || got.History[1].Decision.Operations[0].Reason != retire.Reason {
		t.Fatal("потеряна точная причина отмены")
	}
}

func TestAcceptedHashVector(t *testing.T) {
	// Fixed vectors independently computed with Ruby SHA-256, not production helpers.
	const seed = "64bc12d0fc9a6d16a4f0d9ccf044804f642834bc26d34e1c90822351f5c60ba7"
	rawBytes := []byte(`{"version":1,"source_set":[],"candidates":[],"limitations":[]}`)
	decisionBytes := []byte(`{"version":1,"decision_id":"hash-vector","base_index":"64bc12d0fc9a6d16a4f0d9ccf044804f642834bc26d34e1c90822351f5c60ba7","raw_sha256":"decf4b6077a9f172574b20c9aefe4c18b9f036463c0889e6caf8e5f947cd96d7","operations":[]}`)
	if emptyAccepted().Head != seed {
		t.Fatal("изменён seed принятого индекса")
	}
	var raw legacyRaw
	var decision AcceptedDecision
	if err := legacyDecode(rawBytes, &raw); err != nil {
		t.Fatal(err)
	}
	if err := legacyDecode(decisionBytes, &decision); err != nil {
		t.Fatal(err)
	}
	state, err := applyAccepted(emptyAccepted(), raw, decision, rawBytes, decisionBytes)
	if err != nil || state.Head != "4ecd27261cba2e086d35b5943b49c9383c9fc78e866c1bfd09a9a66aa7d7f0cf" {
		t.Fatal("изменена цепочка head", err)
	}
}

func TestAcceptedAppendBounds(t *testing.T) {
	t.Run("commits-and-stale-base", func(t *testing.T) {
		config, base := acceptedFixture(t)
		raw, stale := acceptedInputs(t, config, "stale-empty", []legacyCandidate{})
		var lastRaw, lastDecision string
		for i := 1; i <= 128; i++ {
			lastRaw, lastDecision = acceptedInputs(t, config, fmt.Sprintf("commit-%03d", i), []legacyCandidate{})
			runOK(t, "reconcile", config, lastRaw, lastDecision)
			if i == 1 {
				before := readFixture(t, filepath.Join(base, "runs", acceptedFile))
				if _, err := execute([]string{"reconcile", config, raw, stale}); err == nil || !strings.Contains(err.Error(), "stale base_index") {
					t.Fatal("stale CAS на пустом active не отклонён нужным guard", err)
				}
				if !bytes.Equal(before, readFixture(t, filepath.Join(base, "runs", acceptedFile))) {
					t.Fatal("stale CAS изменил журнал")
				}
			}
		}
		before := readFixture(t, filepath.Join(base, "runs", acceptedFile))
		if len(acceptedRead(t, config).History) != 128 || runOK(t, "reconcile", config, lastRaw, lastDecision).(map[string]any)["duplicate"] != true {
			t.Fatal("128-й пакет или duplicate на границе не приняты")
		}
		raw, decision := acceptedInputs(t, config, "commit-129", []legacyCandidate{})
		if _, err := execute([]string{"reconcile", config, raw, decision}); err == nil || !strings.Contains(err.Error(), "лимит 128 пакетов") {
			t.Fatal("129-й пакет не отклонён нужным guard", err)
		}
		if !bytes.Equal(before, readFixture(t, filepath.Join(base, "runs", acceptedFile))) {
			t.Fatal("append на пределе изменил историю")
		}
	})
	t.Run("serialized-bytes", func(t *testing.T) {
		config, base := acceptedFixture(t)
		cfg, err := loadConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		set, _, err := acceptedSources(cfg)
		if err != nil {
			t.Fatal(err)
		}
		rawValue := legacyRaw{1, set, []legacyCandidate{}, []string{}}
		plain := legacyMarshal(t, rawValue)
		state, ledger := emptyAccepted(), acceptedLedger{1, []acceptedCommit{}}
		// Valid large whitespace in old raw strings, not a corrupt padded ledger.
		for i := 0; i < 4; i++ {
			raw := append(append([]byte{}, plain...), bytes.Repeat([]byte("\t"), 3<<20)...)
			decision := AcceptedDecision{1, fmt.Sprintf("large-%d", i), state.Head, digest(raw), []AcceptedOperation{}}
			encoded := legacyMarshal(t, decision)
			state, err = applyAccepted(state, rawValue, decision, raw, encoded)
			if err != nil {
				t.Fatal(err)
			}
			ledger.Commits = append(ledger.Commits, acceptedCommit{string(raw), string(encoded)})
		}
		// Two large raw strings in the next commit's raw and decision can fill the remaining space.
		decision := AcceptedDecision{1, "exact-bytes", state.Head, digest(plain), []AcceptedOperation{}}
		makeCommit := func(extra int) acceptedCommit {
			raw := append(append([]byte{}, plain...), bytes.Repeat([]byte("\t"), 3<<20)...)
			decision.RawSHA256 = digest(raw)
			encoded := append(legacyMarshal(t, decision), bytes.Repeat([]byte(" "), extra)...)
			if len(raw) > maxResult || len(encoded) > maxResult {
				t.Fatal("некорректная fixture: input превышает 4 MiB")
			}
			return acceptedCommit{string(raw), string(encoded)}
		}
		probe := acceptedLedger{1, append(append([]acceptedCommit{}, ledger.Commits...), makeCommit(0))}
		encoded, err := json.MarshalIndent(probe, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		padding := maxState - len(encoded) - 1
		if padding <= 0 {
			t.Fatal("нет места для boundary fixture")
		}
		path := filepath.Join(base, "runs", acceptedFile)
		before := legacyMarshal(t, ledger)
		writeFixture(t, path, before)
		rawPath, decisionPath := filepath.Join(base, "boundary-raw.json"), filepath.Join(base, "boundary-decision.json")
		for _, extra := range []int{1, 0} {
			commit := makeCommit(padding + extra)
			writeFixture(t, rawPath, []byte(commit.Raw))
			writeFixture(t, decisionPath, []byte(commit.Decision))
			_, err := execute([]string{"reconcile", config, rawPath, decisionPath})
			if extra == 1 {
				if err == nil || !strings.Contains(err.Error(), "32 MiB") {
					t.Fatal("append max+1 не отклонён размером журнала", err)
				}
				if !bytes.Equal(before, readFixture(t, path)) {
					t.Fatal("append max+1 изменил журнал")
				}
			} else if err != nil {
				t.Fatal("ровно 32 MiB не принято", err)
			}
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != int64(maxState) || len(acceptedRead(t, config).History) != 5 {
			t.Fatal("не проверена точная граница сериализованного журнала", err)
		}
	})
}

func TestDeclaredHistoricalRun(t *testing.T) {
	config, base := fixture(t)
	writeFixture(t, config, bytes.Replace(readFixture(t, config), []byte("runtime: {kind: none}"), []byte("runtime: {kind: go, paths: ['.'], tests: [], timeout_seconds: 30}"), 1))
	// Actual pre-accepted run; never regenerated by prepare or by the current binary.
	for name, hash := range map[string]string{
		"manifest.json": "01af1dd07bbee7d9fbc373952b755d0df727e2905ce2b1529c4b7cd062f4521e",
		"state.json":    "1bbe9eb1a22946d544e820b054d0524795439846da45d38ba635b8cbef5b6be9",
	} {
		data := readFixture(t, "../acceptance/declared-v01/"+name)
		if digest(data) != hash {
			t.Fatal("исторический эталон изменён", name)
		}
		writeFixture(t, filepath.Join(base, "runs/blind-v1", name), data)
	}
	status := runOK(t, "status", config, "blind-v1").(Status)
	if status.SnapshotID != "b1de29c6196a72dda225c946bf3b238e2677329dcd98735dca83c6f547f79f21" || status.Freshness != "fresh" || !status.DeliveryComplete || status.RequirementsTotal != 5 {
		t.Fatal("несовместимость с историческим declared run")
	}
	runOK(t, "report", config, "blind-v1")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/blind-v1/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Executions) != 1 || report.Executions[0].ID != "observed-green" || report.Executions[0].State != "passed" || report.Requirements[2].Roles[0].Assessment.Implementation != "contradicted" {
		t.Fatal("чтение старого run потеряло или усилило свидетельства")
	}
}

// tool-spec §19.1: the apply response carries the same view as a read, with measured freshness and record-level revision/clarity.
func TestReconcileApplyView(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	unclear := candidateAt(t, base, "rules.md", "C003", "Уведомить", 4, 4)
	unclear.Clarity, unclear.Unresolved = "ambiguous", []string{"Какой срок?"}
	raw, decision := acceptedInputs(t, config, "view", []legacyCandidate{c, unclear},
		acceptOperation("accept", []string{}, "C001"), acceptOperation("accept", []string{}, "C003"))
	applied := runOK(t, "reconcile", config, raw, decision).(map[string]any)
	if applied["accepted"] != true || applied["duplicate"] != false || applied["freshness"] != "fresh" || applied["semantic_completeness_proven"] != false {
		t.Fatalf("apply view: %v", applied)
	}
	for _, key := range []string{"base_index", "source_set", "records", "history", "journal"} {
		if _, ok := applied[key]; !ok {
			t.Fatalf("apply без %s", key)
		}
	}
	records := applied["records"].([]AcceptedRecord)
	if len(records) != 2 {
		t.Fatalf("records: %d", len(records))
	}
	for _, record := range records {
		if record.Revision != record.Requirement.Accepted.Revision || record.Clarity != record.Requirement.Accepted.Clarity || record.Revision != 1 {
			t.Fatalf("record %s: %+v", record.Requirement.ID, record)
		}
	}
	if records[1].Clarity != "ambiguous" {
		t.Fatal("clarity не продублирована")
	}
	read := runOK(t, "reconcile", config).(map[string]any)
	if read["freshness"] != "fresh" || len(read["records"].([]AcceptedRecord)) != 2 || read["records"].([]AcceptedRecord)[1].Clarity != "ambiguous" {
		t.Fatalf("read view: %v", read["freshness"])
	}
	replay := runOK(t, "reconcile", config, raw, decision).(map[string]any)
	if replay["duplicate"] != true || replay["freshness_checked"] != false {
		t.Fatalf("replay: %v", replay)
	}
	if _, ok := replay["freshness"]; ok {
		t.Fatal("duplicate не подтверждает свежесть")
	}
	// A specification edited between decisions is reported honestly, not assumed fresh.
	rules := filepath.Join(base, "source", "rules.md")
	prior := readFixture(t, rules)
	writeFixture(t, rules, append(prior, []byte("\n\nДополнение.\n")...))
	if runOK(t, "reconcile", config).(map[string]any)["freshness"] != "stale" {
		t.Fatal("устаревание скрыто")
	}
	writeFixture(t, rules, prior)
}
