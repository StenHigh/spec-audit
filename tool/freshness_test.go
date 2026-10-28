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

func TestSavedSnapshotReleaseIsNotSourceChange(t *testing.T) {
	f := citationLifecycleFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	before := runOK(t, "status", config, "A").(Status)
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	reviews := readFixture(t, filepath.Join(cfg.ReportsDir, "A/host-reviews.json"))
	checks := readFixture(t, filepath.Join(cfg.ReportsDir, "A/review-checks.json"))
	savedReport := readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"))
	beforeOverview := runOK(t, "overview", config).(Overview)
	if before.Freshness != "fresh" || before.HostReviewState != "current" {
		t.Fatal("fixture did not start with a current decision", before)
	}
	runOK(t, "external", "release", config)
	after := runOK(t, "status", config, "A").(Status)
	if after.SnapshotID != before.SnapshotID || after.Freshness != "unchecked" || after.HostReviewState != "saved" || !after.HostReviewComplete {
		t.Fatal("releasing exact temporary sources invalidated the saved decision", after)
	}
	if after.SourceAvailability != "released" {
		t.Fatal("release receipt ignored", after)
	}
	result := runOK(t, "external", "result", config, "A").(ExternalResult)
	if result.SchemaVersion != 3 || result.Report.Freshness != "unchecked" || result.Decision.State != "saved" || result.CurrentFreshness.Status != "unchecked" || result.CurrentFreshness.SourceAvailability != "released" {
		t.Fatal("result did not project saved data honestly", result.CurrentFreshness)
	}
	if !bytes.Equal(savedReport, readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json"))) {
		t.Fatal("read rewrote saved report")
	}
	view := runOK(t, "review", config, "A").(ReviewContext)
	brief := runOK(t, "review", config, "A", "summary").(ReviewBrief)
	norm := runOK(t, "review", config, "A", "REQ-DEMO-001").(RequirementView)
	if !view.Complete || view.State != "saved" || brief.State != "saved" || norm.Host.State != "saved" || brief.SourceAvailability != "released" {
		t.Fatal("review variants disagree")
	}
	afterOverview := runOK(t, "overview", config).(Overview)
	decided := afterOverview.Scopes[0].Decided
	if decided.Freshness != "unchecked" || decided.SourceAvailability != "released" || decided.HostReviewState != "saved" || !decided.HostReviewComplete || afterOverview.Totals.Gap != beforeOverview.Totals.Gap || afterOverview.Totals.Unchecked != 1 {
		t.Fatal("overview changed saved findings", decided)
	}
	site := filepath.Join(f.base, "saved-site")
	runOK(t, "publish", site, config)
	for name, content := range publicationBytes(t, site) {
		if strings.HasSuffix(name, ".html") && (strings.Contains(content, "источники изменились") || strings.Contains(content, "snapshot изменился") || strings.Contains(content, "нужен новый run")) {
			t.Fatal("publication claimed change", name)
		}
	}
	archive := exportTestBundle(t, config, f.base)
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(f.base, "restored-saved")).(map[string]any)
	restoredConfig := restored["configs"].([]string)[0]
	restoredStatus := runOK(t, "status", restoredConfig, "A").(Status)
	if restoredStatus.Freshness != "unchecked" || restoredStatus.HostReviewState != "saved" || restoredStatus.SourceAvailability != "released" {
		t.Fatal("restore changed interpretation", restoredStatus)
	}
	runOK(t, "report", restoredConfig, "A")
	for name, expected := range map[string][]byte{"host-reviews.json": reviews, "review-checks.json": checks} {
		if !bytes.Equal(expected, readFixture(t, filepath.Join(cfg.ReportsDir, "A", name))) {
			t.Fatal("presentation rewrote evidence", name)
		}
	}
	runOK(t, "report", config, "A")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if !report.HostReviewComplete || report.HostReconciliationRequired || report.Requirements[0].Navigation.Basis != "host-saved" {
		t.Fatal("report invalidates saved decision")
	}
	html := string(readFixture(t, filepath.Join(cfg.ReportsDir, "A/report.html")))
	for _, claim := range []string{"источники изменились", "Snapshot изменился", "нужен новый run"} {
		if strings.Contains(html, claim) {
			t.Fatal("release was presented as a source change", claim)
		}
	}
	assertNoLifecycleMarker(t, cfg.ReportsDir)
	f.install(t)
	current := runOK(t, "status", config, "A").(Status)
	if current.Freshness != "fresh" || current.HostReviewState != "current" {
		t.Fatal("exact reimport did not restore verification", current)
	}
}

func TestSavedSnapshotUnavailableChangedAndRetry(t *testing.T) {
	for _, policy := range []string{"archive", "citations"} {
		for _, kind := range []string{"missing", "symlink", "changed", "malformed", "partial", "retry"} {
			t.Run(policy+"/"+kind, func(t *testing.T) {
				config, base := fixture(t)
				if policy == "citations" {
					citationsConfig(t, config)
				}
				runOK(t, "prepare", config, "A")
				finishCitationPrepared(t, config, "A", false)
				cfg, err := loadConfig(config)
				if err != nil {
					t.Fatal(err)
				}
				expected, availability, state, complete := "unchecked", "unavailable", "saved", true
				switch kind {
				case "missing":
					if err := os.RemoveAll(cfg.ProjectRoot); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					path := filepath.Join(cfg.ProjectRoot, "rules.md")
					outside := filepath.Join(base, "outside.md")
					writeFixture(t, outside, readFixture(t, path))
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, path); err != nil {
						t.Fatal(err)
					}
				case "changed":
					path := filepath.Join(cfg.ProjectRoot, "source.go")
					writeFixture(t, path, append(readFixture(t, path), []byte("\n// actual change\n")...))
					expected, availability, state = "stale", "available", "outdated"
				case "malformed":
					writeFixture(t, filepath.Join(cfg.ProjectRoot, "rules.md"), []byte{0xff})
					expected, availability, state = "stale", "available", "outdated"
				case "partial":
					if err := os.Remove(filepath.Join(cfg.ProjectRoot, "rules.md")); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(cfg.ProjectRoot, "source.go")
					writeFixture(t, path, append(readFixture(t, path), []byte("\n// changed while spec unavailable\n")...))
					expected, state = "stale", "outdated"
				case "retry":
					view := runOK(t, "review", config, "A").(ReviewContext)
					runOK(t, "retry", config, "A", view.Entries[0].Task.TaskID)
					if err := os.RemoveAll(cfg.ProjectRoot); err != nil {
						t.Fatal(err)
					}
					state, complete = "outdated", false
				}
				status := runOK(t, "status", config, "A").(Status)
				if status.Freshness != expected || status.SourceAvailability != availability || status.HostReviewState != state || status.HostReviewComplete != complete {
					t.Fatal("observation conflated states", status)
				}
				runOK(t, "report", config, "A")
				view := runOK(t, "review", config, "A").(ReviewContext)
				runFail(t, "retry", config, "A", view.Entries[0].Task.TaskID)
				if expected == "unchecked" {
					runFail(t, "prepare", config, "new")
				}
			})
		}
	}
}

func TestSavedSnapshotRuntimeChangeAfterRelease(t *testing.T) {
	f := citationLifecycleFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	runOK(t, "external", "release", config)
	path := filepath.Join(runtimeSourceRoot(cfg), "source.go")
	writeFixture(t, path, append(readFixture(t, path), []byte("\n// actual live runtime change\n")...))
	status := runOK(t, "status", config, "A").(Status)
	if status.Freshness != "stale" || status.HostReviewState != "outdated" || !status.HostReviewComplete || status.SourceAvailability != "released" {
		t.Fatal("runtime change hidden by released copies", status)
	}
	if !strings.Contains(status.FreshnessReason, "runtime") {
		t.Fatal("change reason only describes missing temporary copies", status.FreshnessReason)
	}
	runFail(t, "external", "import", f.config, f.input)
}

func TestSavedSnapshotExecutionProjection(t *testing.T) {
	config, base, _ := reviewedFixture(t, true)
	runOK(t, "test", config, "review", "observed")
	decision := reviewInput(t, config)
	runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
	runOK(t, "report", config, "review")
	path := filepath.Join(base, "runs/review/report.json")
	var stored Report
	if err := strictJSON(readFixture(t, path), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Executions) != 1 || stored.Executions[0].State != "passed" {
		t.Fatal("fixture did not execute tests", stored.Executions)
	}
	source := filepath.Join(base, "source/source.go")
	original := readFixture(t, source)
	writeFixture(t, source, append(append([]byte{}, original...), []byte("\n// changed\n")...))
	projected, err := projectSavedReport(config, "review", stored)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Freshness != "stale" || projected.Executions[0].State != "stale" {
		t.Fatal("projected receipts disagree with freshness")
	}
	for _, row := range projected.Requirements {
		for _, role := range row.Roles {
			for _, execution := range role.Executions {
				if execution.State != "stale" {
					t.Fatal("role receipt was not projected", execution)
				}
			}
		}
	}
	// A legacy report may have persisted stale execution presentation while
	// original state receipts remained passed. Restore the exact source bytes.
	writeFixture(t, source, original)
	writeFixture(t, path, mustJSON(t, projected))
	recovered, err := projectSavedReport(config, "review", projected)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Freshness != "fresh" || recovered.Executions[0].State != "passed" {
		t.Fatal("stale receipt presentation became evidence")
	}
	if !bytes.Equal(mustJSON(t, projected), readFixture(t, path)) {
		t.Fatal("projection rewrote saved report")
	}
	if err := os.Rename(filepath.Join(base, "source"), filepath.Join(base, "offline-source")); err != nil {
		t.Fatal(err)
	}
	offline, err := projectSavedReport(config, "review", projected)
	if err != nil {
		t.Fatal(err)
	}
	if offline.Freshness != "unchecked" || offline.Executions[0].State != "passed" || offline.HostReviewState != "saved" {
		t.Fatal("unavailable source invalidated saved execution")
	}
}

func TestSavedSnapshotLegacyHistoricalPublication(t *testing.T) {
	f := citationLifecycleFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	cfg, _ := loadConfig(config, true)
	path := filepath.Join(cfg.ReportsDir, "A/report.json")
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(readFixture(t, path), &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "source_availability")
	delete(legacy, "freshness_reason")
	writeFixture(t, path, mustJSON(t, legacy))
	originalJSON := readFixture(t, path)
	completeExternal(t, config, "B")
	runOK(t, "external", "release", config)
	check := func(site, run string, historical bool) {
		dir := filepath.Join(site, externalScopePath(cfg.External), run)
		var result SiteResult
		if err := strictJSON(readFixture(t, filepath.Join(dir, "result.json")), &result); err != nil {
			t.Fatal(err)
		}
		reportRaw := readFixture(t, filepath.Join(dir, "report.json"))
		if err := validateSiteResult(result, reportRaw); err != nil {
			t.Fatal(err)
		}
		if result.CurrentFreshness.Status != "unchecked" {
			t.Fatal("offline result mislabeled")
		}
		if historical && result.ReportBasis != "historical_publication" || !historical && result.ReportBasis != "saved_run" {
			t.Fatal("wrong result basis")
		}
	}
	site := filepath.Join(f.base, "legacy-latest")
	runOK(t, "publish", site, config)
	check(site, "B", false)
	history := filepath.Join(f.base, "legacy-history")
	runOK(t, "publish", history, "--run", "A", config)
	check(history, "A", true)
	if !bytes.Equal(originalJSON, readFixture(t, path)) {
		t.Fatal("saved legacy report changed")
	}
	archive := exportAuditTestBundle(t, []string{config}, f.base)
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(f.base, "restored-legacy")).(map[string]any)
	rc := restored["configs"].([]string)[0]
	out := filepath.Join(f.base, "restored-legacy-site")
	if requested := os.Getenv("SPEC_AUDIT_FRESHNESS_LEGACY_SITE"); requested != "" {
		out = requested
	}
	runOK(t, "publish", out, rc)
	check(out, "B", false)
	old := filepath.Join(f.base, "restored-history")
	runOK(t, "publish", old, "--run", "A", rc)
	check(old, "A", true)
}
