//go:build darwin || linux

package main

import (
	"io/fs"
	"log/slog"
	"os"
	"sort"
)

// Overview is `overview CONFIG...` (tool-spec §34): one read-only map of several scopes — the accepted set, its
// freshness and the latest run's delivery and host verdict per scope, plus totals. It reads what is recorded and
// never renders a "corpus is complete" claim: every scope names only its own accepted set.
type Overview struct {
	Scopes []ScopeOverview `json:"scopes"`
	Totals OverviewTotals  `json:"totals"`
}

type OverviewTotals struct {
	Scopes         int            `json:"scopes"`
	Requirements   int            `json:"requirements_total"`
	Runs           int            `json:"runs"`
	Implementation map[string]int `json:"implementation"`
	Assertion      map[string]int `json:"assertion"`
}

type ScopeOverview struct {
	Config       string       `json:"config"`
	IndexMode    string       `json:"index_mode"`
	Freshness    string       `json:"freshness"`
	Error        string       `json:"error,omitempty"`
	Requirements int          `json:"requirements_total"`
	Head         string       `json:"accepted_head,omitempty"`
	Runs         int          `json:"runs"`
	Latest       *RunOverview `json:"latest"`  // the most recently prepared run: delivery progress
	Decided      *RunOverview `json:"decided"` // the most recent run with a host decision: verdict counts; totals come from here
}

type RunOverview struct {
	RunID            string         `json:"run_id"`
	PreparedAt       string         `json:"prepared_at"`
	ToolVersion      string         `json:"tool_version"`
	SnapshotCurrent  bool           `json:"snapshot_current"`
	DeliveryComplete bool           `json:"delivery_complete"`
	Expected         int            `json:"expected"`
	Submitted        int            `json:"submitted"`
	HostReviewState  string         `json:"host_review_state"`
	ReviewID         string         `json:"review_id,omitempty"`
	Form             string         `json:"form,omitempty"`
	Implementation   map[string]int `json:"implementation"`
	Assertion        map[string]int `json:"assertion"`
	Disagree         []string       `json:"disagree"`
}

func overview(paths []string) (Overview, error) {
	view := Overview{Scopes: []ScopeOverview{}, Totals: OverviewTotals{Implementation: map[string]int{}, Assertion: map[string]int{}}}
	for _, path := range paths {
		cfg, err := loadConfig(path, true)
		if err != nil {
			return Overview{}, err
		}
		scope := scopeOverview(path, cfg)
		view.Scopes = append(view.Scopes, scope)
		view.Totals.Scopes++
		view.Totals.Requirements += scope.Requirements
		view.Totals.Runs += scope.Runs
		if scope.Decided != nil {
			for k, v := range scope.Decided.Implementation {
				view.Totals.Implementation[k] += v
			}
			for k, v := range scope.Decided.Assertion {
				view.Totals.Assertion[k] += v
			}
		}
	}
	slog.Debug("overview: карта scope", "scopes", view.Totals.Scopes, "requirements", view.Totals.Requirements, "runs", view.Totals.Runs)
	return view, nil
}

// scopeOverview never fails the whole map for one scope: a stale or missing index is reported as its freshness/error.
func scopeOverview(path string, cfg Config) ScopeOverview {
	scope := ScopeOverview{Config: path, IndexMode: "declared", Freshness: "n/a"}
	if cfg.IndexMode == "accepted" {
		scope.IndexMode = "accepted"
	}
	current := ""
	m, err := snapshot(cfg)
	if err != nil {
		scope.Error = err.Error()
		scope.Freshness = "unavailable"
		if cfg.IndexMode == "accepted" {
			if ledger, state, readErr := readAccepted(cfg.ReportsDir); readErr == nil {
				if files, scanErr := scanSnapshot(cfg); scanErr == nil {
					scope.Freshness = acceptedFreshness(ledger, state, files.Files)
				}
				for _, record := range state.Records {
					if record.Status == "active" {
						scope.Requirements++
					}
				}
				scope.Head = state.Head
			}
		}
	} else {
		current, scope.Requirements = m.SnapshotID, len(m.Requirements)
		if m.Accepted != nil {
			scope.Freshness, scope.Head = "fresh", m.Accepted.Head
		}
	}
	reports, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return scope
	}
	defer reports.Close()
	dirs, err := fs.ReadDir(reports.FS(), ".")
	if err != nil {
		return scope
	}
	runs := []string{}
	for _, dir := range dirs {
		if dir.IsDir() && slugRE.MatchString(dir.Name()) {
			if _, err := fs.Stat(reports.FS(), dir.Name()+"/manifest.json"); err == nil {
				runs = append(runs, dir.Name())
			}
		}
	}
	scope.Runs = len(runs)
	newer := func(run RunOverview, than *RunOverview) bool {
		return than == nil || run.PreparedAt > than.PreparedAt || (run.PreparedAt == than.PreparedAt && run.RunID > than.RunID)
	}
	for _, runID := range runs {
		run, err := runOverview(reports, runID, current)
		if err != nil {
			slog.Debug("overview: run пропущен", "run_id", runID, "error", err.Error())
			continue
		}
		if newer(run, scope.Latest) {
			copied := run
			scope.Latest = &copied
		}
		if run.ReviewID != "" && newer(run, scope.Decided) {
			copied := run
			scope.Decided = &copied
		}
	}
	return scope
}

func runOverview(reports *os.Root, runID, current string) (RunOverview, error) {
	run, err := reports.OpenRoot(runID)
	if err != nil {
		return RunOverview{}, err
	}
	defer run.Close()
	var m Manifest
	data, err := readRoot(run, "manifest.json", maxState)
	if err != nil {
		return RunOverview{}, err
	}
	if err := strictJSON(data, &m); err != nil {
		return RunOverview{}, err
	}
	var state State
	if data, err = readRoot(run, "state.json", maxState); err != nil {
		return RunOverview{}, err
	}
	if err := strictJSON(data, &state); err != nil {
		return RunOverview{}, err
	}
	versions, err := readToolVersions(run)
	if err != nil {
		return RunOverview{}, err
	}
	view := RunOverview{RunID: runID, SnapshotCurrent: current == m.SnapshotID, Implementation: map[string]int{}, Assertion: map[string]int{}, Disagree: []string{}}
	if len(versions.Records) > 0 {
		view.PreparedAt, view.ToolVersion = versions.Records[0].RecordedAt, versions.Records[0].ToolVersion
	}
	status := makeStatus(runID, m, state, view.SnapshotCurrent)
	view.DeliveryComplete, view.Expected, view.Submitted = status.DeliveryComplete, status.Expected, status.Submitted
	journal, err := readReviews(run, runID, m)
	if err != nil {
		return RunOverview{}, err
	}
	summary := summarizeReviews(runID, m, state, view.SnapshotCurrent, journal)
	view.HostReviewState, view.Form = summary.State, summary.Form
	if summary.Latest != nil {
		view.ReviewID = summary.Latest.ReviewID
		for _, a := range summary.Latest.Assessments {
			view.Implementation[a.Implementation]++
			view.Assertion[a.Assertion]++
		}
	}
	if view.DeliveryComplete {
		for _, row := range outcomes(m, state, nil) {
			if !row.Agree {
				view.Disagree = append(view.Disagree, row.RequirementID)
			}
		}
	}
	sort.Strings(view.Disagree)
	return view, nil
}
