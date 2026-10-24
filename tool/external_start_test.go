//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func saveStartupConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, raw)
}

func startupGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestExternalStartupDiagnostics(t *testing.T) {
	fixture := newExternalFixture(t)
	cfg, err := loadConfig(fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	root := cfg.ProjectRoot
	startupGit(t, root, "init", "-q")
	writeFixture(t, filepath.Join(root, ".gitignore"), []byte("generated.go\n"))
	startupGit(t, root, "add", ".")
	startupGit(t, root, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "initial")
	fixture.request.CodeBasis = mustJSON(t, map[string]string{"kind": "git", "repository": root, "commit": startupGit(t, root, "rev-parse", "HEAD")})
	writeFixture(t, filepath.Join(root, "generated.go"), []byte("package generated\n"))
	if status := startupGit(t, root, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("ignored file must be invisible in ordinary status: %s", status)
	}
	writeFixture(t, filepath.Join(root, "loose.go"), []byte("package loose\n"))
	writeFixture(t, filepath.Join(root, "source.go"), []byte("package changed\n"))
	cfg.Code.Paths = append(cfg.Code.Paths, "generated.go", "loose.go")
	cfg.Specs.Paths = []string{"context.md", "assets"}
	cfg.References.Paths = []string{"rules.md", "context.md"}
	saveStartupConfig(t, fixture.config, cfg)
	fixture.save(t)
	before := readFixture(t, fixture.config)
	var checkError string
	for _, operation := range []string{"check", "import"} {
		_, err := execute([]string{"external", operation, fixture.config, fixture.input})
		if err == nil {
			t.Fatal("conflicting input accepted")
		}
		message := err.Error()
		for _, fragment := range []string{"specs", "references", "ожидаемые", "фактические", "недостающие", "лишние", "обе группы", "rules.md", "context.md", "assets/same-name.bin", "generated.go", "ignored", "loose.go", "untracked", "source.go", "tracked"} {
			if !strings.Contains(message, fragment) {
				t.Errorf("%s: missing %q in %s", operation, fragment, message)
			}
		}
		if operation == "check" {
			checkError = message
		} else if message != checkError {
			t.Fatal("check/import diagnostics diverged")
		}
	}
	if !bytes.Equal(before, readFixture(t, fixture.config)) {
		t.Fatal("diagnostic changed CONFIG")
	}
	if _, err := os.Stat(cfg.ReportsDir); !os.IsNotExist(err) {
		t.Fatalf("failed preflight wrote reports: %v", err)
	}
	fixture.request.ManifestSHA256 = strings.Repeat("0", 64)
	writeFixture(t, fixture.input, mustJSON(t, fixture.request))
	_, err = execute([]string{"external", "check", fixture.config, fixture.input})
	if err == nil || !strings.Contains(err.Error(), "manifest_sha256") || strings.Contains(err.Error(), "generated.go") {
		t.Fatalf("integrity failure must precede diagnostic collection: %v", err)
	}
}

func TestStartupMissingSourceContext(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "external"}[external], func(t *testing.T) {
			config, base := fixture(t)
			if external {
				input := externalFromConfig(t, config, base)
				config = input.install(t)
			}
			cfg, err := loadConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(cfg.ProjectRoot, "rules.md")); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"index", config}, {"index", config, "summary"}, {"overview", config}} {
				value, err := execute(args)
				message := ""
				if args[0] == "overview" {
					if err != nil {
						t.Fatal(err)
					}
					message = value.(Overview).Scopes[0].Error
				} else if err != nil {
					message = err.Error()
				}
				for _, fragment := range []string{config, "rules.md", "project_root"} {
					if !strings.Contains(message, fragment) {
						t.Errorf("%v: missing %q in %q", args, fragment, message)
					}
				}
				if external != strings.Contains(message, "external import") {
					t.Errorf("wrong source routing for %v: %s", args, message)
				}
			}
		})
	}
}

func startupComplete(t *testing.T, config string) {
	t.Helper()
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	batch := runOK(t, "prepare", config, "startup").(TaskBatch)
	for _, task := range batch.Tasks {
		result := Result{TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope,
			Summary: "Детерминированный контроль протокола", Assessments: []Assessment{}, Limitations: []string{"Это test data, не смысловая приёмка"}}
		for _, requirement := range task.Requirements {
			result.Assessments = append(result.Assessments, Assessment{RequirementID: requirement.ID, Specification: "clear", Implementation: "unknown", Assertion: "unknown",
				Statement: "Контроль механики сохраняет неизвестность", Spec: []Citation{requirement.Source}, Code: []Citation{}, Tests: []TestCitation{}, Limitations: []string{}})
		}
		output := filepath.Join(cfg.ReportsDir, "startup", "dispatch", task.TaskID, "result.json")
		writeFixture(t, output, mustJSON(t, result))
		runOK(t, "validate", config, "startup", task.TaskID, output)
		runOK(t, "submit", config, "startup", task.TaskID, output)
	}
	decision := runOK(t, "draft", config, "startup").(ReviewDecisionV3)
	decision.Reviewer, decision.Summary = "synthetic-host", "Детерминированный контроль, не вывод о продукте"
	for index := range decision.Verdicts {
		verdict := &decision.Verdicts[index]
		verdict.Specification, verdict.Implementation, verdict.Assertion = "clear", "unknown", "unknown"
		verdict.Concur, verdict.Statement = "both", "Неизвестность сохранена"
	}
	decision.Counts = countVerdicts(baseVerdicts(decision.Verdicts))
	output := filepath.Join(cfg.ReportsDir, "startup", "decision.json")
	writeFixture(t, output, mustJSON(t, decision))
	runOK(t, "validate", config, "startup", "host", output)
	runOK(t, "review", config, "startup", output)
	runOK(t, "report", config, "startup")
	view := runOK(t, "overview", config).(Overview)
	if view.Totals.Requirements != 1 || view.Totals.Implementation["unknown"] != 1 || view.Totals.Gap.Total != 1 {
		t.Fatalf("lost unknown or requirement: %+v", view.Totals)
	}
}

func TestStartupMissingRootAndRelease(t *testing.T) {
	t.Run("local_root", func(t *testing.T) {
		config, base := fixture(t)
		if err := os.RemoveAll(filepath.Join(base, "source")); err != nil {
			t.Fatal(err)
		}
		_, err := execute([]string{"index", config, "summary"})
		if err == nil || !strings.Contains(err.Error(), config) || !strings.Contains(err.Error(), "project_root") || strings.Contains(err.Error(), "external import") {
			t.Fatalf("missing root context or wrong route: %v", err)
		}
	})
	t.Run("released", func(t *testing.T) {
		input := newExternalFixture(t)
		cfg, err := loadConfig(input.config)
		if err != nil {
			t.Fatal(err)
		}
		cfg.SourceRetention = "citations"
		saveStartupConfig(t, input.config, cfg)
		config := input.install(t)
		completeExternal(t, config, "release-startup")
		runOK(t, "external", "release", config)
		_, err = execute([]string{"index", config, "summary"})
		if err == nil || !strings.Contains(err.Error(), "штатно освобождены") || strings.Contains(err.Error(), "external import") {
			t.Fatalf("release should preserve history route: %v", err)
		}
		view := runOK(t, "overview", config).(Overview)
		if view.Scopes[0].Decided == nil || view.Scopes[0].Decided.HostReviewState != "saved" || !view.Scopes[0].Decided.HostReviewComplete {
			t.Fatalf("lost saved decision: %+v", view.Scopes[0].Decided)
		}
	})
}

func TestStartupUniversality(t *testing.T) {
	for _, layout := range []struct{ name, spec, reference, code, test, origin string }{
		{"python", "requirements/rules.md", "terms/context.json", "engine/logic.py", "checks/check_logic.py", "snapshot"},
		{"javascript", "contract/limits.md", "dictionary/data.json", "lib/limit.js", "verification/limit.test.js", "git"},
	} {
		for _, external := range []bool{false, true} {
			t.Run(layout.name+map[bool]string{false: "/local", true: "/external"}[external], func(t *testing.T) {
				t.Setenv("PATH", t.TempDir())
				base := t.TempDir()
				cfg := Config{Version: 1, ProjectRoot: filepath.Join(base, "product"), ReportsDir: filepath.Join(base, "results"),
					Specs: Sources{Paths: []string{layout.spec}}, References: &Sources{Paths: []string{layout.reference}},
					Code: Sources{Paths: []string{layout.code}}, Tests: Sources{Paths: []string{layout.test}}, Runtime: Runtime{Kind: "none"}}
				sources := map[string][]byte{
					layout.spec:      []byte("### REQ-START-001 — Предел\n\nУсловие: запрос значения предела.\nТребование: значение должно быть равно 3.\nПроверка: сравнить результат с 3.\n"),
					layout.reference: []byte("{\"unit\":\"count\"}\n"), layout.code: []byte("LIMIT = 3\n"), layout.test: []byte("assert LIMIT == 3\n"),
				}
				if layout.name == "javascript" {
					sources[layout.code], sources[layout.test] = []byte("export const limit = 3;\n"), []byte("assert.equal(limit, 3);\n")
				}
				for path, data := range sources {
					writeFixture(t, filepath.Join(cfg.ProjectRoot, path), data)
				}
				config := filepath.Join(base, "config.yaml")
				if external {
					cfg.External = &ExternalConfig{Project: "neutral-" + layout.name, Corpus: "contract", Scope: "limits", ProfileID: "plain-markdown", ProfileVersion: "1"}
				}
				saveStartupConfig(t, config, cfg)
				if external {
					input := externalFixture{config: config, base: base, corpus: filepath.Join(base, "delivery/corpus"), input: filepath.Join(base, "delivery/audit-input.json")}
					profile := []byte("{}\n")
					writeFixture(t, filepath.Join(base, "delivery/profile.json"), profile)
					input.manifest = ExternalManifest{SchemaVersion: 1, SpecVersion: "capture-1", Project: cfg.External.Project, Corpus: cfg.External.Corpus,
						Profile: ExternalProfile{cfg.External.ProfileID, "1", digest(profile)}, FileCount: 2, Files: []ExternalFile{}}
					for _, source := range []struct{ path, role, media string }{{layout.spec, "normative", "text/markdown"}, {layout.reference, "reference", "application/json"}} {
						data := sources[source.path]
						writeFixture(t, filepath.Join(input.corpus, source.path), data)
						origin := map[string]string{"kind": "snapshot", "snapshot_id": "capture-1", "snapshot_sha256": digest(data), "path": source.path}
						if layout.origin == "git" {
							origin = map[string]string{"kind": "git", "repository": "https://example.invalid/neutral", "commit": strings.Repeat("1", 40), "path": source.path}
						}
						input.manifest.Files = append(input.manifest.Files, ExternalFile{source.path, source.role, true, source.media, int64(len(data)), digest(data), mustJSON(t, origin)})
					}
					files, _, err := selectedCode(cfg)
					if err != nil {
						t.Fatal(err)
					}
					input.request = AuditInput{SchemaVersion: 1, CorpusDir: input.corpus, ManifestPath: filepath.Join(base, "delivery/source-manifest.json"), ProfilePath: filepath.Join(base, "delivery/profile.json"),
						CodeBasis: mustJSON(t, map[string]string{"kind": "snapshot", "snapshot_id": "code-1", "sha256": codeDigest(files)}), Freshness: ExternalFreshness{Status: "unknown"}}
					input.save(t)
					cfg.References.Include = []string{"*.md"}
					saveStartupConfig(t, config, cfg)
					if _, err := execute([]string{"external", "check", config, input.input}); err == nil || !strings.Contains(err.Error(), layout.reference) || !strings.Contains(err.Error(), "недостающие") {
						t.Fatalf("JSON reference not diagnosed: %v", err)
					}
					cfg.References.Include = nil
					cfg.Specs.Include = []string{"*.md"}
					cfg.References.Exclude = []string{"unused.*"}
					saveStartupConfig(t, config, cfg)
					runOK(t, "external", "check", config, input.input)
					config = input.install(t)
				}
				runOK(t, "index", config, "summary")
				startupComplete(t, config)
				if _, err := os.Stat(filepath.Join(cfg.ProjectRoot, ".git")); !os.IsNotExist(err) {
					t.Fatalf("Git dependency introduced: %v", err)
				}
				for path, data := range sources {
					if !bytes.Equal(data, readFixture(t, filepath.Join(cfg.ProjectRoot, path))) {
						t.Fatalf("source changed: %s", path)
					}
				}
			})
		}
	}
}

func TestStartupGuideTemplates(t *testing.T) {
	files, err := skillFiles("dev")
	if err != nil {
		t.Fatal(err)
	}
	guide := string(files["references/docs/external-start.md"])
	if guide == "" {
		t.Fatal("startup guide not shipped")
	}
	linkPattern := regexp.MustCompile(`\]\(([^)#]+)(?:#[^)]*)?\)`)
	for _, match := range linkPattern.FindAllStringSubmatch(guide, -1) {
		if _, ok := files[filepath.ToSlash(filepath.Join("references/docs", match[1]))]; !ok {
			t.Fatalf("guide link unavailable without checkout: %s", match[1])
		}
	}
	yamlBlock := regexp.MustCompile("(?s)```yaml\n(.*?)\n```").FindStringSubmatch(guide)
	jsonBlocks := regexp.MustCompile("(?s)```json\n(.*?)\n```").FindAllStringSubmatch(guide, -1)
	if len(yamlBlock) != 2 || len(jsonBlocks) != 3 {
		t.Fatal("missing complete config/request/vector examples")
	}
	for index, basis := range []string{"git", "snapshot"} {
		t.Run(basis, func(t *testing.T) {
			base := t.TempDir()
			var cfg Config
			if err := yaml.Unmarshal([]byte(yamlBlock[1]), &cfg); err != nil {
				t.Fatal(err)
			}
			cfg.ProjectRoot, cfg.ReportsDir = filepath.Join(base, "source"), filepath.Join(base, "reports")
			config := filepath.Join(base, "config.yaml")
			saveStartupConfig(t, config, cfg)
			pythonBlocks := regexp.MustCompile("(?s)```python\n(.*?)\n```").FindAllStringSubmatch(guide, -1)
			if len(pythonBlocks) != 2 {
				t.Fatal("missing digest input bytes")
			}
			for fileIndex, path := range []string{"logic.py", "test_logic.py"} {
				writeFixture(t, filepath.Join(cfg.ProjectRoot, path), []byte(pythonBlocks[fileIndex][1]+"\n"))
			}
			input := externalFixture{config: config, base: base, corpus: filepath.Join(base, "input/corpus"), input: filepath.Join(base, "input/audit-input.json")}
			if err := strictJSON([]byte(jsonBlocks[index][1]), &input.request); err != nil {
				t.Fatal(err)
			}
			profile := []byte("{}\n")
			input.request.CorpusDir, input.request.ManifestPath, input.request.ProfilePath = input.corpus, filepath.Join(base, "input/source-manifest.json"), filepath.Join(base, "input/profile.json")
			writeFixture(t, input.request.ProfilePath, profile)
			input.manifest = ExternalManifest{SchemaVersion: 1, SpecVersion: "example-1", Project: cfg.External.Project, Corpus: cfg.External.Corpus, Profile: ExternalProfile{cfg.External.ProfileID, cfg.External.ProfileVersion, digest(profile)}, Files: []ExternalFile{}, FileCount: 2}
			for _, file := range []struct{ path, role, media, data string }{{"rules.md", "normative", "text/markdown", "### REQ-EXAMPLE-001 — Предел\n\nУсловие: запрос значения предела.\nТребование: значение должно быть равно 3.\nПроверка: сравнить с 3.\n"}, {"context.json", "reference", "application/json", "{}\n"}} {
				data := []byte(file.data)
				writeFixture(t, filepath.Join(input.corpus, file.path), data)
				origin := map[string]string{"kind": "snapshot", "snapshot_id": "example-1", "snapshot_sha256": digest(data), "path": file.path}
				input.manifest.Files = append(input.manifest.Files, ExternalFile{file.path, file.role, true, file.media, int64(len(data)), digest(data), mustJSON(t, origin)})
			}
			if basis == "git" {
				startupGit(t, cfg.ProjectRoot, "init", "-q")
				startupGit(t, cfg.ProjectRoot, "add", ".")
				startupGit(t, cfg.ProjectRoot, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "commit", "-qm", "example")
				input.request.CodeBasis = mustJSON(t, map[string]string{"kind": "git", "repository": cfg.ProjectRoot, "commit": startupGit(t, cfg.ProjectRoot, "rev-parse", "HEAD")})
				writeFixture(t, filepath.Join(cfg.ProjectRoot, ".git/info/exclude"), []byte("scratch.bin\n"))
				writeFixture(t, filepath.Join(cfg.ProjectRoot, "scratch.bin"), []byte("unselected ignored data"))
			} else {
				t.Setenv("PATH", t.TempDir())
				selected, _, err := selectedCode(cfg)
				if err != nil {
					t.Fatal(err)
				}
				vector, err := json.Marshal(selected)
				if err != nil || string(vector) != jsonBlocks[2][1] {
					t.Fatalf("documented digest vector diverges: %s %v", vector, err)
				}
			}
			input.save(t)
			runOK(t, "external", "check", config, input.input)
			imported := input.install(t)
			runOK(t, "index", imported, "summary")
			if basis == "snapshot" {
				input.request.CodeBasis = mustJSON(t, map[string]string{"kind": "snapshot", "snapshot_id": "example-code-1", "sha256": strings.Repeat("0", 64)})
				input.save(t)
				runFail(t, "external", "check", config, input.input)
			}
		})
	}
}
