//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type externalFixture struct {
	config, base, input, corpus string
	manifest                    ExternalManifest
	request                     AuditInput
}

func TestExactSystemPathsPreserveSymlinkBoundary(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "input.json")
	if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := readExact(file, 64); err != nil || string(data) != "{}" {
		t.Fatalf("OS temporary path must be readable: %q, %v", data, err)
	}
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp", "/etc"} {
			if got := exactSystemPath(alias + "/fixture"); got != "/private"+alias+"/fixture" {
				t.Fatalf("system alias %s: %s", alias, got)
			}
		}
	}
	link := filepath.Join(base, "caller-link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readExact(filepath.Join(link, "input.json"), 64); err == nil {
		t.Fatal("caller-supplied ancestor symlink was accepted")
	}
	if err := mkdirExact(filepath.Join(link, "new", "child"), 0700); err == nil {
		t.Fatal("destination symlink was accepted")
	}
	if _, err := os.Lstat(filepath.Join(base, "new")); !os.IsNotExist(err) {
		t.Fatalf("rejected destination created children: %v", err)
	}
}

func newExternalFixture(t *testing.T) externalFixture {
	t.Helper()
	config, base := fixture(t)
	writeFixture(t, filepath.Join(base, "source/rules.md"), readFixture(t, "testdata/external/A/corpus/rules.md"))
	return externalFromConfig(t, config, base)
}
func externalFromConfig(t *testing.T, config, base string) externalFixture {
	t.Helper()
	f := externalFixture{config: config, base: base, input: filepath.Join(base, "audit-input.json"), corpus: filepath.Join(base, "corpus")}
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	cfg.References = &Sources{Paths: []string{"context.md"}}
	cfg.External = &ExternalConfig{Scope: "fixture-scope", Project: "fixture-project", Corpus: "fixture-corpus", ProfileID: "fixture-markdown", ProfileVersion: "1"}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, raw)
	reference := []byte("\xef\xbb\xbfПервая строка · е\u0308\r\n### DEF-DEMO-001 — Контекст\r\nОпределение: сохранённая ссылка.\r\n")
	writeFixture(t, filepath.Join(base, "source/context.md"), reference)
	profile := []byte("{ \"navigation\": \"REQ/Q/DEF\", \"fixture_only\": true }\r\n")
	writeFixture(t, filepath.Join(base, "profile.json"), profile)
	f.manifest = ExternalManifest{SchemaVersion: 1, SpecVersion: "fixture-A", Project: "fixture-project", Corpus: "fixture-corpus", Profile: ExternalProfile{"fixture-markdown", "1", digest(profile)}, Files: []ExternalFile{}}
	for i, item := range []struct {
		path, role, media string
		data              []byte
	}{{"rules.md", "normative", "text/markdown", readFixture(t, filepath.Join(base, "source/rules.md"))}, {"context.md", "reference", "text/markdown", reference}, {"assets/same-name.bin", "archive", "application/octet-stream", []byte{0, 1, 2, 255}}} {
		writeFixture(t, filepath.Join(f.corpus, item.path), item.data)
		origin := map[string]any{"kind": "trucker", "project_id": "fixture-project", "document_id": fmt.Sprintf("synthetic-document-%d", i), "revision_id": "synthetic-revision-A", "file_version_id": nil, "source_url": "https://example.invalid/current"}
		if item.role == "archive" {
			origin["file_version_id"] = "synthetic-file-A"
		}
		f.manifest.Files = append(f.manifest.Files, ExternalFile{item.path, item.role, true, item.media, int64(len(item.data)), digest(item.data), mustJSON(t, origin)})
	}
	f.manifest.FileCount = len(f.manifest.Files)
	cfg, err = loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := selectedCode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.request = AuditInput{SchemaVersion: 1, CorpusDir: f.corpus, ManifestPath: filepath.Join(base, "source-manifest.json"), ProfilePath: filepath.Join(base, "profile.json"), CodeBasis: mustJSON(t, map[string]string{"kind": "snapshot", "snapshot_id": "fixture-code", "sha256": codeDigest(code)}), Freshness: ExternalFreshness{Status: "unknown"}}
	f.save(t)
	return f
}
func (f *externalFixture) save(t *testing.T) {
	t.Helper()
	raw := append([]byte(" \r\n"), mustJSON(t, f.manifest)...)
	writeFixture(t, f.request.ManifestPath, raw)
	f.request.ManifestSHA256 = digest(raw)
	writeFixture(t, f.input, append(mustJSON(t, f.request), []byte("\r\n")...))
}
func (f externalFixture) install(t *testing.T) string {
	t.Helper()
	value := runOK(t, "external", "import", f.config, f.input).(map[string]any)
	return value["config"].(string)
}
func completeExternal(t *testing.T, config, run string) TaskBatch {
	t.Helper()
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	batch := runOK(t, "prepare", config, run).(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, cfg.ProjectRoot)
		if task.Role == "redteam" {
			for i := range result.Assessments {
				a := &result.Assessments[i]
				if a.RequirementID == "REQ-DEMO-001" {
					a.Implementation = "unknown"
					a.Assertion = "unknown"
					a.Statement = "Детерминированное расхождение ролей для проверки отображения"
				}
			}
		}
		present := map[string]bool{}
		for _, a := range result.Assessments {
			present[a.RequirementID] = true
		}
		for _, req := range task.Requirements {
			if !present[req.ID] {
				clarity := "clear"
				spec := []Citation{req.Source}
				if req.Accepted != nil {
					clarity = req.Accepted.Clarity
					spec = req.Accepted.Citations
				}
				result.Assessments = append(result.Assessments, Assessment{RequirementID: req.ID, Specification: clarity, Implementation: "unknown", Assertion: "unknown", Statement: "Детерминированный fixture без смыслового вывода", Spec: spec, Code: []Citation{}, Tests: []TestCitation{}, Limitations: []string{"test data"}})
			}
		}

		path := filepath.Join(cfg.ReportsDir, run, "dispatch", task.TaskID, "result.json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "validate", config, run, task.TaskID, path)
		runOK(t, "submit", config, run, task.TaskID, path)
	}
	view := runOK(t, "review", config, run).(ReviewContext)
	assessments := []Assessment{}
	for _, e := range view.Entries {
		if e.Task.Role == "mapper" && e.Result != nil {
			assessments = append(assessments, e.Result.Assessments...)
		}
	}
	if batch.Incremental != nil {
		// The host still explicitly reviews the carried rows through the ordinary v2 draft.
		draft := runOK(t, "draft", config, run).(ReviewDecisionV3)
		draft.ReviewID = "fixture-decision"
		draft.Reviewer = "deterministic-test-data"
		draft.Summary = "Механический fixture, не аудит продукта"
		for i := range draft.Verdicts {
			v := &draft.Verdicts[i]
			for _, a := range assessments {
				if v.RequirementID == a.RequirementID {
					v.Specification = a.Specification
					v.Implementation = a.Implementation
					v.Assertion = a.Assertion
					v.Concur = "both"
					v.Statement = a.Statement
					v.Limitations = []string{}
				}
			}
		}
		draft.Counts = countVerdicts(baseVerdicts(draft.Verdicts))
		path := filepath.Join(cfg.ReportsDir, run, "fixture-host.json")
		writeFixture(t, path, mustJSON(t, draft))
		runOK(t, "review", config, run, path)
	} else {
		decision := ReviewDecision{1, "fixture-decision", run, view.SnapshotID, view.BasisSHA256, "deterministic-test-data", "Механический fixture, не аудит продукта", assessments, []string{"Детерминированные результаты ролей: test data"}}
		path := filepath.Join(cfg.ReportsDir, run, "fixture-host.json")
		writeFixture(t, path, mustJSON(t, decision))
		runOK(t, "review", config, run, path)
	}
	view = runOK(t, "review", config, run).(ReviewContext)
	check := ReviewCheck{1, run, view.SnapshotID, view.Latest.ReviewID, view.History[len(view.History)-1].RawSHA256, view.Latest.BasisSHA256, "independent-fixture-session", "deterministic-test-data-only", "confirmed", "Механический fixture перепроверки; не реальный аудит продукта", []string{"Тестовые данные, независимость модели этим тестом не доказана"}}
	checkPath := filepath.Join(cfg.ReportsDir, run, "fixture-check.json")
	writeFixture(t, checkPath, mustJSON(t, check))
	runOK(t, "review-check", config, run, checkPath)
	runOK(t, "report", config, run)
	return batch
}

func TestExternalBoundaryAndSourceBytes(t *testing.T) {
	f := newExternalFixture(t)
	manifestBefore := readFixture(t, f.request.ManifestPath)
	profileBefore := readFixture(t, f.request.ProfilePath)
	requestBefore := readFixture(t, f.input)
	runOK(t, "external", "check", f.config, f.input)
	if _, err := os.Stat(filepath.Join(f.base, "runs")); !os.IsNotExist(err) {
		t.Fatal("check installed state")
	}
	config := f.install(t)
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"source-manifest.json": manifestBefore, "profile.json": profileBefore, "audit-input.json": requestBefore} {
		if !bytes.Equal(want, readFixture(t, filepath.Join(filepath.Dir(config), name))) {
			t.Fatal("raw changed", name)
		}
	}
	m, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m.External.Scope != "fixture-scope" {
		t.Fatal("scope lost")
	}
	original := cfg
	original.External = nil
	original.ProjectRoot = filepath.Join(f.base, "source")
	local, err := snapshot(original)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(local.Requirements, m.Requirements) {
		t.Fatal("different requirement semantics")
	}
	c := runOK(t, "cite", config, "context.md", "1", "1").(Citation)
	want, _ := lineQuote(readFixture(t, filepath.Join(f.corpus, "context.md")), 1, 1)
	if c.Quote != want {
		t.Fatal("BOM/CRLF quote changed")
	}
	batch := completeExternal(t, config, "A")
	var report Report
	raw := readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"))
	if err := strictJSON(raw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Sources) != len(m.Files)+1+5 || !report.HostReviewComplete || report.External.Freshness.Status != "unknown" {
		t.Fatal("incomplete external report", len(report.Sources))
	}
	for _, s := range report.Sources {
		if !s.Full || s.URL == "" {
			t.Fatal("source not archived")
		}
		if s.Path == "context.md" {
			if !bytes.Equal(readFixture(t, filepath.Join(cfg.ReportsDir, "A", s.RawURL)), readFixture(t, filepath.Join(f.corpus, s.Path))) {
				t.Fatal("CRLF/BOM raw changed")
			}
		}
	}
	result := runOK(t, "external", "result", config, "A").(ExternalResult)
	if result.Scope != "fixture-scope" || result.Basis.ManifestSHA256 != f.request.ManifestSHA256 || result.Decision.State != "current" {
		t.Fatal("result identity")
	}
	bad := sampleResult(t, batch.Tasks[0], cfg.ProjectRoot)
	bad.Assessments[0].Code[0].LineStart = 0
	path := filepath.Join(f.base, "bad-result.json")
	writeFixture(t, path, mustJSON(t, bad))
	runFail(t, "validate", config, "A", batch.Tasks[0].TaskID, path)
	if !bytes.Equal(manifestBefore, readFixture(t, f.request.ManifestPath)) {
		t.Fatal("modified producer manifest")
	}
	// The frozen input keeps working after the producer and original code disappear.
	if err := os.RemoveAll(f.corpus); err != nil {
		t.Fatal(err)
	}
	if after, err := snapshot(cfg); err != nil || after.SnapshotID != m.SnapshotID {
		t.Fatal("binding depends on producer paths", err)
	}
	if err := os.RemoveAll(filepath.Join(f.base, "source")); err != nil {
		t.Fatal(err)
	}
	if status := runOK(t, "status", config, "A").(Status); status.Freshness != "stale" {
		t.Fatal("missing live code root called fresh")
	}
	runOK(t, "report", config, "A") // Archived sources remain readable without either original tree.
}

func TestExternalRejectsBeforeInstall(t *testing.T) {
	for _, kind := range []string{"manifest_hash", "file_hash", "missing", "extra", "duplicate", "traversal", "symlink", "parent_symlink", "schema", "profile_hash", "profile_version", "project", "corpus", "line", "code_hash", "unknown_origin", "missing_field", "null_size", "null_export", "duplicate_key"} {
		t.Run(kind, func(t *testing.T) {
			f := newExternalFixture(t)
			switch kind {
			case "file_hash":
				f.manifest.Files[0].SHA256 = strings.Repeat("0", 64)
			case "missing":
				_ = os.Remove(filepath.Join(f.corpus, "rules.md"))
			case "extra":
				writeFixture(t, filepath.Join(f.corpus, "extra.md"), []byte("extra"))
			case "duplicate":
				f.manifest.Files = append(f.manifest.Files, f.manifest.Files[0])
				f.manifest.FileCount++
			case "traversal":
				f.manifest.Files[0].Path = "../escape.md"
			case "symlink":
				_ = os.Remove(filepath.Join(f.corpus, "rules.md"))
				if err := os.Symlink(filepath.Join(f.base, "source/rules.md"), filepath.Join(f.corpus, "rules.md")); err != nil {
					t.Fatal(err)
				}
			case "parent_symlink":
				link := filepath.Join(f.base, "linked")
				if err := os.Symlink(f.corpus, link); err != nil {
					t.Fatal(err)
				}
				f.request.CorpusDir = link
			case "schema":
				f.manifest.SchemaVersion = 2
			case "profile_hash":
				writeFixture(t, f.request.ProfilePath, []byte("{}"))
			case "profile_version":
				f.manifest.Profile.Version = "unsupported"
			case "project":
				f.manifest.Project = "other"
			case "corpus":
				f.manifest.Corpus = "other"
			case "line":
				line := "other"
				f.manifest.Line = &line
			case "code_hash":
				f.request.CodeBasis = mustJSON(t, map[string]string{"kind": "snapshot", "snapshot_id": "fixture", "sha256": strings.Repeat("0", 64)})
			case "unknown_origin":
				f.manifest.Files[0].Origin = mustJSON(t, map[string]string{"kind": "latest"})
			}
			f.save(t)
			switch kind {
			case "manifest_hash":
				f.request.ManifestSHA256 = strings.Repeat("0", 64)
				writeFixture(t, f.input, mustJSON(t, f.request))
			case "missing_field", "null_size", "null_export", "duplicate_key":
				var object map[string]any
				_ = json.Unmarshal(readFixture(t, f.request.ManifestPath), &object)
				files := object["files"].([]any)
				first := files[0].(map[string]any)
				if kind == "missing_field" {
					delete(first, "export")
				}
				if kind == "null_size" {
					first["size_bytes"] = nil
				}
				if kind == "null_export" {
					first["export"] = nil
				}
				raw := mustJSON(t, object)
				if kind == "duplicate_key" {
					raw = bytes.Replace(raw, []byte("\"file_count\":"), []byte("\"file_count\":3,\"file_count\":"), 1)
				}
				writeFixture(t, f.request.ManifestPath, raw)
				f.request.ManifestSHA256 = digest(raw)
				writeFixture(t, f.input, mustJSON(t, f.request))
			}
			runFail(t, "external", "import", f.config, f.input)
			if _, err := os.Stat(filepath.Join(f.base, "runs/._inputs")); !os.IsNotExist(err) {
				t.Fatal("rejected input installed state")
			}
		})
	}
}

func TestExternalGitBasis(t *testing.T) {
	f := newExternalFixture(t)
	root := filepath.Join(f.base, "source")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture")
	commit := git("rev-parse", "HEAD")
	f.request.CodeBasis = mustJSON(t, map[string]string{"kind": "git", "repository": root, "commit": commit})
	f.save(t)
	runOK(t, "external", "check", f.config, f.input)
	writeFixture(t, filepath.Join(root, "source.go"), []byte("package changed\n"))
	runFail(t, "external", "check", f.config, f.input)
}

func TestExternalHistoryPublicationAndScopes(t *testing.T) {
	f := newExternalFixture(t)
	configA := f.install(t)
	completeExternal(t, configA, "A")
	cfgA, _ := loadConfig(configA)
	before := readFixture(t, filepath.Join(cfgA.ReportsDir, "A/report.html"))
	// Independent scope with the same direct requirement IDs and the same sources.
	other := filepath.Join(f.base, "other/config.yaml")
	cfgOriginal, _ := loadConfig(f.config)
	cfgOriginal.ReportsDir = filepath.Join(f.base, "other-runs")
	ext := *cfgOriginal.External
	ext.Scope = "second-scope"
	cfgOriginal.External = &ext
	raw, _ := yaml.Marshal(cfgOriginal)
	writeFixture(t, other, raw)
	second := runOK(t, "external", "import", other, f.input).(map[string]any)["config"].(string)
	completeExternal(t, second, "A")
	siteA := filepath.Join(f.base, "site-A")
	runOK(t, "publish", siteA, configA, second)
	historical := readFixture(t, filepath.Join(siteA, externalScopePath(cfgA.External)+"/A/report.html"))
	f.manifest.SpecVersion = "fixture-B"
	previous := "fixture-A"
	f.manifest.PreviousSpecVersion = &previous
	checked := "2026-10-16T12:00:00+03:00"
	f.request.Freshness = ExternalFreshness{Status: "changed", CheckedAt: &checked}
	rules := bytes.Replace(readFixture(t, filepath.Join(f.corpus, "rules.md")), []byte("равен 14"), []byte("равен 16"), 1)
	writeFixture(t, filepath.Join(f.corpus, "rules.md"), rules)
	f.manifest.Files[0].SHA256 = digest(rules)
	f.manifest.Files[0].Size = int64(len(rules))
	reference := readFixture(t, filepath.Join(f.corpus, "context.md"))
	writeFixture(t, filepath.Join(f.corpus, "refs/context.md"), reference)
	_ = os.Remove(filepath.Join(f.corpus, "context.md"))
	f.manifest.Files[1].Path = "refs/context.md"
	asset := []byte("synthetic replacement\x00")
	writeFixture(t, filepath.Join(f.corpus, "assets/same-name.bin"), asset)
	f.manifest.Files[2].SHA256 = digest(asset)
	f.manifest.Files[2].Size = int64(len(asset))
	sourceCfg, _ := loadConfig(f.config)
	sourceCfg.References.Paths = []string{"refs/context.md"}
	sourceRaw, _ := yaml.Marshal(sourceCfg)
	writeFixture(t, f.config, sourceRaw)
	f.save(t)
	configB := f.install(t)
	completeExternal(t, configB, "B")
	siteB := filepath.Join(f.base, "site-B")
	runOK(t, "publish", siteB, configB, second)
	if !bytes.Equal(before, historical) || !bytes.Equal(historical, readFixture(t, filepath.Join(siteB, externalScopePath(cfgA.External)+"/A/report.html"))) {
		t.Fatal("old publication changed")
	}
	var view Overview
	if err := json.Unmarshal(readFixture(t, filepath.Join(siteB, "overview.json")), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Scopes[0].History) != 2 || view.Scopes[0].External.SpecVersion != "fixture-B" || view.Scopes[0].Delta == nil || len(view.Scopes[0].Delta.Incomparable) == 0 {
		t.Fatal("history/delta missing", view.Scopes[0])
	}
	for _, name := range []string{externalScopePath(cfgA.External) + "/A/report.json", externalScopePath(cfgA.External) + "/B/result.json", externalScopePath(cfgOriginal.External) + "/A/report.html"} {
		_ = readFixture(t, filepath.Join(siteB, name))
	}
	if output := os.Getenv("SPEC_AUDIT_EXTERNAL_UI"); output != "" {
		copyTestTree(t, siteB, output)
	}
	if output := os.Getenv("SPEC_AUDIT_EXTERNAL_DELIVERY"); output != "" {
		runOK(t, "prepare", configB, "pending")
		request := filepath.Join(f.base, "delivery-request.json")
		writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{configB, second}, []string{siteA, siteB}}))
		archive := filepath.Join(f.base, "delivery.tar.gz")
		runOK(t, "bundle", "export", request, archive)
		writeFixture(t, filepath.Join(output, "declared-history.tar.gz"), readFixture(t, archive))
	}

}
func copyTestTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := walkBundleArea(source, func(path string, data []byte, _ uint32) error {
		writeFixture(t, filepath.Join(destination, path), data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalAcceptedIdentityRelocateAndRestore(t *testing.T) {
	config, base := acceptedFixture(t)
	f := externalFromConfig(t, config, base)
	conflict := []byte("Лимит одного файла — 10 МиБ включительно.\r\n")
	writeFixture(t, filepath.Join(f.corpus, "context.md"), conflict)
	writeFixture(t, filepath.Join(base, "source/context.md"), conflict)
	f.manifest.Files[1].SHA256 = digest(conflict)
	f.manifest.Files[1].Size = int64(len(conflict))
	f.save(t)
	configA := f.install(t)
	candidate := candidateAt(t, base, "rules.md", "C001", "Лимит одного файла — 8 МиБ включительно.", 2, 2)
	candidate.Clarity = "ambiguous"
	candidate.Unresolved = []string{"В rules.md лимит 8 МиБ, в context.md — 10 МиБ. Какой считать обязательным?"}
	quote, _ := lineQuote(readFixture(t, filepath.Join(f.corpus, "context.md")), 1, 1)
	candidate.Citations = append(candidate.Citations, Citation{"context.md", 1, 1, quote})
	raw, decision := acceptedInputs(t, configA, "first", []legacyCandidate{candidate}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", configA, raw, decision)
	completeExternal(t, configA, "A")
	before := acceptedRead(t, configA)
	cfgA, _ := loadConfig(configA)
	ledger := readFixture(t, filepath.Join(cfgA.ReportsDir, acceptedFile))
	f.manifest.SpecVersion = "fixture-B"
	f.save(t)
	configB := f.install(t)
	if !bytes.Equal(ledger, readFixture(t, filepath.Join(cfgA.ReportsDir, acceptedFile))) || !reflect.DeepEqual(before.Records, acceptedRead(t, configB).Records) {
		t.Fatal("external version auto-reconciled index")
	}
	// Move the document without changing the source document identity or meaning.
	data := readFixture(t, filepath.Join(f.corpus, "rules.md"))
	writeFixture(t, filepath.Join(f.corpus, "moved/rules.md"), append([]byte("\n"), data...))
	_ = os.Remove(filepath.Join(f.corpus, "rules.md"))
	f.manifest.SpecVersion = "fixture-C"
	f.manifest.Files[0].Path = "moved/rules.md"
	f.manifest.Files[0].Size = int64(len(data) + 1)
	f.manifest.Files[0].SHA256 = digest(append([]byte("\n"), data...))
	f.save(t)
	cfgOriginal, _ := loadConfig(f.config)
	cfgOriginal.Specs.Paths = []string{"moved/rules.md"}
	configRaw, _ := yaml.Marshal(cfgOriginal)
	writeFixture(t, f.config, configRaw)
	configC := f.install(t)
	runFail(t, "prepare", configC, "unaccepted")
	proposal := runOK(t, "reconcile", configC, "relocate").(map[string]any)
	if proposal["counts"].(map[string]int)["relocate"] != 1 {
		t.Fatal("rename not proposed", proposal)
	}
	raw, decision = acceptedInputs(t, configC, "relocate", []legacyCandidate{})
	var d AcceptedDecision
	if err := decodeDecision(readFixture(t, decision), &d); err != nil {
		t.Fatal(err)
	}
	d.Version = 4
	writeFixture(t, decision, mustJSON(t, d))
	runOK(t, "check", configC, raw, decision)
	runOK(t, "reconcile", configC, raw, decision)
	after := acceptedRead(t, configC)
	if before.Records[0].Requirement.ID != after.Records[0].Requirement.ID || before.Records[0].Revision != after.Records[0].Revision || !reflect.DeepEqual(before.Records[0].Requirement.Accepted.Unresolved, after.Records[0].Requirement.Accepted.Unresolved) {
		t.Fatal("rename lost ID/revision/questions")
	}
	if after.Records[0].Requirement.Source.Path != "moved/rules.md" {
		t.Fatal("source not relocated")
	}
	completeExternal(t, configC, "C")
	if output := os.Getenv("SPEC_AUDIT_EXTERNAL_UI"); output != "" {
		site := filepath.Join(base, "conflict-site")
		runOK(t, "publish", site, configC)
		copyTestTree(t, site, filepath.Join(output, "conflict"))
	}
	runOK(t, "prepare", configC, "pending")
	archive := exportTestBundle(t, configC, base)
	if output := os.Getenv("SPEC_AUDIT_EXTERNAL_DELIVERY"); output != "" {
		writeFixture(t, filepath.Join(output, "accepted-history.tar.gz"), readFixture(t, archive))
	}
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(base, "restored")).(map[string]any)
	next := restored["configs"].([]string)[0]
	if !reflect.DeepEqual(after, acceptedRead(t, next)) {
		t.Fatal("ledger state changed on restore")
	}
	runOK(t, "prepare", next, "next")
}

func TestExternalIncrementalAndReviewCheck(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, _ := loadConfig(config)
	plan := runOK(t, "plan", config, "A").(map[string]any)
	if plan["carried"].(int) == 0 {
		t.Fatal("no carried verdicts")
	}
	// Reuse the ordinary full-cycle helper after preparing the incremental task pair.
	batch := runOK(t, "prepare", config, "B", "since", "A").(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, cfg.ProjectRoot)
		path := filepath.Join(cfg.ReportsDir, "B/dispatch", task.TaskID, "result.json")
		writeFixture(t, path, mustJSON(t, result))
		runOK(t, "submit", config, "B", task.TaskID, path)
	}
	draft := runOK(t, "draft", config, "B").(ReviewDecisionV3)
	view := runOK(t, "review", config, "B").(ReviewContext)
	draft.Reviewer = "deterministic-test-host"
	draft.Summary = "Тест инкрементального контракта"
	for i := range draft.Verdicts {
		v := &draft.Verdicts[i]
		if v.Concur == "carried" {
			continue
		}
		for _, e := range view.Entries {
			if e.Task.Role != "mapper" || e.Result == nil {
				continue
			}
			for _, a := range e.Result.Assessments {
				if a.RequirementID == v.RequirementID {
					v.Specification = a.Specification
					v.Implementation = a.Implementation
					v.Assertion = a.Assertion
					v.Concur = "both"
					v.Statement = a.Statement
				}
			}
		}
	}
	draft.Counts = countVerdicts(baseVerdicts(draft.Verdicts))
	path := filepath.Join(f.base, "incremental-host.json")
	writeFixture(t, path, mustJSON(t, draft))
	runOK(t, "review", config, "B", path)
	runOK(t, "report", config, "B")
	runFail(t, "publish", filepath.Join(f.base, "unchecked-site"), config)
	view = runOK(t, "review", config, "B").(ReviewContext)
	check := ReviewCheck{1, "B", view.SnapshotID, view.Latest.ReviewID, view.History[len(view.History)-1].RawSHA256, view.Latest.BasisSHA256, "fixture-independent", "fresh-fixture-context", "confirmed", "Механический тест независимой перепроверки", []string{"Детерминированный fixture"}}
	checkPath := filepath.Join(f.base, "check.json")
	bad := check
	bad.ReviewSHA256 = strings.Repeat("0", 64)
	writeFixture(t, checkPath, mustJSON(t, bad))
	runFail(t, "review-check", config, "B", checkPath)
	bad = check
	bad.Reviewer = view.Latest.Reviewer
	writeFixture(t, checkPath, mustJSON(t, bad))
	runFail(t, "review-check", config, "B", checkPath)
	writeFixture(t, checkPath, mustJSON(t, check))
	runOK(t, "review-check", config, "B", checkPath)
	runOK(t, "report", config, "B")
	runOK(t, "publish", filepath.Join(f.base, "checked-site"), config)
	status := runOK(t, "status", config, "B").(Status)
	if !status.DeliveryComplete {
		t.Fatal("incomplete delivery")
	}
	runOK(t, "retry", config, "B", batch.Tasks[0].TaskID)
	runFail(t, "review-check", config, "B", checkPath)
}

func TestExternalExchangeFixtures(t *testing.T) {
	for _, version := range []string{"A", "B"} {
		t.Run(version, func(t *testing.T) {
			f := newExternalFixture(t)
			dir, err := filepath.Abs(filepath.Join("testdata/external", version))
			if err != nil {
				t.Fatal(err)
			}
			f.request.CorpusDir = filepath.Join(dir, "corpus")
			f.request.ManifestPath = filepath.Join(dir, "source-manifest.json")
			f.request.ProfilePath = filepath.Join(dir, "profile.json")
			f.request.ManifestSHA256 = digest(readFixture(t, f.request.ManifestPath))
			writeFixture(t, f.input, mustJSON(t, f.request))
			cfg, _ := loadConfig(f.config)
			if version == "B" {
				cfg.References.Paths = []string{"refs/context.md"}
				raw, _ := yaml.Marshal(cfg)
				writeFixture(t, f.config, raw)
			}
			runOK(t, "external", "check", f.config, f.input)
			config := f.install(t)
			completeExternal(t, config, version)
		})
	}
}

func TestExternalNativeStaleness(t *testing.T) {
	for _, kind := range []string{"code", "reference"} {
		t.Run(kind, func(t *testing.T) {
			f := newExternalFixture(t)
			configA := f.install(t)
			completeExternal(t, configA, "A")
			if kind == "code" {
				path := filepath.Join(f.base, "source/source.go")
				writeFixture(t, path, append(readFixture(t, path), []byte("\n// Fixture code change\n")...))
				cfg, _ := loadConfig(f.config)
				files, _, err := selectedCode(cfg)
				if err != nil {
					t.Fatal(err)
				}
				f.request.CodeBasis = mustJSON(t, map[string]string{"kind": "snapshot", "snapshot_id": "fixture-code-B", "sha256": codeDigest(files)})
			} else {
				path := filepath.Join(f.corpus, "context.md")
				data := append(readFixture(t, path), []byte("Новая справочная строка.\r\n")...)
				writeFixture(t, path, data)
				f.manifest.Files[1].SHA256 = digest(data)
				f.manifest.Files[1].Size = int64(len(data))
			}
			f.manifest.SpecVersion = "fixture-B"
			f.save(t)
			configB := f.install(t)
			status := runOK(t, "status", configB, "A").(Status)
			if status.Freshness != "stale" {
				t.Fatal("changed basis became fresh")
			}
			runOK(t, "report", configB, "A")
			cfg, _ := loadConfig(configB)
			var report Report
			if err := json.Unmarshal(readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json")), &report); err != nil {
				t.Fatal(err)
			}
			if !report.HostReviewComplete || report.HostReview.State != "outdated" || report.External.SpecVersion != "fixture-A" || report.External.Freshness.Status != "unknown" {
				t.Fatal("historical basis/freshness mixed", report.HostReview)
			}
		})
	}
}

func TestExternalTypedRuntimePreservesContainerRoot(t *testing.T) {
	typed := newTypedFixture(t, "php")
	f := externalFromConfig(t, typed.config, typed.base)
	config := f.install(t)
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeSourceRoot(cfg) != typed.source || cfg.ProjectRoot == typed.source {
		t.Fatal("runtime/saved source roots not separated")
	}
	m, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	typed.mockDocker(t, phpstanJSON(t, typed.body(t, nil), 0, nil))
	if _, err := phpTyped(cfg, m, []string{"Subject.php"}); err != nil {
		t.Fatal("external typed SDK", err)
	}
	before := len(callLog(t, filepath.Join(typed.base, "docker-calls.log")))
	writeFixture(t, filepath.Join(typed.source, "Subject.php"), []byte("<?php // changed\n"))
	if _, err := phpTyped(cfg, m, []string{"Subject.php"}); err == nil {
		t.Fatal("executed changed code basis")
	}
	if len(callLog(t, filepath.Join(typed.base, "docker-calls.log"))) != before {
		t.Fatal("dirty runtime reached container")
	}
}

func TestExternalObservationDoesNotChangeNativeSnapshot(t *testing.T) {
	f := newExternalFixture(t)
	a := f.install(t)
	completeExternal(t, a, "A")
	cfgA, _ := loadConfig(a)
	mA, err := snapshot(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	checked := "2026-10-16T12:00:00+03:00"
	f.request.Freshness = ExternalFreshness{Status: "changed", CheckedAt: &checked}
	f.save(t)
	b := f.install(t)
	cfgB, _ := loadConfig(b)
	mB, err := snapshot(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	if mA.SnapshotID != mB.SnapshotID || mA.External.InputSHA256 == mB.External.InputSHA256 {
		t.Fatal("observation changed native identity or lost request provenance")
	}
	status := runOK(t, "status", b, "A").(Status)
	if status.Freshness != "fresh" {
		t.Fatal("observation made native snapshot stale")
	}
	view := runOK(t, "overview", b).(Overview)
	if view.Scopes[0].External.Freshness.Status != "changed" {
		t.Fatal("current external observation hidden")
	}
	var report Report
	_ = json.Unmarshal(readFixture(t, filepath.Join(cfgA.ReportsDir, "A/report.json")), &report)
	if report.External.Freshness.Status != "unknown" {
		t.Fatal("old run request rewritten")
	}
	codePath := filepath.Join(f.base, "source/source.go")
	writeFixture(t, codePath, append(readFixture(t, codePath), []byte("\n// Further live drift\n")...))
	drifted := runOK(t, "overview", b).(Overview)
	if drifted.Scopes[0].External == nil || drifted.Scopes[0].External.Freshness.Status != "changed" || drifted.Scopes[0].Requirements != 5 || len(drifted.Scopes[0].Questions) == 0 {
		t.Fatal("native drift hid chosen corpus/observation/questions")
	}

}

func TestExternalPublishRejectsSymlinkBeforeCreatingChildren(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	out := filepath.Join(f.base, "site")
	outside := filepath.Join(f.base, "outside")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(out, "external")); err != nil {
		t.Fatal(err)
	}
	runFail(t, "publish", out, config)
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("symlink refusal created outside files", err)
	}
}

func TestExternalSourcesCannotDowngradeToQuoteOnly(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, _ := loadConfig(config)
	if err := os.Remove(filepath.Join(cfg.ReportsDir, "A/sources.json")); err != nil {
		t.Fatal(err)
	}
	runFail(t, "report", config, "A")
	request := filepath.Join(f.base, "bundle-request.json")
	writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{config}, []string{}}))
	out := filepath.Join(f.base, "bad.tar.gz")
	runFail(t, "bundle", "export", request, out)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("incomplete external run exported")
	}
}
