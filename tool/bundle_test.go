//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func exportTestBundle(t *testing.T, config, base string, sites ...string) string {
	t.Helper()
	request := filepath.Join(base, "bundle-request.json")
	writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{config}, sites}))
	out := filepath.Join(base, "evidence.tar.gz")
	// Frozen legacy writer shape: old bundles must remain readable after the
	// public exporter switches to audit-only v3. New export is exercised separately.
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := collectBundle(BundleRequest{1, []string{config}, sites}, []Config{cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBundle(out, b); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestBundleExternalRestoreAndCleanup(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "complete")
	runOK(t, "prepare", config, "pending")
	cfg, _ := loadConfig(config)
	site := filepath.Join(f.base, "site")
	runOK(t, "publish", site, config)
	archive := exportTestBundle(t, config, f.base, site)
	b, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Objects) >= len(b.Index.Files) {
		t.Fatal("no byte deduplication")
	}
	plan := runOK(t, "cleanup", "plan", archive).(CleanupPlan)
	if len(plan.Candidates) != 2 || plan.FreedBytes <= 0 {
		t.Fatal("no safe duplicates", plan)
	}
	for _, candidate := range plan.Candidates {
		if strings.HasPrefix(candidate.Path, "pending/") {
			t.Fatal("active run selected")
		}
	}
	planPath := filepath.Join(f.base, "cleanup-plan.json")
	writeFixture(t, planPath, mustJSON(t, plan))
	proof := runOK(t, "cleanup", "verify", archive, planPath).(CleanupProof)
	proofPath := filepath.Join(f.base, "cleanup-proof.json")
	writeFixture(t, proofPath, mustJSON(t, proof))
	cleanupResult := runOK(t, "cleanup", "apply", archive, planPath, proofPath)
	if output := os.Getenv("SPEC_AUDIT_EXTERNAL_DELIVERY"); output != "" {
		for name, data := range map[string][]byte{"cleanup-evidence.tar.gz": readFixture(t, archive), "cleanup-plan.json": mustJSON(t, plan), "cleanup-proof.json": mustJSON(t, proof), "cleanup-result.json": mustJSON(t, cleanupResult)} {
			writeFixture(t, filepath.Join(output, name), data)
		}
	}
	runOK(t, "cleanup", "apply", archive, planPath, proofPath) // idempotent repeat
	_ = readFixture(t, filepath.Join(cfg.ReportsDir, "complete/report.html"))
	_ = readFixture(t, filepath.Join(site, externalScopePath(cfg.External)+"/complete/report.html"))
	_ = readFixture(t, filepath.Join(cfg.ReportsDir, "pending/state.json"))
	destination := filepath.Join(f.base, "restored")
	restored := runOK(t, "bundle", "restore", archive, destination).(map[string]any)
	restoredConfig := restored["configs"].([]string)[0]
	restoredCfg, err := loadConfig(restoredConfig)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := snapshot(cfg)
	after, err := snapshot(restoredCfg)
	if err != nil || before.SnapshotID != after.SnapshotID {
		t.Fatal("restored snapshot changed", err)
	}
	status := runOK(t, "status", restoredConfig, "pending").(Status)
	if status.DeliveryComplete {
		t.Fatal("pending run became complete")
	}
	for _, entry := range b.Index.Files {
		data := readFixture(t, filepath.Join(destination, "areas", entry.Area, entry.Path))
		if digest(data) != entry.SHA256 {
			t.Fatal("restored bytes changed", entry.Path)
		}
	}
	rawBefore := readFixture(t, filepath.Join(filepath.Dir(config), "source-manifest.json"))
	rawAfter := readFixture(t, filepath.Join(filepath.Dir(restoredCfg.External.Input), "source-manifest.json"))
	if !bytes.Equal(rawBefore, rawAfter) {
		t.Fatal("manifest rebuilt")
	}
	runOK(t, "prepare", restoredConfig, "next")
	runFail(t, "bundle", "restore", archive, destination)
}

func TestCleanupStaleBasisAndActiveWriter(t *testing.T) {
	for _, kind := range []string{"new_file", "candidate_changed", "new_run", "changed_plan", "active_writer", "bad_archive"} {
		t.Run(kind, func(t *testing.T) {
			f := newExternalFixture(t)
			config := f.install(t)
			completeExternal(t, config, "complete")
			cfg, _ := loadConfig(config)
			archive := exportTestBundle(t, config, f.base)
			plan := runOK(t, "cleanup", "plan", archive).(CleanupPlan)
			planPath := filepath.Join(f.base, "plan.json")
			writeFixture(t, planPath, mustJSON(t, plan))
			proof := runOK(t, "cleanup", "verify", archive, planPath).(CleanupProof)
			proofPath := filepath.Join(f.base, "proof.json")
			writeFixture(t, proofPath, mustJSON(t, proof))
			switch kind {
			case "new_file":
				writeFixture(t, filepath.Join(cfg.ReportsDir, "unknown.dat"), []byte("not disposable"))
			case "candidate_changed":
				writeFixture(t, filepath.Join(cfg.ReportsDir, plan.Candidates[0].Path), []byte("new data"))
			case "new_run":
				runOK(t, "prepare", config, "new-run")
			case "changed_plan":
				plan.Candidates[0].Path = "../escape"
				plan.ID = cleanupPlanID(plan)
				writeFixture(t, planPath, mustJSON(t, plan))
			case "bad_archive":
				raw := readFixture(t, archive)
				raw[len(raw)/2] ^= 255
				writeFixture(t, archive, raw)
			case "active_writer":
				handle, err := os.Open(cfg.ReportsDir)
				if err != nil {
					t.Fatal(err)
				}
				defer handle.Close()
				if err := syscall.Flock(int(handle.Fd()), syscall.LOCK_SH); err != nil {
					t.Fatal(err)
				}
				defer syscall.Flock(int(handle.Fd()), syscall.LOCK_UN)
			}
			runFail(t, "cleanup", "apply", archive, planPath, proofPath)
			for _, c := range plan.Candidates {
				if strings.HasPrefix(c.Path, "../") {
					continue
				}
				if _, err := os.Stat(filepath.Join(cfg.ReportsDir, c.Path)); err != nil {
					t.Fatal("refusal deleted candidate", err)
				}
			}
		})
	}
}

func TestBundleRejectsCorruptionDependenciesAndSchema(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	archive := exportTestBundle(t, config, f.base)
	pristine, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"object", "missing", "schema", "path", "duplicate", "source_dependency", "gzip_checksum"} {
		t.Run(kind, func(t *testing.T) {
			b := evidenceBundle{Index: pristine.Index, Objects: map[string][]byte{}}
			encoded, _ := json.Marshal(pristine.Index)
			_ = json.Unmarshal(encoded, &b.Index)
			for hash, data := range pristine.Objects {
				b.Objects[hash] = append([]byte{}, data...)
			}
			switch kind {
			case "object":
				for hash := range b.Objects {
					b.Objects[hash] = []byte("corrupt")
					break
				}
			case "missing":
				for hash := range b.Objects {
					delete(b.Objects, hash)
					break
				}
			case "schema":
				b.Index.SchemaVersion = 2
				b.Index.ID = bundleID(b.Index)
			case "path":
				b.Index.Files[0].Path = "../escape"
				b.Index.ID = bundleID(b.Index)
			case "duplicate":
				b.Index.Files = append(b.Index.Files, b.Index.Files[0])
				b.Index.ID = bundleID(b.Index)
			case "source_dependency":
				for i, file := range b.Index.Files {
					if strings.Contains(file.Path, "._objects/") {
						b.Index.Files = append(b.Index.Files[:i], b.Index.Files[i+1:]...)
						break
					}
				}
				b.Index.ID = bundleID(b.Index)
			}
			path := filepath.Join(f.base, "bad-"+kind+".tar.gz")
			if kind == "gzip_checksum" {
				raw := append([]byte{}, readFixture(t, archive)...)
				raw[len(raw)-8] ^= 255
				writeFixture(t, path, raw)
			} else {
				if err := writeBundle(path, b); err != nil {
					t.Fatal(err)
				}
			}
			runFail(t, "bundle", "verify", path)
			destination := filepath.Join(f.base, "restore-"+kind)
			runFail(t, "bundle", "restore", path, destination)
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("refused archive partially restored")
			}
		})
	}
}

func TestQuoteOnlyHistoryDoesNotBorrowCurrentSource(t *testing.T) {
	config, base, _ := reviewedFixture(t, false)
	decision := reviewInput(t, config)
	runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
	// Simulate a run created before full-source retention was introduced.
	if err := os.Remove(filepath.Join(base, "runs/review/sources.json")); err != nil {
		t.Fatal(err)
	}
	runOK(t, "report", config, "review")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	for _, source := range report.Sources {
		if source.Full || source.URL != "" || source.Limitation == "" || bytes.Contains(source.Origin, []byte("trucker")) {
			t.Fatal("invented full/Wiki source")
		}
	}
	archive := exportTestBundle(t, config, base)
	b, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Index.Limitations) == 0 {
		t.Fatal("quote-only limitation lost")
	}
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(base, "restored")).(map[string]any)
	cfg, _ := loadConfig(restored["configs"].([]string)[0])
	if !bytes.Equal(readFixture(t, filepath.Join(base, "runs/review/report.html")), readFixture(t, filepath.Join(cfg.ReportsDir, "review/report.html"))) {
		t.Fatal("old HTML regenerated")
	}
}

func TestCleanupDoesNotUseObsoleteReport(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	batch := completeExternal(t, config, "A")
	cfg, _ := loadConfig(config)
	oldHTML := readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.html"))
	oldJSON := readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"))
	task := batch.Tasks[0]
	runOK(t, "retry", config, "A", task.TaskID)
	next := runOK(t, "tasks", config, "A").(TaskBatch).Tasks[0]
	result := sampleResult(t, next, cfg.ProjectRoot)
	scratch := filepath.Join(cfg.ReportsDir, "A/dispatch", next.TaskID, "result.json")
	writeFixture(t, scratch, mustJSON(t, result))
	runOK(t, "submit", config, "A", next.TaskID, scratch)
	// Negative fixture: restore a previously cached representation after the ordinary invalidation.
	writeFixture(t, filepath.Join(cfg.ReportsDir, "A/report.html"), oldHTML)
	writeFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"), oldJSON)
	archive := exportTestBundle(t, config, f.base)
	plan := runOK(t, "cleanup", "plan", archive).(CleanupPlan)
	if len(plan.Candidates) != 0 {
		t.Fatal("outdated decision treated as finished run")
	}
	runFail(t, "publish", filepath.Join(f.base, "stale-site"), config)
}

func TestBundleSharedWorkspaceAndRawDependency(t *testing.T) {
	config, base, _ := reviewedFixture(t, false)
	d := reviewInput(t, config)
	runOK(t, "review", config, "review", writeReviewInput(t, base, d))
	runOK(t, "report", config, "review")
	second := filepath.Join(base, "second/config.yaml")
	cfg, _ := loadConfig(config)
	cfg.ReportsDir = filepath.Join(base, "second-runs")
	raw, _ := yaml.Marshal(cfg)
	writeFixture(t, second, raw)
	runOK(t, "prepare", second, "pending")
	request := filepath.Join(base, "request.json")
	writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{config, second}, []string{}}))
	archive := filepath.Join(base, "shared.tar.gz")
	runOK(t, "bundle", "export", request, archive)
	runOK(t, "bundle", "verify", archive)
	b, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	filtered := []BundleFile{}
	for _, f := range b.Index.Files {
		if strings.Contains(f.Path, "/results/") {
			continue
		}
		filtered = append(filtered, f)
	}
	b.Index.Files = filtered
	used := map[string]bool{}
	for _, f := range filtered {
		used[f.SHA256] = true
	}
	for hash := range b.Objects {
		if !used[hash] {
			delete(b.Objects, hash)
		}
	}
	b.Index.ID = bundleID(b.Index)
	broken := filepath.Join(base, "missing-raw.tar.gz")
	if err := writeBundle(broken, b); err != nil {
		t.Fatal(err)
	}
	runFail(t, "bundle", "verify", broken)
}
