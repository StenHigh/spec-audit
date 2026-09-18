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

// manyRequirements renders n declared norms in the §4 Markdown profile (format of acceptance/corpus/rules.md).
func manyRequirements(n int) []byte {
	var b strings.Builder
	b.WriteString("# Нормы синтетического среза\n\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "### REQ-DEMO-%03d — Норма %d\n\nУсловие: вызвана операция %d.\nТребование: результат должен быть определён.\nПроверка: проверить точный результат.\n\n", i, i, i)
	}
	return []byte(b.String())
}

func scopesYAML(sizes ...int) string {
	parts, next := []string{}, 1
	for i, size := range sizes {
		ids := []string{}
		for j := 0; j < size; j++ {
			ids = append(ids, fmt.Sprintf("REQ-DEMO-%03d", next))
			next++
		}
		parts = append(parts, fmt.Sprintf("{id: part-%d, focus: 'Часть %d', requirements: [%s]}", i+1, i+1, strings.Join(ids, ", ")))
	}
	return "scopes: [" + strings.Join(parts, ", ") + "]\n"
}

// tool-spec 20.1: the binary never splits norms; it names every scope above scopeAdvisoryRequirements.
func TestScopeAdvisories(t *testing.T) {
	config, base := fixture(t)
	plain := readFixture(t, config)
	setup := func(n int, scopes string) {
		t.Helper()
		writeFixture(t, filepath.Join(base, "source/rules.md"), manyRequirements(n))
		writeFixture(t, config, bytes.Replace(plain, []byte("scopes: []\n"), []byte(scopes), 1))
	}
	advisories := func() []string {
		t.Helper()
		value, _ := runOK(t, "index", config).(map[string]any)["advisories"].([]string)
		return value
	}
	setup(scopeAdvisoryRequirements+1, "scopes: []\n")
	if got := advisories(); len(got) != 1 || !strings.Contains(got[0], "scope all: 25 норм") || !strings.Contains(got[0], "≤ 24") {
		t.Fatal("25 норм в scope all должны дать одну рекомендацию", got)
	}
	setup(scopeAdvisoryRequirements, "scopes: []\n")
	if got := advisories(); len(got) != 0 {
		t.Fatal("24 нормы не требуют рекомендации", got)
	}
	setup(25, scopesYAML(13, 12))
	if got := advisories(); len(got) != 0 {
		t.Fatal("явные scope в пределах рекомендации", got)
	}
	setup(25, scopesYAML(25))
	if got := advisories(); len(got) != 1 || !strings.Contains(got[0], "scope part-1: 25 норм") {
		t.Fatal("явный большой scope тоже называется", got)
	}
	setup(65, "scopes: []\n")
	if _, err := execute([]string{"index", config}); err == nil || !strings.Contains(err.Error(), "более 64 требований") {
		t.Fatal("предел §6 остаётся отказом", err)
	}
	if testing.Short() {
		return
	}
	// Через процесс: WARN в stderr у index и prepare, stdout — один JSON; LOG_LEVEL=error молчит.
	setup(25, "scopes: []\n")
	binary := filepath.Join(base, "spec-audit")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("сборка: %v: %s", err, output)
	}
	run := func(level string, args ...string) (string, string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir, cmd.Env = base, []string{"PATH=", "LOG_LEVEL=" + level}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, &stderr)
		}
		return stdout.String(), stderr.String()
	}
	if stdout, stderr := run("warn", "index", "config.yaml"); !strings.Contains(stderr, "scope содержит много норм") || !json.Valid([]byte(stdout)) || !strings.Contains(stdout, `"advisories"`) {
		t.Fatal("index: ожидались WARN и один JSON с advisories", stderr)
	}
	if stdout, stderr := run("warn", "prepare", "config.yaml", "wide"); !strings.Contains(stderr, "scope содержит много норм") || strings.Contains(stdout, "advisories") {
		t.Fatal("prepare: ожидался WARN без изменения TaskBatch", stderr)
	}
	if _, stderr := run("error", "index", "config.yaml"); stderr != "" {
		t.Fatal("LOG_LEVEL=error должен молчать", stderr)
	}
}

// tool-spec §22.2: stale scopes name the scope and the IDs instead of a bare refusal.
func TestScopeAssignmentErrors(t *testing.T) {
	config, _ := fixture(t)
	plain := readFixture(t, config)
	check := func(scopes string, fragments ...string) {
		t.Helper()
		writeFixture(t, config, bytes.Replace(plain, []byte("scopes: []\n"), []byte(scopes), 1))
		_, err := execute([]string{"index", config})
		if err == nil {
			t.Fatal("устаревшие scopes должны быть отклонены", scopes)
		}
		for _, fragment := range fragments {
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("ошибка %q не называет %q", err, fragment)
			}
		}
	}
	writeFixture(t, config, bytes.Replace(plain, []byte("scopes: []\n"), []byte(scopesYAML(5)), 1))
	runOK(t, "index", config)
	check("scopes: [{id: stale, focus: 'A', requirements: [REQ-DEMO-001, REQ-DEMO-009]}, {id: rest, focus: 'B', requirements: [REQ-DEMO-002, REQ-DEMO-003, REQ-DEMO-004, REQ-DEMO-005]}]\n",
		"scope stale", "неизвестные ID [REQ-DEMO-009]", "повторно назначенные []")
	check("scopes: [{id: a, focus: 'A', requirements: [REQ-DEMO-001, REQ-DEMO-002]}, {id: b, focus: 'B', requirements: [REQ-DEMO-001, REQ-DEMO-003, REQ-DEMO-004, REQ-DEMO-005]}]\n",
		"scope b", "неизвестные ID []", "повторно назначенные [REQ-DEMO-001]")
	check("scopes: [{id: part, focus: 'A', requirements: [REQ-DEMO-001, REQ-DEMO-002]}]\n",
		"не распределены по scope: [REQ-DEMO-003 REQ-DEMO-004 REQ-DEMO-005]")
}

// tool-spec §26.1: a successful accepted-mode index is fresh by construction and says so; declared mode has no such field.
func TestIndexFreshness(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, config, "initial", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	if index := runOK(t, "index", config).(map[string]any); index["freshness"] != "fresh" {
		t.Fatal("index в accepted-режиме должен называть freshness", index["freshness"])
	}
	writeFixture(t, filepath.Join(base, "source/rules.md"), append(readFixture(t, filepath.Join(base, "source/rules.md")), []byte("\nещё строка\n")...))
	runFail(t, "index", config)
	declared, _ := fixture(t)
	if index := runOK(t, "index", declared).(map[string]any); index["freshness"] != nil {
		t.Fatal("в declared-режиме поля freshness нет", index["freshness"])
	}
}

// tool-spec §26.2: anchors a norm names must be defined (not merely mentioned) in the snapshot's spec files.
func TestAnchorAdvisories(t *testing.T) {
	config, base := acceptedFixture(t)
	rules := filepath.Join(base, "source/rules.md")
	writeFixture(t, rules, append(readFixture(t, rules), []byte("Срок согласуется по §9.9 и A-777 (см. также §1.2).\n")...))
	c := candidateAt(t, base, "rules.md", "C001", "Срок по §9.9 и правилу A-777 не превышает суток", 6, 6)
	c.Exceptions = []string{"Кроме случаев §1.2"}
	plain := candidateAt(t, base, "rules.md", "C002", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, config, "initial", []legacyCandidate{c, plain}, acceptOperation("accept", []string{}, "C001"), acceptOperation("accept", []string{}, "C002"))
	want := "C001: якоря §9.9, A-777, §1.2 не определены в spec-файлах snapshot (source_set+references); норма может опираться на раздел вне scope"
	if view := runOK(t, "check", config, raw).(map[string]any); !reflect.DeepEqual(view["advisories"], []string{want}) {
		t.Fatal("check RAW должен назвать неопределённые якоря по кандидатам", view["advisories"])
	}
	runOK(t, "reconcile", config, raw, decision)
	index := runOK(t, "index", config).(map[string]any)
	advisories, _ := index["advisories"].([]string)
	if len(advisories) != 1 || !strings.HasPrefix(advisories[0], "REQ-AI-001: якоря §9.9, A-777, §1.2 не определены") {
		t.Fatal("index должен повторить подсказку по принятой норме", index["advisories"])
	}
	if view := runOK(t, "reconcile", config).(map[string]any); !reflect.DeepEqual(view["advisories"], index["advisories"]) {
		t.Fatal("reconcile без RAW должен нести ту же подсказку", view["advisories"])
	}
	// Definitions: a heading in the spec file and a list item in a references file; §1.2 stays only a mention.
	writeFixture(t, rules, append(readFixture(t, rules), []byte("## 9.9 Сроки\nТекст раздела.\n")...))
	writeFixture(t, filepath.Join(base, "source/clarification.md"), []byte("# Уточнения\n* A-777: правило суток.\n"))
	writeFixture(t, config, append(readFixture(t, config), []byte("references: {paths: [clarification.md]}\n")...))
	c = candidateAt(t, base, "rules.md", "C001", "Срок по §9.9 и правилу A-777 не превышает суток", 6, 6)
	c.Exceptions = []string{"Кроме случаев §1.2"}
	plain = candidateAt(t, base, "rules.md", "C002", "Лимит 8 МиБ", 2, 2)
	raw, decision = acceptedInputs(t, config, "defined", []legacyCandidate{c, plain}, acceptOperation("rebind", []string{"REQ-AI-001"}, "C001"), acceptOperation("rebind", []string{"REQ-AI-002"}, "C002"))
	if view := runOK(t, "check", config, raw, decision).(map[string]any); !reflect.DeepEqual(view["advisories"], []string{"REQ-AI-001: якоря §1.2 не определены в spec-файлах snapshot (source_set+references); норма может опираться на раздел вне scope"}) {
		t.Fatal("определения в заголовке и references снимают подсказку, упоминание в прозе — нет", view["advisories"])
	}
	runOK(t, "reconcile", config, raw, decision)
	index = runOK(t, "index", config).(map[string]any)
	if advisories, _ := index["advisories"].([]string); len(advisories) != 1 || !strings.Contains(advisories[0], "якоря §1.2 не определены") {
		t.Fatal("после apply index показывает только §1.2", index["advisories"])
	}
}

// tool-spec §32.1: `index CONFIG summary` keeps everything but the requirement and file bodies.
func TestIndexSummary(t *testing.T) {
	config, _ := fixture(t)
	plain := readFixture(t, config)
	writeFixture(t, config, bytes.Replace(plain, []byte("scopes: []\n"), []byte(scopesYAML(3, 2)), 1))
	full := runOK(t, "index", config).(map[string]any)
	summary := runOK(t, "index", config, "summary").(map[string]any)
	requirements := full["requirements"].([]Requirement)
	ids := []string{}
	for _, req := range requirements {
		ids = append(ids, req.ID)
	}
	if _, ok := summary["requirements"]; ok || summary["files"] != nil || summary["requirements_total"] != len(requirements) || !reflect.DeepEqual(summary["active_ids"], ids) || summary["files_total"] != len(full["files"].([]SourceFile)) || summary["snapshot_id"] != full["snapshot_id"] || len(summary["scopes"].([]Scope)) != 2 {
		t.Fatal("сводка index: счётчики и ID вместо тел", summary)
	}
	if _, ok := summary["advisories"].([]string); !ok || summary["freshness"] != nil {
		t.Fatal("advisories остаются, freshness в declared-режиме нет", summary["advisories"], summary["freshness"])
	}
	accepted, _ := acceptedFixture(t)
	c := candidateAt(t, filepath.Dir(accepted), "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, accepted, "initial", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", accepted, raw, decision)
	if summary := runOK(t, "index", accepted, "summary").(map[string]any); summary["freshness"] != "fresh" || summary["requirements_total"] != 1 || !reflect.DeepEqual(summary["accepted"], map[string]any{"head": runOK(t, "index", accepted).(map[string]any)["accepted"].(*AcceptedSummary).Head, "history_total": 1}) {
		t.Fatal("сводка в accepted-режиме — head и число пакетов вместо журнала (§37.2)", summary["accepted"])
	}
	runFail(t, "index", config, "other")
}

// tool-spec §33.1: anchors names where normative files mention an anchor and where any spec file defines it, before any index.
func TestAnchors(t *testing.T) {
	config, base := acceptedFixture(t)
	rules := filepath.Join(base, "source/rules.md")
	writeFixture(t, rules, append(readFixture(t, rules), []byte("Срок согласуется по §9.9 и A-777 (см. также §1.2).\n\n## 9.9 Сроки\nТекст раздела.\nЕщё строка.\n\n## 10 Прочее\n")...))
	writeFixture(t, filepath.Join(base, "source/clarification.md"), []byte("# Уточнения\n* A-777: правило суток (см. A-778 и §9.9).\n  продолжение.\n\n* A-778: другое.\nУпоминание A-777 в прозе.\n\n| A-777 | P2/9.9 |\n"))
	writeFixture(t, config, append(readFixture(t, config), []byte("references: {paths: [clarification.md]}\n")...))
	index := runOK(t, "anchors", config).(AnchorIndex)
	byAnchor := map[string]AnchorEntry{}
	for _, e := range index.Anchors {
		byAnchor[e.Anchor] = e
	}
	if index.SnapshotFiles != 2 || len(index.Anchors) != 3 || !reflect.DeepEqual(index.Undefined, []string{"1.2"}) {
		t.Fatal("индекс якорей", index)
	}
	// tool-spec §37.1: kind tells a list item from a coverage-table row; references are the anchors the definition names.
	if e := byAnchor["A-777"]; !reflect.DeepEqual(e.Mentions, []CitationRef{{"rules.md", 6, 6}}) || !reflect.DeepEqual(e.Definitions, []AnchorDefinition{{"clarification.md", 2, 3, "list", []string{"A-778", "9.9"}}, {"clarification.md", 8, 8, "table", []string{}}}) {
		t.Fatal("A-777: упоминание в нормативном файле, определения — пункт списка со ссылками и строка таблицы", e)
	}
	if e := byAnchor["9.9"]; !reflect.DeepEqual(e.Definitions, []AnchorDefinition{{"rules.md", 8, 10, "heading", []string{}}}) {
		t.Fatal("заголовок определяется до следующего заголовка", e)
	}
	if _, ok := byAnchor["A-778"]; ok {
		t.Fatal("якорь без упоминания в нормативном файле не индексируется")
	}
	if _, err := os.Stat(filepath.Join(base, "runs")); !os.IsNotExist(err) {
		t.Fatal("anchors не создаёт каталог отчётов")
	}
}
