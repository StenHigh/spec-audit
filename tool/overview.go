//go:build darwin || linux

package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Overview is `overview CONFIG...` (tool-spec §34): one read-only map of several scopes — the accepted set, its
// freshness and the latest run's delivery and host verdict per scope, plus totals. It reads what is recorded and
// never renders a "corpus is complete" claim: every scope names only its own accepted set.
type Overview struct {
	Scopes []ScopeOverview `json:"scopes"`
	Totals OverviewTotals  `json:"totals"`
	// ContradictedCode is the cross-scope memory (tool-spec §42.2): every code line the host cited under a contradicted
	// verdict in any scope's decided run, so a host in a new scope sees «this file:lines is already contradicted in
	// scope X» instead of deciding the same defect an eighth time. Hints, not verdicts.
	ContradictedCode []ContradictedCitation `json:"contradicted_code"`
}

type ContradictedCitation struct {
	Path          string `json:"path"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	Config        string `json:"config"`
	RunID         string `json:"run_id"`
	RequirementID string `json:"requirement_id"`
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

// NormBrief is one norm of the decided run for the corpus page (tool-spec §45): the host's verdict in a sentence.
type NormBrief struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Specification  string `json:"specification"`
	Implementation string `json:"implementation"`
	Assertion      string `json:"assertion"`
	Statement      string `json:"statement"`
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
	Report           string         `json:"report,omitempty"` // relative link to report.html, filled by corpus (§45)
	Form             string         `json:"form,omitempty"`
	Implementation   map[string]int `json:"implementation"`
	Assertion        map[string]int `json:"assertion"`
	Contradicted     []string       `json:"contradicted"` // norms the host found contradicted — the GAP list of the scope
	Disagree         []string       `json:"disagree"`
	// Attention lists the host verdicts that are not supported+relevant (contradicted, unknown, ambiguous, weak/missing/
	// contradicts) with title and statement — what the corpus page shows per scope (§45).
	Attention []NormBrief `json:"attention"`
}

func overview(paths []string) (Overview, error) {
	view := Overview{Scopes: []ScopeOverview{}, Totals: OverviewTotals{Implementation: map[string]int{}, Assertion: map[string]int{}}, ContradictedCode: []ContradictedCitation{}}
	for _, path := range paths {
		cfg, err := loadConfig(path, true)
		if err != nil {
			return Overview{}, err
		}
		scope, cited := scopeOverview(path, cfg)
		view.Scopes = append(view.Scopes, scope)
		view.ContradictedCode = append(view.ContradictedCode, cited...)
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
	sort.Slice(view.ContradictedCode, func(i, j int) bool {
		a, b := view.ContradictedCode[i], view.ContradictedCode[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.LineStart != b.LineStart {
			return a.LineStart < b.LineStart
		}
		if a.Config != b.Config {
			return a.Config < b.Config
		}
		return a.RequirementID < b.RequirementID
	})
	slog.Debug("overview: карта scope", "scopes", view.Totals.Scopes, "requirements", view.Totals.Requirements, "runs", view.Totals.Runs, "contradicted_code", len(view.ContradictedCode))
	return view, nil
}

// scopeOverview never fails the whole map for one scope: a stale or missing index is reported as its freshness/error.
func scopeOverview(path string, cfg Config) (ScopeOverview, []ContradictedCitation) {
	scope := ScopeOverview{Config: path, IndexMode: "declared", Freshness: "n/a"}
	cited := []ContradictedCitation{}
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
		return scope, cited
	}
	defer reports.Close()
	dirs, err := fs.ReadDir(reports.FS(), ".")
	if err != nil {
		return scope, cited
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
	var decidedCode []ContradictedCitation
	for _, runID := range runs {
		run, code, err := runOverview(reports, runID, current)
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
			scope.Decided, decidedCode = &copied, code
		}
	}
	for _, c := range decidedCode {
		c.Config = path
		cited = append(cited, c)
	}
	return scope, cited
}

func runOverview(reports *os.Root, runID, current string) (RunOverview, []ContradictedCitation, error) {
	run, err := reports.OpenRoot(runID)
	if err != nil {
		return RunOverview{}, nil, err
	}
	defer run.Close()
	var m Manifest
	data, err := readRoot(run, "manifest.json", maxState)
	if err != nil {
		return RunOverview{}, nil, err
	}
	if err := strictJSON(data, &m); err != nil {
		return RunOverview{}, nil, err
	}
	var state State
	if data, err = readRoot(run, "state.json", maxState); err != nil {
		return RunOverview{}, nil, err
	}
	if err := strictJSON(data, &state); err != nil {
		return RunOverview{}, nil, err
	}
	versions, err := readToolVersions(run)
	if err != nil {
		return RunOverview{}, nil, err
	}
	view := RunOverview{RunID: runID, SnapshotCurrent: current == m.SnapshotID, Implementation: map[string]int{}, Assertion: map[string]int{}, Contradicted: []string{}, Disagree: []string{}, Attention: []NormBrief{}}
	titles := map[string]string{}
	for _, req := range m.Requirements {
		titles[req.ID] = req.Title
	}
	if len(versions.Records) > 0 {
		view.PreparedAt, view.ToolVersion = versions.Records[0].RecordedAt, versions.Records[0].ToolVersion
	}
	status := makeStatus(runID, m, state, view.SnapshotCurrent)
	view.DeliveryComplete, view.Expected, view.Submitted = status.DeliveryComplete, status.Expected, status.Submitted
	journal, err := readReviews(run, runID, m)
	if err != nil {
		return RunOverview{}, nil, err
	}
	summary := summarizeReviews(runID, m, state, view.SnapshotCurrent, journal)
	view.HostReviewState, view.Form = summary.State, summary.Form
	code := []ContradictedCitation{}
	if summary.Latest != nil {
		view.ReviewID = summary.Latest.ReviewID
		// tool-spec §44.1: only the lines that back the contradiction — the roles that themselves found the norm
		// contradicted plus the host's own citations — not a supporting role's context lines.
		own := map[string][]Citation{}
		if n := len(journal.Records); n > 0 {
			if record, err := validateReview([]byte(journal.Records[n-1]), runID, m, nil, false); err == nil {
				for _, a := range record.Assessments {
					own[a.RequirementID] = a.Code
				}
			}
		}
		for _, a := range summary.Latest.Assessments {
			view.Implementation[a.Implementation]++
			view.Assertion[a.Assertion]++
			if a.Implementation != "supported" || a.Assertion != "relevant" || a.Specification != "clear" {
				view.Attention = append(view.Attention, NormBrief{a.RequirementID, titles[a.RequirementID], a.Specification, a.Implementation, a.Assertion, a.Statement})
			}
			if a.Implementation != "contradicted" {
				continue
			}
			view.Contradicted = append(view.Contradicted, a.RequirementID)
			seen := map[string]bool{}
			add := func(c Citation) {
				key := fmt.Sprintf("%s:%d-%d", c.Path, c.LineStart, c.LineEnd)
				if !seen[key] {
					seen[key] = true
					code = append(code, ContradictedCitation{Path: c.Path, LineStart: c.LineStart, LineEnd: c.LineEnd, RunID: runID, RequirementID: a.RequirementID})
				}
			}
			for _, entry := range state.Entries {
				if entry.Result == nil {
					continue
				}
				for _, ra := range entry.Result.Assessments {
					if ra.RequirementID == a.RequirementID && ra.Implementation == "contradicted" {
						for _, c := range ra.Code {
							add(c)
						}
					}
				}
			}
			for _, c := range own[a.RequirementID] {
				add(c)
			}
		}
	}
	if view.DeliveryComplete {
		for _, row := range outcomes(m, state, nil, nil) {
			if !row.Agree {
				view.Disagree = append(view.Disagree, row.RequirementID)
			}
		}
	}
	sort.Strings(view.Disagree)
	return view, code, nil
}

//go:embed corpus.html
var corpusHTML string

var corpusTemplate = template.Must(template.New("corpus").Funcs(template.FuncMap{
	"scopeName": scopeName,
	"join":      strings.Join,
}).Parse(corpusHTML))

// scopeName is the scope directory of a CONFIG path — what the pilot calls the scope.
func scopeName(config string) string {
	return filepath.Base(filepath.Dir(config))
}

// CorpusPage is the data of `corpus OUT_HTML CONFIG...` (tool-spec §45): the overview plus per-file counts and
// relative links to each decided run's report.html.
type CorpusPage struct {
	Overview
	GeneratedAt string
	Files       []CorpusFile
}

type CorpusFile struct {
	Path   string
	Norms  int
	Scopes []string
}

// corpus writes the static corpus page and answers the overview with the page path. OUT_HTML is the host's file:
// it is written whole, never inside a run directory.
func corpus(out string, paths []string) (any, error) {
	view, err := overview(paths)
	if err != nil {
		return nil, err
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	// Links are computed from the canonical directory (reports_dir is canonical too), so /var vs /private/var never leaks in.
	if dir, err := canonicalPath(filepath.Dir(absOut)); err == nil {
		absOut = filepath.Join(dir, filepath.Base(absOut))
	}
	for i := range view.Scopes {
		if d := view.Scopes[i].Decided; d != nil {
			d.Report = reportLink(absOut, view.Scopes[i].Config, d.RunID)
		}
	}
	page := CorpusPage{Overview: view, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Files: corpusFiles(view.ContradictedCode)}
	var html bytes.Buffer
	if err := corpusTemplate.Execute(&html, page); err != nil {
		return nil, err
	}
	if err := os.WriteFile(absOut, html.Bytes(), 0600); err != nil {
		return nil, err
	}
	slog.Info("карта корпуса записана", "path", absOut, "scopes", view.Totals.Scopes, "files", len(page.Files))
	return map[string]any{"html": absOut, "overview": view}, nil
}

// reportLink is the relative path from the corpus page to a decided run's report.html; empty when it does not exist.
func reportLink(absOut, config, runID string) string {
	cfg, err := loadConfig(config, true)
	if err != nil {
		return ""
	}
	target := filepath.Join(cfg.ReportsDir, runID, "report.html")
	if _, err := os.Stat(target); err != nil {
		return ""
	}
	rel, err := filepath.Rel(filepath.Dir(absOut), target)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func corpusFiles(code []ContradictedCitation) []CorpusFile {
	norms := map[string]map[string]bool{}
	scopes := map[string]map[string]bool{}
	for _, c := range code {
		if norms[c.Path] == nil {
			norms[c.Path], scopes[c.Path] = map[string]bool{}, map[string]bool{}
		}
		norms[c.Path][c.Config+"|"+c.RequirementID] = true
		scopes[c.Path][scopeName(c.Config)] = true
	}
	files := []CorpusFile{}
	for path, ids := range norms {
		names := []string{}
		for name := range scopes[path] {
			names = append(names, name)
		}
		sort.Strings(names)
		files = append(files, CorpusFile{path, len(ids), names})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Norms != files[j].Norms {
			return files[i].Norms > files[j].Norms
		}
		return files[i].Path < files[j].Path
	})
	return files
}
