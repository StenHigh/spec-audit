package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
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
	if got := advisories(); got != nil {
		t.Fatal("24 нормы не требуют рекомендации", got)
	}
	setup(25, scopesYAML(13, 12))
	if got := advisories(); got != nil {
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
