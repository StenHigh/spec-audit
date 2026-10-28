//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func publicationView(t *testing.T, out string, args ...string) Overview {
	t.Helper()
	runOK(t, append([]string{"publish", out}, args...)...)
	var view Overview
	if err := strictJSON(readFixture(t, filepath.Join(out, "overview.json")), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestPublishLatestAwaitingIndependentReview(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	completeExternalHost(t, config, "B")
	runOK(t, "report", config, "B")
	cfg, _ := loadConfig(config, true)
	before := publicationBytes(t, cfg.ReportsDir)
	assertA := func(name string) {
		t.Helper()
		out := filepath.Join(f.base, name)
		view := publicationView(t, out, config)
		if len(view.Scopes) != 1 || view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "A" || view.Scopes[0].Latest.RunID != "B" || view.Scopes[0].Latest.PublicationPending == "" {
			t.Fatal("pending independent review replaced the latest confirmed run")
		}
		if !bytes.Contains(bytes.ToLower(readFixture(t, filepath.Join(out, "index.html"))), []byte("независим")) {
			t.Fatal("pending independent review is not visible")
		}
		rejected := filepath.Join(f.base, name+"-explicit-B")
		runFail(t, "publish", rejected, "--run", "B", config)
		if _, err := os.Stat(rejected); !os.IsNotExist(err) {
			t.Fatal("incomplete historical run exposed output")
		}
	}
	assertA("awaiting-check")
	if !reflect.DeepEqual(before, publicationBytes(t, cfg.ReportsDir)) {
		t.Fatal("selection changed saved audit state")
	}
	confirmExternal(t, config, "B", "changes_required")
	runOK(t, "report", config, "B")
	assertA("awaiting-corrections")
	confirmExternal(t, config, "B", "confirmed")
	// A receipt missing from the cached report remains an error, not a fallback.
	runFail(t, "publish", filepath.Join(f.base, "stale-report"), config)
	runOK(t, "report", config, "B")
	view := publicationView(t, filepath.Join(f.base, "confirmed-B"), config)
	if view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "B" {
		t.Fatal("newly confirmed B was not selected")
	}
	history := publicationView(t, filepath.Join(f.base, "historical-A"), "--run", "A", config)
	if history.Scopes[0].Decided.RunID != "A" {
		t.Fatal("historical selection was replaced by latest")
	}
	journal := filepath.Join(cfg.ReportsDir, "B/review-checks.json")
	writeFixture(t, journal, []byte("{"))
	runFail(t, "publish", filepath.Join(f.base, "corrupt-check"), config)
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	runFail(t, "publish", filepath.Join(f.base, "lost-confirmation"), config)
}

func TestPublishOnlyAwaitingIndependentReview(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternalHost(t, config, "A")
	runOK(t, "report", config, "A")
	view := publicationView(t, filepath.Join(f.base, "only-pending"), config)
	if len(view.Scopes) != 1 || view.Scopes[0].Decided != nil || view.Scopes[0].Latest.PublicationPending == "" || view.Totals.Gap.Total != 0 {
		t.Fatal("an unconfirmed audit invented published verdicts or disappeared")
	}
	runFail(t, "publish", filepath.Join(f.base, "pending-history"), "--run", "A", config)
	cfg, _ := loadConfig(config, true)
	if err := os.Remove(filepath.Join(cfg.ReportsDir, "A/report.json")); err != nil {
		t.Fatal(err)
	}
	// Corruption remains an error even before a printable report exists.
	writeFixture(t, filepath.Join(cfg.ReportsDir, "A/review-checks.json"), []byte("{"))
	runFail(t, "publish", filepath.Join(f.base, "corrupt-pending"), config)
}

func TestPublishDiscoveryPreservesOrdinaryDirectoryNames(t *testing.T) {
	f := newExternalFixture(t)
	configs := []string{f.install(t)}
	completeExternal(t, configs[0], "A")
	original, _ := loadConfig(f.config, true)
	for _, name := range []string{"results", "artifacts", "sources", "dispatch"} {
		cfg := original
		cfg.ReportsDir = filepath.Join(f.base, name)
		ext := *original.External
		ext.Scope = name + "-scope"
		cfg.External = &ext
		raw, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(f.base, name+".yaml")
		writeFixture(t, path, raw)
		config := runOK(t, "external", "import", path, f.input).(map[string]any)["config"].(string)
		completeExternal(t, config, "A")
		configs = append(configs, config)
	}
	// Actual run resources are opaque even when they contain audit-like names.
	cfg, _ := loadConfig(configs[0], true)
	for _, name := range []string{"results", "artifacts", "sources", "dispatch"} {
		path := filepath.Join(cfg.ReportsDir, "A", name, "decoy")
		writeFixture(t, filepath.Join(path, "manifest.json"), []byte("{"))
		writeFixture(t, filepath.Join(path, "state.json"), []byte("{"))
	}
	explicit := publicationView(t, filepath.Join(f.base, "explicit"), configs...)
	view := publicationView(t, filepath.Join(f.base, "discovered"), "--from", f.base)
	if len(view.Scopes) != len(explicit.Scopes) || len(view.Scopes) != 5 {
		t.Fatalf("discovery omitted valid stores: got %d, want 5", len(view.Scopes))
	}
	t.Chdir(f.base)
	if view := publicationView(t, filepath.Join(f.base, "default-cwd")); len(view.Scopes) != 5 {
		t.Fatal("default discovery differs from the explicit audit root")
	}
	// A run directory is not a root authorizing access to its parent and siblings.
	runFail(t, "publish", filepath.Join(f.base, "outside-root"), "--from", filepath.Join(cfg.ReportsDir, "A"))
}

func TestPublishLocalArchiveInsideProject(t *testing.T) {
	config, base := fixture(t)
	completeExternal(t, config, "A")
	cfg, _ := loadConfig(config, true)
	before := publicationBytes(t, cfg.ReportsDir)
	for i, args := range [][]string{{config}, {"--run", "A", config}} {
		view := publicationView(t, filepath.Join(cfg.ProjectRoot, fmt.Sprintf("audit-site-%d", i)), args...)
		if view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "A" {
			t.Fatal("local archive publication lost the selected audit")
		}
	}
	if !reflect.DeepEqual(before, publicationBytes(t, cfg.ReportsDir)) {
		t.Fatal("publication modified saved state")
	}
	// Keep an output inside a selected source directory forbidden.
	cfg.Code.Paths = append(cfg.Code.Paths, "selected")
	writeFixture(t, filepath.Join(cfg.ProjectRoot, "selected/extra.go"), []byte("package fixture\n"))
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(base, "selected.yaml")
	writeFixture(t, selected, raw)
	runFail(t, "publish", filepath.Join(cfg.ProjectRoot, "selected/site"), selected)
	runFail(t, "publish", filepath.Join(cfg.ReportsDir, "site"), config)
}

// The independent receipt binds the exact raw decision, not merely its parsed content.
func TestPublishExactHostRawReceiptBinding(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(cfg.ReportsDir, "A/host-reviews.json")
	original := readFixture(t, journalPath)
	var journal reviewJournal
	if err := strictJSON(original, &journal); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(f.base, "existing-site")
	publicationView(t, existing, config)
	savedSite := publicationBytes(t, existing)

	// Reformatting the outer journal preserves every raw record and its receipt.
	outer, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, journalPath, append(outer, '\n'))
	for i, args := range [][]string{{config}, {"--run", "A", config}} {
		publicationView(t, filepath.Join(f.base, fmt.Sprintf("outer-format-%d", i)), args...)
	}

	journal.Records[len(journal.Records)-1] = " \n" + journal.Records[len(journal.Records)-1]
	writeFixture(t, journalPath, mustJSON(t, journal))
	before := publicationBytes(t, cfg.ReportsDir)
	check := runOK(t, "review-check", config, "A").(map[string]any)
	if check["check"].(*ReviewCheck) != nil {
		t.Fatal("changed raw still has a matching independent receipt")
	}
	for i, args := range [][]string{{config}, {"--run", "A", config}} {
		out := filepath.Join(f.base, fmt.Sprintf("stale-receipt-%d", i))
		if _, err := execute(append([]string{"publish", out}, args...)); err == nil {
			t.Errorf("publication accepted stale independent receipt: case %d", i)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("failed publication exposed OUT: case %d", i)
		}
	}
	if !reflect.DeepEqual(before, publicationBytes(t, cfg.ReportsDir)) {
		t.Fatal("failed publication changed saved audit")
	}
	if !reflect.DeepEqual(savedSite, publicationBytes(t, existing)) {
		t.Fatal("failed publication changed existing site")
	}
	// Regenerating the report cannot carry the old receipt onto the new raw hash.
	reportPath := filepath.Join(cfg.ReportsDir, "A/report.json")
	originalReport := readFixture(t, reportPath)
	runOK(t, "report", config, "A")
	pending := publicationView(t, filepath.Join(f.base, "regenerated-unconfirmed"), config)
	if pending.Scopes[0].Decided != nil || pending.Scopes[0].Latest.PublicationPending == "" {
		t.Fatal("regenerated report reused stale confirmation")
	}
	runFail(t, "publish", filepath.Join(f.base, "regenerated-history"), "--run", "A", config)
	writeFixture(t, reportPath, originalReport)
	writeFixture(t, journalPath, original)
	for i, args := range [][]string{{config}, {"--run", "A", config}} {
		view := publicationView(t, filepath.Join(f.base, fmt.Sprintf("restored-raw-%d", i)), args...)
		if view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "A" {
			t.Fatal("restored exact decision was not selected")
		}
	}
}

func TestPublishDiscoveryPreservesSourceOutputBoundary(t *testing.T) {
	config, base := fixture(t)
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Specs.Paths = append(cfg.Specs.Paths, "selected-spec")
	cfg.Code.Paths = append(cfg.Code.Paths, "selected-code")
	cfg.Tests.Paths = append(cfg.Tests.Paths, "selected-tests")
	cfg.References = &Sources{Paths: []string{"selected-reference"}}
	for path, data := range map[string]string{
		"selected-spec/context.md":      "Supplemental context\n",
		"selected-code/helper.go":       "package fixture\n",
		"selected-tests/helper_test.go": "package fixture\n",
		"selected-reference/context.md": "Reference context\n",
	} {
		writeFixture(t, filepath.Join(cfg.ProjectRoot, path), []byte(data))
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, raw)
	completeExternal(t, config, "A")
	before, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	saved := publicationBytes(t, cfg.ReportsDir)
	existing := filepath.Join(base, "existing-site")
	publicationView(t, existing, config)
	savedSite := publicationBytes(t, existing)
	t.Chdir(cfg.ReportsDir)
	for _, path := range []string{"selected-spec", "selected-code", "selected-tests", "selected-reference"} {
		for i, args := range [][]string{{"--from", cfg.ReportsDir}, {}} {
			out := filepath.Join(cfg.ProjectRoot, path, fmt.Sprintf("generated-%d", i))
			// The explicit CONFIG and config-less discovery must protect the same inputs.
			if _, err := execute([]string{"publish", out, config}); err == nil {
				t.Fatal("explicit CONFIG allowed source output")
			}
			if _, err := execute(append([]string{"publish", out}, args...)); err == nil {
				t.Errorf("discovery allowed OUT in %s (case %d)", path, i)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("source OUT was exposed in %s (case %d)", path, i)
			}
		}
	}
	after, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if before.SnapshotID != after.SnapshotID {
		t.Errorf("publication changed selected sources: %d -> %d files", len(before.Files), len(after.Files))
	}
	if !reflect.DeepEqual(saved, publicationBytes(t, cfg.ReportsDir)) || !reflect.DeepEqual(savedSite, publicationBytes(t, existing)) {
		t.Fatal("refused publication changed saved audit/site")
	}
	// The local archive exception still permits OUT outside declared source paths.
	publicationView(t, filepath.Join(cfg.ProjectRoot, "allowed-site"), "--from", cfg.ReportsDir)
}

func TestPublishHistoricalSelectionIgnoresOtherRunMetadata(t *testing.T) {
	for _, policy := range []string{"archive", "citations"} {
		t.Run(policy, func(t *testing.T) {
			f := newExternalFixture(t)
			if policy == "citations" {
				cfg, err := loadConfig(f.config, true)
				if err != nil {
					t.Fatal(err)
				}
				cfg.SourceRetention = "citations"
				raw, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, f.config, raw)
			}
			config := f.install(t)
			completeExternal(t, config, "A")
			completeExternal(t, config, "B")
			cfg, err := loadConfig(config, true)
			if err != nil {
				t.Fatal(err)
			}
			existing := filepath.Join(f.base, "existing-site")
			publicationView(t, existing, "--run", "A", config)
			savedSite := publicationBytes(t, existing)
			replace := func(path string, data []byte) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, path, data)
				if err := os.Chmod(path, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"manifest.json", toolVersionsFile} {
				t.Run(name, func(t *testing.T) {
					other := filepath.Join(cfg.ReportsDir, "B", name)
					original := readFixture(t, other)
					replace(other, []byte("{"))
					defer replace(other, original)
					before := publicationBytes(t, cfg.ReportsDir)
					defaultOut := filepath.Join(f.base, "default-"+name)
					runFail(t, "publish", defaultOut, config)
					if _, err := os.Stat(defaultOut); !os.IsNotExist(err) {
						t.Fatal("default corruption exposed OUT")
					}
					out := filepath.Join(f.base, "selected-A-"+name)
					view := publicationView(t, out, "--run", "A", config)
					if len(view.Scopes) != 1 || view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "A" {
						t.Fatal("unrelated metadata changed selected A")
					}
					if !reflect.DeepEqual(before, publicationBytes(t, cfg.ReportsDir)) {
						t.Fatal("historical selection changed saved audit")
					}
					// The same corruption inside the selected run must still refuse publication.
					selected := filepath.Join(cfg.ReportsDir, "A", name)
					selectedRaw := readFixture(t, selected)
					replace(selected, []byte("{"))
					defer replace(selected, selectedRaw)
					badOut := filepath.Join(f.base, "bad-selected-"+name)
					runFail(t, "publish", badOut, "--run", "A", config)
					if _, err := os.Stat(badOut); !os.IsNotExist(err) {
						t.Fatal("corrupt selected run exposed OUT")
					}
				})
			}
			runFail(t, "publish", filepath.Join(f.base, "missing-run"), "--run", "missing", config)
			if !reflect.DeepEqual(savedSite, publicationBytes(t, existing)) {
				t.Fatal("historical selection changed previous site")
			}
		})
	}
}

func TestPublishDiscoveryProtectsOlderSourceRoots(t *testing.T) {
	config, base := fixture(t)
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Code.Paths = append(cfg.Code.Paths, "selected-old")
	writeFixture(t, filepath.Join(cfg.ProjectRoot, "selected-old/helper.go"), []byte("package fixture\n"))
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, raw)
	completeExternal(t, config, "A")
	oldRoot := cfg.ProjectRoot
	oldSources := publicationBytes(t, oldRoot)
	newRoot := filepath.Join(base, "new-source")
	copyTestTree(t, oldRoot, newRoot)
	cfg.ProjectRoot = newRoot
	cfg.Code.Paths = []string{"source.go", "go.mod", "selected-new"}
	writeFixture(t, filepath.Join(newRoot, "selected-new/helper.go"), []byte("package fixture\n"))
	raw, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, raw)
	completeExternal(t, config, "B")
	for i, args := range [][]string{{config}, {"--from", base}, {"--from", cfg.ReportsDir}} {
		out := filepath.Join(oldRoot, "selected-old", fmt.Sprintf("generated-%d", i))
		runFail(t, append([]string{"publish", out}, args...)...)
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("older source root was exposed as OUT")
		}
	}
	if !reflect.DeepEqual(oldSources, publicationBytes(t, oldRoot)) {
		t.Fatal("old source root changed")
	}
	if err := os.RemoveAll(oldRoot); err != nil {
		t.Fatal(err)
	}
	// Missing old sources remain write boundaries, without becoming read dependencies.
	runFail(t, "publish", filepath.Join(oldRoot, "selected-old/generated"), "--from", cfg.ReportsDir)
	if _, err := os.Stat(oldRoot); !os.IsNotExist(err) {
		t.Fatal("refused output recreated the missing checkout")
	}
	view := publicationView(t, filepath.Join(base, "offline-publication"), "--from", cfg.ReportsDir)
	if view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "B" {
		t.Fatal("old checkout became a read dependency")
	}
}

func TestPublishDiscoveryRejectsConflictingConfigBindings(t *testing.T) {
	for _, oldFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("old-first-%t", oldFirst), func(t *testing.T) {
			config, base := fixture(t)
			original, err := loadConfig(config, true)
			if err != nil {
				t.Fatal(err)
			}
			completeExternal(t, config, "A")
			current := original
			current.Code.Paths = append(append([]string{}, original.Code.Paths...), "new-input")
			writeFixture(t, filepath.Join(current.ProjectRoot, "new-input/helper.go"), []byte("package fixture\n"))
			alias := filepath.Join(base, ".spec-audit.yaml")
			currentPath, oldPath := config, alias
			if !oldFirst {
				currentPath, oldPath = alias, config
			}
			for path, cfg := range map[string]Config{oldPath: original, currentPath: current} {
				raw, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, path, raw)
			}
			existing := filepath.Join(base, "existing-site")
			publicationView(t, existing, currentPath)
			savedSite := publicationBytes(t, existing)
			savedAudit := publicationBytes(t, current.ReportsDir)
			before, err := snapshot(current)
			if err != nil {
				t.Fatal(err)
			}
			for _, out := range []string{filepath.Join(current.ProjectRoot, "new-input/generated"), filepath.Join(base, "ambiguous-site")} {
				if _, err := execute([]string{"publish", out, "--from", base}); err == nil {
					t.Errorf("conflicting CONFIG bindings were silently selected for %s", out)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Error("ambiguous discovery exposed OUT")
				}
			}
			after, err := snapshot(current)
			if err != nil {
				t.Fatal(err)
			}
			if before.SnapshotID != after.SnapshotID {
				t.Errorf("ambiguous discovery changed source snapshot: %d -> %d files", len(before.Files), len(after.Files))
			}
			if !reflect.DeepEqual(savedSite, publicationBytes(t, existing)) || !reflect.DeepEqual(savedAudit, publicationBytes(t, current.ReportsDir)) {
				t.Fatal("ambiguous discovery changed previous site/audit")
			}
		})
	}
}

func TestPublishDiscoveryCoalescesEquivalentConfigBindings(t *testing.T) {
	config, base := fixture(t)
	completeExternal(t, config, "A")
	normalized, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	normalized.SourceRetention = "archive"
	normalized.Code.Include = []string{}
	normalized.Related = []string{}
	raw, err := yaml.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(base, ".spec-audit.yaml"), raw)
	view := publicationView(t, filepath.Join(base, "equivalent-site"), "--from", base)
	if len(view.Scopes) != 1 || view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "A" {
		t.Fatal("equivalent aliases duplicated or lost their store")
	}
}
