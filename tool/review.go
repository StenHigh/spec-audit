//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
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
	TaskID         string `json:"task_id"`
	Attempt        int    `json:"attempt"`
	Specification  string `json:"specification"`
	Implementation string `json:"implementation"`
	Assertion      string `json:"assertion"`
}

// Outcome is the derived per-norm row of the review context: both roles side by side and whether they agree.
type Outcome struct {
	RequirementID string                 `json:"requirement_id"`
	Scope         string                 `json:"scope"`
	Roles         map[string]RoleOutcome `json:"roles"`
	Agree         bool                   `json:"agree"`
}

func outcomes(m Manifest, state State) []Outcome {
	rows := []Outcome{}
	disagree := 0
	for _, req := range m.Requirements {
		row := Outcome{RequirementID: req.ID, Roles: map[string]RoleOutcome{}}
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
					row.Roles[entry.Task.Role] = RoleOutcome{entry.Task.TaskID, entry.Task.Attempt, assessment.Specification, assessment.Implementation, assessment.Assertion}
				}
			}
		}
		mapper, redteam := row.Roles["mapper"], row.Roles["redteam"]
		_, hasMapper := row.Roles["mapper"]
		_, hasRedteam := row.Roles["redteam"]
		row.Agree = hasMapper && hasRedteam && mapper.Specification == redteam.Specification && mapper.Implementation == redteam.Implementation && mapper.Assertion == redteam.Assertion
		if !row.Agree {
			disagree++
		}
		rows = append(rows, row)
	}
	slog.Debug("review: расхождения ролей", "requirements", len(rows), "disagree", disagree)
	return rows
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
		draft.Verdicts = append(draft.Verdicts, ReviewVerdictV3{RequirementID: req.ID, Limitations: []string{}, Spec: []Citation{}, Code: []Citation{}, Tests: []TestCitation{}})
	}
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
			adopted, err := adoptEvidence(verdict.base(), m, *state)
			if err != nil {
				return reviewRecord{}, err
			}
			assessment.Spec, assessment.Code, assessment.Tests = mergeCitations(adopted, assessment)
			// Version 3 promises §7 completeness on the union (REQ-SA-046); version 2 keeps its §23 contract unchanged.
			if record.Version == 3 {
				if len(assessment.Spec) == 0 || (assessment.Implementation != "unknown" && len(assessment.Code) == 0) {
					return reviewRecord{}, fmt.Errorf("%s: нужна spec; supported/contradicted требуют code", verdict.RequirementID)
				}
				if oneOf(assessment.Assertion, "relevant", "weak", "contradicts") && len(assessment.Tests) == 0 {
					return reviewRecord{}, fmt.Errorf("%s: оценка assertion требует тестовый источник", verdict.RequirementID)
				}
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
	if !oneOf(verdict.Concur, "mapper", "redteam", "both") {
		return errors.New("concur допускает только mapper, redteam или both")
	}
	if strings.TrimSpace(verdict.Statement) == "" {
		return errors.New("пустой statement вердикта")
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
	roles := []string{verdict.Concur}
	if verdict.Concur == "both" {
		roles = []string{"mapper", "redteam"}
	}
	adopted := Assessment{Spec: []Citation{}, Code: []Citation{}, Tests: []TestCitation{}}
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
func reviewContext(run *os.Root, runID string, m Manifest, state State, fresh bool) (ReviewContext, error) {
	journal, err := readReviews(run, runID, m)
	if err != nil {
		return ReviewContext{}, err
	}
	status := makeStatus(runID, m, state, fresh)
	return ReviewContext{runID, m.SnapshotID, status.DeliveryComplete, status.Freshness, summarizeReviews(runID, m, state, fresh, journal), m.Requirements, roleEntries(state), outcomes(m, state), state.Entries, state.Executions}, nil
}
func submitReview(run *os.Root, runID string, m Manifest, state State, path string, versions []byte) (any, error) {
	slog.Debug("проверка согласования", "run_id", runID)
	journal, err := readReviews(run, runID, m)
	if err != nil {
		return nil, err
	}
	data, err := readPath(path, maxResult)
	if err != nil {
		return nil, err
	}
	// Form first: a duplicate or an incomplete delivery must answer as §14 says before any evidence is adopted.
	decision, err := validateReview(data, runID, m, nil, true)
	if err != nil {
		return nil, err
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
			return nil, errors.New("конфликт review_id: исходные байты отличаются")
		}
		return map[string]any{"accepted": true, "duplicate": true, "review_id": decision.ReviewID, "review_state": view.State, "latest_review_id": view.Latest.ReviewID}, nil
	}
	if len(pending(state)) != 0 || decision.BasisSHA256 != view.BasisSHA256 {
		return nil, errors.New("нужны все ответы ролей и текущая база review; перечитайте review")
	}
	if decision.Version >= 2 {
		if decision, err = validateReview(data, runID, m, &state, true); err != nil {
			return nil, err
		}
	}
	if len(journal.Records) >= 64 {
		return nil, errors.New("достигнут лимит 64 host reviews")
	}
	journal.Records = append(journal.Records, string(data))
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil || len(body)+1 > maxState {
		return nil, errors.New("журнал host review превышает 32 MiB")
	}
	current, err := snapshot(m.Config)
	if err != nil || current.SnapshotID != m.SnapshotID {
		return nil, errors.New("источники изменились во время review")
	}
	if err := invalidateReports(run); err != nil {
		return nil, err
	}
	if err := atomicWrite(run, "host-reviews.json", append(body, '\n'), 0600); err != nil {
		return nil, err
	}
	publishToolVersion(run, versions)
	slog.Info("согласование сохранено", "run_id", runID, "review_id", decision.ReviewID, "requirements", len(decision.Assessments), "form", decision.form())
	return map[string]any{"accepted": true, "duplicate": false, "review_id": decision.ReviewID, "review_state": "current"}, nil
}
