//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublishSavedJSONWithoutHTML(t *testing.T) {
	f, config, cfg := citationPublicationFixture(t)
	before := readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"))
	if err := os.Remove(filepath.Join(cfg.ReportsDir, "A/report.html")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.base, "regenerated")
	runOK(t, "publish", out, config)
	var v Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Scopes) != 1 || v.Scopes[0].Decided == nil || v.Scopes[0].Decided.Report == "" {
		t.Fatal("saved JSON was not published")
	}
	html := readFixture(t, filepath.Join(out, filepath.FromSlash(v.Scopes[0].Decided.Report)))
	if !bytes.Contains(html, []byte("REQ-DEMO-003")) || !bytes.Contains(html, []byte("contradicted")) {
		t.Fatal("verdict/evidence lost")
	}
	if !bytes.Equal(before, readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"))) {
		t.Fatal("saved report changed")
	}
	if _, err := os.Stat(filepath.Join(cfg.ReportsDir, "._publications")); !os.IsNotExist(err) {
		t.Fatal("publication archived", err)
	}
}

func TestPublishHistoricalRunSelection(t *testing.T) {
	f, config, cfg := citationPublicationFixture(t)
	before := publicationBytes(t, cfg.ReportsDir)
	out := filepath.Join(f.base, "historical")
	runOK(t, "publish", out, "--run", "A", config)
	var v Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Scopes) != 1 || v.Scopes[0].Decided == nil || v.Scopes[0].Decided.RunID != "A" {
		t.Fatal("wrong historical run")
	}
	if !reflect.DeepEqual(before, publicationBytes(t, cfg.ReportsDir)) {
		t.Fatal("historical publish changed state")
	}
	runFail(t, "publish", filepath.Join(f.base, "missing"), "--run", "absent", config)
}

func TestPublishAutomaticDiscovery(t *testing.T) {
	f, _, cfg := citationPublicationFixture(t)
	out := filepath.Join(f.base, "automatic")
	runOK(t, "publish", out, "--from", cfg.ReportsDir)
	t.Chdir(cfg.ReportsDir)
	runOK(t, "publish", filepath.Join(f.base, "default-cwd"))
	var v Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Scopes) != 1 || v.Scopes[0].Decided == nil || v.Scopes[0].Decided.RunID != "A" {
		t.Fatal("discovery lost scope")
	}
	if !strings.Contains(string(readFixture(t, filepath.Join(out, "index.html"))), "fixture-A") {
		t.Fatal("audited revision not shown")
	}
}

func TestCompactSiteResult(t *testing.T) {
	f, config, _ := citationPublicationFixture(t)
	out := filepath.Join(f.base, "compact")
	runOK(t, "publish", out, config)
	var v Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &v); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(filepath.Join(out, filepath.FromSlash(v.Scopes[0].Decided.Report)))
	raw := readFixture(t, filepath.Join(dir, "result.json"))
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if string(obj["schema_version"]) != "4" || obj["report"] != nil || obj["decision"] != nil {
		t.Fatal("not compact v4")
	}
	var ref struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Bytes  int    `json:"bytes"`
	}
	if err := json.Unmarshal(obj["report_ref"], &ref); err != nil {
		t.Fatal(err)
	}
	report := readFixture(t, filepath.Join(dir, ref.Path))
	if ref.SHA256 != digest(report) || ref.Bytes != len(report) {
		t.Fatal("wrong report reference")
	}
	t.Logf("report=%d compact_result=%d", len(report), len(raw))
}

func exportAuditTestBundle(t *testing.T, configs []string, base string) string {
	t.Helper()
	request := filepath.Join(base, "audit-request.json")
	writeFixture(t, request, mustJSON(t, map[string]any{"schema_version": 2, "configs": configs}))
	out := filepath.Join(base, "audit-only.tar.gz")
	runOK(t, "bundle", "export", request, out)
	return out
}

func TestAuditOnlyBundleRegeneratesAfterRelease(t *testing.T) {
	f, config, cfg := citationPublicationFixture(t)
	out := filepath.Join(f.base, "temporary-site")
	runOK(t, "publish", out, config)
	if err := recordPublication(out, []string{config}); err != nil {
		t.Fatal(err)
	} // legacy state
	bpath := exportAuditTestBundle(t, []string{config}, f.base)
	b, err := readBundle(bpath)
	if err != nil {
		t.Fatal(err)
	}
	if b.Index.SchemaVersion != 3 {
		t.Fatal("not audit-only schema 3")
	}
	for _, a := range b.Index.Areas {
		if a.Kind == "site" {
			t.Fatal("site archived")
		}
	}
	for _, f := range b.Index.Files {
		if strings.Contains(f.Path, "._publications/") || strings.HasSuffix(f.Path, "/report.html") {
			t.Fatal("generated representation retained", f.Path)
		}
	}
	t.Logf("audit bundle logical_files=%d unique_objects=%d compressed=%d", len(b.Index.Files), len(b.Objects), len(readFixture(t, bpath)))
	if err := os.RemoveAll(out); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfg.ReportsDir, "A/report.html")); err != nil {
		t.Fatal(err)
	}
	restored := runOK(t, "bundle", "restore", bpath, filepath.Join(f.base, "restored-audit")).(map[string]any)
	if len(restored["sites"].([]string)) != 0 {
		t.Fatal("site restored from audit-only bundle")
	}
	rc := restored["configs"].([]string)[0]
	output := filepath.Join(f.base, "regenerated-restored")
	runOK(t, "publish", output, rc)
	runOK(t, "publish", filepath.Join(f.base, "regenerated-history"), "--run", "A", rc)
	assertNoLifecycleMarker(t, output)
}

func TestPublishSelectionAndRevisionPerScope(t *testing.T) {
	f := newExternalFixture(t)
	configA := f.install(t)
	completeExternal(t, configA, "A")
	cfgA, _ := loadConfig(configA, true)
	original, _ := loadConfig(f.config, true)
	original.ReportsDir = filepath.Join(f.base, "second-runs")
	ext := *original.External
	ext.Scope = "second-scope"
	original.External = &ext
	raw, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.base, "other.yaml")
	writeFixture(t, other, raw)
	second := runOK(t, "external", "import", other, f.input).(map[string]any)["config"].(string)
	completeExternal(t, second, "A")
	f.manifest.SpecVersion = "fixture-R2"
	f.save(t)
	thirdConfig := original
	thirdConfig.ReportsDir = filepath.Join(f.base, "third-runs")
	thirdExt := *original.External
	thirdExt.Scope = "third-scope"
	thirdConfig.External = &thirdExt
	thirdRaw, _ := yaml.Marshal(thirdConfig)
	thirdPath := filepath.Join(f.base, "third.yaml")
	writeFixture(t, thirdPath, thirdRaw)
	third := runOK(t, "external", "import", thirdPath, f.input).(map[string]any)["config"].(string)
	completeExternal(t, third, "C")
	f.manifest.SpecVersion = "fixture-R3"
	f.save(t)
	configB := f.install(t)
	completeExternal(t, configB, "B")
	runOK(t, "prepare", configB, "pending")
	oldB := publicationBytes(t, original.ReportsDir)
	out := filepath.Join(f.base, "all-scopes")
	runOK(t, "publish", out, "--from", f.base)
	var v Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Scopes) != 3 {
		t.Fatal("wrong discovery count", len(v.Scopes))
	}
	versions := map[string]string{}
	for _, s := range v.Scopes {
		if s.Decided == nil {
			t.Fatal("missing decision")
		}
		versions[s.Name] = s.Decided.External.SpecVersion
	}
	if versions["fixture-scope"] != "fixture-R3" || versions["second-scope"] != "fixture-A" || versions["third-scope"] != "fixture-R2" {
		t.Fatal("wrong per-scope version", versions)
	}
	if !reflect.DeepEqual(oldB, publicationBytes(t, original.ReportsDir)) {
		t.Fatal("unmodified scope B state changed")
	}
	html := string(readFixture(t, filepath.Join(out, "index.html")))
	if !strings.Contains(html, "проверено на fixture-R3") || !strings.Contains(html, "проверено на fixture-A") {
		t.Fatal("revision column missing")
	}
	h := filepath.Join(f.base, "old-A")
	runOK(t, "publish", h, "--run", "A", configB)
	var historical Overview
	if err := strictJSON(readFixture(t, filepath.Join(h, "overview.json")), &historical); err != nil {
		t.Fatal(err)
	}
	if historical.Scopes[0].Decided.External.SpecVersion != "fixture-A" {
		t.Fatal("historical version replaced")
	}
	// Source edit alone does not create tasks or invalidate publication eligibility.
	writeFixture(t, filepath.Join(f.base, "source", "source.go"), append(readFixture(t, filepath.Join(f.base, "source", "source.go")), []byte("\n// changed after audit\n")...))
	runOK(t, "publish", filepath.Join(f.base, "after-code-change"), configB)
	if output := os.Getenv("SPEC_AUDIT_PUBLICATION_UI"); output != "" {
		copyTestTree(t, out, output)
		copyTestTree(t, h, filepath.Join(output, "historical"))
	}
	_ = cfgA
}

func TestSiteResultReferenceRejectsCorruption(t *testing.T) {
	f, config, _ := citationPublicationFixture(t)
	out := filepath.Join(f.base, "refs")
	runOK(t, "publish", out, config)
	var v Overview
	_ = strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &v)
	dir := filepath.Dir(filepath.Join(out, filepath.FromSlash(v.Scopes[0].Decided.Report)))
	report := readFixture(t, filepath.Join(dir, "report.json"))
	var result SiteResult
	if err := strictJSON(readFixture(t, filepath.Join(dir, "result.json")), &result); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"hash", "bytes", "path", "snapshot", "scope", "pointer"} {
		t.Run(kind, func(t *testing.T) {
			r := result
			switch kind {
			case "hash":
				r.ReportRef.SHA256 = strings.Repeat("0", 64)
			case "bytes":
				r.ReportRef.Bytes++
			case "path":
				r.ReportRef.Path = "../report.json"
			case "snapshot":
				r.SnapshotID = strings.Repeat("0", 64)
			case "scope":
				r.Scope = "foreign"
			case "pointer":
				r.DecisionRef = &DecisionReference{"report.json", "/absent"}
			}
			if validateSiteResult(r, report) == nil {
				t.Fatal("bad reference accepted")
			}
		})
	}
}

func TestPublicationCleanupAuditOnlyBackupAndResume(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, _ := loadConfig(config, true)
	site := filepath.Join(f.base, "legacy-site")
	runOK(t, "publish", site, config)
	if err := recordPublication(site, []string{config}); err != nil {
		t.Fatal(err)
	}
	ids, err := filepath.Glob(filepath.Join(cfg.ReportsDir, "._publications", "*.json"))
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	id := strings.TrimSuffix(filepath.Base(ids[0]), ".json")
	backup := exportAuditTestBundle(t, []string{config}, f.base)
	b, err := readBundle(backup)
	if err != nil {
		t.Fatal(err)
	}
	plan := runOK(t, "cleanup", "plan", backup, "--publications", id).(CleanupPlan)
	if plan.SchemaVersion != 2 || plan.FreedBytes <= 0 || len(plan.Candidates) < 2 {
		t.Fatal("no publication candidates")
	}
	pp, proofPath := filepath.Join(f.base, "publication-plan.json"), filepath.Join(f.base, "publication-proof.json")
	writeFixture(t, pp, mustJSON(t, plan))
	proof := runOK(t, "cleanup", "verify", backup, pp).(CleanupProof)
	writeFixture(t, proofPath, mustJSON(t, proof))
	// No full archived source object can be removed as a duplicate site asset.
	var inv sourceInventory
	if err := strictJSON(readFixture(t, filepath.Join(cfg.ReportsDir, "A/sources.json")), &inv); err != nil {
		t.Fatal(err)
	}
	for _, s := range inv.Sources {
		if !s.Full {
			continue
		}
		for _, c := range plan.Candidates {
			if c.Path == "._objects/"+s.SHA256 {
				t.Fatal("source object selected")
			}
		}
	}
	// Simulate interruption immediately after the metadata deletion.
	first := plan.Candidates[0]
	if first.Class != "publication_manifest" {
		t.Fatal("unsafe deletion order")
	}
	if err := os.Remove(filepath.Join(cfg.ReportsDir, first.Path)); err != nil {
		t.Fatal(err)
	}
	runOK(t, "cleanup", "apply", backup, pp, proofPath)
	runOK(t, "cleanup", "apply", backup, pp, proofPath)
	runOK(t, "publish", filepath.Join(f.base, "after-cleanup"), config)
	for _, file := range b.Index.Files {
		if file.Area == "configs" {
			continue
		}
		for _, a := range b.Index.Areas {
			if a.ID == file.Area && a.Kind == "reports" {
				data := readFixture(t, filepath.Join(a.OriginalRoot, file.Path))
				if digest(data) != file.SHA256 {
					t.Fatal("audit state damaged")
				}
			}
		}
	}
	t.Logf("publication cleanup candidates=%d freed=%d backup_compressed=%d", len(plan.Candidates), plan.FreedBytes, len(readFixture(t, backup)))
}

func TestPublishPendingScopeAndInvalidSelectors(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	runOK(t, "prepare", config, "pending")
	cfg, _ := loadConfig(config, true)
	out := filepath.Join(f.base, "pending-site")
	runOK(t, "publish", out, "--from", cfg.ReportsDir)
	var view Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Scopes) != 1 || view.Scopes[0].Name != "fixture-scope" || view.Scopes[0].Decided != nil || view.Scopes[0].Requirements == 0 {
		t.Fatal("pending scope lost or invented a verdict")
	}
	if !strings.Contains(string(readFixture(t, filepath.Join(out, "index.html"))), "Аудит не завершён") {
		t.Fatal("pending audit not labeled")
	}
	for i, args := range [][]string{{"--run", "pending", config}, {"--run", "pending", "--from", cfg.ReportsDir}, {"--run", "pending", config, config}, {"--from", cfg.ReportsDir, config}, {"--unknown"}} {
		output := filepath.Join(f.base, fmt.Sprintf("invalid-%d", i))
		runFail(t, append([]string{"publish", output}, args...)...)
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("invalid selector exposed output")
		}
	}
	link := filepath.Join(f.base, "linked-reports")
	if err := os.Symlink(cfg.ReportsDir, link); err != nil {
		t.Fatal(err)
	}
	runFail(t, "publish", filepath.Join(f.base, "unsafe"), "--from", link)
}

func TestPublishRejectsChangedRawWithoutTouchingEarlierOutput(t *testing.T) {
	f, config, cfg := citationPublicationFixture(t)
	out := filepath.Join(f.base, "earlier")
	runOK(t, "publish", out, config)
	before := publicationBytes(t, out)
	files, err := filepath.Glob(filepath.Join(cfg.ReportsDir, "A/results/*.json"))
	if err != nil || len(files) == 0 {
		t.Fatal(err)
	}
	if err := os.Chmod(files[0], 0600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, files[0], []byte("{}"))
	failed := filepath.Join(f.base, "corrupt")
	runFail(t, "publish", failed, config)
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("corrupt evidence exposed output")
	}
	if !reflect.DeepEqual(before, publicationBytes(t, out)) {
		t.Fatal("previous publication changed")
	}
}

func TestPublicationCleanupSharedObjectsAndMultipleCopies(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, _ := loadConfig(config, true)
	original, _ := loadConfig(f.config, true)
	original.ReportsDir = filepath.Join(f.base, "second-runs")
	ext := *original.External
	ext.Scope = "second-scope"
	original.External = &ext
	secondPath := filepath.Join(f.base, "second.yaml")
	raw, _ := yaml.Marshal(original)
	writeFixture(t, secondPath, raw)
	second := runOK(t, "external", "import", secondPath, f.input).(map[string]any)["config"].(string)
	completeExternal(t, second, "A")
	site := filepath.Join(f.base, "site")
	runOK(t, "publish", site, config, second)
	if err := recordPublication(site, []string{config, second}); err != nil {
		t.Fatal(err)
	}
	first, err := filepath.Glob(filepath.Join(cfg.ReportsDir, "._publications/*.json"))
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	var old PublicationSnapshot
	if err := strictJSON(readFixture(t, first[0]), &old); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(site, "index.html"), []byte("second generated site"))
	if err := recordPublication(site, []string{config, second}); err != nil {
		t.Fatal(err)
	}
	// Cleanup proof must also work while a newer run awaits independent review.
	completeExternalHost(t, config, "B")
	runOK(t, "report", config, "B")
	backup := exportAuditTestBundle(t, []string{config, second}, f.base)
	plan := runOK(t, "cleanup", "plan", backup, "--publications", old.ID).(CleanupPlan)
	// All non-index objects remain referenced by the second publication.
	for _, c := range plan.Candidates {
		if c.Class == "publication_object" {
			for _, f := range old.Files {
				if f.Path != "index.html" && c.Path == "._objects/"+f.SHA256 {
					t.Fatal("shared publication object selected")
				}
			}
		}
	}
	pp := filepath.Join(f.base, "shared-plan.json")
	writeFixture(t, pp, mustJSON(t, plan))
	proof := runOK(t, "cleanup", "verify", backup, pp).(CleanupProof)
	proofPath := filepath.Join(f.base, "shared-proof.json")
	writeFixture(t, proofPath, mustJSON(t, proof))
	writeFixture(t, filepath.Join(cfg.ReportsDir, "new-dependency.dat"), []byte("new retained state"))
	runFail(t, "cleanup", "apply", backup, pp, proofPath)
	if err := os.Remove(filepath.Join(cfg.ReportsDir, "new-dependency.dat")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{cfg.ReportsDir, original.ReportsDir} {
		func() {
			unlock, err := lockWorkspaces([]string{dir}, false)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			for _, args := range [][]string{
				{"publish", filepath.Join(f.base, "busy-site"), config, second},
				{"cleanup", "apply", backup, pp, proofPath},
			} {
				if _, err := execute(args); err == nil || !strings.Contains(err.Error(), "занята") {
					t.Fatal("active writer did not protect a selected workspace", args, err)
				}
			}
		}()
	}
	archive, err := readBundle(backup)
	if err != nil {
		t.Fatal(err)
	}
	areas := map[string]BundleArea{}
	for _, area := range archive.Index.Areas {
		areas[area.ID] = area
	}
	type originalCandidate struct {
		data []byte
		mode os.FileMode
	}
	originals := map[string]originalCandidate{}
	manifests, objects := 0, 0
	for _, candidate := range plan.Candidates {
		path := filepath.Join(areas[candidate.Area].OriginalRoot, candidate.Path)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = originalCandidate{readFixture(t, path), info.Mode().Perm()}
		if candidate.Class == "publication_manifest" {
			manifests++
		} else if candidate.Class == "publication_object" {
			objects++
		}
	}
	if manifests != 2 || objects < 2 {
		t.Fatal("fault matrix needs manifests and objects in both stores", manifests, objects)
	}
	// Every prefix includes the boundary after the last manifest and the
	// boundaries within object deletion. Replay uses the exact same plan/proof.
	for prefix := 0; prefix <= len(plan.Candidates); prefix++ {
		t.Run(fmt.Sprintf("after-%d-removals", prefix), func(t *testing.T) {
			for path, original := range originals {
				if _, err := os.Stat(path); os.IsNotExist(err) {
					writeFixture(t, path, original.data)
					if err := os.Chmod(path, original.mode); err != nil {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			for _, candidate := range plan.Candidates[:prefix] {
				if err := os.Remove(filepath.Join(areas[candidate.Area].OriginalRoot, candidate.Path)); err != nil {
					t.Fatal(err)
				}
			}
			runOK(t, "cleanup", "apply", backup, pp, proofPath)
			replay := runOK(t, "cleanup", "apply", backup, pp, proofPath).(map[string]any)
			if len(replay["removed"].([]string)) != 0 || replay["complete"] != true {
				t.Fatal("cleanup replay was not idempotent")
			}
			for _, area := range archive.Index.Areas {
				if area.Kind != "reports" {
					continue
				}
				remaining, err := filepath.Glob(filepath.Join(area.OriginalRoot, "._publications/*.json"))
				if err != nil || len(remaining) != 1 {
					t.Fatal("unselected publication lost", area.ID, err)
				}
				if _, err := auditBundleOmissions(area.OriginalRoot); err != nil {
					t.Fatal("remaining publication broken", area.ID, err)
				}
			}
			for _, file := range archive.Index.Files {
				if areas[file.Area].Kind != "reports" {
					continue
				}
				path := filepath.Join(areas[file.Area].OriginalRoot, file.Path)
				info, err := os.Stat(path)
				if err != nil || uint32(info.Mode().Perm()) != file.Mode || digest(readFixture(t, path)) != file.SHA256 {
					t.Fatal("retained audit bytes/mode changed", file.Path, err)
				}
			}
			view := publicationView(t, filepath.Join(f.base, fmt.Sprintf("after-cleanup-%d", prefix)), config, second)
			if len(view.Scopes) != 2 || view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "A" || view.Scopes[0].Latest.RunID != "B" {
				t.Fatal("cleanup changed selection of the confirmed audit")
			}
			publicationView(t, filepath.Join(f.base, fmt.Sprintf("history-after-cleanup-%d", prefix)), "--run", "A", config)
		})
	}
}

func TestPublishMixedCompletedRetentions(t *testing.T) {
	c, citations, _ := citationPublicationFixture(t)
	a := newExternalFixture(t)
	original, err := loadConfig(a.config, true)
	if err != nil {
		t.Fatal(err)
	}
	external := *original.External
	external.Scope = "archive-scope"
	original.External = &external
	raw, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, a.config, raw)
	archive := a.install(t)
	completeExternal(t, archive, "archive")
	out := filepath.Join(c.base, "mixed-completed")
	runOK(t, "publish", out, citations, archive)
	var view Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Scopes) != 2 {
		t.Fatal("lost mixed scopes")
	}
	for _, scope := range view.Scopes {
		if scope.Decided == nil {
			t.Fatal("completed scope lost")
		}
		file := filepath.Join(out, filepath.FromSlash(scope.Decided.Report))
		var report Report
		if err := strictJSON(readFixture(t, filepath.Join(filepath.Dir(file), "report.json")), &report); err != nil {
			t.Fatal(err)
		}
		for _, source := range report.Sources {
			if report.SourceRetention == "citations" {
				if source.Full || source.URL != "" || source.RawURL != "" {
					t.Fatal("archive resource entered citations scope")
				}
			} else if source.Full {
				if digest(readFixture(t, filepath.Join(filepath.Dir(file), source.RawURL))) != source.SHA256 {
					t.Fatal("archive resource lost")
				}
			}
		}
	}
}
