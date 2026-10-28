//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublishLatestRejectsMissingManifest(t *testing.T) {
	for _, retention := range []string{"archive", "citations"} {
		t.Run(retention, func(t *testing.T) {
			f := newExternalFixture(t)
			if retention == "citations" {
				f = citationLifecycleFixture(t)
			}
			config := f.install(t)
			completeExternal(t, config, "A")
			completeExternal(t, config, "B")
			cfg, err := loadConfig(config, true)
			if err != nil {
				t.Fatal(err)
			}
			previous := filepath.Join(f.base, "previous-A")
			publicationView(t, previous, "--run", "A", config)
			previousBytes := publicationBytes(t, previous)
			manifest := filepath.Join(cfg.ReportsDir, "B", "manifest.json")
			raw, info := readFixture(t, manifest), mustStat(t, manifest)
			if err := os.Remove(manifest); err != nil {
				t.Fatal(err)
			}
			before := publicationBytes(t, cfg.ReportsDir)
			for i, selection := range [][]string{{config}, {"--from", cfg.ReportsDir}, {"--run", "B", config}} {
				out := filepath.Join(f.base, fmt.Sprintf("missing-B-%d", i))
				if _, err := execute(append([]string{"publish", out}, selection...)); err == nil {
					t.Fatal("saved B without manifest was hidden", selection)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("missing manifest exposed OUT", err)
				}
			}
			view := publicationView(t, filepath.Join(f.base, "historical-A"), "--run", "A", config)
			if view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "A" {
				t.Fatal("historical A depends on missing B manifest")
			}
			if !reflect.DeepEqual(before, publicationBytes(t, cfg.ReportsDir)) || !reflect.DeepEqual(previousBytes, publicationBytes(t, previous)) {
				t.Fatal("refusal or historical publication changed audit/previous site")
			}
			writeFixture(t, manifest, raw)
			if err := os.Chmod(manifest, info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			view = publicationView(t, filepath.Join(f.base, "repaired-B"), config)
			if view.Scopes[0].Decided == nil || view.Scopes[0].Decided.RunID != "B" {
				t.Fatal("repaired latest did not select B")
			}
		})
	}
}

func TestPublishMissingManifestIndependentEvidence(t *testing.T) {
	for _, remaining := range []string{"state.json", "sources.json", toolVersionsFile} {
		t.Run(remaining, func(t *testing.T) {
			f := newExternalFixture(t)
			config := f.install(t)
			completeExternal(t, config, "A")
			completeExternal(t, config, "B")
			cfg, err := loadConfig(config, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"manifest.json", "state.json", "sources.json", toolVersionsFile} {
				if name != remaining {
					if err := os.Remove(filepath.Join(cfg.ReportsDir, "B", name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			out := filepath.Join(f.base, "missing-manifest")
			if _, err := execute([]string{"publish", out, config}); err == nil {
				t.Fatal("remaining audit evidence did not prevent fallback", remaining)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("failed selection created OUT", err)
			}
		})
	}
}

func TestAuditOnlyBundlePreservesUnknownReportPairs(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	unknown := map[string][]byte{}
	for _, dir := range []string{".", "notes", "A/notes", "notes/deep", "copied-report"} {
		json := []byte("operator data, intentionally not a Report\n")
		if dir == "copied-report" {
			json = readFixture(t, filepath.Join(cfg.ReportsDir, "A", "report.json"))
		}
		for name, raw := range map[string][]byte{"report.json": json, "report.html": []byte("<p>retained operator notes</p>\n")} {
			path := filepath.ToSlash(filepath.Join(dir, name))
			unknown[path] = raw
			absolute := filepath.Join(cfg.ReportsDir, filepath.FromSlash(path))
			writeFixture(t, absolute, raw)
			if err := os.Chmod(absolute, 0640); err != nil {
				t.Fatal(err)
			}
		}
	}
	legacy := filepath.Join(f.base, "legacy-site")
	writeFixture(t, filepath.Join(legacy, "index.html"), []byte("disposable legacy publication\n"))
	if err := recordPublication(legacy, []string{config}); err != nil {
		t.Fatal(err)
	}
	publications, err := filepath.Glob(filepath.Join(cfg.ReportsDir, "._publications", "*.json"))
	if err != nil || len(publications) != 1 {
		t.Fatal(publications, err)
	}
	id := strings.TrimSuffix(filepath.Base(publications[0]), ".json")
	backup := exportAuditTestBundle(t, []string{config}, f.base)
	b, err := readBundle(backup)
	if err != nil {
		t.Fatal(err)
	}
	reportsArea := ""
	for _, area := range b.Index.Areas {
		if area.Kind == "reports" {
			reportsArea = area.ID
		}
	}
	for path, raw := range unknown {
		found := false
		for _, file := range b.Index.Files {
			if file.Area == reportsArea && file.Path == path {
				found = file.SHA256 == digest(raw) && file.Mode == 0640
			}
		}
		if !found {
			t.Fatal("unknown pair lost its bytes/mode in backup", path)
		}
	}
	for _, schema := range []int{1, 3} {
		bad := evidenceBundle{Index: b.Index, Objects: map[string][]byte{}}
		bad.Index.SchemaVersion = schema
		bad.Index.Files = append([]BundleFile{}, b.Index.Files...)
		for hash, raw := range b.Objects {
			bad.Objects[hash] = raw
		}
		html := []byte("actual A report HTML\n")
		bad.Objects[digest(html)] = html
		bad.Index.Files = append(bad.Index.Files, BundleFile{reportsArea, "A/report.html", digest(html), int64(len(html)), 0640})
		bad.Index.ID = bundleID(bad.Index)
		err := validateBundle(bad)
		if schema == 3 && err == nil || schema == 1 && err != nil {
			t.Fatal("derived HTML guard changed legacy or admitted a real run HTML", schema, err)
		}
	}
	restored := filepath.Join(f.base, "restored")
	runOK(t, "bundle", "restore", backup, restored)
	check := func(root string) {
		t.Helper()
		for path, raw := range unknown {
			absolute := filepath.Join(root, filepath.FromSlash(path))
			if string(readFixture(t, absolute)) != string(raw) || mustStat(t, absolute).Mode().Perm() != 0640 {
				t.Fatal("unknown pair lost bytes/mode", absolute)
			}
		}
	}
	check(filepath.Join(restored, "areas", reportsArea))
	plan := runOK(t, "cleanup", "plan", backup, "--publications", id).(CleanupPlan)
	for _, candidate := range plan.Candidates {
		if candidate.Area == reportsArea && unknown[candidate.Path] != nil {
			t.Fatal("unknown pair became a cleanup candidate", candidate.Path)
		}
	}
	pp, pf := filepath.Join(f.base, "plan.json"), filepath.Join(f.base, "proof.json")
	writeFixture(t, pp, mustJSON(t, plan))
	proof := runOK(t, "cleanup", "verify", backup, pp).(CleanupProof)
	writeFixture(t, pf, mustJSON(t, proof))
	for i := 0; i < 2; i++ {
		runOK(t, "cleanup", "apply", backup, pp, pf)
		check(cfg.ReportsDir)
	}
	publicationView(t, filepath.Join(f.base, "after-cleanup"), config)
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
