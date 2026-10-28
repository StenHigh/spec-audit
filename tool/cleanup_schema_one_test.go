//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCleanupSchemaOneProtectsPhysicalConsumers(t *testing.T) {
	for _, schema := range []int{1, 3} {
		for _, consumer := range []string{"archive-shared", "archive-copy", "citations-shared", "citations-copy"} {
			shared := strings.HasSuffix(consumer, "shared")
			t.Run(fmt.Sprintf("bundle-%d/%s", schema, consumer), func(t *testing.T) {
				f := newExternalFixture(t)
				f.base = exactSystemPath(f.base)
				parent := f.install(t)
				completeExternal(t, parent, "A")
				pc, err := loadConfig(parent, true)
				if err != nil {
					t.Fatal(err)
				}
				scratch := filepath.Join(pc.ReportsDir, "A", "dispatch", "all-mapper", "result.json")
				before := readFixture(t, scratch)
				retained := scratch
				if !shared {
					retained = filepath.Join(f.base, "secondary", "copy.json")
					writeFixture(t, retained, before)
				}
				for _, name := range []string{"rules.md", "source.go", "source_test.go"} {
					writeFixture(t, filepath.Join(f.base, "secondary", name), readFixture(t, "../acceptance/corpus/"+name))
				}
				relative, err := filepath.Rel(f.base, retained)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("[FIX:darwin-fixture] source relative to canonical fixture root: %s", relative)
				cfg := Config{Version: 1, ProjectRoot: f.base, ReportsDir: filepath.Join(f.base, "secondary-runs"), Specs: Sources{Paths: []string{"secondary/rules.md"}}, Code: Sources{Paths: []string{"secondary/source.go", filepath.ToSlash(relative)}}, Tests: Sources{Paths: []string{"secondary/source_test.go"}}, Runtime: Runtime{Kind: "none"}}
				if strings.HasPrefix(consumer, "citations-") {
					cfg.SourceRetention = "citations"
				}
				raw, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				secondary := filepath.Join(f.base, "secondary.yaml")
				writeFixture(t, secondary, raw)
				runOK(t, "prepare", secondary, "B")
				cfg, err = loadConfig(secondary, true)
				if err != nil {
					t.Fatal(err)
				}
				snapshotBefore, err := snapshot(cfg)
				if err != nil {
					t.Fatal(err)
				}
				mode := mustStat(t, retained).Mode().Perm()
				backup := ""
				if schema == 3 {
					backup = exportAuditTestBundle(t, []string{parent, secondary}, t.TempDir())
				} else {
					b, err := collectBundle(BundleRequest{SchemaVersion: 1, Configs: []string{parent, secondary}}, []Config{pc, cfg})
					if err != nil {
						t.Fatal(err)
					}
					backup = filepath.Join(t.TempDir(), "legacy.tar.gz")
					if err := writeBundle(backup, b); err != nil {
						t.Fatal(err)
					}
				}
				b, err := readBundle(backup)
				if err != nil {
					t.Fatal(err)
				}
				plan := runOK(t, "cleanup", "plan", backup).(CleanupPlan)
				if plan.SchemaVersion != 1 || len(plan.Candidates) == 0 {
					t.Fatal("schema 1 lost independent disposable duplicates")
				}
				areas := map[string]BundleArea{}
				parentArea := ""
				for _, area := range b.Index.Areas {
					areas[area.ID] = area
					if area.Kind == "reports" && area.OriginalRoot == pc.ReportsDir {
						parentArea = area.ID
					}
				}
				selected := false
				for _, candidate := range plan.Candidates {
					if filepath.Join(areas[candidate.Area].OriginalRoot, filepath.FromSlash(candidate.Path)) == scratch {
						selected = true
					}
				}
				if selected == shared {
					t.Fatalf("physical ownership ignored: shared=%t selected=%t", shared, selected)
				}
				if shared {
					// Recreate the formerly accepted unsafe plan and its genuine restore proof.
					unsafe := plan
					unsafe.Candidates = append([]CleanupCandidate{}, plan.Candidates...)
					unsafe.Candidates = append(unsafe.Candidates, CleanupCandidate{parentArea, "A/dispatch/all-mapper/result.json", digest(before), int64(len(before)), "byte_duplicate", "Побайтовый дубль сданного raw завершённого run; оригинал и пакет сохранены", []string{"A/state.json", "A/host-reviews.json"}, "A/results/all-mapper-attempt-1.json"})
					sort.Slice(unsafe.Candidates, func(i, j int) bool {
						return unsafe.Candidates[i].Area+"/"+unsafe.Candidates[i].Path < unsafe.Candidates[j].Area+"/"+unsafe.Candidates[j].Path
					})
					unsafe.FreedBytes += int64(len(before))
					unsafe.BytesAfter = unsafe.BytesBefore - unsafe.FreedBytes
					unsafe.ID = cleanupPlanID(unsafe)
					proof, err := proveBundleRestore(b, unsafe)
					if err != nil {
						t.Fatal(err)
					}
					pp, pf := filepath.Join(f.base, "unsafe-plan.json"), filepath.Join(f.base, "unsafe-proof.json")
					writeFixture(t, pp, mustJSON(t, unsafe))
					writeFixture(t, pf, mustJSON(t, proof))
					for _, args := range [][]string{{"cleanup", "verify", backup, pp}, {"cleanup", "apply", backup, pp, pf}} {
						if _, err := execute(args); err == nil {
							t.Fatal("old unsafe plan was accepted", args[:2])
						}
					}
				}
				pp, pf := filepath.Join(f.base, "plan.json"), filepath.Join(f.base, "proof.json")
				writeFixture(t, pp, mustJSON(t, plan))
				proof := runOK(t, "cleanup", "verify", backup, pp).(CleanupProof)
				writeFixture(t, pf, mustJSON(t, proof))
				// Resume after one disposable file has already been removed.
				first := plan.Candidates[0]
				if err := os.Remove(filepath.Join(areas[first.Area].OriginalRoot, filepath.FromSlash(first.Path))); err != nil {
					t.Fatal(err)
				}
				for repeat := 0; repeat < 2; repeat++ {
					runOK(t, "cleanup", "apply", backup, pp, pf)
					if !reflect.DeepEqual(before, readFixture(t, retained)) || mustStat(t, retained).Mode().Perm() != mode {
						t.Fatal("cleanup changed retained physical source")
					}
				}
				after, err := snapshot(cfg)
				if err != nil || after.SnapshotID != snapshotBefore.SnapshotID {
					t.Fatal("cleanup changed secondary snapshot", err)
				}
				if !shared {
					if _, err := os.Stat(scratch); !os.IsNotExist(err) {
						t.Fatal("independent duplicate was not deleted", err)
					}
				}
				publicationView(t, filepath.Join(f.base, "historical-after-cleanup"), "--run", "A", parent)
			})
		}
	}
}

// Exercise the omitted JSON runtime root without requiring project execution.
func TestCleanupSchemaOneProtectsExternalRuntime(t *testing.T) {
	f := newExternalFixture(t)
	f.base = exactSystemPath(f.base)
	parent := f.install(t)
	completeExternal(t, parent, "A")
	pc, err := loadConfig(parent, true)
	if err != nil {
		t.Fatal(err)
	}
	code := filepath.Join(pc.ReportsDir, "A", "dispatch", "all-mapper", "result.json")
	relative, err := filepath.Rel(f.base, code)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("[FIX:darwin-fixture] runtime source relative to canonical fixture root: %s", relative)
	for _, name := range []string{"rules.md", "source.go", "source_test.go", "go.mod"} {
		writeFixture(t, filepath.Join(f.base, "secondary", name), readFixture(t, "../acceptance/corpus/"+name))
	}
	cfg := Config{Version: 1, ProjectRoot: f.base, ReportsDir: filepath.Join(f.base, "external-runs"), Specs: Sources{Paths: []string{"rules.md"}}, Code: Sources{Paths: []string{"secondary/source.go", filepath.ToSlash(relative)}}, Tests: Sources{Paths: []string{"secondary/source_test.go"}}, Runtime: Runtime{Kind: "none"}}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.base, "external-template.yaml")
	writeFixture(t, path, raw)
	input := externalFromConfig(t, path, f.base)
	secondary := input.install(t)
	backup := exportAuditTestBundle(t, []string{parent, secondary}, t.TempDir())
	plan := runOK(t, "cleanup", "plan", backup).(CleanupPlan)
	for _, c := range plan.Candidates {
		if c.Path == "A/dispatch/all-mapper/result.json" {
			t.Fatal("external runtime source became disposable")
		}
	}
}
