//go:build darwin || linux

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestPublishBaselineRequiresSelectedCompleteRun(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	for _, id := range []string{"A", "B", "C"} {
		completeExternal(t, config, id)
	}
	runOK(t, "prepare", config, "P")
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.Default()
	defer slog.SetDefault(logger)
	for _, tc := range []struct {
		name, requested, selected string
		pinned, warn              bool
	}{
		{"default", "", "B", false, false},
		{"missing", "absent", "B", false, true},
		{"pending", "P", "B", false, true},
		{"latest", "C", "B", false, true},
		{"older", "A", "A", true, false},
		{"previous", "B", "B", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.BaselineRun = tc.requested
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.base, tc.name+".yaml")
			writeFixture(t, path, raw)
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			out := filepath.Join(f.base, "site-"+tc.name)
			view := publicationView(t, out, path)
			delta := view.Scopes[0].Delta
			if delta == nil || delta.BaselineRun != tc.selected || delta.Pinned != tc.pinned || view.Scopes[0].Decided.RunID != "C" {
				t.Fatalf("wrong baseline selection: delta=%+v", delta)
			}
			warn := strings.Contains(logs.String(), "baseline_run не найден среди решённых run")
			if warn != tc.warn {
				t.Fatalf("wrong fallback warning: got %t want %t: %s", warn, tc.warn, logs.String())
			}
			label := tc.selected + " (baseline)"
			if strings.Contains(string(readFixture(t, filepath.Join(out, "index.html"))), label) != tc.pinned {
				t.Fatal("HTML baseline marker differs from JSON")
			}
		})
	}
	slog.SetDefault(logger)
	// A missing baseline stays harmless after restore and publication cleanup.
	cfg.BaselineRun = "absent"
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.base, "missing.yaml")
	writeFixture(t, path, raw)
	legacy := filepath.Join(f.base, "legacy-site")
	writeFixture(t, filepath.Join(legacy, "index.html"), []byte("old publication"))
	if err := recordPublication(legacy, []string{path}); err != nil {
		t.Fatal(err)
	}
	pubs, err := filepath.Glob(filepath.Join(cfg.ReportsDir, "._publications", "*.json"))
	if err != nil || len(pubs) != 1 {
		t.Fatal(pubs, err)
	}
	id := strings.TrimSuffix(filepath.Base(pubs[0]), ".json")
	backup := exportAuditTestBundle(t, []string{path}, t.TempDir())
	plan := runOK(t, "cleanup", "plan", backup, "--publications", id).(CleanupPlan)
	pp, pf := filepath.Join(f.base, "plan.json"), filepath.Join(f.base, "proof.json")
	writeFixture(t, pp, mustJSON(t, plan))
	proof := runOK(t, "cleanup", "verify", backup, pp).(CleanupProof)
	writeFixture(t, pf, mustJSON(t, proof))
	for i := 0; i < 2; i++ {
		runOK(t, "cleanup", "apply", backup, pp, pf)
	}
	view := publicationView(t, filepath.Join(f.base, "after-cleanup"), path)
	if view.Scopes[0].Delta.Pinned || view.Scopes[0].Delta.BaselineRun != "B" {
		t.Fatal("cleanup changed missing baseline fallback")
	}
	if err := os.Remove(filepath.Join(cfg.ReportsDir, "B", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	before := publicationBytes(t, cfg.ReportsDir)
	history := publicationView(t, filepath.Join(f.base, "historical-A"), "--run", "A", path)
	if history.Scopes[0].Delta != nil || history.Scopes[0].Decided.RunID != "A" {
		t.Fatal("historical A depends on unrelated baseline")
	}
	if !reflect.DeepEqual(before, publicationBytes(t, cfg.ReportsDir)) {
		t.Fatal("historical publication changed saved audit")
	}
}
