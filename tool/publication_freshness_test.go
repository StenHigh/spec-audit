//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublishRestoredSourcesRefreshesExecutionObservation(t *testing.T) {
	config, base, _ := reviewedFixture(t, true)
	receipt := runOK(t, "test", config, "review", "observed").(Receipt)
	if receipt.State != "passed" {
		t.Fatal("synthetic runtime did not pass", receipt)
	}
	runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
	runOK(t, "report", config, "review")
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(cfg.ProjectRoot, "source.go")
	original := readFixture(t, source)
	writeFixture(t, source, append(append([]byte{}, original...), []byte("\n// temporary change\n")...))
	runOK(t, "report", config, "review")
	runDir := filepath.Join(cfg.ReportsDir, "review")
	var saved Report
	if err := strictJSON(readFixture(t, filepath.Join(runDir, "report.json")), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Freshness != "stale" || saved.Executions[0].State != "stale" || !strings.Contains(saved.Conclusion, "STALE") {
		t.Fatal("fixture did not persist the stale presentation")
	}
	before := publicationBytes(t, runDir)
	mode := mustStat(t, filepath.Join(runDir, "report.json")).Mode().Perm()
	check := func(name, freshness, hostState string, args ...string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(base, name)
			view := publicationView(t, out, args...)
			decided := view.Scopes[0].Decided
			if decided == nil || decided.RunID != "review" || decided.Freshness != freshness || decided.HostReviewState != hostState {
				t.Fatal("wrong selected observation", decided)
			}
			path := filepath.Join(out, filepath.FromSlash(decided.Report))
			var report Report
			if err := strictJSON(readFixture(t, filepath.Join(filepath.Dir(path), "report.json")), &report); err != nil {
				t.Fatal(err)
			}
			if report.Freshness != freshness || report.HostReviewState != hostState || !report.HostReviewComplete {
				t.Fatal("report and overview disagree")
			}
			want := receipt
			if freshness == "stale" {
				want.State = "stale"
			}
			if !reflect.DeepEqual(report.Executions, []Receipt{want}) {
				t.Errorf("publication used cached receipts: got %+v want %+v", report.Executions, want)
			}
			html := string(readFixture(t, path))
			if freshness == "fresh" {
				if !strings.HasPrefix(report.Conclusion, "Хост согласовал") || strings.Contains(html, "нужен новый run") || strings.Contains(html, "STALE:") {
					t.Error("fresh publication retained a stale conclusion", report.Conclusion)
				}
			} else if freshness == "unchecked" && report.Conclusion != savedSnapshotConclusion {
				t.Error("unchecked publication lost the saved-snapshot conclusion", report.Conclusion)
			}
			if !strings.Contains(html, "observed — "+want.State) {
				t.Error("HTML execution differs from the saved receipt", want.State)
			}
			linked, hostLinked := 0, 0
			checkLinks := func(executions []TestExecution) {
				for _, execution := range executions {
					if execution.ReceiptID == receipt.ID {
						linked++
						if execution.State != want.State {
							t.Error("linked execution used cached state", execution)
						}
					}
				}
			}
			for _, row := range report.Requirements {
				for _, role := range row.Roles {
					checkLinks(role.Executions)
				}
				checkLinks(row.Navigation.HostExecutions)
				hostLinked += len(row.Navigation.HostExecutions)
			}
			if linked == 0 || hostLinked == 0 {
				t.Fatal("fixture has no linked role/host executions")
			}
			if !reflect.DeepEqual(before, publicationBytes(t, runDir)) || mustStat(t, filepath.Join(runDir, "report.json")).Mode().Perm() != mode {
				t.Fatal("publication rewrote saved report/state/raw")
			}
		})
	}
	check("still-stale", "stale", "outdated", config)
	writeFixture(t, source, original)
	check("fresh-again", "fresh", "current", config)
	check("historical", "unchecked", "saved", "--run", "review", config)
	if err := os.Rename(cfg.ProjectRoot, filepath.Join(base, "offline-source")); err != nil {
		t.Fatal(err)
	}
	check("offline", "unchecked", "saved", config)
	check("offline-historical", "unchecked", "saved", "--run", "review", config)
}

func TestPublishPreservesOriginalExecutionStates(t *testing.T) {
	config, base, batch := reviewedFixture(t, false)
	runDir := filepath.Join(base, "runs", "review")
	var state State
	if err := strictJSON(readFixture(t, filepath.Join(runDir, "state.json")), &state); err != nil {
		t.Fatal(err)
	}
	// Synthetic reader fixtures are evidence of state preservation, not runtime runs.
	state.Executions = []Receipt{}
	for _, status := range []string{"passed", "failed", "incomplete", "no_tests", "stale", "foreign-snapshot"} {
		receipt := Receipt{ID: status, SnapshotID: batch.SnapshotID, State: status, Tests: []ExecutedTest{}, Limitations: []string{"synthetic reader fixture"}}
		if status == "foreign-snapshot" {
			receipt.State, receipt.SnapshotID = "passed", digest([]byte("another snapshot"))
		}
		state.Executions = append(state.Executions, receipt)
	}
	writeFixture(t, filepath.Join(runDir, "state.json"), mustJSON(t, state))
	runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
	runOK(t, "report", config, "review")
	var cached Report
	if err := strictJSON(readFixture(t, filepath.Join(runDir, "report.json")), &cached); err != nil {
		t.Fatal(err)
	}
	for i := range cached.Executions {
		cached.Executions[i].State = "passed"
	}
	writeFixture(t, filepath.Join(runDir, "report.json"), mustJSON(t, cached))
	before := publicationBytes(t, runDir)
	for _, selection := range []string{"latest", "historical", "offline"} {
		t.Run(selection, func(t *testing.T) {
			args := []string{config}
			if selection == "historical" {
				args = []string{"--run", "review", config}
			}
			if selection == "offline" {
				if err := os.Rename(filepath.Join(base, "source"), filepath.Join(base, "offline-source")); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(base, selection)
			view := publicationView(t, out, args...)
			var report Report
			path := filepath.Join(out, filepath.Dir(view.Scopes[0].Decided.Report), "report.json")
			if err := strictJSON(readFixture(t, path), &report); err != nil {
				t.Fatal(err)
			}
			want := append([]Receipt{}, state.Executions...)
			want[len(want)-1].State = "stale"
			if !reflect.DeepEqual(report.Executions, want) {
				t.Error("cached presentation replaced original execution states", report.Executions)
			}
			if !reflect.DeepEqual(before, publicationBytes(t, runDir)) {
				t.Fatal("publication changed original execution evidence")
			}
		})
	}
}
