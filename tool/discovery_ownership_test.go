//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func nestedOwnershipFixture(t *testing.T, suffix string) (externalFixture, string, string, Config, Config) {
	t.Helper()
	f := newExternalFixture(t)
	parent := f.install(t)
	completeExternal(t, parent, "A")
	pc, err := loadConfig(parent, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(f.config, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReportsDir = filepath.Join(pc.ReportsDir, filepath.FromSlash(suffix))
	ext := *cfg.External
	ext.Scope = "child-scope"
	cfg.External = &ext
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(f.base, "child.yaml")
	writeFixture(t, template, raw)
	child := runOK(t, "external", "import", template, f.input).(map[string]any)["config"].(string)
	completeExternal(t, child, "A")
	cc, err := loadConfig(child, true)
	if err != nil {
		t.Fatal(err)
	}
	return f, parent, child, pc, cc
}

func TestPublishRejectsReportsRootInsideParentRun(t *testing.T) {
	for _, suffix := range []string{"A", "A/nested"} {
		t.Run(strings.ReplaceAll(suffix, "/", "-"), func(t *testing.T) {
			f, parent, child, pc, cc := nestedOwnershipFixture(t, suffix)
			previous := filepath.Join(f.base, "historical-parent")
			historical := publicationView(t, previous, "--run", "A", parent)
			if historical.Scopes[0].Decided == nil {
				t.Fatal("parent A is not a complete audit")
			}
			auditBefore, siteBefore := publicationBytes(t, pc.ReportsDir), publicationBytes(t, previous)
			for i, order := range [][]string{{parent, child}, {child, parent}} {
				out := filepath.Join(f.base, fmt.Sprintf("conflict-site-%d", i))
				_, err := execute(append([]string{"publish", out}, order...))
				if err == nil || !strings.Contains(err.Error(), "run родительской области") {
					t.Fatalf("reports/run ownership conflict was not refused: %v", err)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("conflicting selection exposed OUT", err)
				}
				request, archive := filepath.Join(f.base, fmt.Sprintf("request-%d.json", i)), filepath.Join(f.base, fmt.Sprintf("conflict-%d.tar.gz", i))
				writeFixture(t, request, mustJSON(t, map[string]any{"schema_version": 2, "configs": order}))
				if _, err := execute([]string{"bundle", "export", request, archive}); err == nil || !strings.Contains(err.Error(), "run родительской области") {
					t.Fatal("bundle did not reject the same ownership conflict", err)
				}
				if _, err := os.Stat(archive); !os.IsNotExist(err) {
					t.Fatal("conflicting bundle exposed archive", err)
				}
			}
			// Old or otherwise supplied indices must not bypass the shared cleanup boundary.
			b := evidenceBundle{Index: BundleIndex{SchemaVersion: 3, Areas: []BundleArea{{"parent", "reports", pc.ReportsDir}, {"child", "reports", cc.ReportsDir}}}}
			if _, err := capturePublicationCleanup(b, []string{digest([]byte("selection"))}); err == nil || !strings.Contains(err.Error(), "run родительской области") {
				t.Fatal("cleanup capture missed ownership conflict", err)
			}
			if err := comparePublicationLive(b, CleanupPlan{PublicationBasis: &PublicationCleanupBasis{}}); err == nil || !strings.Contains(err.Error(), "run родительской области") {
				t.Fatal("cleanup apply check missed ownership conflict", err)
			}
			if !reflect.DeepEqual(auditBefore, publicationBytes(t, pc.ReportsDir)) || !reflect.DeepEqual(siteBefore, publicationBytes(t, previous)) {
				t.Fatal("refusal changed audit or previous site")
			}
			publicationView(t, filepath.Join(f.base, "historical-still-independent"), "--run", "A", parent)
		})
	}
}

func TestPublishDiscoveryClassifiesRootMetadata(t *testing.T) {
	for _, where := range []string{"child", "root", "both"} {
		t.Run(where, func(t *testing.T) {
			f, parent, child, pc, cc := nestedOwnershipFixture(t, "child")
			roots := []string{cc.ReportsDir}
			if where == "root" {
				roots = []string{pc.ReportsDir}
			}
			if where == "both" {
				roots = append(roots, pc.ReportsDir)
			}
			for _, root := range roots {
				writeFixture(t, filepath.Join(root, "manifest.json"), []byte("{\"owner\":\"store\"}\n"))
				writeFixture(t, filepath.Join(root, "state.json"), []byte("{\"status\":\"stored\"}\n"))
			}
			publicationView(t, filepath.Join(f.base, "explicit"), parent, child)
			if err := os.Rename(f.config, filepath.Join(f.base, "template-only.yaml")); err != nil {
				t.Fatal(err)
			}
			before := publicationBytes(t, pc.ReportsDir)
			for _, fromCWD := range []bool{false, true} {
				out := filepath.Join(f.base, fmt.Sprintf("discovered-%t", fromCWD))
				args := []string{"--from", pc.ReportsDir}
				if fromCWD {
					cwd, err := os.Getwd()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chdir(pc.ReportsDir); err != nil {
						t.Fatal(err)
					}
					defer os.Chdir(cwd)
					args = nil
				}
				view := publicationView(t, out, args...)
				if len(view.Scopes) != 2 || view.Scopes[0].Decided == nil || view.Scopes[1].Decided == nil {
					t.Fatal("discovery lost complete scopes")
				}
			}
			if !reflect.DeepEqual(before, publicationBytes(t, pc.ReportsDir)) {
				t.Fatal("discovery changed root metadata/audit")
			}
		})
	}
}

func TestPublishDiscoveryKeepsAmbiguousMetadataErrors(t *testing.T) {
	for _, kind := range []string{"unowned", "broken-run", "broken-run-with-child", "broken-manifest-and-state-with-child", "missing-manifest-with-child", "missing-manifest-and-state-with-child"} {
		t.Run(kind, func(t *testing.T) {
			suffix := "child"
			if strings.HasSuffix(kind, "with-child") {
				suffix = "A"
			}
			f, _, _, pc, _ := nestedOwnershipFixture(t, suffix)
			target := filepath.Join(pc.ReportsDir, "A")
			if kind == "unowned" {
				target = filepath.Join(pc.ReportsDir, "unrelated")
				writeFixture(t, filepath.Join(target, "manifest.json"), []byte("{\"owner\":\"unrelated\"}\n"))
				writeFixture(t, filepath.Join(target, "state.json"), []byte("{\"status\":\"stored\"}\n"))
			} else if strings.HasPrefix(kind, "missing-") {
				if err := os.Remove(filepath.Join(target, "manifest.json")); err != nil {
					t.Fatal(err)
				}
				if kind == "missing-manifest-and-state-with-child" {
					if err := os.Remove(filepath.Join(target, "state.json")); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := os.Chmod(filepath.Join(target, "manifest.json"), 0600); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(target, "manifest.json"), []byte("{broken"))
				if kind == "broken-manifest-and-state-with-child" {
					if err := os.Chmod(filepath.Join(target, "state.json"), 0600); err != nil {
						t.Fatal(err)
					}
					writeFixture(t, filepath.Join(target, "state.json"), []byte("{broken"))
				}
			}
			before := publicationBytes(t, pc.ReportsDir)
			out := filepath.Join(f.base, "invalid-discovery")
			if _, err := execute([]string{"publish", out, "--from", pc.ReportsDir}); err == nil {
				t.Fatal("unowned metadata or broken actual run was hidden", kind)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("failed discovery exposed OUT", err)
			}
			if !reflect.DeepEqual(before, publicationBytes(t, pc.ReportsDir)) {
				t.Fatal("refusal changed retained bytes")
			}
		})
	}
}

func TestPublishRejectsMissingParentManifestWithNestedStore(t *testing.T) {
	for _, suffix := range []string{"A", "A/nested"} {
		t.Run(strings.ReplaceAll(suffix, "/", "-"), func(t *testing.T) {
			f, parent, child, pc, cc := nestedOwnershipFixture(t, suffix)
			if err := os.Remove(filepath.Join(pc.ReportsDir, "A", "manifest.json")); err != nil {
				t.Fatal(err)
			}
			before := publicationBytes(t, pc.ReportsDir)
			for i, selection := range [][]string{{parent, child}, {child, parent}, {"--from", pc.ReportsDir}} {
				out := filepath.Join(f.base, fmt.Sprintf("missing-manifest-%d", i))
				if _, err := execute(append([]string{"publish", out}, selection...)); err == nil {
					t.Fatal("missing parent manifest was hidden", selection)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("damaged parent run exposed OUT", err)
				}
			}
			roots := []string{pc.ReportsDir, cc.ReportsDir}
			if _, err := nestedReportPaths(pc.ReportsDir, roots); err == nil {
				t.Fatal("missing manifest bypassed shared export/cleanup boundary")
			}
			if !reflect.DeepEqual(before, publicationBytes(t, pc.ReportsDir)) {
				t.Fatal("refusal changed retained audit")
			}
		})
	}
}
