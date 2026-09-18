//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type ReviewDecision struct {
	Version     int          `json:"version"`
	ReviewID    string       `json:"review_id"`
	RunID       string       `json:"run_id"`
	SnapshotID  string       `json:"snapshot_id"`
	BasisSHA256 string       `json:"basis_sha256"`
	Reviewer    string       `json:"reviewer"`
	Summary     string       `json:"summary"`
	Assessments []Assessment `json:"assessments"`
	Limitations []string     `json:"limitations"`
}
type reviewJournal struct {
	Version int      `json:"version"`
	Records []string `json:"records"`
}
type ReviewHistory struct {
	ReviewID    string `json:"review_id"`
	Reviewer    string `json:"reviewer"`
	RawSHA256   string `json:"raw_sha256"`
	BasisSHA256 string `json:"basis_sha256"`
	Summary     string `json:"summary"`
}
type ReviewSummary struct {
	State       string          `json:"state"`
	BasisSHA256 string          `json:"basis_sha256"`
	History     []ReviewHistory `json:"history"`
	Latest      *ReviewDecision `json:"latest"`
	// Form and Concur describe the latest decision (tool-spec §23): "assessments" for version 1, "verdicts" for version 2.
	Form   string            `json:"form,omitempty"`
	Concur map[string]string `json:"concur,omitempty"`
	Own    map[string]bool   `json:"own_citations,omitempty"`
}

// ReviewVerdict is one norm of a version 2 decision: the host's states without citations (REQ-SA-044).
type ReviewVerdict struct {
	RequirementID  string   `json:"requirement_id"`
	Specification  string   `json:"specification"`
	Implementation string   `json:"implementation"`
	Assertion      string   `json:"assertion"`
	Concur         string   `json:"concur"`
	Statement      string   `json:"statement"`
	Limitations    []string `json:"limitations"`
}

// ReviewCounts is recomputed by the binary from the verdicts; any mismatch refuses the decision.
type ReviewCounts struct {
	Supported             int `json:"supported"`
	Contradicted          int `json:"contradicted"`
	ImplementationUnknown int `json:"implementation_unknown"`
	Relevant              int `json:"relevant"`
	Weak                  int `json:"weak"`
	Contradicts           int `json:"contradicts"`
	Missing               int `json:"missing"`
	AssertionUnknown      int `json:"assertion_unknown"`
	Ambiguous             int `json:"ambiguous"`
}

// ReviewVerdictV3 is a version 2 verdict plus the host's own citations (tool-spec §25, REQ-SA-046); empty arrays are allowed.
type ReviewVerdictV3 struct {
	RequirementID  string         `json:"requirement_id"`
	Specification  string         `json:"specification"`
	Implementation string         `json:"implementation"`
	Assertion      string         `json:"assertion"`
	Concur         string         `json:"concur"`
	Statement      string         `json:"statement"`
	Limitations    []string       `json:"limitations"`
	Spec           []Citation     `json:"spec"`
	Code           []Citation     `json:"code"`
	Tests          []TestCitation `json:"tests"`
}

func (verdict ReviewVerdictV3) base() ReviewVerdict {
	return ReviewVerdict{verdict.RequirementID, verdict.Specification, verdict.Implementation, verdict.Assertion, verdict.Concur, verdict.Statement, verdict.Limitations}
}

type ReviewDecisionV3 struct {
	Version     int               `json:"version"`
	ReviewID    string            `json:"review_id"`
	RunID       string            `json:"run_id"`
	SnapshotID  string            `json:"snapshot_id"`
	BasisSHA256 string            `json:"basis_sha256"`
	Reviewer    string            `json:"reviewer"`
	Summary     string            `json:"summary"`
	Verdicts    []ReviewVerdictV3 `json:"verdicts"`
	Counts      ReviewCounts      `json:"counts"`
	Limitations []string          `json:"limitations"`
}

type ReviewDecisionV2 struct {
	Version     int             `json:"version"`
	ReviewID    string          `json:"review_id"`
	RunID       string          `json:"run_id"`
	SnapshotID  string          `json:"snapshot_id"`
	BasisSHA256 string          `json:"basis_sha256"`
	Reviewer    string          `json:"reviewer"`
	Summary     string          `json:"summary"`
	Verdicts    []ReviewVerdict `json:"verdicts"`
	Counts      ReviewCounts    `json:"counts"`
	Limitations []string        `json:"limitations"`
}

// reviewRecord is the common shape of a journal record after parsing either version.
type reviewRecord struct {
	Version     int
	ReviewID    string
	RunID       string
	SnapshotID  string
	Reviewer    string
	BasisSHA256 string
	Summary     string
	Assessments []Assessment
	Concur      map[string]string
	Own         map[string]bool // requirement_id → the host added citations of its own (version 3)
	Limitations []string
}

func (record reviewRecord) form() string {
	if record.Version >= 2 {
		return "verdicts"
	}
	return "assessments"
}

func (record reviewRecord) decision() *ReviewDecision {
	return &ReviewDecision{record.Version, record.ReviewID, record.RunID, record.SnapshotID, record.BasisSHA256, record.Reviewer, record.Summary, record.Assessments, record.Limitations}
}

// RoleEntry is the derived per-task line of the review context (tool-spec §23.1); entries stay as they are.
type RoleEntry struct {
	TaskID    string `json:"task_id"`
	Role      string `json:"role"`
	Scope     string `json:"scope"`
	Attempt   int    `json:"attempt"`
	Submitted bool   `json:"submitted"`
	RawSHA256 string `json:"raw_sha256,omitempty"`
}

type ReviewContext struct {
	RunID            string `json:"run_id"`
	SnapshotID       string `json:"snapshot_id"`
	DeliveryComplete bool   `json:"delivery_complete"`
	Freshness        string `json:"freshness"`
	ReviewSummary
	Requirements []Requirement `json:"requirements"`
	Roles        []RoleEntry   `json:"roles"`
	Outcomes     []Outcome     `json:"outcomes"`
	Entries      []Entry       `json:"entries"`
	Executions   []Receipt     `json:"executions"`
}

func roleEntries(state State) []RoleEntry {
	roles := []RoleEntry{}
	submitted := 0
	for _, entry := range state.Entries {
		if entry.Result != nil {
			submitted++
		}
		roles = append(roles, RoleEntry{entry.Task.TaskID, entry.Task.Role, entry.Task.Scope, entry.Task.Attempt, entry.Result != nil, entry.RawSHA256})
	}
	slog.Debug("review: контекст", "roles", len(roles), "submitted", submitted)
	return roles
}

// RoleOutcome is one role's current states for a norm (tool-spec §24.1); citations stay in entries.
type RoleOutcome struct {
	TaskID         string         `json:"task_id"`
	Attempt        int            `json:"attempt"`
	Specification  string         `json:"specification"`
	Implementation string         `json:"implementation"`
	Assertion      string         `json:"assertion"`
	Limitations    []string       `json:"limitations"`
	Citations      CitationCounts `json:"citations"`
	OnlyHere       CitationDiff   `json:"only_here"`
}

// CitationRef points at a cited range without its quote (tool-spec §27.2); entries keep the full citation.
type CitationRef struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type TestRef struct {
	TestID    string `json:"test_id"`
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type CitationCounts struct {
	Spec  int `json:"spec"`
	Code  int `json:"code"`
	Tests int `json:"tests"`
}

// CitationDiff lists what only this role cites for the norm, compared with the other role's current result.
type CitationDiff struct {
	Spec  []CitationRef `json:"spec"`
	Code  []CitationRef `json:"code"`
	Tests []TestRef     `json:"tests"`
}

// PreviousHost is the last host verdict on the same norm from an earlier run with the same snapshot and accepted head
// (tool-spec §25.2): a reading aid the binary never carries over.
type PreviousHost struct {
	RunID          string   `json:"run_id"`
	ReviewID       string   `json:"review_id"`
	Specification  string   `json:"specification"`
	Implementation string   `json:"implementation"`
	Assertion      string   `json:"assertion"`
	Statement      string   `json:"statement"`
	Limitations    []string `json:"limitations"`
}

// Outcome is the derived per-norm row of the review context: both roles side by side and whether they agree.
type Outcome struct {
	RequirementID string                 `json:"requirement_id"`
	Scope         string                 `json:"scope"`
	Roles         map[string]RoleOutcome `json:"roles"`
	Agree         bool                   `json:"agree"`
	// AmbiguousOverClear names the roles that call an accepted clear norm ambiguous (tool-spec §41.2): allowed by the
	// protocol (only the reverse is refused) and always the host's explicit call.
	AmbiguousOverClear []string      `json:"ambiguous_over_clear"`
	PreviousHost       *PreviousHost `json:"previous_host,omitempty"`
	// ContradictedElsewhere lists the related scopes (§43.1) whose decided runs cite, under a contradicted verdict, code
	// lines that overlap what a role cites here — a consistency hint, never a verdict.
	ContradictedElsewhere []ElsewhereHit `json:"contradicted_elsewhere"`
	// Carried names the run and decision a norm was carried from in an incremental run (§50): no role assessed it here.
	Carried *CarriedRef `json:"carried,omitempty"`
}

type CarriedRef struct {
	RunID          string `json:"run_id"`
	ReviewID       string `json:"review_id"`
	Specification  string `json:"specification"`
	Implementation string `json:"implementation"`
	Assertion      string `json:"assertion"`
}

type ElsewhereHit struct {
	Config        string `json:"config"`
	RunID         string `json:"run_id"`
	RequirementID string `json:"requirement_id"`
	Path          string `json:"path"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	Role          string `json:"role"`      // the role here whose citation overlaps
	RoleHere      string `json:"role_here"` // that role's implementation verdict in this run (§44.1): a supported role citing
	// context lines is the usual false positive — the host reads it as such
}

// elsewhereIndex reads the related scopes' contradicted code once per review (§43.1); a broken sibling is skipped.
func elsewhereIndex(related []string) map[string][]ContradictedCitation {
	index := map[string][]ContradictedCitation{}
	if len(related) == 0 {
		return index
	}
	view, err := overview(related)
	if err != nil {
		slog.Debug("review: related scope пропущен", "error", err.Error())
		return index
	}
	for _, c := range view.ContradictedCode {
		index[c.Path] = append(index[c.Path], c)
	}
	return index
}

func elsewhereHits(index map[string][]ContradictedCitation, role, implementation string, code []Citation) []ElsewhereHit {
	hits := []ElsewhereHit{}
	seen := map[string]bool{}
	for _, c := range code {
		for _, other := range index[c.Path] {
			if other.LineEnd < c.LineStart || other.LineStart > c.LineEnd {
				continue
			}
			key := other.Config + "|" + other.RequirementID + "|" + role
			if seen[key] {
				continue
			}
			seen[key] = true
			hits = append(hits, ElsewhereHit{other.Config, other.RunID, other.RequirementID, other.Path, other.LineStart, other.LineEnd, role, implementation})
		}
	}
	return hits
}

func outcomes(m Manifest, state State, previous map[string]PreviousHost, elsewhere map[string][]ContradictedCitation) []Outcome {
	rows := []Outcome{}
	disagree := 0
	differing := map[string]bool{}
	for _, req := range m.Requirements {
		row := Outcome{RequirementID: req.ID, Roles: map[string]RoleOutcome{}, AmbiguousOverClear: []string{}, ContradictedElsewhere: []ElsewhereHit{}}
		if carried := carriedVerdict(m, req.ID); carried != nil {
			row.Carried = &CarriedRef{m.Incremental.SinceRun, m.Incremental.ReviewID, carried.Specification, carried.Implementation, carried.Assertion}
			for _, scope := range m.Config.Scopes {
				for _, id := range scope.Requirements {
					if id == req.ID {
						row.Scope = scope.ID
					}
				}
			}
		}
		cited := map[string]Assessment{}
		if prior, ok := previous[req.ID]; ok {
			row.PreviousHost = &prior
		}
		for _, entry := range state.Entries {
			assigned := false
			for _, task := range entry.Task.Requirements {
				if task.ID == req.ID {
					assigned = true
					break
				}
			}
			if !assigned {
				continue
			}
			if row.Scope == "" {
				row.Scope = entry.Task.Scope
			}
			if entry.Result == nil {
				continue
			}
			for _, assessment := range entry.Result.Assessments {
				if assessment.RequirementID == req.ID {
					limitations := assessment.Limitations
					if limitations == nil {
						limitations = []string{}
					}
					cited[entry.Task.Role] = assessment
					row.ContradictedElsewhere = append(row.ContradictedElsewhere, elsewhereHits(elsewhere, entry.Task.Role, assessment.Implementation, assessment.Code)...)
					row.Roles[entry.Task.Role] = RoleOutcome{TaskID: entry.Task.TaskID, Attempt: entry.Task.Attempt, Specification: assessment.Specification, Implementation: assessment.Implementation, Assertion: assessment.Assertion, Limitations: limitations,
						Citations: CitationCounts{len(assessment.Spec), len(assessment.Code), len(assessment.Tests)}}
				}
			}
		}
		for role, outcome := range row.Roles {
			other := Assessment{}
			for name, assessment := range cited {
				if name != role {
					other = assessment
				}
			}
			outcome.OnlyHere = citationDiff(cited[role], other)
			if len(outcome.OnlyHere.Spec)+len(outcome.OnlyHere.Code)+len(outcome.OnlyHere.Tests) > 0 {
				differing[req.ID] = true
			}
			row.Roles[role] = outcome
		}
		if req.Accepted != nil && req.Accepted.Clarity == "clear" {
			for _, role := range []string{"mapper", "redteam"} {
				if outcome, ok := row.Roles[role]; ok && outcome.Specification == "ambiguous" {
					row.AmbiguousOverClear = append(row.AmbiguousOverClear, role)
				}
			}
		}
		mapper, redteam := row.Roles["mapper"], row.Roles["redteam"]
		_, hasMapper := row.Roles["mapper"]
		_, hasRedteam := row.Roles["redteam"]
		row.Agree = hasMapper && hasRedteam && mapper.Specification == redteam.Specification && mapper.Implementation == redteam.Implementation && mapper.Assertion == redteam.Assertion
		if row.Carried != nil {
			row.Agree = true // no roles to disagree; the verdict is the host's own carried one
		} else if !row.Agree {
			disagree++
		}
		rows = append(rows, row)
	}
	slog.Debug("review: расхождения ролей", "requirements", len(rows), "disagree", disagree)
	slog.Debug("review: разность цитат", "requirements", len(rows), "only_here", len(differing))
	return rows
}

// citationDiff keeps the citations of mine that the other role does not cite, compared by path and line range.
func citationDiff(mine, other Assessment) CitationDiff {
	key := func(c Citation) string { return fmt.Sprintf("%s:%d-%d", c.Path, c.LineStart, c.LineEnd) }
	seenSpec, seenCode, seenTest := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range other.Spec {
		seenSpec[key(c)] = true
	}
	for _, c := range other.Code {
		seenCode[key(c)] = true
	}
	for _, t := range other.Tests {
		seenTest[t.TestID+"|"+key(t.Citation)] = true
	}
	diff := CitationDiff{Spec: []CitationRef{}, Code: []CitationRef{}, Tests: []TestRef{}}
	for _, c := range mine.Spec {
		if !seenSpec[key(c)] {
			diff.Spec = append(diff.Spec, CitationRef{c.Path, c.LineStart, c.LineEnd})
		}
	}
	for _, c := range mine.Code {
		if !seenCode[key(c)] {
			diff.Code = append(diff.Code, CitationRef{c.Path, c.LineStart, c.LineEnd})
		}
	}
	for _, t := range mine.Tests {
		if !seenTest[t.TestID+"|"+key(t.Citation)] {
			diff.Tests = append(diff.Tests, TestRef{t.TestID, t.Citation.Path, t.Citation.LineStart, t.Citation.LineEnd})
		}
	}
	return diff
}

// previousHost scans the sibling runs of reports_dir for the latest host decision on the same snapshot and accepted
// head (tool-spec §25.2). Only reads; a damaged or unrelated run is skipped.
func previousHost(reports *os.Root, runID string, m Manifest) map[string]PreviousHost {
	dirs, err := fs.ReadDir(reports.FS(), ".")
	if err != nil {
		slog.Debug("review: прошлый вердикт: каталог пропущен", "run_id", "", "error", err.Error())
		return nil
	}
	type candidate struct {
		runID, reviewID, recordedAt string
		states                      map[string]PreviousHost
	}
	var best *candidate
	for _, dir := range dirs {
		other := dir.Name()
		if !dir.IsDir() || other == runID || !slugRE.MatchString(other) {
			continue
		}
		states, reviewID, recordedAt, err := previousHostRun(reports, other, m)
		if err != nil {
			slog.Debug("review: прошлый вердикт: каталог пропущен", "run_id", other, "error", err.Error())
			continue
		}
		if states == nil {
			continue
		}
		current := &candidate{other, reviewID, recordedAt, states}
		if best == nil || current.recordedAt > best.recordedAt || (current.recordedAt == best.recordedAt && current.runID > best.runID) {
			best = current
		}
	}
	if best == nil {
		return nil
	}
	for id, prior := range best.states {
		prior.RunID, prior.ReviewID = best.runID, best.reviewID
		best.states[id] = prior
	}
	slog.Debug("review: прошлый вердикт", "run_id", best.runID, "review_id", best.reviewID, "requirements", len(best.states))
	return best.states
}

// sameNorm is the §40.1 key of a norm across runs: the accepted content and revision, independent of the index head.
func sameNorm(req Requirement) string {
	revision := 0
	if req.Accepted != nil {
		revision = req.Accepted.Revision
	}
	return fmt.Sprintf("%s|%s|%d", req.ID, req.ContentHash, revision)
}

// filesDigest hashes every source file of a manifest: two runs with equal digests audited the same bytes even when
// their accepted sets differ (a later package carried norms with keep, tool-spec §39).
func filesDigest(files []SourceFile) string {
	parts := []string{}
	for _, file := range files {
		parts = append(parts, file.Path+"="+file.SHA256)
	}
	sort.Strings(parts)
	return digest([]byte(strings.Join(parts, "\n")))
}

// previousHostRun reads one sibling run; nil states mean "different sources or no decision". A norm is carried over
// only when the sibling audited the same files and the norm's content and revision are identical (§25, §40.1).
func previousHostRun(reports *os.Root, other string, m Manifest) (map[string]PreviousHost, string, string, error) {
	run, err := reports.OpenRoot(other)
	if err != nil {
		return nil, "", "", err
	}
	defer run.Close()
	manifest, err := readRoot(run, "manifest.json", maxState)
	if err != nil {
		return nil, "", "", err
	}
	var brief struct {
		Files        []SourceFile  `json:"files"`
		Requirements []Requirement `json:"requirements"`
	}
	if err := json.Unmarshal(manifest, &brief); err != nil {
		return nil, "", "", err
	}
	if filesDigest(brief.Files) != filesDigest(m.Files) {
		return nil, "", "", nil
	}
	comparable := map[string]bool{}
	for _, req := range brief.Requirements {
		comparable[sameNorm(req)] = true
	}
	shared := map[string]bool{}
	for _, req := range m.Requirements {
		if comparable[sameNorm(req)] {
			shared[req.ID] = true
		}
	}
	if len(shared) == 0 {
		return nil, "", "", nil
	}
	data, err := readRoot(run, "host-reviews.json", maxState)
	if os.IsNotExist(err) {
		return nil, "", "", nil
	}
	if err != nil {
		return nil, "", "", err
	}
	var journal reviewJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, "", "", err
	}
	if len(journal.Records) == 0 {
		return nil, "", "", nil
	}
	type row struct {
		RequirementID  string   `json:"requirement_id"`
		Specification  string   `json:"specification"`
		Implementation string   `json:"implementation"`
		Assertion      string   `json:"assertion"`
		Statement      string   `json:"statement"`
		Limitations    []string `json:"limitations"`
	}
	var last struct {
		ReviewID    string `json:"review_id"`
		Assessments []row  `json:"assessments"`
		Verdicts    []row  `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(journal.Records[len(journal.Records)-1]), &last); err != nil {
		return nil, "", "", err
	}
	states := map[string]PreviousHost{}
	for _, r := range append(last.Assessments, last.Verdicts...) {
		if !shared[r.RequirementID] {
			continue
		}
		limitations := r.Limitations
		if limitations == nil {
			limitations = []string{}
		}
		states[r.RequirementID] = PreviousHost{Specification: r.Specification, Implementation: r.Implementation, Assertion: r.Assertion, Statement: r.Statement, Limitations: limitations}
	}
	recordedAt := ""
	if versions, err := readToolVersions(run); err == nil {
		for _, record := range versions.Records {
			if record.Command == "review" {
				recordedAt = record.RecordedAt
			}
		}
	}
	return states, last.ReviewID, recordedAt, nil
}

// draftDecision prints an empty version 3 decision for the current basis (REQ-SA-045); states stay the host's call.
func draftDecision(runID string, m Manifest, state State, journal reviewJournal) (ReviewDecisionV3, error) {
	if len(pending(state)) != 0 {
		return ReviewDecisionV3{}, errors.New("нужны все ответы ролей; перечитайте review")
	}
	previous := ""
	if n := len(journal.Records); n > 0 {
		previous = journal.Records[n-1]
	}
	draft := ReviewDecisionV3{Version: 3, ReviewID: fmt.Sprintf("host-review-%03d", len(journal.Records)+1), RunID: runID, SnapshotID: m.SnapshotID,
		BasisSHA256: reviewBasis(m.SnapshotID, state, previous), Verdicts: []ReviewVerdictV3{}, Limitations: []string{}}
	for _, req := range m.Requirements {
		verdict := ReviewVerdictV3{RequirementID: req.ID, Limitations: []string{}, Spec: []Citation{}, Code: []Citation{}, Tests: []TestCitation{}}
		if carried := carriedVerdict(m, req.ID); carried != nil {
			// §50: the host's own previous verdict, not a role's — prefilled with concur carried; the host may still change it.
			verdict.Specification, verdict.Implementation, verdict.Assertion, verdict.Concur, verdict.Statement = carried.Specification, carried.Implementation, carried.Assertion, "carried", carried.Statement
			verdict.Limitations = append([]string{}, carried.Limitations...)
		}
		draft.Verdicts = append(draft.Verdicts, verdict)
	}
	draft.Counts = countVerdicts(baseVerdicts(draft.Verdicts))
	slog.Info("черновик решения", "run_id", runID, "review_id", draft.ReviewID, "requirements", len(draft.Verdicts))
	return draft, nil
}

type RawProvenance struct {
	TaskID  string `json:"task_id"`
	Attempt int    `json:"attempt"`
	Kind    string `json:"kind"`
	SHA256  string `json:"sha256"`
}

func reviewBasis(snapshotID string, state State, previousRaw string) string {
	head := ""
	if previousRaw != "" {
		head = digest([]byte(previousRaw))
	}
	body, _ := json.Marshal(struct {
		SnapshotID string `json:"snapshot_id"`
		State      State  `json:"state"`
		ReviewHead string `json:"review_head"`
	}{snapshotID, state, head})
	return digest(body)
}
func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

// reviewVersion reads only the version field; the strict parse of the matching form follows.
func reviewVersion(data []byte) int {
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return 0
	}
	return head.Version
}

func validReviewIdentity(version int, reviewID, runID, wantRun, snapshotID, wantSnapshot, basis, reviewer string) bool {
	return (version == 1 || version == 2 || version == 3) && slugRE.MatchString(reviewID) && runID == wantRun && snapshotID == wantSnapshot && validDigest(basis) && strings.TrimSpace(reviewer) != ""
}

// validateReview parses a decision of either version into the common record. state supplies the adopted evidence of a
// version 2 decision; nil (journal replay) checks the form only.
func validateReview(data []byte, runID string, m Manifest, state *State, checkSources bool) (reviewRecord, error) {
	switch reviewVersion(data) {
	case 3:
		return validateReviewV3(data, runID, m, state, checkSources)
	case 2:
		return validateReviewV2(data, runID, m, state)
	case 1:
	default:
		if json.Valid(data) {
			return reviewRecord{}, errors.New("неверная версия/идентичность host review")
		}
	}
	var decision ReviewDecision
	if err := legacyDecode(data, &decision); err != nil {
		return reviewRecord{}, err
	}
	if !validReviewIdentity(decision.Version, decision.ReviewID, decision.RunID, runID, decision.SnapshotID, m.SnapshotID, decision.BasisSHA256, decision.Reviewer) || decision.Version != 1 {
		return reviewRecord{}, errors.New("неверная версия/идентичность host review")
	}
	task := Task{TaskID: "host-review", Attempt: 1, SnapshotID: m.SnapshotID, Role: "host", Scope: "host", Requirements: m.Requirements}
	result := Result{task.TaskID, 1, m.SnapshotID, task.Role, task.Scope, decision.Summary, decision.Assessments, decision.Limitations}
	if err := validateResult(result, task, m, checkSources); err != nil {
		return reviewRecord{}, err
	}
	return reviewRecord{1, decision.ReviewID, decision.RunID, decision.SnapshotID, decision.Reviewer, decision.BasisSHA256, decision.Summary, decision.Assessments, nil, nil, decision.Limitations}, nil
}

func baseVerdicts(verdicts []ReviewVerdictV3) []ReviewVerdict {
	bases := []ReviewVerdict{}
	for _, verdict := range verdicts {
		bases = append(bases, verdict.base())
	}
	return bases
}

func countVerdicts(verdicts []ReviewVerdict) ReviewCounts {
	var counts ReviewCounts
	for _, verdict := range verdicts {
		switch verdict.Implementation {
		case "supported":
			counts.Supported++
		case "contradicted":
			counts.Contradicted++
		case "unknown":
			counts.ImplementationUnknown++
		}
		switch verdict.Assertion {
		case "relevant":
			counts.Relevant++
		case "weak":
			counts.Weak++
		case "contradicts":
			counts.Contradicts++
		case "missing":
			counts.Missing++
		case "unknown":
			counts.AssertionUnknown++
		}
		if verdict.Specification == "ambiguous" {
			counts.Ambiguous++
		}
	}
	return counts
}

// validateReviewV2 applies the §7 state rules without citations, checks the counts and adopts the named roles' evidence.
func validateReviewV2(data []byte, runID string, m Manifest, state *State) (reviewRecord, error) {
	var decision ReviewDecisionV2
	if err := legacyDecode(data, &decision); err != nil {
		return reviewRecord{}, err
	}
	if !validReviewIdentity(decision.Version, decision.ReviewID, decision.RunID, runID, decision.SnapshotID, m.SnapshotID, decision.BasisSHA256, decision.Reviewer) || decision.Version != 2 {
		return reviewRecord{}, errors.New("неверная версия/идентичность host review")
	}
	verdicts := []ReviewVerdictV3{}
	for _, verdict := range decision.Verdicts {
		verdicts = append(verdicts, ReviewVerdictV3{verdict.RequirementID, verdict.Specification, verdict.Implementation, verdict.Assertion, verdict.Concur, verdict.Statement, verdict.Limitations, []Citation{}, []Citation{}, []TestCitation{}})
	}
	record := reviewRecord{Version: 2, ReviewID: decision.ReviewID, RunID: decision.RunID, SnapshotID: decision.SnapshotID, Reviewer: decision.Reviewer, BasisSHA256: decision.BasisSHA256, Summary: decision.Summary, Limitations: decision.Limitations}
	return validateVerdicts(record, verdicts, decision.Counts, m, state, false)
}

// validateReviewV3 is version 2 plus the host's own citations, checked like a role's (§25).
func validateReviewV3(data []byte, runID string, m Manifest, state *State, checkSources bool) (reviewRecord, error) {
	var decision ReviewDecisionV3
	if err := legacyDecode(data, &decision); err != nil {
		return reviewRecord{}, err
	}
	if !validReviewIdentity(decision.Version, decision.ReviewID, decision.RunID, runID, decision.SnapshotID, m.SnapshotID, decision.BasisSHA256, decision.Reviewer) || decision.Version != 3 {
		return reviewRecord{}, errors.New("неверная версия/идентичность host review")
	}
	record := reviewRecord{Version: 3, ReviewID: decision.ReviewID, RunID: decision.RunID, SnapshotID: decision.SnapshotID, Reviewer: decision.Reviewer, BasisSHA256: decision.BasisSHA256, Summary: decision.Summary, Limitations: decision.Limitations}
	return validateVerdicts(record, decision.Verdicts, decision.Counts, m, state, checkSources)
}

// agreedStatement replaces an empty verdict statement when the host merely concurs with both roles (tool-spec §28.3).
const agreedStatement = "Совпадает с оценками обеих ролей."

// validateVerdicts is the shared version 2/3 body: form, counts, the host's own citations, then the adopted evidence.
func validateVerdicts(record reviewRecord, verdicts []ReviewVerdictV3, counts ReviewCounts, m Manifest, state *State, checkSources bool) (reviewRecord, error) {
	if strings.TrimSpace(record.Summary) == "" || len(verdicts) != len(m.Requirements) {
		return reviewRecord{}, errors.New("нужны summary и ровно один вердикт на каждую норму")
	}
	for _, limitation := range record.Limitations {
		if strings.TrimSpace(limitation) == "" {
			return reviewRecord{}, errors.New("пустое limitation")
		}
	}
	requirements := map[string]Requirement{}
	for _, req := range m.Requirements {
		requirements[req.ID] = req
	}
	var root *os.Root
	if checkSources {
		for _, verdict := range verdicts {
			if len(verdict.Spec)+len(verdict.Code)+len(verdict.Tests) > 0 {
				opened, err := os.OpenRoot(m.Config.ProjectRoot)
				if err != nil {
					return reviewRecord{}, err
				}
				root = opened
				defer root.Close()
				break
			}
		}
	}
	seen := map[string]bool{}
	bases := []ReviewVerdict{}
	for _, verdict := range verdicts {
		req, ok := requirements[verdict.RequirementID]
		if !ok || seen[verdict.RequirementID] {
			return reviewRecord{}, errors.New("вердикт для неизвестной или повторной нормы")
		}
		seen[verdict.RequirementID] = true
		if err := checkVerdict(verdict.base(), req); err != nil {
			return reviewRecord{}, fmt.Errorf("%s: %w", verdict.RequirementID, err)
		}
		if err := checkCitations(verdict.Spec, verdict.Code, verdict.Tests, req, m, root, checkSources); err != nil {
			return reviewRecord{}, fmt.Errorf("%s: цитаты хоста: %w", verdict.RequirementID, err)
		}
		bases = append(bases, verdict.base())
	}
	given, computed := counts, countVerdicts(bases)
	for _, pair := range []struct {
		name            string
		given, computed int
	}{
		{"supported", given.Supported, computed.Supported}, {"contradicted", given.Contradicted, computed.Contradicted},
		{"implementation_unknown", given.ImplementationUnknown, computed.ImplementationUnknown}, {"relevant", given.Relevant, computed.Relevant},
		{"weak", given.Weak, computed.Weak}, {"contradicts", given.Contradicts, computed.Contradicts}, {"missing", given.Missing, computed.Missing},
		{"assertion_unknown", given.AssertionUnknown, computed.AssertionUnknown}, {"ambiguous", given.Ambiguous, computed.Ambiguous},
	} {
		if pair.given != pair.computed {
			return reviewRecord{}, fmt.Errorf("counts.%s: %d ≠ %d", pair.name, pair.given, pair.computed)
		}
	}
	record.Assessments, record.Concur, record.Own = []Assessment{}, map[string]string{}, map[string]bool{}
	for _, verdict := range verdicts {
		record.Concur[verdict.RequirementID] = verdict.Concur
		own := len(verdict.Spec)+len(verdict.Code)+len(verdict.Tests) > 0
		if own {
			record.Own[verdict.RequirementID] = true
		}
		assessment := Assessment{RequirementID: verdict.RequirementID, Specification: verdict.Specification, Implementation: verdict.Implementation,
			Assertion: verdict.Assertion, Statement: verdict.Statement, Spec: verdict.Spec, Code: verdict.Code, Tests: verdict.Tests, Limitations: verdict.Limitations}
		if assessment.Spec == nil {
			assessment.Spec = []Citation{}
		}
		if assessment.Code == nil {
			assessment.Code = []Citation{}
		}
		if assessment.Tests == nil {
			assessment.Tests = []TestCitation{}
		}
		if state != nil {
			if strings.TrimSpace(verdict.Statement) == "" {
				if carried := carriedVerdict(m, verdict.RequirementID); carried != nil && verdict.Concur == "carried" {
					// §50: an empty statement on a carried norm keeps the previous host statement.
					assessment.Statement = carried.Statement
				} else if verdict.Concur != "both" || !rolesAgreeWith(verdict.base(), *state) {
					return reviewRecord{}, fmt.Errorf("%s: пустой statement допустим только при concur both и совпадении с оценками обеих ролей", verdict.RequirementID)
				} else {
					assessment.Statement = agreedStatement
					slog.Debug("review: стандартный statement", "requirement_id", verdict.RequirementID)
				}
			}
			adopted, err := adoptEvidence(verdict.base(), m, *state)
			if err != nil {
				return reviewRecord{}, err
			}
			if carried := carriedVerdict(m, verdict.RequirementID); carried != nil && len(verdict.Spec)+len(verdict.Code)+len(verdict.Tests) == 0 &&
				(verdict.Specification != carried.Specification || verdict.Implementation != carried.Implementation || verdict.Assertion != carried.Assertion) {
				return reviewRecord{}, fmt.Errorf("%s: перенесённый вердикт %s/%s/%s; иные состояния требуют собственных цитат хоста (version 3)", verdict.RequirementID, carried.Specification, carried.Implementation, carried.Assertion)
			}
			assessment.Spec, assessment.Code, assessment.Tests = mergeCitations(adopted, assessment)
			// §7 completeness holds on the union for both verdict forms (REQ-SA-046, §48.1): a positive state needs the
			// kind of evidence that backs it, whichever role the host adopted it from.
			if len(assessment.Spec) == 0 || (assessment.Implementation != "unknown" && len(assessment.Code) == 0) {
				return reviewRecord{}, fmt.Errorf("%s: нужна spec; supported/contradicted требуют code", verdict.RequirementID)
			}
			if oneOf(assessment.Assertion, "relevant", "weak", "contradicts") && len(assessment.Tests) == 0 {
				return reviewRecord{}, fmt.Errorf("%s: оценка assertion требует тестовый источник", verdict.RequirementID)
			}
			slog.Debug("review: свидетельства по concur", "requirement_id", verdict.RequirementID, "concur", verdict.Concur, "spec", len(assessment.Spec), "code", len(assessment.Code), "tests", len(assessment.Tests), "own_spec", len(verdict.Spec), "own_code", len(verdict.Code), "own_tests", len(verdict.Tests))
		}
		record.Assessments = append(record.Assessments, assessment)
	}
	if len(record.Own) == 0 {
		record.Own = nil
	}
	return record, nil
}

// rolesAgreeWith reports whether both mapper and redteam currently assess the norm exactly as the verdict does.
func rolesAgreeWith(verdict ReviewVerdict, state State) bool {
	matched := map[string]bool{}
	for _, entry := range state.Entries {
		if entry.Result == nil {
			continue
		}
		for _, assessment := range entry.Result.Assessments {
			if assessment.RequirementID == verdict.RequirementID {
				matched[entry.Task.Role] = assessment.Specification == verdict.Specification && assessment.Implementation == verdict.Implementation && assessment.Assertion == verdict.Assertion
			}
		}
	}
	return matched["mapper"] && matched["redteam"]
}

// mergeCitations appends the host's own citations after the roles' ones, dropping duplicates by value.
func mergeCitations(adopted, own Assessment) ([]Citation, []Citation, []TestCitation) {
	spec, code, tests := append([]Citation{}, adopted.Spec...), append([]Citation{}, adopted.Code...), append([]TestCitation{}, adopted.Tests...)
	seenSpec, seenCode, seenTest := map[Citation]bool{}, map[Citation]bool{}, map[TestCitation]bool{}
	for _, cite := range spec {
		seenSpec[cite] = true
	}
	for _, cite := range code {
		seenCode[cite] = true
	}
	for _, test := range tests {
		seenTest[test] = true
	}
	for _, cite := range own.Spec {
		if !seenSpec[cite] {
			seenSpec[cite] = true
			spec = append(spec, cite)
		}
	}
	for _, cite := range own.Code {
		if !seenCode[cite] {
			seenCode[cite] = true
			code = append(code, cite)
		}
	}
	for _, test := range own.Tests {
		if !seenTest[test] {
			seenTest[test] = true
			tests = append(tests, test)
		}
	}
	return spec, code, tests
}

func checkVerdict(verdict ReviewVerdict, req Requirement) error {
	if !oneOf(verdict.Specification, "clear", "ambiguous") || !oneOf(verdict.Implementation, "supported", "contradicted", "unknown") || !oneOf(verdict.Assertion, "relevant", "weak", "contradicts", "missing", "unknown") {
		return errors.New("недопустимое состояние specification/implementation/assertion")
	}
	if req.Accepted != nil && req.Accepted.Clarity == "ambiguous" && verdict.Specification != "ambiguous" {
		return errors.New("принятая неоднозначность требует новой редакции, не оценки clear")
	}
	if !oneOf(verdict.Concur, "mapper", "redteam", "both", "carried") {
		return errors.New("concur допускает только mapper, redteam, both или carried")
	}
	for _, limitation := range verdict.Limitations {
		if strings.TrimSpace(limitation) == "" {
			return errors.New("пустое limitation")
		}
	}
	return nil
}

// adoptEvidence collects the named roles' current citations for one norm; both = mapper then redteam, deduplicated.
func adoptEvidence(verdict ReviewVerdict, m Manifest, state State) (Assessment, error) {
	adopted := Assessment{Spec: []Citation{}, Code: []Citation{}, Tests: []TestCitation{}}
	// tool-spec §50: a carried norm's evidence is the previous host verdict recorded in the manifest; no role assessed it,
	// so a role concur is refused, and a carried concur on an assessed norm is refused likewise.
	if carried := carriedVerdict(m, verdict.RequirementID); carried != nil {
		if verdict.Concur != "carried" {
			return adopted, fmt.Errorf("%s: норма перенесена из %s, роли её не оценивали — concur: carried", verdict.RequirementID, m.Incremental.SinceRun)
		}
		return Assessment{Spec: append([]Citation{}, carried.Spec...), Code: append([]Citation{}, carried.Code...), Tests: append([]TestCitation{}, carried.Tests...)}, nil
	}
	if verdict.Concur == "carried" {
		return adopted, fmt.Errorf("%s: норма оценивалась ролями в этом run — concur: carried недопустим", verdict.RequirementID)
	}
	roles := []string{verdict.Concur}
	if verdict.Concur == "both" {
		roles = []string{"mapper", "redteam"}
	}
	seenSpec, seenCode, seenTest := map[Citation]bool{}, map[Citation]bool{}, map[TestCitation]bool{}
	for _, role := range roles {
		found := false
		for _, entry := range state.Entries {
			if entry.Task.Role != role || entry.Result == nil {
				continue
			}
			for _, assessment := range entry.Result.Assessments {
				if assessment.RequirementID != verdict.RequirementID {
					continue
				}
				found = true
				for _, cite := range assessment.Spec {
					if !seenSpec[cite] {
						seenSpec[cite] = true
						adopted.Spec = append(adopted.Spec, cite)
					}
				}
				for _, cite := range assessment.Code {
					if !seenCode[cite] {
						seenCode[cite] = true
						adopted.Code = append(adopted.Code, cite)
					}
				}
				for _, test := range assessment.Tests {
					if !seenTest[test] {
						seenTest[test] = true
						adopted.Tests = append(adopted.Tests, test)
					}
				}
			}
		}
		if !found {
			return adopted, fmt.Errorf("%s: у роли %s нет текущего результата по норме", verdict.RequirementID, role)
		}
	}
	return adopted, nil
}
func readReviews(run *os.Root, runID string, m Manifest) (reviewJournal, error) {
	journal := reviewJournal{1, []string{}}
	data, err := readRoot(run, "host-reviews.json", maxState)
	if os.IsNotExist(err) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	if err := strictJSON(data, &journal); err != nil {
		return journal, err
	}
	if err := requiredJSON(data, reflect.TypeOf(reviewJournal{})); err != nil {
		return journal, err
	}
	if journal.Version != 1 || len(journal.Records) > 64 {
		return journal, errors.New("неверная версия/лимит журнала review")
	}
	seen := map[string]bool{}
	for _, raw := range journal.Records {
		record, err := validateReview([]byte(raw), runID, m, nil, false)
		if err != nil {
			return journal, err
		}
		if seen[record.ReviewID] {
			return journal, errors.New("повторный review_id в журнале")
		}
		seen[record.ReviewID] = true
	}
	return journal, nil
}
func summarizeReviews(runID string, m Manifest, state State, fresh bool, journal reviewJournal) ReviewSummary {
	summary := ReviewSummary{State: "missing", History: []ReviewHistory{}}
	previous := ""
	for _, raw := range journal.Records {
		// readReviews already validated the form; a version 2 record adopts evidence from the current state and may
		// legitimately find a role without that norm after retry — the decision is then outdated, not lost.
		record, err := validateReview([]byte(raw), runID, m, &state, false)
		if err != nil {
			record, _ = validateReview([]byte(raw), runID, m, nil, false)
		}
		summary.History = append(summary.History, ReviewHistory{record.ReviewID, record.Reviewer, digest([]byte(raw)), record.BasisSHA256, record.Summary})
		summary.Latest, summary.Form, summary.Concur, summary.Own = record.decision(), record.form(), record.Concur, record.Own
		summary.State = "outdated"
		if fresh && len(pending(state)) == 0 && record.BasisSHA256 == reviewBasis(m.SnapshotID, state, previous) {
			summary.State = "current"
		}
		previous = raw
	}
	summary.BasisSHA256 = reviewBasis(m.SnapshotID, state, previous)
	return summary
}
func reviewContext(reports, run *os.Root, runID string, m Manifest, state State, fresh bool, related []string) (ReviewContext, error) {
	journal, err := readReviews(run, runID, m)
	if err != nil {
		return ReviewContext{}, err
	}
	status := makeStatus(runID, m, state, fresh)
	return ReviewContext{runID, m.SnapshotID, status.DeliveryComplete, status.Freshness, summarizeReviews(runID, m, state, fresh, journal), m.Requirements, roleEntries(state), outcomes(m, state, previousHost(reports, runID, m), elsewhereIndex(related)), state.Entries, state.Executions}, nil
}

// RequirementView is review for one norm (tool-spec §29.1): both roles' texts and citations, the derived outcome row
// and the host's latest verdict, so the host reads nothing out of entries by hand.
type RequirementView struct {
	RunID       string              `json:"run_id"`
	SnapshotID  string              `json:"snapshot_id"`
	Freshness   string              `json:"freshness"`
	Requirement Requirement         `json:"requirement"`
	Outcome     Outcome             `json:"outcome"`
	Roles       map[string]RoleView `json:"roles"`
	Host        *HostView           `json:"host"`
	Executions  []TestExecution     `json:"executions"`
}

type RoleView struct {
	TaskID      string         `json:"task_id"`
	Attempt     int            `json:"attempt"`
	Statement   string         `json:"statement"`
	Limitations []string       `json:"limitations"`
	Spec        []Citation     `json:"spec"`
	Code        []Citation     `json:"code"`
	Tests       []TestCitation `json:"tests"`
}

type HostView struct {
	ReviewID       string         `json:"review_id"`
	State          string         `json:"state"`
	Form           string         `json:"form"`
	Specification  string         `json:"specification"`
	Implementation string         `json:"implementation"`
	Assertion      string         `json:"assertion"`
	Statement      string         `json:"statement"`
	OwnCitations   bool           `json:"own_citations"`
	Spec           []Citation     `json:"spec"`
	Code           []Citation     `json:"code"`
	Tests          []TestCitation `json:"tests"`
}

func requirementView(reports, run *os.Root, runID string, m Manifest, state State, fresh bool, related []string, id string) (RequirementView, error) {
	context, err := reviewContext(reports, run, runID, m, state, fresh, related)
	if err != nil {
		return RequirementView{}, err
	}
	view := RequirementView{RunID: runID, SnapshotID: m.SnapshotID, Freshness: context.Freshness, Roles: map[string]RoleView{}, Executions: []TestExecution{}}
	found := false
	for _, req := range m.Requirements {
		if req.ID == id {
			view.Requirement, found = req, true
		}
	}
	if !found {
		return RequirementView{}, errors.New("неизвестный requirement_id")
	}
	for _, row := range context.Outcomes {
		if row.RequirementID == id {
			view.Outcome = row
		}
	}
	seenTests := map[string]bool{}
	addExecutions := func(assessment Assessment) {
		for _, execution := range assessmentExecutions(assessment, state.Executions) {
			if !seenTests[execution.TestID] {
				seenTests[execution.TestID] = true
				view.Executions = append(view.Executions, execution)
			}
		}
	}
	for _, entry := range state.Entries {
		if entry.Result == nil {
			continue
		}
		for _, assessment := range entry.Result.Assessments {
			if assessment.RequirementID == id {
				view.Roles[entry.Task.Role] = RoleView{entry.Task.TaskID, entry.Task.Attempt, assessment.Statement, assessment.Limitations, assessment.Spec, assessment.Code, assessment.Tests}
				addExecutions(assessment)
			}
		}
	}
	if context.Latest != nil {
		for _, assessment := range context.Latest.Assessments {
			if assessment.RequirementID == id {
				view.Host = &HostView{context.Latest.ReviewID, context.State, context.Form, assessment.Specification, assessment.Implementation, assessment.Assertion, assessment.Statement, context.Own[id], assessment.Spec, assessment.Code, assessment.Tests}
				addExecutions(assessment)
			}
		}
	}
	slog.Debug("review: норма", "run_id", runID, "requirement_id", id, "roles", len(view.Roles), "host", view.Host != nil)
	return view, nil
}

// ReviewBrief is `review CONFIG RUN_ID summary` (tool-spec §32.2): the context without entries, executions, requirement
// bodies and the decision's assessments — what the host reads first, small enough to read whole.
type ReviewBrief struct {
	RunID             string          `json:"run_id"`
	SnapshotID        string          `json:"snapshot_id"`
	DeliveryComplete  bool            `json:"delivery_complete"`
	Freshness         string          `json:"freshness"`
	State             string          `json:"state"`
	BasisSHA256       string          `json:"basis_sha256"`
	History           []ReviewHistory `json:"history"`
	Form              string          `json:"form,omitempty"`
	RequirementsTotal int             `json:"requirements_total"`
	Roles             []RoleEntry     `json:"roles"`
	Outcomes          []Outcome       `json:"outcomes"`
	Agree             int             `json:"agree"`
	Disagree          []string        `json:"disagree"`
	Carried           int             `json:"carried"` // norms carried from an earlier run (§50)
	Assessed          int             `json:"assessed"`
}

func reviewBrief(context ReviewContext) ReviewBrief {
	brief := ReviewBrief{context.RunID, context.SnapshotID, context.DeliveryComplete, context.Freshness, context.State, context.BasisSHA256, context.History, context.Form, len(context.Requirements), context.Roles, context.Outcomes, 0, []string{}, 0, 0}
	for _, row := range context.Outcomes {
		if row.Carried != nil {
			brief.Carried++
			continue
		}
		brief.Assessed++
		if row.Agree {
			brief.Agree++
		} else {
			brief.Disagree = append(brief.Disagree, row.RequirementID)
		}
	}
	slog.Debug("review: сводка", "run_id", context.RunID, "agree", brief.Agree, "disagree", len(brief.Disagree))
	return brief
}

// CitationRow is one line of the run's citation index (tool-spec §30.2): who cites which lines under which norm, no quote.
type CitationRow struct {
	Path          string `json:"path"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	Kind          string `json:"kind"`
	TestID        string `json:"test_id"`
	Role          string `json:"role"`
	TaskID        string `json:"task_id"`
	RequirementID string `json:"requirement_id"`
}

// citationIndex lists the citations of both roles' current results and the host's own citations from the latest
// decision (version 1 cites everything itself; version 3 only what no role cited), optionally for one file. It reads
// what is recorded and writes nothing; a damaged journal is an error like everywhere else.
func citationIndex(run *os.Root, runID string, m Manifest, state State, filter []string) ([]CitationRow, error) {
	journal, err := readReviews(run, runID, m)
	if err != nil {
		return nil, err
	}
	rows := []CitationRow{}
	add := func(role, taskID string, a Assessment) {
		for _, c := range a.Spec {
			rows = append(rows, CitationRow{c.Path, c.LineStart, c.LineEnd, "spec", "", role, taskID, a.RequirementID})
		}
		for _, c := range a.Code {
			rows = append(rows, CitationRow{c.Path, c.LineStart, c.LineEnd, "code", "", role, taskID, a.RequirementID})
		}
		for _, t := range a.Tests {
			rows = append(rows, CitationRow{t.Citation.Path, t.Citation.LineStart, t.Citation.LineEnd, "tests", t.TestID, role, taskID, a.RequirementID})
		}
	}
	for _, entry := range state.Entries {
		if entry.Result == nil {
			continue
		}
		for _, a := range entry.Result.Assessments {
			add(entry.Task.Role, entry.Task.TaskID, a)
		}
	}
	hostRows := 0
	if n := len(journal.Records); n > 0 {
		// Without state the record keeps only the citations the host wrote itself (§25), not the adopted evidence.
		record, err := validateReview([]byte(journal.Records[n-1]), runID, m, nil, false)
		if err != nil {
			return nil, err
		}
		for _, a := range record.Assessments {
			add("host", "", a)
			hostRows++
		}
	}
	// tool-spec §30.2, §33.2: one filter — a file path, a norm ID or a role.
	if len(filter) == 1 {
		match := func(row CitationRow) bool { return row.Path == filter[0] }
		if requirementIDRE.MatchString(filter[0]) {
			match = func(row CitationRow) bool { return row.RequirementID == filter[0] }
		} else if oneOf(filter[0], "mapper", "redteam", "host") {
			match = func(row CitationRow) bool { return row.Role == filter[0] }
		}
		kept := rows[:0]
		for _, row := range rows {
			if match(row) {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.LineStart != b.LineStart {
			return a.LineStart < b.LineStart
		}
		if a.LineEnd != b.LineEnd {
			return a.LineEnd < b.LineEnd
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.RequirementID < b.RequirementID
	})
	slog.Debug("review: индекс цитат", "run_id", runID, "path", strings.Join(filter, ""), "citations", len(rows), "host_norms", hostRows)
	return rows, nil
}

// requirementText renders a RequirementView as one readable text (tool-spec §33.4): the outcome row, each role's
// states, statement, limitations and citations by location, then the host's verdict. JSON stays the answer envelope.
func requirementText(view RequirementView, brief bool) string {
	var b strings.Builder
	req := view.Requirement
	fmt.Fprintf(&b, "%s — %s (run %s, %s)\n", req.ID, req.Title, view.RunID, view.Freshness)
	fmt.Fprintf(&b, "Условие: %s\nТребование: %s\nПроверка: %s\n", req.Condition, req.Statement, req.Verification)
	if req.Accepted != nil {
		fmt.Fprintf(&b, "Принято: revision %d, clarity %s", req.Accepted.Revision, req.Accepted.Clarity)
		if len(req.Accepted.Exceptions) > 0 {
			fmt.Fprintf(&b, "; исключения: %s", strings.Join(req.Accepted.Exceptions, " | "))
		}
		if len(req.Accepted.Unresolved) > 0 {
			fmt.Fprintf(&b, "; unresolved: %s", strings.Join(req.Accepted.Unresolved, " | "))
		}
		b.WriteString("\n")
	}
	if c := view.Outcome.Carried; c != nil {
		fmt.Fprintf(&b, "\nПеренесено из %s/%s без переоценки: %s/%s/%s (цитируемые файлы не менялись)", c.RunID, c.ReviewID, c.Specification, c.Implementation, c.Assertion)
	}
	fmt.Fprintf(&b, "\nИтог ролей: agree=%t", view.Outcome.Agree)
	// tool-spec §44.2: one line per sibling norm with the number of overlapping citations, not one per line pair.
	type memory struct {
		label   string
		count   int
		backing int // overlaps from a role that itself found this norm contradicted
	}
	order, counts := []string{}, map[string]*memory{}
	for _, hit := range view.Outcome.ContradictedElsewhere {
		key := hit.Config + "|" + hit.RequirementID
		if counts[key] == nil {
			counts[key] = &memory{fmt.Sprintf("%s/%s %s", filepath.Base(filepath.Dir(hit.Config)), hit.RunID, hit.RequirementID), 0, 0}
			order = append(order, key)
		}
		counts[key].count++
		if hit.RoleHere == "contradicted" {
			counts[key].backing++
		}
	}
	for _, key := range order {
		m := counts[key]
		note := "здесь роли тоже contradicted"
		if m.backing == 0 {
			note = "здесь роли не contradicted — вероятно, контекстные строки"
		}
		fmt.Fprintf(&b, "; уже contradicted в %s (пересечений цитат: %d; %s)", m.label, m.count, note)
	}
	if view.Outcome.PreviousHost != nil {
		p := view.Outcome.PreviousHost
		fmt.Fprintf(&b, "; прошлый хост %s/%s: %s/%s/%s — %s", p.RunID, p.ReviewID, p.Specification, p.Implementation, p.Assertion, p.Statement)
	}
	b.WriteString("\n")
	locate := func(c Citation) string { return fmt.Sprintf("%s:%d-%d", c.Path, c.LineStart, c.LineEnd) }
	cite := func(spec, code []Citation, tests []TestCitation) {
		// tool-spec §35.2: brief keeps the judgments and counts the citations instead of listing them.
		if brief {
			fmt.Fprintf(&b, "  цитаты: spec %d, code %d, tests %d\n", len(spec), len(code), len(tests))
			return
		}
		for _, c := range spec {
			fmt.Fprintf(&b, "  spec %s\n", locate(c))
		}
		for _, c := range code {
			fmt.Fprintf(&b, "  code %s\n", locate(c))
		}
		for _, t := range tests {
			fmt.Fprintf(&b, "  test %s %s\n", t.TestID, locate(t.Citation))
		}
	}
	for _, role := range []string{"mapper", "redteam"} {
		r, ok := view.Roles[role]
		if !ok {
			fmt.Fprintf(&b, "\n[%s] нет результата\n", role)
			continue
		}
		o := view.Outcome.Roles[role]
		fmt.Fprintf(&b, "\n[%s] %s/%s/%s (%s, попытка %d)\n%s\n", role, o.Specification, o.Implementation, o.Assertion, r.TaskID, r.Attempt, r.Statement)
		for _, l := range r.Limitations {
			fmt.Fprintf(&b, "  ограничение: %s\n", l)
		}
		cite(r.Spec, r.Code, r.Tests)
	}
	if h := view.Host; h != nil {
		fmt.Fprintf(&b, "\n[host] %s/%s/%s (%s, %s, %s, own_citations=%t)\n%s\n", h.Specification, h.Implementation, h.Assertion, h.ReviewID, h.State, h.Form, h.OwnCitations, h.Statement)
		cite(h.Spec, h.Code, h.Tests)
	} else {
		b.WriteString("\n[host] решения нет\n")
	}
	// tool-spec §37.3: brief drops the per-test execution lines; text keeps them.
	if !brief {
		for _, e := range view.Executions {
			fmt.Fprintf(&b, "  запуск %s: %s (%s)\n", e.TestID, e.State, e.ReceiptID)
		}
	}
	return b.String()
}

// stagedReview is everything review checks before it writes; validate … host stops here (tool-spec §27, REQ-SA-047).
type stagedReview struct {
	record    reviewRecord
	view      ReviewSummary
	body      []byte // the journal as it would be written
	duplicate bool
}

func stageReview(run *os.Root, runID string, m Manifest, state State, path string) (stagedReview, error) {
	journal, err := readReviews(run, runID, m)
	if err != nil {
		return stagedReview{}, err
	}
	data, err := readPath(path, maxResult)
	if err != nil {
		return stagedReview{}, err
	}
	// Form first: a duplicate or an incomplete delivery must answer as §14 says before any evidence is adopted.
	decision, err := validateReview(data, runID, m, nil, true)
	if err != nil {
		return stagedReview{}, err
	}
	view := summarizeReviews(runID, m, state, true, journal)
	for _, raw := range journal.Records {
		var prior struct {
			ReviewID string `json:"review_id"`
		}
		_ = json.Unmarshal([]byte(raw), &prior)
		if prior.ReviewID != decision.ReviewID {
			continue
		}
		if raw != string(data) {
			return stagedReview{}, errors.New("конфликт review_id: исходные байты отличаются")
		}
		return stagedReview{record: decision, view: view, duplicate: true}, nil
	}
	if len(pending(state)) != 0 || decision.BasisSHA256 != view.BasisSHA256 {
		return stagedReview{}, errors.New("нужны все ответы ролей и текущая база review; перечитайте review")
	}
	if decision.Version >= 2 {
		if decision, err = validateReview(data, runID, m, &state, true); err != nil {
			return stagedReview{}, err
		}
	}
	if len(journal.Records) >= 64 {
		return stagedReview{}, errors.New("достигнут лимит 64 host reviews")
	}
	journal.Records = append(journal.Records, string(data))
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil || len(body)+1 > maxState {
		return stagedReview{}, errors.New("журнал host review превышает 32 MiB")
	}
	current, err := snapshot(m.Config)
	if err != nil || current.SnapshotID != m.SnapshotID {
		return stagedReview{}, errors.New("источники изменились во время review")
	}
	return stagedReview{record: decision, view: view, body: body}, nil
}

func submitReview(run *os.Root, runID string, m Manifest, state State, path string, versions []byte) (any, error) {
	slog.Debug("проверка согласования", "run_id", runID)
	staged, err := stageReview(run, runID, m, state, path)
	if err != nil {
		return nil, err
	}
	decision := staged.record
	if staged.duplicate {
		return map[string]any{"accepted": true, "duplicate": true, "review_id": decision.ReviewID, "review_state": staged.view.State, "latest_review_id": staged.view.Latest.ReviewID,
			"version": decision.Version, "requirements": len(decision.Assessments), "own_citations": sortedKeys(decision.Own)}, nil
	}
	if err := invalidateReports(run); err != nil {
		return nil, err
	}
	if err := atomicWrite(run, "host-reviews.json", append(staged.body, '\n'), 0600); err != nil {
		return nil, err
	}
	publishToolVersion(run, versions)
	slog.Info("согласование сохранено", "run_id", runID, "review_id", decision.ReviewID, "requirements", len(decision.Assessments), "form", decision.form())
	return map[string]any{"accepted": true, "duplicate": false, "review_id": decision.ReviewID, "review_state": "current",
		"version": decision.Version, "requirements": len(decision.Assessments), "own_citations": sortedKeys(decision.Own)}, nil
}

// validateHostDecision answers what review would do with these bytes now, writing nothing (REQ-SA-047).
func validateHostDecision(run *os.Root, runID string, m Manifest, state State, path string) (any, error) {
	staged, err := stageReview(run, runID, m, state, path)
	if err != nil {
		return nil, err
	}
	reviewState := "current"
	if staged.duplicate {
		reviewState = staged.view.State
	}
	own := sortedKeys(staged.record.Own)
	slog.Info("validate: решение хоста проверено", "run_id", runID, "review_id", staged.record.ReviewID, "version", staged.record.Version, "duplicate", staged.duplicate, "own_citations", len(own))
	return map[string]any{"valid": true, "version": staged.record.Version, "review_id": staged.record.ReviewID, "duplicate": staged.duplicate, "review_state": reviewState, "requirements": len(staged.record.Assessments), "own_citations": own}, nil
}
