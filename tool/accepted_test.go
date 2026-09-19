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
		target := AcceptedTarget{id, "Норма", "Проверить точный результат", ""}
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
	set, _, _, err := acceptedSources(cfg)
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
	set, _, _, err := acceptedSources(cfg)
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
	ops[0].Targets[0] = AcceptedTarget{c.ID, "Уведомление", "Проверить <срок>", ""}
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
		set, _, _, err := acceptedSources(cfg)
		if err != nil {
			t.Fatal(err)
		}
		rawValue := legacyRaw{1, set, []legacyCandidate{}, []string{}}
		plain := legacyMarshal(t, rawValue)
		state, ledger := emptyAccepted(), acceptedLedger{ledgerVersion, []acceptedCommit{}}
		// Valid large whitespace in old raw strings, not a corrupt padded ledger.
		for i := 0; i < 4; i++ {
			raw := append(append([]byte{}, plain...), bytes.Repeat([]byte("\t"), 3<<20)...)
			decision := AcceptedDecision{1, fmt.Sprintf("large-%d", i), state.Head, digest(raw), []AcceptedOperation{}}
			encoded := legacyMarshal(t, decision)
			state, err = applyAccepted(state, rawValue, decision, raw, encoded)
			if err != nil {
				t.Fatal(err)
			}
			ledger.Commits = append(ledger.Commits, acceptedCommit{string(raw), string(encoded), []string{}, true})
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
			return acceptedCommit{string(raw), string(encoded), []string{}, true}
		}
		probe := acceptedLedger{ledgerVersion, append(append([]acceptedCommit{}, ledger.Commits...), makeCommit(0))}
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

// referenceFixture — accepted-корпус с одним справочным файлом (tool-spec §20, REQ-SA-041).
func referenceFixture(t *testing.T) (string, string) {
	t.Helper()
	config, base := acceptedFixture(t)
	writeFixture(t, filepath.Join(base, "source/clarification.md"), []byte("# Уточнение\nУсловия исходной нормы сохраняются.\nСрок задержки определяется договором.\n"))
	writeFixture(t, config, append(readFixture(t, config), []byte("references: {paths: [clarification.md]}\n")...))
	return config, base
}

func TestReferenceSources(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		config, base := referenceFixture(t)
		cfg, err := loadConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		m, err := scanSnapshot(cfg)
		if err != nil {
			t.Fatal(err)
		}
		flagged := map[string]bool{}
		for _, file := range m.Files {
			flagged[file.Path] = file.Reference
			if file.Reference && file.Kind != "spec" {
				t.Fatal("справочный файл должен оставаться kind=spec", file)
			}
		}
		if !flagged["clarification.md"] || flagged["rules.md"] || flagged["source.go"] {
			t.Fatal("признак reference расставлен неверно", flagged)
		}
		withReferences, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(withReferences, []byte(`"references":{`)) || !bytes.Contains(withReferences, []byte(`"reference":true`)) {
			t.Fatal("manifest не показывает группу references")
		}
		plain := cfg
		plain.References = nil
		pm, err := scanSnapshot(plain)
		if err != nil {
			t.Fatal(err)
		}
		without, err := json.Marshal(pm)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(without, []byte("reference")) || bytes.Equal(withReferences, without) {
			t.Fatal("без группы manifest должен остаться прежним и отличаться от manifest с группой")
		}
		// REQ-SA-041: только accepted-режим, непустой paths, файл в одной категории, непустая группа.
		for name, mutate := range map[string]func([]byte) []byte{
			"declared": func(c []byte) []byte { return bytes.Replace(c, []byte("index_mode: accepted\n"), nil, 1) },
			"empty_paths": func(c []byte) []byte {
				return bytes.Replace(c, []byte("references: {paths: [clarification.md]}"), []byte("references: {paths: []}"), 1)
			},
			"overlap": func(c []byte) []byte {
				return bytes.Replace(c, []byte("references: {paths: [clarification.md]}"), []byte("references: {paths: [rules.md]}"), 1)
			},
			"no_files": func(c []byte) []byte {
				return bytes.Replace(c, []byte("references: {paths: [clarification.md]}"), []byte("references: {paths: [clarification.md], include: ['*.txt']}"), 1)
			},
		} {
			broken := filepath.Join(base, name+".yaml")
			writeFixture(t, broken, mutate(readFixture(t, config)))
			cfg, err := loadConfig(broken)
			if err == nil {
				_, err = scanSnapshot(cfg)
			}
			if err == nil {
				t.Fatalf("%s: конфигурация должна быть отклонена", name)
			}
		}
	})
	t.Run("reconcile", func(t *testing.T) {
		config, base := referenceFixture(t)
		ledger := filepath.Join(base, "runs", acceptedFile)
		view := runOK(t, "reconcile", config).(map[string]any)
		flags := map[string]bool{}
		for _, source := range view["source_set"].([]acceptedSource) {
			flags[source.Path] = source.Reference
		}
		if len(flags) != 2 || !flags["clarification.md"] || flags["rules.md"] {
			t.Fatal("читающий reconcile должен показать признак reference", flags)
		}
		mixed := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
		mixed.Citations = append(mixed.Citations, candidateAt(t, base, "clarification.md", "C001", "", 3, 3).Citations[0])
		only := candidateAt(t, base, "clarification.md", "C002", "Срок по договору", 3, 3)
		// Кандидат только со справочными цитатами не становится нормой; журнал остаётся прежним.
		raw, decision := acceptedInputs(t, config, "ref-only", []legacyCandidate{mixed, only},
			acceptOperation("accept", []string{}, "C001"), acceptOperation("accept", []string{}, "C002"))
		if _, err := execute([]string{"reconcile", config, raw, decision}); err == nil || !strings.Contains(err.Error(), "C002") {
			t.Fatal("ожидался отказ с ID кандидата", err)
		}
		if _, err := os.Stat(ledger); !os.IsNotExist(err) {
			t.Fatal("отклонённый пакет не должен создавать журнал")
		}
		// Тот же кандидат как reject/defer допустим; смешанный принимается со всеми цитатами.
		raw, decision = acceptedInputs(t, config, "mixed", []legacyCandidate{mixed, only},
			acceptOperation("accept", []string{}, "C001"), acceptOperation("defer", []string{}, "C002"))
		applied := runOK(t, "reconcile", config, raw, decision).(map[string]any)
		if applied["accepted"] != true || applied["freshness"] != "fresh" {
			t.Fatal("apply должен вернуть полный view", applied)
		}
		state := acceptedRead(t, config)
		if len(state.Records) != 1 || len(state.Records[0].Requirement.Accepted.Citations) != 2 || state.Records[0].Requirement.Accepted.Citations[1].Path != "clarification.md" {
			t.Fatal("справочная цитата потеряна при приёмке", state.Records)
		}
		before := readFixture(t, ledger)
		// split с target только из справочного файла отклоняется; raw без справочного файла в source_set — прежний отказ.
		raw, decision = acceptedInputs(t, config, "split", []legacyCandidate{mixed, only},
			acceptOperation("split", []string{"REQ-AI-001"}, "C001", "C002"))
		if _, err := execute([]string{"reconcile", config, raw, decision}); err == nil || !strings.Contains(err.Error(), "C002") {
			t.Fatal("split со справочным target должен быть отклонён guard", err)
		}
		cfg, err := loadConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		set, _, _, err := acceptedSources(cfg)
		if err != nil {
			t.Fatal(err)
		}
		partial := []legacySource{}
		for _, source := range set {
			if source.Path != "clarification.md" {
				partial = append(partial, source)
			}
		}
		rawBytes := legacyMarshal(t, legacyRaw{1, partial, []legacyCandidate{candidateAt(t, base, "rules.md", "C001", "Лимит", 2, 2)}, []string{}})
		decisionBytes := legacyMarshal(t, AcceptedDecision{1, "partial", state.Head, digest(rawBytes), []AcceptedOperation{acceptOperation("rebind", []string{"REQ-AI-001"}, "C001")}})
		writeFixture(t, filepath.Join(base, "partial-raw.json"), rawBytes)
		writeFixture(t, filepath.Join(base, "partial-decision.json"), decisionBytes)
		runFail(t, "reconcile", config, filepath.Join(base, "partial-raw.json"), filepath.Join(base, "partial-decision.json"))
		if !bytes.Equal(before, readFixture(t, ledger)) {
			t.Fatal("отклонённые пакеты изменили журнал")
		}
	})
	// Принятая норма с нормативной и справочной цитатами; роль цитирует справочный диапазон.
	acceptMixed := func(t *testing.T, config, base string) {
		t.Helper()
		mixed := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
		mixed.Citations = append(mixed.Citations, candidateAt(t, base, "clarification.md", "C001", "", 3, 3).Citations[0])
		raw, decision := acceptedInputs(t, config, "mixed", []legacyCandidate{mixed}, acceptOperation("accept", []string{}, "C001"))
		runOK(t, "reconcile", config, raw, decision)
	}
	roleResult := func(t *testing.T, task Task, spec Citation) string {
		t.Helper()
		result := Result{TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope,
			Summary: "Проверка справочной цитаты", Limitations: []string{}, Assessments: []Assessment{{
				RequirementID: task.Requirements[0].ID, Specification: "clear", Implementation: "unknown", Assertion: "unknown",
				Statement: "Реализация не найдена", Spec: []Citation{spec}, Code: []Citation{}, Tests: []TestCitation{}, Limitations: []string{}}}}
		path := filepath.Join(t.TempDir(), task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		return path
	}
	t.Run("roles", func(t *testing.T) {
		config, base := referenceFixture(t)
		acceptMixed(t, config, base)
		batch := runOK(t, "prepare", config, "ref-run").(TaskBatch)
		task := batch.Tasks[0]
		citations := task.Requirements[0].Accepted.Citations
		if len(batch.Tasks) != 2 || len(citations) != 2 || citations[1].Path != "clarification.md" {
			t.Fatal("задание должно нести справочную цитату", task.Requirements)
		}
		flagged := map[string]bool{}
		for _, file := range batch.Files {
			flagged[file.Path] = file.Reference
		}
		if !flagged["clarification.md"] || flagged["rules.md"] {
			t.Fatal("TaskBatch.files должен нести признак reference", flagged)
		}
		inside := roleResult(t, task, citations[1])
		runOK(t, "validate", config, "ref-run", task.TaskID, inside)
		runOK(t, "submit", config, "ref-run", task.TaskID, inside)
		outside := roleResult(t, batch.Tasks[1], candidateAt(t, base, "clarification.md", "C009", "", 2, 2).Citations[0])
		if _, err := execute([]string{"validate", config, "ref-run", batch.Tasks[1].TaskID, outside}); err == nil || !strings.Contains(err.Error(), "вне блока") {
			t.Fatal("цитата справочного файла вне принятого диапазона должна быть отклонена", err)
		}
	})
	t.Run("report", func(t *testing.T) {
		config, base := referenceFixture(t)
		acceptMixed(t, config, base)
		batch := runOK(t, "prepare", config, "ref-run").(TaskBatch)
		runOK(t, "submit", config, "ref-run", batch.Tasks[0].TaskID, roleResult(t, batch.Tasks[0], batch.Tasks[0].Requirements[0].Accepted.Citations[1]))
		runOK(t, "report", config, "ref-run")
		var report Report
		if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/ref-run/report.json")), &report); err != nil {
			t.Fatal(err)
		}
		sections := map[string]AuditSection{}
		for _, section := range report.Navigation.Sections {
			sections[section.Path] = section
		}
		if !sections["clarification.md"].Reference || sections["clarification.md"].Total != 0 || sections["rules.md"].Reference || sections["rules.md"].Total != 1 {
			t.Fatal("секции ТЗ должны отличать справочный файл", report.Navigation.Sections)
		}
		groups := map[string]NavigationGroup{}
		for _, group := range report.Navigation.Groups {
			groups[group.Kind] = group
		}
		if groups["reference"].Files != 1 || groups["spec"].Files != 1 || groups["reference"].Selection.Paths[0] != "clarification.md" {
			t.Fatal("группа reference должна считать только справочные файлы", report.Navigation.Groups)
		}
		html := string(readFixture(t, filepath.Join(base, "runs/ref-run/report.html")))
		if !strings.Contains(html, "справочный источник — кандидаты не извлекались") || !strings.Contains(html, `data-reference="yes"`) {
			t.Fatal("HTML не помечает справочный источник")
		}
		// Review 2.6 / §59: справочная цитата первой не делает справочный файл владельцем нормы в сводке.
		config, base = referenceFixture(t)
		refFirst := candidateAt(t, base, "clarification.md", "C002", "Срок задержки — рабочий день", 3, 3)
		refFirst.Citations = append(refFirst.Citations, candidateAt(t, base, "rules.md", "C002", "", 4, 4).Citations[0])
		mixed := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
		mixed.Citations = append(mixed.Citations, candidateAt(t, base, "clarification.md", "C001", "", 3, 3).Citations[0])
		raw, decision := acceptedInputs(t, config, "ref-first", []legacyCandidate{mixed, refFirst}, acceptOperation("accept", []string{}, "C001"), acceptOperation("accept", []string{}, "C002"))
		runOK(t, "reconcile", config, raw, decision)
		runOK(t, "prepare", config, "ref-run-2")
		runOK(t, "report", config, "ref-run-2")
		if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/ref-run-2/report.json")), &report); err != nil {
			t.Fatal(err)
		}
		sections = map[string]AuditSection{}
		for _, section := range report.Navigation.Sections {
			sections[section.Path] = section
		}
		var refFirstRow *RequirementReport
		for i := range report.Requirements {
			if report.Requirements[i].Requirement.Source.Path == "clarification.md" {
				refFirstRow = &report.Requirements[i]
			}
		}
		if refFirstRow == nil || refFirstRow.Navigation.Section != "rules.md" || sections["clarification.md"].Total != 0 || sections["rules.md"].Total != 2 {
			t.Fatal("владелец нормы в сводке — первая нормативная цитата, source не меняется", report.Navigation.Sections)
		}
		// In-memory: карта без run различает группы по признаку, а не по kind.
		m := Manifest{Config: Config{ProjectRoot: "/work", References: &Sources{Paths: []string{"TZ/defs.md"}}}, Files: []SourceFile{
			{Path: "TZ/defs.md", Kind: "spec", Reference: true}, {Path: "TZ/spec.md", Kind: "spec"}, {Path: "src/code.go", Kind: "code"},
		}}
		r := Report{Status: Status{Freshness: "fresh"}}
		buildNavigation(&r, m)
		if len(r.Navigation.Groups) != 4 || r.Navigation.Groups[1].Kind != "reference" || r.Navigation.Groups[1].Files != 1 || r.Navigation.Groups[0].Files != 1 || !r.Navigation.Sections[0].Reference || r.Navigation.Sections[1].Reference {
			t.Fatal("buildNavigation: группа и секция reference", r.Navigation.Groups, r.Navigation.Sections)
		}
	})
	t.Run("stale", func(t *testing.T) {
		config, base := referenceFixture(t)
		acceptMixed(t, config, base)
		runOK(t, "prepare", config, "ref-run")
		// (а) правка справочного файла делает индекс stale наравне с нормативным.
		writeFixture(t, filepath.Join(base, "source/clarification.md"), []byte("# Уточнение\nУсловия исходной нормы сохраняются.\nСрок задержки — один рабочий день.\n"))
		runFail(t, "index", config)
		runFail(t, "prepare", config, "ref-run-2")
		if view := runOK(t, "reconcile", config).(map[string]any); view["freshness"] != "stale" {
			t.Fatal("правка справочного файла должна давать stale", view["freshness"])
		}
		if status := runOK(t, "status", config, "ref-run").(Status); status.Freshness != "stale" {
			t.Fatal("прежний run остаётся историей со stale", status)
		}
		// (б) путь пилота: индекс принят без references, группа добавлена позже.
		config, base = acceptedFixture(t)
		writeFixture(t, filepath.Join(base, "source/clarification.md"), []byte("# Уточнение\nУсловия исходной нормы сохраняются.\n"))
		raw, decision := acceptedInputs(t, config, "plain", []legacyCandidate{candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)}, acceptOperation("accept", []string{}, "C001"))
		runOK(t, "reconcile", config, raw, decision)
		runOK(t, "index", config)
		writeFixture(t, config, append(readFixture(t, config), []byte("references: {paths: [clarification.md]}\n")...))
		view := runOK(t, "reconcile", config).(map[string]any)
		flags := map[string]bool{}
		for _, source := range view["source_set"].([]acceptedSource) {
			flags[source.Path] = source.Reference
		}
		if view["freshness"] != "stale" || len(flags) != 2 || !flags["clarification.md"] {
			t.Fatal("добавление references после приёмки должно давать stale и показывать новый source_set", view["freshness"], flags)
		}
		runFail(t, "index", config)
	})
}

// tool-spec §22 / REQ-SA-043: reanchor keeps the accepted content, ID, revision and hash; only citations follow the candidate.
func TestReanchor(t *testing.T) {
	config, base := acceptedFixture(t)
	rules := filepath.Join(base, "source/rules.md")
	first := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, config, "first", []legacyCandidate{first}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	before := acceptedRead(t, config).Records[0]
	reanchor := func(previous string, candidate string) AcceptedOperation {
		return AcceptedOperation{"reanchor", []string{previous}, []AcceptedTarget{{candidate, "", "", ""}}, "Та же норма в новой редакции текста"}
	}
	// The text moves and the extractor rephrases the norm; the host keeps the accepted wording.
	writeFixture(t, rules, append([]byte("Вводная строка.\n"), readFixture(t, rules)...))
	moved := candidateAt(t, base, "rules.md", "C001", "Ограничение размера 8 МиБ", 3, 3)
	raw, decision = acceptedInputs(t, config, "reanchor", []legacyCandidate{moved}, reanchor("REQ-AI-001", "C001"))
	preview := checkView(t, config, raw, decision)
	if got := preview["assignments"].([]checkedAssignment); !reflect.DeepEqual(got, []checkedAssignment{{"C001", "REQ-AI-001", "reanchor", 1, []string{"REQ-AI-001"}}}) || len(preview["retired"].([]string)) != 0 {
		t.Fatal("dry-run reanchor", preview)
	}
	runOK(t, "reconcile", config, raw, decision)
	state := acceptedRead(t, config)
	after := state.Records[0]
	if after.Requirement.ID != "REQ-AI-001" || after.Revision != 1 || after.Requirement.ContentHash != before.Requirement.ContentHash ||
		after.Requirement.Title != before.Requirement.Title || after.Requirement.Statement != before.Requirement.Statement ||
		after.Requirement.Verification != before.Requirement.Verification || after.Clarity != before.Clarity ||
		!reflect.DeepEqual(after.Requirement.Accepted.Unresolved, before.Requirement.Accepted.Unresolved) {
		t.Fatal("reanchor изменил принятое содержание", before, after)
	}
	if !reflect.DeepEqual(after.Requirement.Accepted.Citations, moved.Citations) || after.Requirement.Source != moved.Citations[0] || after.Reason != "Та же норма в новой редакции текста" {
		t.Fatal("reanchor должен взять цитаты кандидата", after.Requirement.Accepted.Citations)
	}
	if !reflect.DeepEqual(state.History[1].Assignments, []AcceptedAssignment{{"C001", "REQ-AI-001"}}) || !reflect.DeepEqual(acceptedRead(t, config).Records, state.Records) {
		t.Fatal("история и повторное чтение журнала", state.History[1])
	}
	// Refusals: filled title/verification, unknown previous; the ledger stays as applied.
	ledgerBytes := readFixture(t, filepath.Join(base, "runs", acceptedFile))
	titled := reanchor("REQ-AI-001", "C001")
	titled.Targets[0].Title = "Норма"
	raw, decision = acceptedInputs(t, config, "titled", []legacyCandidate{moved}, titled)
	if _, err := execute([]string{"reconcile", config, raw, decision}); err == nil || !strings.Contains(err.Error(), "reanchor сохраняет") {
		t.Fatal("непустой title", err)
	}
	raw, decision = acceptedInputs(t, config, "unknown", []legacyCandidate{moved}, reanchor("REQ-AI-009", "C001"))
	if _, err := execute([]string{"reconcile", config, raw, decision}); err == nil || !strings.Contains(err.Error(), "previous") {
		t.Fatal("неизвестный previous", err)
	}
	if !bytes.Equal(ledgerBytes, readFixture(t, filepath.Join(base, "runs", acceptedFile))) {
		t.Fatal("отказы изменили журнал")
	}
	// Roles receive the new citations; the old range is no longer an accepted range.
	batch := runOK(t, "prepare", config, "re-run").(TaskBatch)
	task := batch.Tasks[0]
	if task.Requirements[0].Accepted.Citations[0].LineStart != 3 || task.Requirements[0].Accepted.Revision != 1 {
		t.Fatal("задание должно нести новые цитаты и прежнюю редакцию", task.Requirements[0])
	}
	result := func(spec Citation) string {
		path := filepath.Join(t.TempDir(), "result.json")
		writeFixture(t, path, legacyMarshal(t, Result{TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope,
			Summary: "Проверка после reanchor", Limitations: []string{}, Assessments: []Assessment{{RequirementID: "REQ-AI-001", Specification: "clear", Implementation: "unknown", Assertion: "unknown",
				Statement: "Реализация не найдена", Spec: []Citation{spec}, Code: []Citation{}, Tests: []TestCitation{}, Limitations: []string{}}}}))
		return path
	}
	runOK(t, "validate", config, "re-run", task.TaskID, result(moved.Citations[0]))
	if _, err := execute([]string{"validate", config, "re-run", task.TaskID, result(candidateAt(t, base, "rules.md", "C009", "", 2, 2).Citations[0])}); err == nil || !strings.Contains(err.Error(), "вне блока") {
		t.Fatal("старый диапазон больше не принят", err)
	}
	// Reference-only candidate cannot reanchor a norm (REQ-SA-041 guard).
	config, base = referenceFixture(t)
	mixed := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
	mixed.Citations = append(mixed.Citations, candidateAt(t, base, "clarification.md", "C001", "", 3, 3).Citations[0])
	raw, decision = acceptedInputs(t, config, "mixed", []legacyCandidate{mixed}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	only := candidateAt(t, base, "clarification.md", "C001", "Срок по договору", 3, 3)
	raw, decision = acceptedInputs(t, config, "guard", []legacyCandidate{only}, reanchor("REQ-AI-001", "C001"))
	if _, err := execute([]string{"reconcile", config, raw, decision}); err == nil || !strings.Contains(err.Error(), "C001") {
		t.Fatal("guard нормативной цитаты", err)
	}
}

// tool-spec §39 / REQ-SA-048: a second package on the same scope carries prior norms with keep when their sources are
// unchanged; the raw need not repeat them, IDs continue, records stay byte-for-byte.
func TestKeepPackage(t *testing.T) {
	config, base := acceptedFixture(t)
	first := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, config, "pkg-1", []legacyCandidate{first}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	before := runOK(t, "reconcile", config).(map[string]any)["records"]
	second := candidateAt(t, base, "rules.md", "C001", "Название карточки обязательно", 3, 3)
	raw, decision = acceptedInputs(t, config, "pkg-2", []legacyCandidate{second}, acceptOperation("keep", []string{"REQ-AI-001"}), acceptOperation("accept", []string{}, "C001"))
	view := runOK(t, "check", config, raw, decision).(map[string]any)
	if !reflect.DeepEqual(view["kept"], []string{"REQ-AI-001"}) || len(view["assignments"].([]checkedAssignment)) != 1 || view["assignments"].([]checkedAssignment)[0].RequirementID != "REQ-AI-002" {
		t.Fatal("check: keep не занимает ID, нумерация продолжается", view["kept"], view["assignments"])
	}
	applied := runOK(t, "reconcile", config, raw, decision).(map[string]any)
	records := applied["records"].([]AcceptedRecord)
	if !reflect.DeepEqual(applied["kept"], []string{"REQ-AI-001"}) || len(records) != 2 || records[0].Status != "active" || records[1].Requirement.ID != "REQ-AI-002" {
		t.Fatal("reconcile: два active после keep + accept", applied["kept"], len(records))
	}
	if kept, prior := records[0].Requirement, before.([]AcceptedRecord)[0].Requirement; !reflect.DeepEqual(kept, prior) {
		t.Fatal("keep не меняет норму: ID, revision, цитаты, hash", kept.Accepted, prior.Accepted)
	}
	if index := runOK(t, "index", config, "summary").(map[string]any); index["requirements_total"] != 2 || index["accepted"].(map[string]any)["history_total"] != 2 {
		t.Fatal("индекс после второго пакета", index["requirements_total"])
	}
	// keep with a target, and keep after the cited source changed, are refused with the reason.
	bad := acceptOperation("keep", []string{"REQ-AI-002"}, "C001")
	raw, decision = acceptedInputs(t, config, "pkg-bad", []legacyCandidate{first}, bad, acceptOperation("keep", []string{"REQ-AI-001"}))
	runFail(t, "check", config, raw, decision)
	rules := filepath.Join(base, "source/rules.md")
	writeFixture(t, rules, append(readFixture(t, rules), []byte("ещё строка\n")...))
	third := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
	raw, decision = acceptedInputs(t, config, "pkg-3", []legacyCandidate{third}, acceptOperation("keep", []string{"REQ-AI-001"}), acceptOperation("keep", []string{"REQ-AI-002"}), acceptOperation("accept", []string{}, "C001"))
	if _, err := execute([]string{"check", config, raw, decision}); err == nil || !strings.Contains(err.Error(), "keep REQ-AI-001") || !strings.Contains(err.Error(), "rules.md") {
		t.Fatal("keep после изменения источника — отказ с именем нормы и файла", err)
	}
}

// tool-spec §46 / REQ-SA-049: a version 2 decision may narrow a candidate's statement — words removed, never added.
func TestAcceptNarrowed(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит одного файла — 8 МиБ включительно", 2, 2)
	rawPath, _ := acceptedInputs(t, config, "probe", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	raw := readFixture(t, rawPath)
	write := func(id, narrowed, action string) string {
		decision := acceptedDecisionV2{2, id, runOK(t, "reconcile", config).(map[string]any)["base_index"].(string), digest(raw), []acceptedOperationV2{{action, []string{}, []acceptedTargetV2{{"C001", "Лимит", "Проверить границу", narrowed}}, "Сужение по тексту ТЗ"}}}
		path := filepath.Join(base, id+".json")
		writeFixture(t, path, legacyMarshal(t, decision))
		return path
	}
	if _, err := execute([]string{"check", config, rawPath, write("wider", "Лимит одного файла — 8 МиБ включительно для всех", "accept")}); err == nil || !strings.Contains(err.Error(), "добавляет слово") {
		t.Fatal("добавление смысла — отказ", err)
	}
	if _, err := execute([]string{"check", config, rawPath, write("same", "Лимит одного файла — 8 МиБ включительно", "accept")}); err == nil || !strings.Contains(err.Error(), "отличаться") {
		t.Fatal("тот же statement — отказ", err)
	}
	if _, err := execute([]string{"check", config, rawPath, write("defer", "Лимит одного файла", "defer")}); err == nil || !strings.Contains(err.Error(), "narrowed_statement допустим только") {
		t.Fatal("defer не сужает", err)
	}
	narrowed := write("narrow", "Лимит одного файла — 8 МиБ", "accept")
	if view := runOK(t, "check", config, rawPath, narrowed).(map[string]any); view["valid"] != true {
		t.Fatal("сужение принимается dry-run", view)
	}
	applied := runOK(t, "reconcile", config, rawPath, narrowed).(map[string]any)
	record := applied["records"].([]AcceptedRecord)[0]
	if record.Requirement.Statement != "Лимит одного файла — 8 МиБ" || record.Requirement.Title != "Лимит" || record.Status != "active" {
		t.Fatal("принятая норма несёт суженный statement", record.Requirement.Statement)
	}
	// The journal replays the version 2 decision; the raw still carries the original wording.
	if again := runOK(t, "reconcile", config).(map[string]any); again["records"].([]AcceptedRecord)[0].Requirement.Statement != "Лимит одного файла — 8 МиБ" || !strings.Contains(again["journal"].(acceptedLedger).Commits[0].Raw, "включительно") {
		t.Fatal("журнал воспроизводит сужение, raw хранит оригинал")
	}
	if index := runOK(t, "index", config).(map[string]any); index["requirements"].([]Requirement)[0].Statement != "Лимит одного файла — 8 МиБ" {
		t.Fatal("snapshot несёт суженную норму")
	}
	// A version 3 decision is refused before anything is read.
	bad := filepath.Join(base, "v3.json")
	writeFixture(t, bad, []byte(`{"version":3,"decision_id":"x","base_index":"y","raw_sha256":"z","operations":[]}`))
	runFail(t, "check", config, rawPath, bad)
}

// tool-spec §55: moving an unchanged file between specs and references makes the index stale; a version 1 ledger
// (no classification) is still read and compares bytes only.
func TestReferenceGroupFreshness(t *testing.T) {
	config, base := referenceFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, config, "initial", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	applied := runOK(t, "reconcile", config, raw, decision).(map[string]any)
	if applied["freshness"] != "fresh" {
		t.Fatal("после применения индекс fresh", applied["freshness"])
	}
	var ledger acceptedLedger
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs", acceptedFile)), &ledger); err != nil || ledger.Version != ledgerVersion || !ledger.Commits[0].Classified || !reflect.DeepEqual(ledger.Commits[0].References, []string{"clarification.md"}) {
		t.Fatal("журнал версии 2 записывает классификацию источников", err, ledger)
	}
	// Same bytes, clarification.md regrouped into specs: stale.
	plain := readFixture(t, config)
	regrouped := bytes.Replace(plain, []byte("specs: {paths: [rules.md]}"), []byte("specs: {paths: [rules.md, clarification.md]}"), 1)
	regrouped = bytes.Replace(regrouped, []byte("references: {paths: [clarification.md]}\n"), nil, 1)
	writeFixture(t, config, regrouped)
	if view := runOK(t, "reconcile", config).(map[string]any); view["freshness"] != "stale" {
		t.Fatal("перенос файла между группами при тех же байтах — stale", view["freshness"])
	}
	runFail(t, "index", config)
	writeFixture(t, config, plain)
	if view := runOK(t, "reconcile", config).(map[string]any); view["freshness"] != "fresh" {
		t.Fatal("прежняя группировка — снова fresh", view["freshness"])
	}
	// A version 1 ledger of the same commits: readable, unclassified, fresh under either grouping.
	old := acceptedLedgerV1{1, []acceptedCommitV1{}}
	for _, commit := range ledger.Commits {
		old.Commits = append(old.Commits, acceptedCommitV1{commit.Raw, commit.Decision})
	}
	writeFixture(t, filepath.Join(base, "runs", acceptedFile), legacyMarshal(t, old))
	if view := runOK(t, "reconcile", config).(map[string]any); view["freshness"] != "fresh" || view["journal"].(acceptedLedger).Commits[0].Classified {
		t.Fatal("журнал версии 1 читается без классификации", view["freshness"])
	}
	writeFixture(t, config, regrouped)
	if view := runOK(t, "reconcile", config).(map[string]any); view["freshness"] != "fresh" {
		t.Fatal("без классификации сравниваются только байты", view["freshness"])
	}
	writeFixture(t, config, plain)
	// The next package rewrites the ledger in version 2, keeping the old commit unclassified.
	second := candidateAt(t, base, "rules.md", "C001", "Название карточки обязательно", 3, 3)
	raw, decision = acceptedInputs(t, config, "second", []legacyCandidate{second}, acceptOperation("keep", []string{"REQ-AI-001"}), acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs", acceptedFile)), &ledger); err != nil || ledger.Version != ledgerVersion || ledger.Commits[0].Classified || !ledger.Commits[1].Classified {
		t.Fatal("после нового пакета журнал версии 2, старый commit без классификации", err, ledger.Version)
	}
	writeFixture(t, filepath.Join(base, "runs", acceptedFile), []byte(`{"version":3,"commits":[]}`))
	runFail(t, "reconcile", config)
}
