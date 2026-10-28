//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestPublicationCleanupProtectsPhysicalConsumers(t *testing.T) {
	for _, consumer := range []string{"object", "manifest", "config", "external-runtime", "local-citations", "separate-copy"} {
		t.Run(consumer, func(t *testing.T) {
			f := newExternalFixture(t)
			parent := f.install(t)
			completeExternal(t, parent, "A")
			pc, err := loadConfig(parent, true)
			if err != nil {
				t.Fatal(err)
			}
			cfg := Config{Version: 1, ProjectRoot: filepath.Join(f.base, "secondary-source"), ReportsDir: filepath.Join(f.base, "secondary-runs"), Specs: Sources{Paths: []string{"rules.md"}}, Code: Sources{Paths: []string{"source.go", "go.mod"}}, Tests: Sources{Paths: []string{"source_test.go"}}, Runtime: Runtime{Kind: "none"}}
			if consumer == "object" || consumer == "external-runtime" || consumer == "local-citations" {
				cfg.ProjectRoot = filepath.Join(pc.ReportsDir, "._objects")
			}
			if consumer == "local-citations" {
				cfg.SourceRetention = "citations"
			}
			if consumer == "manifest" {
				cfg.ProjectRoot = f.base
			}
			payload := []byte("package fixture\n// retained source used by another CONFIG\n")
			if consumer == "config" {
				payload, err = yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			legacy := filepath.Join(f.base, "legacy-site")
			writeFixture(t, filepath.Join(legacy, "index.html"), payload)
			if err := recordPublication(legacy, []string{parent}); err != nil {
				t.Fatal(err)
			}
			pubs, err := filepath.Glob(filepath.Join(pc.ReportsDir, "._publications/*.json"))
			if err != nil || len(pubs) != 1 {
				t.Fatal(pubs, err)
			}
			selectedID := strings.TrimSuffix(filepath.Base(pubs[0]), ".json")
			objectPath := filepath.Join(pc.ReportsDir, "._objects", digest(payload))
			retainedPath := objectPath
			if consumer == "manifest" {
				retainedPath = pubs[0]
			}
			if consumer == "separate-copy" {
				retainedPath = filepath.Join(cfg.ProjectRoot, "current-code", "copy.go")
				writeFixture(t, retainedPath, payload)
				cfg.Code.Paths = append(cfg.Code.Paths, "current-code")
			} else if consumer != "config" {
				relative, err := filepath.Rel(cfg.ProjectRoot, retainedPath)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Code.Paths = append(cfg.Code.Paths, filepath.ToSlash(relative))
			}
			for _, name := range []string{"rules.md", "source.go", "source_test.go", "go.mod"} {
				writeFixture(t, filepath.Join(cfg.ProjectRoot, name), readFixture(t, filepath.Join("../acceptance/corpus", name)))
			}
			secondary := filepath.Join(f.base, "secondary.yaml")
			if consumer == "config" {
				secondary = objectPath
			} else {
				raw, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, secondary, raw)
			}
			if consumer == "external-runtime" {
				input := externalFromConfig(t, secondary, f.base)
				template, err := loadConfig(secondary, true)
				if err != nil {
					t.Fatal(err)
				}
				template.External.Scope = "secondary-runtime"
				raw, err := yaml.Marshal(template)
				if err != nil {
					t.Fatal(err)
				}
				writeFixture(t, secondary, raw)
				secondary = input.install(t)
			}
			cfg, err = loadConfig(secondary, true)
			if err != nil {
				t.Fatal(err)
			}
			completeExternal(t, secondary, "B")
			if err := runtimeSourcesCurrent(cfg); err != nil {
				t.Fatal(err)
			}
			before, err := snapshot(cfg)
			if err != nil {
				t.Fatal(err)
			}
			retained := readFixture(t, retainedPath)
			retainedInfo, err := os.Stat(retainedPath)
			if err != nil {
				t.Fatal(err)
			}
			savedAudit := publicationBytes(t, cfg.ReportsDir)
			backup := exportAuditTestBundle(t, []string{parent, secondary}, t.TempDir())
			b, err := readBundle(backup)
			if err != nil {
				t.Fatal(err)
			}
			if consumer == "external-runtime" {
				for _, config := range b.Index.Configs {
					if config.OriginalPath == secondary && config.Config.External.CodeRoot != "" {
						t.Fatal("regression no longer crosses omitted JSON runtime root")
					}
				}
			}
			plan := runOK(t, "cleanup", "plan", backup, "--publications", selectedID).(CleanupPlan)
			areas := map[string]BundleArea{}
			for _, area := range b.Index.Areas {
				areas[area.ID] = area
			}
			sawObject := false
			for _, c := range plan.Candidates {
				absolute := filepath.Join(areas[c.Area].OriginalRoot, filepath.FromSlash(c.Path))
				if absolute == retainedPath {
					t.Fatal("retained physical consumer became a cleanup candidate", consumer, absolute)
				}
				if absolute == objectPath {
					sawObject = true
				}
			}
			if consumer == "separate-copy" && !sawObject {
				t.Fatal("equal bytes at a different physical path prevented cleanup")
			}
			if consumer == "manifest" && sawObject {
				t.Fatal("object of a retained publication manifest became a candidate")
			}
			pp, pf := filepath.Join(f.base, "plan.json"), filepath.Join(f.base, "proof.json")
			writeFixture(t, pp, mustJSON(t, plan))
			proof := runOK(t, "cleanup", "verify", backup, pp).(CleanupProof)
			writeFixture(t, pf, mustJSON(t, proof))
			if consumer == "separate-copy" {
				for _, mutation := range []string{"bytes", "mode", "new-source"} {
					switch mutation {
					case "bytes":
						writeFixture(t, retainedPath, append(append([]byte{}, retained...), []byte("// changed\n")...))
					case "mode":
						if err := os.Chmod(retainedPath, retainedInfo.Mode().Perm()^0100); err != nil {
							t.Fatal(err)
						}
					case "new-source":
						writeFixture(t, filepath.Join(cfg.ProjectRoot, "current-code", "new.go"), []byte("package fixture\n"))
					}
					for _, args := range [][]string{{"cleanup", "verify", backup, pp}, {"cleanup", "apply", backup, pp, pf}} {
						if _, err := execute(args); err == nil {
							t.Fatalf("%s workspace change did not invalidate %v", mutation, args[:2])
						}
					}
					if _, err := os.Stat(pubs[0]); err != nil {
						t.Fatal("stale workspace check removed manifest", err)
					}
					if err := os.Chmod(retainedPath, 0600); err != nil {
						t.Fatal(err)
					}
					writeFixture(t, retainedPath, retained)
					if err := os.Chmod(retainedPath, retainedInfo.Mode().Perm()); err != nil {
						t.Fatal(err)
					}
					if mutation == "new-source" {
						if err := os.Remove(filepath.Join(cfg.ProjectRoot, "current-code", "new.go")); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Resume after the selected manifest has already been removed.
				if err := os.Remove(pubs[0]); err != nil {
					t.Fatal(err)
				}
			}
			for repeat := 0; repeat < 2; repeat++ {
				result := runOK(t, "cleanup", "apply", backup, pp, pf).(map[string]any)
				if result["complete"] != true {
					t.Fatal("cleanup did not complete", result)
				}
			}
			if err := runtimeSourcesCurrent(cfg); err != nil {
				t.Fatal("cleanup changed external runtime sources", err)
			}
			after, err := snapshot(cfg)
			if err != nil || before.SnapshotID != after.SnapshotID {
				t.Fatal("cleanup changed retained source snapshot", err)
			}
			info, err := os.Stat(retainedPath)
			if err != nil || !reflect.DeepEqual(retained, readFixture(t, retainedPath)) || info.Mode().Perm() != retainedInfo.Mode().Perm() {
				t.Fatal("retained physical bytes/mode changed", err)
			}
			if !reflect.DeepEqual(savedAudit, publicationBytes(t, cfg.ReportsDir)) {
				t.Fatal("secondary audit changed")
			}
			if consumer == "separate-copy" {
				if _, err := os.Stat(objectPath); !os.IsNotExist(err) {
					t.Fatal("disposable physical duplicate remained", err)
				}
			}
			publicationView(t, filepath.Join(f.base, "site-after-cleanup"), parent, secondary)
			publicationView(t, filepath.Join(f.base, "history-after-cleanup"), "--run", "B", secondary)
		})
	}
}
