//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const neighboringQuestion = "\n### Q-AREA-001 — Общая граница\n\nВопрос: Как согласовать две области?\nНормы: REQ-DEMO-001, REQ-OTHER-001\n"
const neighboringNorm = "### REQ-OTHER-001 — Соседняя норма\n\nСправочный текст без обязательных полей declared-профиля.\n"

func replaceExternalFile(t *testing.T, f *externalFixture, path, text string) {
	t.Helper()
	for i := range f.manifest.Files {
		file := &f.manifest.Files[i]
		if file.Path == path {
			file.Size, file.SHA256 = int64(len(text)), digest([]byte(text))
			writeFixture(t, filepath.Join(f.corpus, path), []byte(text))
			writeFixture(t, filepath.Join(f.base, "source", path), []byte(text))
			f.save(t)
			return
		}
	}
	t.Fatalf("missing fixture path %s", path)
}

func TestQuestionReferenceLinks(t *testing.T) {
	f := newExternalFixture(t)
	replaceExternalFile(t, &f, "rules.md", string(readFixture(t, filepath.Join(f.corpus, "rules.md")))+neighboringQuestion)
	replaceExternalFile(t, &f, "context.md", neighboringNorm+"\n### Q-OTHER-001 — Чужой вопрос\n\nВопрос: Не относится к этому аудиту.\nНормы: REQ-ABSENT-999\n")
	runOK(t, "external", "check", f.config, f.input)
	if _, err := os.Stat(filepath.Join(f.base, "runs")); !os.IsNotExist(err) {
		t.Fatalf("check created reports: %v", err)
	}
	config := f.install(t)
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Requirements) != 5 || len(m.Questions) != 2 {
		t.Fatalf("reference changed audit scope: norms=%d questions=%d", len(m.Requirements), len(m.Questions))
	}
	view := specQuestions(m, &RunOverview{verdicts: map[string]NormBrief{"REQ-OTHER-001": {ID: "REQ-OTHER-001", Implementation: "supported"}}})
	norm := view[1].Norms[1]
	data := mustJSON(t, norm)
	if norm.Title != "Соседняя норма" || norm.Verdict != nil || !bytes.Contains(data, []byte(`"reference":true`)) || !bytes.Contains(data, []byte(`"path":"context.md"`)) {
		t.Fatalf("reference metadata/verdict: %s", data)
	}
	local := cfg
	local.External = nil
	local.ProjectRoot = filepath.Join(f.base, "source")
	lm, err := snapshot(local)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lm.Questions, m.Questions) {
		t.Fatal("local/external question semantics differ")
	}
	batch := runOK(t, "prepare", config, "question-links").(TaskBatch)
	for _, task := range batch.Tasks {
		if len(task.Requirements) != 5 {
			t.Fatal("reference norm entered role task")
		}
	}
	out := filepath.Join(f.base, "corpus.html")
	runOK(t, "corpus", out, config)
	html := readFixture(t, out)
	if !bytes.Contains(html, []byte("Соседняя норма")) || !bytes.Contains(html, []byte("справочная норма")) {
		t.Fatal("corpus omitted reference identity")
	}
}

func TestExternalQuestionPreflight(t *testing.T) {
	for _, kind := range []string{"unknown", "mention", "fence", "quote", "nested", "archive", "duplicate", "malformed_question", "malformed_norm", "duplicate_question"} {
		t.Run(kind, func(t *testing.T) {
			f := newExternalFixture(t)
			rules := string(readFixture(t, filepath.Join(f.corpus, "rules.md"))) + neighboringQuestion
			context := "Нет определения.\n"
			switch kind {
			case "mention":
				context = "Связь с REQ-OTHER-001.\n"
			case "fence":
				context = "```markdown\n" + neighboringNorm + "```\n"
			case "quote":
				context = "> ### REQ-OTHER-001 — Пример\n"
			case "nested":
				context = "- ### REQ-OTHER-001 — Пример\n"
			case "archive":
				replaceExternalFile(t, &f, "assets/same-name.bin", neighboringNorm)
			case "duplicate":
				context = neighboringNorm + "\n" + neighboringNorm
			case "malformed_question":
				rules = strings.ReplaceAll(rules, "Вопрос:", "Ответ:")
				context = neighboringNorm
			case "malformed_norm":
				rules = strings.ReplaceAll(rules, "Требование:", "Описание:")
				context = neighboringNorm
			case "duplicate_question":
				rules += neighboringQuestion
				context = neighboringNorm
			}
			replaceExternalFile(t, &f, "rules.md", rules)
			replaceExternalFile(t, &f, "context.md", context)
			_, checkErr := execute([]string{"external", "check", f.config, f.input})
			if checkErr == nil {
				t.Fatal("check accepted invalid question input")
			}
			if _, err := os.Stat(filepath.Join(f.base, "runs")); !os.IsNotExist(err) {
				t.Fatal("failed check wrote state")
			}
			_, importErr := execute([]string{"external", "import", f.config, f.input})
			if importErr == nil || importErr.Error() != checkErr.Error() {
				t.Fatalf("check/import differ: %v / %v", checkErr, importErr)
			}
			if _, err := os.Stat(filepath.Join(f.base, "runs")); !os.IsNotExist(err) {
				t.Fatal("semantic refusal wrote state")
			}
		})
	}
}

func TestQuestionReferencesPreserveLegacy(t *testing.T) {
	f := newExternalFixture(t)
	cfg, err := loadConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	cfg.External = nil
	m, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range m.Questions {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(mustJSON(t, q), &object); err != nil {
			t.Fatal(err)
		}
		if len(object) != 5 {
			t.Fatalf("legacy question bytes changed: %s", mustJSON(t, q))
		}
	}
	replaceExternalFile(t, &f, "context.md", neighboringNorm)
	m2, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Questions, m2.Questions) {
		t.Fatal("unreferenced declarations changed questions")
	}
	// Arbitrary reference bytes and local priority retain the declared profile contract.
	replaceExternalFile(t, &f, "context.md", "{\"note\":\"REQ-DEMO-005\"}\n")
	runOK(t, "external", "check", f.config, f.input)
	replaceExternalFile(t, &f, "context.md", "### REQ-DEMO-005 — Справочная копия\n\n### REQ-DEMO-005 — Ещё копия\n")
	runOK(t, "external", "check", f.config, f.input)
	// Accepted Markdown is raw input to reconcile, not a declared spec.
	cfg, err = loadConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	cfg.IndexMode = "accepted"
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, f.config, raw)
	replaceExternalFile(t, &f, "rules.md", "### Q-BROKEN\n\nПроизвольный исходный текст.\n")
	runOK(t, "external", "check", f.config, f.input)
	f.install(t)
}

func TestQuestionReferencesSurviveReleaseAndRestore(t *testing.T) {
	f := citationLifecycleFixture(t)
	replaceExternalFile(t, &f, "rules.md", string(readFixture(t, filepath.Join(f.corpus, "rules.md")))+neighboringQuestion)
	replaceExternalFile(t, &f, "context.md", neighboringNorm)
	config := f.install(t)
	completeExternal(t, config, "linked")
	before := runOK(t, "overview", config).(Overview)
	if before.Totals.Requirements != 5 || before.Totals.Questions != 2 {
		t.Fatal("reference changed audit counts")
	}
	runOK(t, "external", "release", config)
	if err := os.RemoveAll(filepath.Join(f.base, "source")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.corpus); err != nil {
		t.Fatal(err)
	}
	assertQuestions := func(config string) {
		t.Helper()
		view := runOK(t, "overview", config).(Overview)
		if !reflect.DeepEqual(view.Scopes[0].Questions, before.Scopes[0].Questions) {
			t.Fatal("saved question metadata lost")
		}
	}
	assertQuestions(config)
	request := filepath.Join(f.base, "bundle.json")
	writeFixture(t, request, mustJSON(t, map[string]any{"schema_version": 2, "configs": []string{config}}))
	archive := filepath.Join(f.base, "linked.tar.gz")
	runOK(t, "bundle", "export", request, archive)
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(f.base, "restored")).(map[string]any)
	restoredConfig := restored["configs"].([]string)[0]
	assertQuestions(restoredConfig)
	site := filepath.Join(f.base, "site")
	if target := os.Getenv("SPEC_AUDIT_QUESTION_UI"); target != "" {
		site = target
	}
	runOK(t, "publish", site, restoredConfig)
	html := readFixture(t, filepath.Join(site, "index.html"))
	if !bytes.Contains(html, []byte("Соседняя норма")) || !bytes.Contains(html, []byte("справочная норма")) {
		t.Fatal("saved publication lost reference")
	}
}

func TestQuestionLinksAnyCode(t *testing.T) {
	for _, source := range []string{"def compute(x): return x + 1\n", "export const compute = x => x + 1;\n", "fn compute(x: i32) -> i32 { x + 1 }\n"} {
		t.Run(strings.Fields(source)[0], func(t *testing.T) {
			config, base := fixture(t)
			cfg, err := loadConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Code = Sources{Paths: []string{"component.txt"}}
			cfg.Tests = Sources{Paths: []string{"checks.txt"}}
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, config, raw)
			writeFixture(t, filepath.Join(base, "source/component.txt"), []byte(source))
			writeFixture(t, filepath.Join(base, "source/checks.txt"), []byte("opaque static assertions\n"))
			f := externalFromConfig(t, config, base)
			replaceExternalFile(t, &f, "rules.md", string(readFixture(t, filepath.Join(f.corpus, "rules.md")))+neighboringQuestion)
			replaceExternalFile(t, &f, "context.md", neighboringNorm)
			runOK(t, "external", "check", f.config, f.input)
			f.install(t)
		})
	}
}

func TestQuestionReferenceSourceChanged(t *testing.T) {
	data := []byte(neighboringNorm)
	previous := bytes.Clone(data)
	previous[len(previous)-2] ^= 1 // Same length and definition, different full-file bytes.
	m := Manifest{
		Files:     []SourceFile{{Path: "context.md", Kind: "spec", Reference: true, SHA256: digest(previous), Bytes: len(data)}},
		Questions: []SpecQuestion{{ID: "Q-AREA-001", Requirements: []string{"REQ-OTHER-001"}}},
	}
	err := resolveQuestionReferences(&m, func(string) ([]byte, error) { return data, nil })
	if err == nil || !strings.Contains(err.Error(), "context.md") {
		t.Fatalf("changed source must not confirm a reference: %v", err)
	}
}
