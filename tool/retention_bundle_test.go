//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const retentionBundleMarker = "UNQUOTED_SOURCE_MARKER_BUNDLE"

func citationBundleFixture(t *testing.T) (string, string) {
	t.Helper()
	config, base := fixture(t)
	writeFixture(t, config, append(readFixture(t, config), []byte("source_retention: citations\n")...))
	code := filepath.Join(base, "source/go.mod")
	writeFixture(t, code, append(readFixture(t, code), []byte("\n// "+retentionBundleMarker+"\n")...))
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	decision := reviewInput(t, config)
	runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
	runOK(t, "report", config, "review")
	return config, base
}

func TestCitationBundleLocalOfflineRestoreAndCleanup(t *testing.T) {
	config, base := citationBundleFixture(t)
	archive := exportTestBundle(t, config, base)
	b, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	if b.Index.SchemaVersion != 2 {
		t.Fatal("citations package uses the legacy schema")
	}
	for _, area := range b.Index.Areas {
		if area.Kind == "workspace" {
			t.Fatal("citations package retained a workspace")
		}
	}
	for _, data := range b.Objects {
		if bytes.Contains(data, []byte(retentionBundleMarker)) {
			t.Fatal("unquoted source leaked into an archive object")
		}
	}
	// The proof must preserve results even while the original checkout exists.
	plan := runOK(t, "cleanup", "plan", archive).(CleanupPlan)
	planPath := filepath.Join(base, "cleanup-plan.json")
	writeFixture(t, planPath, mustJSON(t, plan))
	runOK(t, "cleanup", "verify", archive, planPath)
	if err := os.RemoveAll(filepath.Join(base, "source")); err != nil {
		t.Fatal(err)
	}
	runOK(t, "bundle", "verify", archive)
	destination := filepath.Join(base, "restored")
	restored := runOK(t, "bundle", "restore", archive, destination).(map[string]any)
	restoredConfig := restored["configs"].([]string)[0]
	cfg, err := loadConfig(restoredConfig, true)
	if err != nil {
		t.Fatal(err)
	}
	physicalDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !within(filepath.Join(physicalDestination, "unavailable"), cfg.ProjectRoot) {
		t.Fatal("restored config retained the old checkout", cfg.ProjectRoot)
	}
	status := runOK(t, "status", restoredConfig, "review").(Status)
	if !status.DeliveryComplete || status.Freshness != "unchecked" {
		t.Fatal("offline results lost or marked current", status)
	}
	view := runOK(t, "review", restoredConfig, "review").(ReviewContext)
	if !view.Complete || len(view.History) != 1 || len(view.Entries[0].Result.Assessments[0].Spec) == 0 {
		t.Fatal("historical decision or citations lost")
	}
	runOK(t, "report", restoredConfig, "review")
	runFail(t, "prepare", restoredConfig, "new-analysis")
}

func TestCitationBundleExternalRequiresReleaseAndRestoresMetadata(t *testing.T) {
	f := newExternalFixture(t)
	writeFixture(t, f.config, append(readFixture(t, f.config), []byte("source_retention: citations\n")...))
	config := f.install(t)
	completeExternal(t, config, "complete")
	request := filepath.Join(f.base, "request.json")
	writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{config}, []string{}}))
	archive := filepath.Join(f.base, "released.tar.gz")
	runFail(t, "bundle", "export", request, archive)
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("failed export published an archive")
	}
	runOK(t, "external", "release", config)
	runOK(t, "bundle", "export", request, archive)
	if err := os.RemoveAll(filepath.Join(f.base, "source")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.corpus); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(f.base, "restored")
	restored := runOK(t, "bundle", "restore", archive, destination).(map[string]any)
	restoredConfig := restored["configs"].([]string)[0]
	cfg, err := loadConfig(restoredConfig, true)
	if err != nil {
		t.Fatal(err)
	}
	physicalDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.External.CodeRoot != cfg.ProjectRoot || !within(physicalDestination, cfg.ProjectRoot) {
		t.Fatal("runtime root escaped restored package")
	}
	runOK(t, "status", restoredConfig, "complete")
	runOK(t, "report", restoredConfig, "complete")
	runFail(t, "prepare", restoredConfig, "missing-input")
	reexported := exportTestBundle(t, restoredConfig, destination)
	runOK(t, "bundle", "verify", reexported)
	b, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	mutateRetentionBundleFile(t, &b, "/profile.json", func(data []byte) []byte {
		return append(append([]byte{}, data...), ' ')
	})
	broken := filepath.Join(f.base, "bad-metadata.tar.gz")
	if err := writeBundle(broken, b); err != nil {
		t.Fatal(err)
	}
	runFail(t, "bundle", "verify", broken)
	runFail(t, "bundle", "restore", broken, filepath.Join(f.base, "bad-restored"))
	for _, name := range []string{"code-files.json", "original-config.yaml"} {
		t.Run("exact_metadata_"+name, func(t *testing.T) {
			b, err := readBundle(archive)
			if err != nil {
				t.Fatal(err)
			}
			// A whitespace-only edit preserves decoded meaning; the original bytes
			// are nevertheless pinned by the run's source inventory.
			mutateRetentionBundleFile(t, &b, "/"+name, func(data []byte) []byte { return append(append([]byte{}, data...), '\n') })
			broken := filepath.Join(f.base, "bad-"+name+".tar.gz")
			if err := writeBundle(broken, b); err != nil {
				t.Fatal(err)
			}
			runFail(t, "bundle", "verify", broken)
			runFail(t, "bundle", "restore", broken, filepath.Join(f.base, "restore-bad-"+name))
		})
	}
}

func mutateRetentionBundleFile(t *testing.T, b *evidenceBundle, contains string, mutate func([]byte) []byte) {
	t.Helper()
	found := false
	for i, file := range b.Index.Files {
		if strings.Contains(file.Path, contains) {
			data := mutate(b.Objects[file.SHA256])
			if data == nil {
				continue
			}
			file.SHA256, file.Bytes = digest(data), int64(len(data))
			b.Index.Files[i] = file
			b.Objects[file.SHA256] = data
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing fixture dependency", contains)
	}
	used := map[string]bool{}
	for _, file := range b.Index.Files {
		used[file.SHA256] = true
	}
	for hash := range b.Objects {
		if !used[hash] {
			delete(b.Objects, hash)
		}
	}
	b.Index.ID = bundleID(b.Index)
}

func TestCitationBundleRejectsModeAndInventoryCorruption(t *testing.T) {
	config, base := citationBundleFixture(t)
	archive := exportTestBundle(t, config, base)
	for _, kind := range []string{"legacy_schema", "unsupported_schema", "inventory_schema", "inventory_full", "inventory_hash", "run_mode"} {
		t.Run(kind, func(t *testing.T) {
			b, err := readBundle(archive)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "legacy_schema":
				b.Index.SchemaVersion = 1
			case "unsupported_schema":
				b.Index.SchemaVersion = 99
			case "run_mode":
				mutateRetentionBundleFile(t, &b, "review/manifest.json", func(data []byte) []byte {
					var m Manifest
					if err := json.Unmarshal(data, &m); err != nil {
						t.Fatal(err)
					}
					m.Config.SourceRetention = "archive"
					return mustJSON(t, m)
				})
			default:
				mutateRetentionBundleFile(t, &b, "review/sources.json", func(data []byte) []byte {
					var inv sourceInventory
					if err := json.Unmarshal(data, &inv); err != nil {
						t.Fatal(err)
					}
					switch kind {
					case "inventory_schema":
						inv.SchemaVersion = 1
					case "inventory_full":
						inv.Sources[0].Full = true
					case "inventory_hash":
						inv.Sources[0].SHA256 = digest([]byte("different"))
					}
					return mustJSON(t, inv)
				})
			}
			b.Index.ID = bundleID(b.Index)
			broken := filepath.Join(base, kind+".tar.gz")
			if err := writeBundle(broken, b); err != nil {
				t.Fatal(err)
			}
			runFail(t, "bundle", "verify", broken)
			destination := filepath.Join(base, "restore-"+kind)
			runFail(t, "bundle", "restore", broken, destination)
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("refused package partially restored")
			}
		})
	}
}

func TestCitationMixedBundleKeepsArchiveRequirements(t *testing.T) {
	config, base := citationBundleFixture(t)
	archivedConfig, _, _ := reviewedFixture(t, false)
	request := filepath.Join(base, "mixed-request.json")
	writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{config, archivedConfig}, []string{}}))
	archive := filepath.Join(base, "mixed.tar.gz")
	runOK(t, "bundle", "export", request, archive)
	runOK(t, "bundle", "verify", archive)
	b, err := readBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	mutateRetentionBundleFile(t, &b, "sources.json", func(data []byte) []byte {
		var inv sourceInventory
		if err := json.Unmarshal(data, &inv); err != nil {
			t.Fatal(err)
		}
		if inv.SchemaVersion != 1 {
			return nil
		}
		// A citations neighbor must not allow an archive scope to omit full bytes.
		inv.SchemaVersion = 2
		for i := range inv.Sources {
			inv.Sources[i].Full = false
			inv.Sources[i].Limitation = "sources intentionally omitted"
		}
		return mustJSON(t, inv)
	})
	broken := filepath.Join(base, "mixed-broken.tar.gz")
	if err := writeBundle(broken, b); err != nil {
		t.Fatal(err)
	}
	runFail(t, "bundle", "verify", broken)
}

func TestCitationMixedBundleDoesNotBorrowArchivedCheckout(t *testing.T) {
	config, base := citationBundleFixture(t)
	// Both policies may audit the same selected checkout in separate reports_dir.
	archiveConfig := filepath.Join(base, "archive-config.yaml")
	raw := bytes.Replace(readFixture(t, config), []byte("source_retention: citations\n"), nil, 1)
	raw = bytes.Replace(raw, []byte("reports_dir: runs"), []byte("reports_dir: archived-runs"), 1)
	writeFixture(t, archiveConfig, raw)
	runOK(t, "prepare", archiveConfig, "archived")
	request := filepath.Join(base, "shared-root-request.json")
	writeFixture(t, request, mustJSON(t, BundleRequest{1, []string{config, archiveConfig}, []string{}}))
	archive := filepath.Join(base, "shared-root.tar.gz")
	runOK(t, "bundle", "export", request, archive)
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(base, "shared-restored")).(map[string]any)
	for _, path := range restored["configs"].([]string) {
		cfg, err := loadConfig(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if !citationRetention(cfg) {
			continue
		}
		if _, err := os.Stat(cfg.ProjectRoot); !os.IsNotExist(err) {
			t.Fatal("citations borrowed the archive workspace", cfg.ProjectRoot, err)
		}
		if status := runOK(t, "status", path, "review").(Status); status.Freshness != "unchecked" {
			t.Fatal("archived neighbor made citations current")
		}
	}
}
