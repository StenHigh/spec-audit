//go:build darwin || linux

package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

const retentionLifecycleMarker = "UNQUOTED_SOURCE_MARKER_LIFECYCLE"

func citationLifecycleFixture(t *testing.T) externalFixture {
	t.Helper()
	config, base := fixture(t)
	writeFixture(t, filepath.Join(base, "source/rules.md"), append([]byte("<!-- "+retentionLifecycleMarker+" -->\n\n"), readFixture(t, "testdata/external/A/corpus/rules.md")...))
	mod := filepath.Join(base, "source/go.mod")
	writeFixture(t, mod, append(readFixture(t, mod), []byte("\n// "+retentionLifecycleMarker+"\n")...))
	writeFixture(t, filepath.Join(base, "source/extra_test.go"), []byte("package fixture\n// "+retentionLifecycleMarker+"\n"))
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SourceRetention = "citations"
	cfg.Tests.Paths = append(cfg.Tests.Paths, "extra_test.go")
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, raw)
	f := externalFromConfig(t, config, base)
	for i := range f.manifest.Files {
		file := &f.manifest.Files[i]
		if file.Role == "reference" || file.Role == "archive" {
			path := filepath.Join(f.corpus, file.Path)
			data := append([]byte(retentionLifecycleMarker+"\n"), readFixture(t, path)...)
			writeFixture(t, path, data)
			file.Size, file.SHA256 = int64(len(data)), digest(data)
		}
	}
	f.save(t)
	return f
}

// These deterministic role/check records exercise mechanics, not a semantic
// audit or an independent model review of a product.
func finishCitationPrepared(t *testing.T, config, run string, withCheck bool) {
	t.Helper()
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	batch := runOK(t, "tasks", config, run).(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, cfg.ProjectRoot)
		path := filepath.Join(cfg.ReportsDir, run, fmt.Sprintf("fixture-%s-%d.json", task.TaskID, task.Attempt))
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, run, task.TaskID, path)
	}
	view := runOK(t, "review", config, run).(ReviewContext)
	draft := runOK(t, "draft", config, run).(ReviewDecisionV3)
	draft.ReviewID = fmt.Sprintf("fixture-%s-%d", run, len(view.History)+1)
	draft.Reviewer, draft.Summary = "deterministic-test-data", "Механическая регрессия; не аудит продукта"
	for i := range draft.Verdicts {
		v := &draft.Verdicts[i]
		if v.Concur == "carried" {
			continue
		}
		found := false
		for _, entry := range view.Entries {
			if entry.Task.Role != "mapper" || entry.Result == nil {
				continue
			}
			for _, a := range entry.Result.Assessments {
				if a.RequirementID != v.RequirementID {
					continue
				}
				v.Specification, v.Implementation, v.Assertion, v.Statement = a.Specification, a.Implementation, a.Assertion, a.Statement
				v.Concur, v.Limitations = "mapper", []string{}
				found = true
			}
		}
		if !found {
			t.Fatalf("no mapper fixture for %s", v.RequirementID)
		}
	}
	draft.Counts = countVerdicts(baseVerdicts(draft.Verdicts))
	decision := filepath.Join(cfg.ReportsDir, run, draft.ReviewID+".json")
	writeFixture(t, decision, mustJSON(t, draft))
	runOK(t, "review", config, run, decision)
	if withCheck {
		view = runOK(t, "review", config, run).(ReviewContext)
		check := ReviewCheck{1, run, view.SnapshotID, view.Latest.ReviewID, view.History[len(view.History)-1].RawSHA256, view.Latest.BasisSHA256, "independent-fixture-session", "deterministic-test-data-only", "confirmed", "Механическая перепроверка; не смысловая приёмка", []string{"test data"}}
		path := filepath.Join(cfg.ReportsDir, run, "fixture-check-current.json")
		writeFixture(t, path, mustJSON(t, check))
		runOK(t, "review-check", config, run, path)
	}
	runOK(t, "report", config, run)
}

func assertNoLifecycleMarker(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected file %s", path)
		}
		if bytes.Contains(readFixture(t, path), []byte(retentionLifecycleMarker)) {
			t.Errorf("unquoted source bytes retained in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCitationLifecycleExternalMarkerHistoryAndRestore(t *testing.T) {
	f := citationLifecycleFixture(t)
	configA := f.install(t)
	batchA := completeExternal(t, configA, "A")
	cfgA, _ := loadConfig(configA)
	runOK(t, "external", "release", configA)
	runOK(t, "external", "release", configA)
	assertNoLifecycleMarker(t, cfgA.ReportsDir)
	siteA := filepath.Join(f.base, "site-A")
	runOK(t, "publish", siteA, configA)
	assertNoLifecycleMarker(t, siteA)
	if f.install(t) != configA {
		t.Fatal("same exact input changed its binding")
	}
	completeExternal(t, configA, "A-repeat")
	runOK(t, "external", "release", configA)
	// A new norm reading and code bytes form a distinct, explicitly pinned input.
	rules := filepath.Join(f.corpus, "rules.md")
	data := bytes.Replace(readFixture(t, rules), []byte("произвольным значением bool"), []byte("значением bool: true или false"), 1)
	writeFixture(t, rules, data)
	f.manifest.SpecVersion = "fixture-B"
	for i := range f.manifest.Files {
		if f.manifest.Files[i].Path == "rules.md" {
			f.manifest.Files[i].SHA256, f.manifest.Files[i].Size = digest(data), int64(len(data))
		}
	}
	codePath := filepath.Join(f.base, "source/source.go")
	writeFixture(t, codePath, bytes.Replace(readFixture(t, codePath), []byte("return value\n"), []byte("if value < 0 { return -value }; return value\n"), 1))
	original, _ := loadConfig(f.config)
	files, _, err := selectedCode(original)
	if err != nil {
		t.Fatal(err)
	}
	f.request.CodeBasis = mustJSON(t, map[string]string{"kind": "snapshot", "snapshot_id": "fixture-code-B", "sha256": codeDigest(files)})
	f.save(t)
	configB := f.install(t)
	batchB := runOK(t, "prepare", configB, "B", "since", "A-repeat").(TaskBatch)
	if batchB.Incremental == nil || batchB.Incremental.SinceRun != "A-repeat" {
		t.Fatal("missing incremental basis")
	}
	finishCitationPrepared(t, configB, "B", true)
	ids := func(batch TaskBatch) map[string]bool {
		set := map[string]bool{}
		for _, task := range batch.Tasks {
			for _, req := range task.Requirements {
				set[req.ID] = true
			}
		}
		return set
	}
	viewB := runOK(t, "review", configB, "B").(ReviewContext)
	wantIDs := ids(batchA)
	gotIDs := map[string]bool{}
	for _, req := range viewB.Requirements {
		gotIDs[req.ID] = true
	}
	if !reflect.DeepEqual(wantIDs, gotIDs) {
		t.Fatal("declared IDs lost across revisions", wantIDs, gotIDs)
	}
	runOK(t, "external", "release", configB)
	assertNoLifecycleMarker(t, cfgA.ReportsDir)
	site := filepath.Join(f.base, "site-B")
	runOK(t, "publish", site, configB)
	assertNoLifecycleMarker(t, site)
	view := runOK(t, "overview", configB).(Overview)
	if len(view.Scopes[0].History) != 3 || view.Scopes[0].Delta == nil || view.Scopes[0].Decided.RunID != "B" || view.Scopes[0].Requirements != 5 {
		t.Fatal("history/delta lost", view.Scopes[0])
	}
	archive := exportTestBundle(t, configB, f.base, site)
	bundle, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Index.SchemaVersion != 2 {
		t.Fatal("wrong result package schema")
	}
	for _, object := range bundle.Objects {
		if bytes.Contains(object, []byte(retentionLifecycleMarker)) {
			t.Fatal("source marker leaked into bundle")
		}
	}
	for _, dir := range []string{f.corpus, filepath.Join(f.base, "source")} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(f.base, "restored")).(map[string]any)
	restoredConfig := restored["configs"].([]string)[0]
	runOK(t, "status", restoredConfig, "A")
	citations := runOK(t, "review", restoredConfig, "B", "citations").([]CitationRow)
	if len(citations) == 0 {
		t.Fatal("offline citations lost")
	}
	runOK(t, "report", restoredConfig, "B")
	runFail(t, "prepare", restoredConfig, "missing-input")
	runFail(t, "cite", restoredConfig, "rules.md", "1", "1")
	assertNoLifecycleMarker(t, filepath.Join(f.base, "restored"))
}

func TestCitationLifecyclePolicyCannotSwitch(t *testing.T) {
	for _, from := range []string{"archive", "citations"} {
		t.Run(from, func(t *testing.T) {
			config, base := fixture(t)
			if from == "citations" {
				citationsConfig(t, config)
			}
			runOK(t, "prepare", config, "existing")
			before := readFixture(t, filepath.Join(base, "runs/existing/manifest.json"))
			cfg, _ := loadConfig(config)
			if from == "archive" {
				cfg.SourceRetention = "citations"
			} else {
				cfg.SourceRetention = "archive"
			}
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, config, raw)
			runFail(t, "prepare", config, "switched")
			if !bytes.Equal(before, readFixture(t, filepath.Join(base, "runs/existing/manifest.json"))) {
				t.Fatal("failed policy switch changed existing run")
			}
		})
	}
}

func TestCitationLifecycleReleaseRejectsUnsafeOrIncomplete(t *testing.T) {
	for _, kind := range []string{"pending", "no_check", "tampered", "symlink", "foreign_lock", "metadata_bytes"} {
		t.Run(kind, func(t *testing.T) {
			f := citationLifecycleFixture(t)
			config := f.install(t)
			cfg, _ := loadConfig(config)
			runOK(t, "prepare", config, "A")
			if kind != "pending" {
				finishCitationPrepared(t, config, "A", kind != "no_check")
			}
			switch kind {
			case "tampered":
				path := filepath.Join(cfg.ProjectRoot, "source.go")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, path, []byte("unknown changed bytes"))
			case "symlink":
				if err := os.Symlink(filepath.Join(f.base, "source/source.go"), filepath.Join(cfg.ProjectRoot, "foreign-link")); err != nil {
					t.Fatal(err)
				}
			case "foreign_lock":
				writeFixture(t, filepath.Join(cfg.ProjectRoot, ".foreign.lock"), []byte("unknown bytes must survive"))
			case "metadata_bytes":
				path := filepath.Join(filepath.Dir(cfg.ProjectRoot), "original-config.yaml")
				data := readFixture(t, path)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, path, append(data, '\n'))
			}
			runFail(t, "external", "release", config)
			if _, err := os.Stat(filepath.Join(cfg.ProjectRoot, "rules.md")); err != nil {
				t.Fatal("refusal removed known sources", err)
			}
			if kind == "foreign_lock" && string(readFixture(t, filepath.Join(cfg.ProjectRoot, ".foreign.lock"))) != "unknown bytes must survive" {
				t.Fatal("refusal deleted foreign lock")
			}
		})
	}
}

func TestCitationLifecycleReleaseResumesAndAllowsCompletedRetry(t *testing.T) {
	f := citationLifecycleFixture(t)
	config := f.install(t)
	cfg, _ := loadConfig(config)
	batch := runOK(t, "prepare", config, "A").(TaskBatch)
	task := runOK(t, "retry", config, "A", batch.Tasks[0].TaskID).(Task)
	if task.Attempt != 2 {
		t.Fatal("fixture did not create retry")
	}
	finishCitationPrepared(t, config, "A", true)
	x, _, err := savedExternalInput(cfg)
	if err != nil {
		t.Fatal(err)
	}
	receipt := externalReleaseReceipt{1, digest(x.manifestRaw), codeDigest(x.code), false}
	writeFixture(t, filepath.Join(filepath.Dir(cfg.ProjectRoot), "release.json"), mustJSON(t, receipt))
	if err := os.Remove(filepath.Join(cfg.ProjectRoot, "source.go")); err != nil {
		t.Fatal(err)
	}
	runOK(t, "external", "release", config)
	runOK(t, "external", "release", config)
	assertNoLifecycleMarker(t, cfg.ReportsDir)
}

func TestCitationLifecycleInterruptedRehydrateCannotLeak(t *testing.T) {
	f := citationLifecycleFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, _ := loadConfig(config)
	runOK(t, "external", "release", config)
	stage := filepath.Join(filepath.Dir(cfg.ProjectRoot), ".rehydrate-interrupted")
	writeFixture(t, filepath.Join(stage, "tree/go.mod"), readFixture(t, filepath.Join(f.base, "source/go.mod")))
	_, err := execute([]string{"external", "release", config})
	if err != nil {
		if _, statErr := os.Stat(filepath.Join(stage, "tree/go.mod")); statErr != nil {
			t.Fatal("refusal modified interrupted staging", statErr)
		}
		request := filepath.Join(f.base, "export-request.json")
		writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{config}, []string{}}))
		runFail(t, "bundle", "export", request, filepath.Join(f.base, "unsafe.tar.gz"))
		return
	}
	assertNoLifecycleMarker(t, cfg.ReportsDir)
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("successful release retained interrupted copies", err)
	}
}

func TestCitationLifecycleReimportBindsVerifiedMovedCheckout(t *testing.T) {
	f := citationLifecycleFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	runOK(t, "external", "release", config)
	original, err := loadConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(f.base, "moved-checkout")
	if err := os.Rename(original.ProjectRoot, moved); err != nil {
		t.Fatal(err)
	}
	original.ProjectRoot = moved
	raw, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, f.config, raw)
	if reimported := f.install(t); reimported != config {
		t.Fatal("moving exact input created a different historical binding")
	}
	bound, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	physicalMoved, err := filepath.EvalSymlinks(moved)
	if err != nil {
		t.Fatal(err)
	}
	if bound.External.CodeRoot != physicalMoved {
		t.Fatal("reimport kept the vanished checkout", bound.External.CodeRoot)
	}
	completeExternal(t, config, "moved")
	runOK(t, "external", "release", config)
	assertNoLifecycleMarker(t, bound.ReportsDir)
}
