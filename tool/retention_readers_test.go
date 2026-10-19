//go:build darwin || linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func citationsConfig(t *testing.T, config string) {
	t.Helper()
	writeFixture(t, config, append(readFixture(t, config), []byte("\nsource_retention: citations\n")...))
}

func TestCitationSourceInventoryDoesNotRetainBytes(t *testing.T) {
	config, base := fixture(t)
	citationsConfig(t, config)
	marker := []byte("\n// UNQUOTED_SOURCE_MARKER\n")
	source := filepath.Join(base, "source/source.go")
	writeFixture(t, source, append(readFixture(t, source), marker...))
	runOK(t, "prepare", config, "citations")
	var inv sourceInventory
	if err := strictJSON(readFixture(t, filepath.Join(base, "runs/citations/sources.json")), &inv); err != nil {
		t.Fatal(err)
	}
	if inv.SchemaVersion != 2 || len(inv.Sources) != 4 {
		t.Fatal("missing citation inventory", inv)
	}
	for _, source := range inv.Sources {
		if source.Full || source.URL != "" || source.RawURL != "" || source.Limitation == "" {
			t.Fatal("citation inventory promises a full source", source)
		}
	}
	for _, name := range []string{"._objects", "citations/sources"} {
		if _, err := os.Stat(filepath.Join(base, "runs", name)); !os.IsNotExist(err) {
			t.Fatalf("retained source assets %s: %v", name, err)
		}
	}
	runOK(t, "report", config, "citations")
	for _, name := range []string{"report.json", "report.html"} {
		if bytes.Contains(readFixture(t, filepath.Join(base, "runs/citations", name)), marker) {
			t.Fatal("unquoted source leaked into", name)
		}
	}
}

func TestCitationSourceInventoryRejectsMissingOrCorruptBasis(t *testing.T) {
	config, base := fixture(t)
	citationsConfig(t, config)
	runOK(t, "prepare", config, "citations")
	path := filepath.Join(base, "runs/citations/sources.json")
	original := readFixture(t, path)
	for _, kind := range []string{"missing", "schema", "snapshot", "full", "url", "empty_limitation", "hash", "size", "unsafe_path", "duplicate", "dependency"} {
		t.Run(kind, func(t *testing.T) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			writeFixture(t, path, original)
			var inv sourceInventory
			if err := strictJSON(original, &inv); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "schema":
				inv.SchemaVersion = 1
			case "snapshot":
				inv.SnapshotID = strings.Repeat("0", 64)
			case "full":
				inv.Sources[0].Full = true
			case "url":
				inv.Sources[0].URL = "sources/full.html"
			case "empty_limitation":
				inv.Sources[0].Limitation = ""
			case "hash":
				inv.Sources[0].SHA256 = strings.Repeat("0", 64)
			case "size":
				inv.Sources[0].Bytes++
			case "unsafe_path":
				inv.Sources[0].Path = "../source.go"
			case "duplicate":
				inv.Sources = append(inv.Sources, inv.Sources[0])
			case "dependency":
				inv.Sources = inv.Sources[1:]
			}
			if kind != "missing" {
				writeFixture(t, path, mustJSON(t, inv))
			}
			runFail(t, "report", config, "citations")
		})
	}
}

func TestCitationLocalOfflineLatestPreservesQuestions(t *testing.T) {
	config, base := fixture(t)
	citationsConfig(t, config)
	path := filepath.Join(base, "source/rules.md")
	question := []byte("\n### Q-DEMO-001 — Неясность\n\nВопрос: Какое ограничение требуется?\nНормы: REQ-DEMO-001\n")
	writeFixture(t, path, append(readFixture(t, path), question...))
	runOK(t, "prepare", config, "local")
	if err := os.RemoveAll(filepath.Join(base, "source")); err != nil {
		t.Fatal(err)
	}
	view := runOK(t, "overview", config).(Overview)
	scope := view.Scopes[0]
	if scope.Requirements != 5 || len(scope.Questions) != 1 || scope.Questions[0].Text != "Какое ограничение требуется?" || scope.Freshness != "unavailable" || scope.Latest == nil || scope.Decided != nil || scope.Latest.SnapshotCurrent {
		t.Fatal("offline local scope lost latest recorded requirements/questions", scope)
	}
	runOK(t, "report", config, "local")
}

func TestCitationReadersPreserveOfflineScopeAndHistoricalResult(t *testing.T) {
	f := newExternalFixture(t)
	citationsConfig(t, f.config)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	before := runOK(t, "overview", config).(Overview)
	result := runOK(t, "external", "result", config, "A").(ExternalResult)
	if result.SchemaVersion != 2 || result.SourceRetention != "citations" || result.ReportBasis != "saved_run" || result.CurrentFreshness == nil || result.CurrentFreshness.Status != "unchecked" || result.CurrentFreshness.Reason == "" {
		t.Fatal("result does not distinguish historical and current freshness", result)
	}
	if result.Report.Freshness != "fresh" || result.Decision.State != "current" {
		t.Fatal("historical as-of assessment changed", result.Report.Freshness, result.Decision.State)
	}
	for _, root := range []string{cfg.ProjectRoot, f.corpus, filepath.Join(f.base, "source")} {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
	}
	after := runOK(t, "overview", config).(Overview)
	scope := after.Scopes[0]
	if scope.Requirements != before.Scopes[0].Requirements || scope.Requirements != 5 || len(scope.Questions) != 1 || scope.External == nil || scope.Decided == nil || scope.Decided.Gap != before.Scopes[0].Decided.Gap || scope.Freshness != "unavailable" || scope.Drift != nil || scope.DriftUnchecked == "" || after.Totals.Unchecked != 1 {
		t.Fatal("offline scope lost recorded facts or claims current freshness", scope)
	}
	runOK(t, "status", config, "A")
	runOK(t, "review", config, "A", "citations")
	runOK(t, "report", config, "A")
	if _, err := os.Stat(filepath.Join(cfg.ReportsDir, "A/sources")); !os.IsNotExist(err) {
		t.Fatalf("offline citation report created full assets: %v", err)
	}
	result = runOK(t, "external", "result", config, "A").(ExternalResult)
	if result.CurrentFreshness == nil || result.CurrentFreshness.Status != "unchecked" {
		t.Fatal("offline result claims current freshness")
	}
	legacy := resultFromReport(Report{}, nil)
	if legacy.SchemaVersion != 1 || legacy.SourceRetention != "" || legacy.CurrentFreshness != nil || legacy.ReportBasis != "" {
		t.Fatal("archive result schema changed", legacy)
	}
}
