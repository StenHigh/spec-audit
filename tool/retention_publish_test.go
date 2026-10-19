//go:build darwin || linux

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func citationPublicationFixture(t *testing.T) (externalFixture, string, Config) {
	t.Helper()
	f := citationLifecycleFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	runOK(t, "external", "release", config)
	runOK(t, "report", config, "A")
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	return f, config, cfg
}

func publicationBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = string(readFixture(t, path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCitationPublishFreshStagingOmitsUnrelatedFullSources(t *testing.T) {
	for _, kind := range []string{"external", "local"} {
		t.Run(kind, func(t *testing.T) {
			var config, base, run string
			var cfg Config
			if kind == "external" {
				f, path, c := citationPublicationFixture(t)
				config, base, cfg, run = path, f.base, c, "A"
			} else {
				config, base = citationBundleFixture(t)
				cfg, _ = loadConfig(config, true)
				run = "review"
			}
			// Foreign viewer resources must not be copied merely because they exist.
			for _, name := range []string{"full.html", "full.bin", "hidden.json", "nested/source.js"} {
				writeFixture(t, filepath.Join(cfg.ReportsDir, run, "sources", name), []byte(retentionLifecycleMarker))
			}
			out := filepath.Join(base, "fresh-site")
			result := runOK(t, "publish", out, config).(map[string]any)
			if result["dir"] != out || result["index"] != filepath.Join(out, "index.html") || result["source_retention"] != "citations" {
				t.Fatal("staging paths leaked into publication response", result)
			}
			assertNoLifecycleMarker(t, out)
			files := publicationBytes(t, out)
			if files["index.html"] == "" || files["overview.json"] == "" {
				t.Fatal("publication is incomplete")
			}
			for name := range files {
				if strings.Contains(name, "/sources/") || strings.HasSuffix(name, ".bin") {
					t.Fatal("full source resource published", name)
				}
			}
			var overview Overview
			if err := strictJSON([]byte(files["overview.json"]), &overview); err != nil {
				t.Fatal(err)
			}
			if len(overview.Scopes) != 1 || overview.Scopes[0].Decided == nil || overview.Scopes[0].Decided.Report == "" {
				t.Fatal("missing published navigation")
			}
			link := overview.Scopes[0].Decided.Report
			if strings.Contains(link, ".citation-site-") || files[link] == "" {
				t.Fatal("published link points at staging", link)
			}
			if kind == "external" {
				resultPath := filepath.ToSlash(filepath.Join(filepath.Dir(link), "result.json"))
				var exported ExternalResult
				if err := strictJSON([]byte(files[resultPath]), &exported); err != nil {
					t.Fatal(err)
				}
				if exported.SchemaVersion != 2 || exported.SourceRetention != "citations" || exported.Report.Freshness != "stale" {
					t.Fatal("publication lost result mode or historical currency")
				}
			}
		})
	}
}

func TestCitationPublishPreservesExistingSiteAndRollsBackFailure(t *testing.T) {
	f, config, cfg := citationPublicationFixture(t)
	good := filepath.Join(f.base, "good-site")
	runOK(t, "publish", good, config)
	writeFixture(t, filepath.Join(good, "existing-private.dat"), []byte("preserve every preexisting byte"))
	before := publicationBytes(t, good)
	runFail(t, "publish", good, config)
	if !reflect.DeepEqual(before, publicationBytes(t, good)) {
		t.Fatal("existing output was changed by refusal")
	}
	// External publication writes report.html before it parses report.json.
	// This refusal therefore exercises cleanup after the staging tree was used.
	writeFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"), []byte("{invalid report"))
	failed := filepath.Join(f.base, "failed-site")
	runFail(t, "publish", failed, config)
	if _, err := os.Lstat(failed); !os.IsNotExist(err) {
		t.Fatal("failed publication exposed a partial site", err)
	}
	if !reflect.DeepEqual(before, publicationBytes(t, good)) {
		t.Fatal("failed fresh publication changed the earlier site")
	}
	stages, err := filepath.Glob(filepath.Join(f.base, ".citation-site-*"))
	if err != nil || len(stages) != 0 {
		t.Fatal("failed publication retained staging", stages, err)
	}
}

func TestCitationPublishRejectsMixedArchiveSite(t *testing.T) {
	f, citations, _ := citationPublicationFixture(t)
	archive, _ := fixture(t)
	out := filepath.Join(f.base, "mixed-site")
	for _, configs := range [][]string{{citations, archive}, {archive, citations}} {
		args := append([]string{"publish", out}, configs...)
		runFail(t, args...)
		if _, err := os.Lstat(out); !os.IsNotExist(err) {
			t.Fatal("mixed publication created an output", err)
		}
	}
}

func TestCitationPublicationBrowserFixture(t *testing.T) {
	out := os.Getenv("SPEC_AUDIT_CITATION_UI")
	if out == "" {
		t.Skip("SPEC_AUDIT_CITATION_UI is not configured")
	}
	_, config, _ := citationPublicationFixture(t)
	runOK(t, "publish", out, config)
	assertNoLifecycleMarker(t, out)
}
