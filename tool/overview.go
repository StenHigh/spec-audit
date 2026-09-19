//go:build darwin || linux

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
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
	Gap            Gap            `json:"gap"`
	Closed         int            `json:"closed"` // sums of the scope deltas (§47)
	Opened         int            `json:"opened"`
	Worsened       int            `json:"worsened"` // §53.1
}

// Gap is the industry reading of a gap analysis over the host's verdicts (tool-spec §45.1): a norm is a gap when it is
// not demonstrably satisfied. Each norm is counted once, in the most severe class it falls into:
// implementation — contradicted or unknown code; verification — tests missing, contradicting or weak for a supported
// norm; specification — an ambiguous norm that is otherwise supported and relevantly tested.
type Gap struct {
	Total          int `json:"total"`
	Implementation int `json:"implementation"`
	Verification   int `json:"verification"`
	Specification  int `json:"specification"`
}

func (g *Gap) add(a Assessment) {
	switch {
	case a.Implementation == "contradicted" || a.Implementation == "unknown":
		g.Implementation++
	case a.Assertion != "relevant":
		g.Verification++
	case a.Specification == "ambiguous":
		g.Specification++
	default:
		return
	}
	g.Total++
}

type ScopeOverview struct {
	Config       string `json:"config"`
	IndexMode    string `json:"index_mode"`
	Freshness    string `json:"freshness"`
	Error        string `json:"error,omitempty"`
	Requirements int    `json:"requirements_total"`
	Head         string `json:"accepted_head,omitempty"`
	Runs         int    `json:"runs"`
	// History lists every decided run of the scope, newest first (tool-spec §49): the time selector of the published site.
	History []RunRef     `json:"history"`
	Latest  *RunOverview `json:"latest"`  // the most recently prepared run: delivery progress
	Decided *RunOverview `json:"decided"` // the most recent run with a host decision: verdict counts; totals come from here
	Delta   *Delta       `json:"delta"`   // decided vs the decided run before it (§47); nil with fewer than two
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

type RunRef struct {
	RunID           string `json:"run_id"`
	PreparedAt      string `json:"prepared_at"`
	ReviewID        string `json:"review_id"`
	HostReviewState string `json:"host_review_state"`
	Gap             int    `json:"gap"`
	Report          string `json:"report,omitempty"`
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
	Attention    []NormBrief `json:"attention"`
	Gap          Gap         `json:"gap"`
	Carried      int         `json:"carried"` // norms carried from an earlier run without reassessment (§50)
	KnownDefects int         `json:"known_defects"`
	SinceRun     string      `json:"since_run,omitempty"`
	carriedIDs   map[string]bool
	verdicts     map[string]NormBrief // id → host verdict, for the scope delta (§47)
	keys         map[string]string    // id → content_hash|revision, so only the same norm is compared
}

// Delta is the change between the two latest decided runs of a scope (tool-spec §47): what the product fix closed,
// what it opened, what moved inside the gap. Norms are compared only when their content and revision are identical.
type Delta struct {
	BaselineRun  string       `json:"baseline_run"`
	Pinned       bool         `json:"pinned"`   // baseline named by CONFIG baseline_run rather than «the run before»
	Closed       []NormChange `json:"closed"`   // was a gap in the baseline, is not one now
	Opened       []NormChange `json:"opened"`   // was not a gap, is one now
	Changed      []NormChange `json:"changed"`  // a gap in both, but the states differ
	Worsened     []NormChange `json:"worsened"` // subset of changed where implementation or assertion got worse (§53.1)
	Unchanged    int          `json:"unchanged"`
	Carried      int          `json:"carried"`      // of the unchanged, how many were carried without reassessment (§52.2)
	Incomparable []string     `json:"incomparable"` // new, retired or revised norms — no like-for-like comparison
	GapBefore    Gap          `json:"gap_before"`
	GapAfter     Gap          `json:"gap_after"`
}

type NormChange struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	From  string `json:"from"` // specification/implementation/assertion in the baseline
	To    string `json:"to"`
}

// Severity orders for §53.1: a move to the right is a worsening even when the norm was already a gap.
var (
	implementationOrder = []string{"supported", "unknown", "contradicted"}
	assertionOrder      = []string{"relevant", "weak", "unknown", "missing", "contradicts"}
)

func severity(state string, order []string) int {
	for i, s := range order {
		if s == state {
			return i
		}
	}
	return len(order)
}

func states(n NormBrief) string { return n.Specification + "/" + n.Implementation + "/" + n.Assertion }

func isGap(n NormBrief) bool {
	return n.Implementation != "supported" || n.Assertion != "relevant" || n.Specification != "clear"
}

func scopeDelta(baseline, current RunOverview) *Delta {
	d := &Delta{BaselineRun: baseline.RunID, Closed: []NormChange{}, Opened: []NormChange{}, Changed: []NormChange{}, Worsened: []NormChange{}, Incomparable: []string{}, GapBefore: baseline.Gap, GapAfter: current.Gap}
	seen := map[string]bool{}
	for id, now := range current.verdicts {
		seen[id] = true
		was, ok := baseline.verdicts[id]
		if !ok || baseline.keys[id] != current.keys[id] {
			d.Incomparable = append(d.Incomparable, id)
			continue
		}
		change := NormChange{id, now.Title, states(was), states(now)}
		switch {
		case isGap(was) && !isGap(now):
			d.Closed = append(d.Closed, change)
		case !isGap(was) && isGap(now):
			d.Opened = append(d.Opened, change)
		case states(was) != states(now):
			d.Changed = append(d.Changed, change)
			if severity(now.Implementation, implementationOrder) > severity(was.Implementation, implementationOrder) || severity(now.Assertion, assertionOrder) > severity(was.Assertion, assertionOrder) {
				d.Worsened = append(d.Worsened, change)
			}
		default:
			d.Unchanged++
			if current.carriedIDs[id] {
				d.Carried++
			}
		}
	}
	for id := range baseline.verdicts {
		if !seen[id] {
			d.Incomparable = append(d.Incomparable, id)
		}
	}
	for _, list := range []*[]NormChange{&d.Closed, &d.Opened, &d.Changed, &d.Worsened} {
		sort.Slice(*list, func(i, j int) bool { return (*list)[i].ID < (*list)[j].ID })
	}
	sort.Strings(d.Incomparable)
	return d
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
			if scope.Delta != nil {
				view.Totals.Closed += len(scope.Delta.Closed)
				view.Totals.Opened += len(scope.Delta.Opened)
				view.Totals.Worsened += len(scope.Delta.Worsened)
			}
			g := scope.Decided.Gap
			view.Totals.Gap.Total += g.Total
			view.Totals.Gap.Implementation += g.Implementation
			view.Totals.Gap.Verification += g.Verification
			view.Totals.Gap.Specification += g.Specification
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
	scope := ScopeOverview{Config: path, IndexMode: "declared", Freshness: "n/a", History: []RunRef{}}
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
	decidedRuns := []RunOverview{}
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
		if run.ReviewID != "" {
			decidedRuns = append(decidedRuns, run)
			if newer(run, scope.Decided) {
				copied := run
				scope.Decided, decidedCode = &copied, code
			}
		}
	}
	sort.Slice(decidedRuns, func(i, j int) bool { return newer(decidedRuns[j], &decidedRuns[i]) })
	for i := len(decidedRuns) - 1; i >= 0; i-- {
		r := decidedRuns[i]
		scope.History = append(scope.History, RunRef{r.RunID, r.PreparedAt, r.ReviewID, r.HostReviewState, r.Gap.Total, ""})
	}
	if n := len(decidedRuns); n >= 2 {
		baseline := decidedRuns[n-2]
		if cfg.BaselineRun != "" {
			for _, run := range decidedRuns[:n-1] {
				if run.RunID == cfg.BaselineRun {
					baseline = run
				}
			}
			if baseline.RunID != cfg.BaselineRun {
				slog.Warn("baseline_run не найден среди решённых run; сравнение с предыдущим", "config", path, "baseline_run", cfg.BaselineRun)
			}
		}
		scope.Delta = scopeDelta(baseline, decidedRuns[n-1])
		scope.Delta.Pinned = cfg.BaselineRun != "" && baseline.RunID == cfg.BaselineRun
		slog.Debug("overview: динамика scope", "config", path, "baseline", scope.Delta.BaselineRun, "closed", len(scope.Delta.Closed), "opened", len(scope.Delta.Opened))
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
	view := RunOverview{RunID: runID, SnapshotCurrent: current == m.SnapshotID, Implementation: map[string]int{}, Assertion: map[string]int{}, Contradicted: []string{}, Disagree: []string{}, Attention: []NormBrief{}, verdicts: map[string]NormBrief{}, keys: map[string]string{}}
	titles := map[string]string{}
	for _, req := range m.Requirements {
		titles[req.ID] = req.Title
		view.keys[req.ID] = sameNorm(req)
	}
	if len(versions.Records) > 0 {
		view.PreparedAt, view.ToolVersion = versions.Records[0].RecordedAt, versions.Records[0].ToolVersion
	}
	if m.Incremental != nil {
		view.Carried, view.SinceRun, view.carriedIDs = len(m.Incremental.Carried), m.Incremental.SinceRun, map[string]bool{}
		for _, c := range m.Incremental.Carried {
			view.carriedIDs[c.RequirementID] = true
			if c.KnownDefect {
				view.KnownDefects++
			}
		}
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
			brief := NormBrief{a.RequirementID, titles[a.RequirementID], a.Specification, a.Implementation, a.Assertion, a.Statement}
			view.verdicts[a.RequirementID] = brief
			if isGap(brief) {
				view.Attention = append(view.Attention, brief)
			}
			view.Gap.add(a)
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
		for j := range view.Scopes[i].History {
			view.Scopes[i].History[j].Report = reportLink(absOut, view.Scopes[i].Config, view.Scopes[i].History[j].RunID)
		}
	}
	html, err := renderCorpus(view)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(absOut, html, 0600); err != nil {
		return nil, err
	}
	slog.Info("карта корпуса записана", "path", absOut, "scopes", view.Totals.Scopes)
	return map[string]any{"html": absOut, "overview": view}, nil
}

func renderCorpus(view Overview) ([]byte, error) {
	page := CorpusPage{Overview: view, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Files: corpusFiles(view.ContradictedCode)}
	var html bytes.Buffer
	if err := corpusTemplate.Execute(&html, page); err != nil {
		return nil, err
	}
	return html.Bytes(), nil
}

// publish assembles a self-contained static site for the customer (tool-spec §49): OUT_DIR/index.html — the corpus map,
// OUT_DIR/overview.json — its data, OUT_DIR/<scope>/<run>/report.html — a copy of every decided run's report. Every
// file is static and script-free except the run reports' own inline navigation; the tree can be served from any host.
// The site is written whole on each publish; files of runs that no longer exist stay until the host removes OUT_DIR.
func publish(out string, paths []string) (any, error) {
	view, err := overview(paths)
	if err != nil {
		return nil, err
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absOut, 0755); err != nil {
		return nil, err
	}
	copied := 0
	for i := range view.Scopes {
		scope := &view.Scopes[i]
		cfg, err := loadConfig(scope.Config, true)
		if err != nil {
			return nil, err
		}
		name := scopeName(scope.Config)
		for j := range scope.History {
			runID := scope.History[j].RunID
			source := filepath.Join(cfg.ReportsDir, runID, "report.html")
			data, err := os.ReadFile(source)
			if err != nil {
				slog.Debug("publish: report.html отсутствует", "config", scope.Config, "run_id", runID)
				continue
			}
			target := filepath.Join(absOut, name, runID)
			if err := os.MkdirAll(target, 0755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(target, "report.html"), data, 0644); err != nil {
				return nil, err
			}
			copied++
			link := filepath.ToSlash(filepath.Join(name, runID, "report.html"))
			scope.History[j].Report = link
			if scope.Decided != nil && scope.Decided.RunID == runID {
				scope.Decided.Report = link
			}
		}
	}
	html, err := renderCorpus(view)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(absOut, "index.html"), html, 0644); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(absOut, "overview.json"), append(data, '\n'), 0644); err != nil {
		return nil, err
	}
	slog.Info("сайт опубликован", "dir", absOut, "scopes", view.Totals.Scopes, "reports", copied)
	return map[string]any{"dir": absOut, "index": filepath.Join(absOut, "index.html"), "reports": copied, "scopes": view.Totals.Scopes}, nil
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
