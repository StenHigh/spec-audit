package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"sort"
	"strings"
	"syscall"
)

const acceptedFile = "._accepted-index.json"

type AcceptedDetails struct {
	Revision   int        `json:"revision"`
	Exceptions []string   `json:"exceptions"`
	Clarity    string     `json:"clarity"`
	Unresolved []string   `json:"unresolved"`
	Citations  []Citation `json:"citations"`
	Parents    []string   `json:"parents"`
}

type AcceptedTarget struct {
	Candidate    string `json:"candidate"`
	Title        string `json:"title"`
	Verification string `json:"verification"`
}

type AcceptedOperation struct {
	Action   string           `json:"action"`
	Previous []string         `json:"previous"`
	Targets  []AcceptedTarget `json:"targets"`
	Reason   string           `json:"reason"`
}

type AcceptedDecision struct {
	Version    int                 `json:"version"`
	DecisionID string              `json:"decision_id"`
	BaseIndex  string              `json:"base_index"`
	RawSHA256  string              `json:"raw_sha256"`
	Operations []AcceptedOperation `json:"operations"`
}

type acceptedCommit struct {
	Raw      string `json:"raw"`
	Decision string `json:"decision"`
}

type acceptedLedger struct {
	Version int              `json:"version"`
	Commits []acceptedCommit `json:"commits"`
}

type AcceptedRecord struct {
	Requirement Requirement `json:"requirement"`
	Status      string      `json:"status"`
	Reason      string      `json:"reason"`
	// Mirrors requirement.accepted.* for hosts (tool-spec §19.1); Requirement itself stays untouched because it is hashed into snapshot_id.
	Revision int    `json:"revision"`
	Clarity  string `json:"clarity"`
}

func acceptedRecord(req Requirement, status, reason string) AcceptedRecord {
	record := AcceptedRecord{Requirement: req, Status: status, Reason: reason}
	if req.Accepted != nil {
		record.Revision, record.Clarity = req.Accepted.Revision, req.Accepted.Clarity
	}
	return record
}

type AcceptedAssignment struct {
	Candidate     string `json:"candidate"`
	RequirementID string `json:"requirement_id"`
}

type AcceptedHistory struct {
	Decision    AcceptedDecision     `json:"decision"`
	Assignments []AcceptedAssignment `json:"assignments"`
	Limitations []string             `json:"limitations"`
}

type AcceptedSummary struct {
	Head    string            `json:"head"`
	History []AcceptedHistory `json:"history"`
}

type acceptedState struct {
	AcceptedSummary
	Records   []AcceptedRecord `json:"records"`
	SourceSet []legacySource   `json:"source_set"`
	nextID    int
}

func emptyAccepted() acceptedState {
	return acceptedState{AcceptedSummary: AcceptedSummary{digest([]byte("accepted-index/1")), []AcceptedHistory{}},
		Records: []AcceptedRecord{}, SourceSet: []legacySource{}, nextID: 1}
}

func acceptedRequirement(candidate legacyCandidate, target AcceptedTarget) Requirement {
	meta := &AcceptedDetails{1, candidate.Exceptions, candidate.Clarity, candidate.Unresolved, candidate.Citations, []string{}}
	req := Requirement{Title: target.Title, Condition: candidate.Condition, Statement: candidate.Statement,
		Verification: target.Verification, Source: candidate.Citations[0], Accepted: meta}
	content, _ := json.Marshal([]any{req.Title, req.Condition, req.Statement, req.Verification, meta.Exceptions, meta.Clarity, meta.Unresolved})
	req.ContentHash = digest(content)
	return req
}

// A complete, bounded transaction. Old records are not implicitly carried or removed.
func applyAccepted(state acceptedState, raw legacyRaw, decision AcceptedDecision, rawBytes, decisionBytes []byte) (acceptedState, error) {
	if decision.Version != 1 || !slugRE.MatchString(decision.DecisionID) || decision.BaseIndex != state.Head || decision.RawSHA256 != digest(rawBytes) || len(decision.Operations) > 128 {
		return state, errors.New("неверное решение, raw hash или stale base_index")
	}
	for _, history := range state.History {
		if history.Decision.DecisionID == decision.DecisionID {
			return state, errors.New("повторный decision_id в журнале")
		}
	}
	candidates, active := map[string]legacyCandidate{}, map[string]int{}
	for _, candidate := range raw.Candidates {
		candidates[candidate.ID] = candidate
	}
	for i, record := range state.Records {
		if record.Status == "active" {
			active[record.Requirement.ID] = i
		}
	}
	state.Records = append([]AcceptedRecord{}, state.Records...)
	seenCandidates, seenPrevious := map[string]bool{}, map[string]bool{}
	history := AcceptedHistory{decision, []AcceptedAssignment{}, raw.Limitations}
	for _, operation := range decision.Operations {
		p, n := len(operation.Previous), len(operation.Targets)
		valid := false
		switch operation.Action {
		case "accept", "reject", "defer":
			valid = p == 0 && n == 1
		case "rebind", "revise", "reanchor":
			valid = p == 1 && n == 1
		case "split":
			valid = p == 1 && n >= 2 && n <= 64
		case "merge":
			valid = p >= 2 && p <= 64 && n == 1
		case "retire":
			valid = p == 1 && n == 0
		}
		if !valid || strings.TrimSpace(operation.Reason) == "" {
			return state, errors.New("недопустимая операция или пустая причина")
		}
		for _, id := range operation.Previous {
			position, ok := active[id]
			if !ok || seenPrevious[id] {
				return state, errors.New("previous требует неповторяющиеся прежние active ID")
			}
			seenPrevious[id] = true
			state.Records[position].Status, state.Records[position].Reason = "retired", operation.Reason
		}
		for _, target := range operation.Targets {
			candidate, ok := candidates[target.Candidate]
			if !ok || seenCandidates[target.Candidate] {
				return state, errors.New("неизвестный или повторно решённый кандидат")
			}
			seenCandidates[target.Candidate] = true
			if oneOf(operation.Action, "reject", "defer") {
				if target.Title != "" || target.Verification != "" {
					return state, errors.New("reject/defer не задают title/verification")
				}
				continue
			}
			if operation.Action == "reanchor" {
				// REQ-SA-043: the accepted content, ID, revision and hash stay; only the citations follow the candidate.
				if target.Title != "" || target.Verification != "" {
					return state, errors.New("reanchor сохраняет прежние title/verification; поля должны быть пустыми")
				}
				position := active[operation.Previous[0]]
				req := state.Records[position].Requirement
				meta := *req.Accepted
				meta.Citations = append([]Citation{}, candidate.Citations...)
				req.Accepted, req.Source = &meta, candidate.Citations[0]
				state.Records[position] = acceptedRecord(req, "active", operation.Reason)
				history.Assignments = append(history.Assignments, AcceptedAssignment{candidate.ID, req.ID})
				slog.Debug("reconcile: reanchor", "id", req.ID, "candidate", candidate.ID)
				continue
			}
			if strings.TrimSpace(target.Title) == "" || strings.TrimSpace(target.Verification) == "" {
				return state, errors.New("принятие требует title/verification")
			}
			req := acceptedRequirement(candidate, target)
			if oneOf(operation.Action, "rebind", "revise") {
				position := active[operation.Previous[0]]
				old := state.Records[position].Requirement
				if (operation.Action == "rebind") != (req.ContentHash == old.ContentHash) {
					return state, errors.New("rebind сохраняет содержание; revise требует изменения")
				}
				req.ID, req.Accepted.Revision, req.Accepted.Parents = old.ID, old.Accepted.Revision, old.Accepted.Parents
				if operation.Action == "revise" {
					req.Accepted.Revision++
				}
				state.Records[position] = acceptedRecord(req, "active", operation.Reason)
			} else {
				req.ID = fmt.Sprintf("REQ-AI-%03d", state.nextID)
				state.nextID++
				req.Accepted.Parents = append([]string{}, operation.Previous...)
				state.Records = append(state.Records, acceptedRecord(req, "active", operation.Reason))
			}
			history.Assignments = append(history.Assignments, AcceptedAssignment{candidate.ID, req.ID})
		}
	}
	if len(seenCandidates) != len(candidates) || len(seenPrevious) != len(active) {
		return state, errors.New("нужен полный учёт кандидатов и прежних active ID")
	}
	state.SourceSet = raw.SourceSet
	state.History = append(state.History, history)
	head, _ := json.Marshal([]string{state.Head, digest(rawBytes), digest(decisionBytes)})
	state.Head = digest(head)
	return state, nil
}

func readAccepted(reportsDir string) (acceptedLedger, acceptedState, error) {
	ledger, state := acceptedLedger{1, []acceptedCommit{}}, emptyAccepted()
	root, err := os.OpenRoot(reportsDir)
	if os.IsNotExist(err) {
		return ledger, state, nil
	}
	if err != nil {
		return ledger, state, err
	}
	defer root.Close()
	data, err := readRoot(root, acceptedFile, maxState)
	if os.IsNotExist(err) {
		return ledger, state, nil
	}
	if err != nil {
		return ledger, state, err
	}
	if err := strictJSON(data, &ledger); err != nil {
		return ledger, state, err
	}
	if err := requiredJSON(data, reflect.TypeOf(ledger)); err != nil {
		return ledger, state, err
	}
	if ledger.Version != 1 || len(ledger.Commits) > 128 {
		return ledger, state, errors.New("неверная версия или размер журнала индекса")
	}
	for _, commit := range ledger.Commits {
		var raw legacyRaw
		var decision AcceptedDecision
		if err := legacyDecode([]byte(commit.Raw), &raw); err != nil {
			return ledger, state, err
		}
		if err := legacyShape(raw); err != nil {
			return ledger, state, err
		}
		if err := legacyDecode([]byte(commit.Decision), &decision); err != nil {
			return ledger, state, err
		}
		state, err = applyAccepted(state, raw, decision, []byte(commit.Raw), []byte(commit.Decision))
		if err != nil {
			return ledger, state, err
		}
	}
	return ledger, state, nil
}

// Reuse the same source scanner; do not parse declared REQ blocks or load the index recursively.
// acceptedSources returns the extraction source_set (normative and reference spec files alike),
// their contents and the set of reference-only paths (tool-spec §20).
func acceptedSources(cfg Config) ([]legacySource, map[string][]byte, map[string]bool, error) {
	cfg.Scopes = nil
	m, err := scanSnapshot(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	defer root.Close()
	set, contents, reference := []legacySource{}, map[string][]byte{}, map[string]bool{}
	for _, file := range m.Files {
		if file.Kind != "spec" {
			continue
		}
		data, err := readRoot(root, file.Path, maxFile)
		if err != nil || digest(data) != file.SHA256 {
			return nil, nil, nil, errors.New("источник изменился во время чтения")
		}
		set = append(set, legacySource{file.Path, file.SHA256})
		contents[file.Path] = data
		if file.Reference {
			reference[file.Path] = true
		}
	}
	return set, contents, reference, nil
}

// normativeAnchor rejects turning a candidate into a norm when every citation comes from a reference-only file (REQ-SA-041).
// reject/defer/retire stay unchecked; an unknown candidate ID is left to applyAccepted so its error text is unchanged.
func normativeAnchor(raw legacyRaw, decision AcceptedDecision, reference map[string]bool) error {
	if len(reference) == 0 {
		return nil
	}
	candidates := map[string]legacyCandidate{}
	for _, candidate := range raw.Candidates {
		candidates[candidate.ID] = candidate
	}
	checked := 0
	for _, operation := range decision.Operations {
		if !oneOf(operation.Action, "accept", "rebind", "revise", "reanchor", "split", "merge") {
			continue
		}
		for _, target := range operation.Targets {
			candidate, ok := candidates[target.Candidate]
			if !ok {
				continue
			}
			checked++
			anchored := false
			for _, cite := range candidate.Citations {
				if !reference[cite.Path] {
					anchored = true
					break
				}
			}
			if !anchored {
				return fmt.Errorf("кандидат %s цитирует только справочные источники (references); нужна цитата из нормативного файла", target.Candidate)
			}
		}
	}
	slog.Debug("reconcile: guard нормативной цитаты", "candidates", checked, "reference_files", len(reference))
	return nil
}

func acceptedFresh(state acceptedState, files []SourceFile) bool {
	selected := map[string]string{}
	for _, file := range files {
		if file.Kind == "spec" {
			selected[file.Path] = file.SHA256
		}
	}
	if len(state.History) == 0 || len(selected) != len(state.SourceSet) {
		return false
	}
	for _, source := range state.SourceSet {
		if selected[source.Path] != source.SHA256 {
			return false
		}
	}
	return true
}

func reconcile(cfg Config, paths []string) (any, error) {
	if cfg.IndexMode != "accepted" {
		return nil, errors.New("reconcile требует index_mode: accepted")
	}
	if len(paths) == 0 {
		ledger, state, err := readAccepted(cfg.ReportsDir)
		if err != nil {
			return nil, err
		}
		return acceptedView(cfg, ledger, state), nil
	}
	rawBytes, err := readPath(paths[0], maxResult)
	if err != nil {
		return nil, err
	}
	decisionBytes, err := readPath(paths[1], maxResult)
	if err != nil {
		return nil, err
	}
	var decision AcceptedDecision
	if err := legacyDecode(decisionBytes, &decision); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.ReportsDir, 0700); err != nil {
		return nil, err
	}
	reports, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	defer reports.Close()
	lock, err := reports.OpenFile("._accepted-index.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	// ponytail: one index lock and at most 128 batches; partial large-corpus acceptance is a later slice.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ledger, state, err := readAccepted(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	staged, err := stageAcceptance(cfg, ledger, state, &decision, rawBytes, decisionBytes)
	if err != nil {
		return nil, err
	}
	if staged.duplicate {
		return map[string]any{"accepted": true, "duplicate": true, "base_index": state.Head, "freshness_checked": false}, nil
	}
	_, sources, _, err := acceptedSources(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := legacyValidate(rawBytes, sources); err != nil {
		return nil, err
	}
	if err := atomicWrite(reports, acceptedFile, append(staged.data, '\n'), 0600); err != nil {
		return nil, err
	}
	// Same view as the read-only call; freshness is measured again under the held lock, not assumed.
	view := acceptedView(cfg, staged.ledger, staged.state)
	view["accepted"], view["duplicate"] = true, false
	slog.Debug("reconcile: view после apply", "freshness", view["freshness"], "records", len(staged.state.Records))
	return view, nil
}

// stagedAcceptance is everything apply would publish, computed without touching the ledger (REQ-SA-042).
type stagedAcceptance struct {
	raw       legacyRaw
	state     acceptedState
	ledger    acceptedLedger
	data      []byte
	files     []SourceFile
	reference map[string]bool
	duplicate bool
}

// stageAcceptance runs the apply checks in apply order; decision == nil checks the raw alone (check CONFIG RAW).
// The decision is decoded by the caller before the ledger is read, so error order matches apply.
func stageAcceptance(cfg Config, ledger acceptedLedger, state acceptedState, decision *AcceptedDecision, rawBytes, decisionBytes []byte) (stagedAcceptance, error) {
	staged := stagedAcceptance{state: state, ledger: ledger}
	if decision != nil {
		// An exact replay answers before any source is read: a historical duplicate proves nothing about freshness.
		for i, history := range state.History {
			if history.Decision.DecisionID == decision.DecisionID {
				commit := ledger.Commits[i]
				if commit.Raw != string(rawBytes) || commit.Decision != string(decisionBytes) {
					return staged, errors.New("decision_id уже принят с другими байтами")
				}
				staged.duplicate = true
				return staged, nil
			}
		}
		if len(ledger.Commits) >= 128 {
			return staged, errors.New("лимит 128 пакетов; история не усекается")
		}
	}
	set, sources, reference, err := acceptedSources(cfg)
	if err != nil {
		return staged, err
	}
	raw, err := legacyValidate(rawBytes, sources)
	if err != nil {
		return staged, err
	}
	staged.raw, staged.reference = raw, reference
	for _, source := range set {
		staged.files = append(staged.files, SourceFile{Path: source.Path, Kind: "spec", SHA256: source.SHA256, Reference: reference[source.Path]})
	}
	if decision == nil {
		return staged, nil
	}
	if err := normativeAnchor(raw, *decision, reference); err != nil {
		return staged, err
	}
	if len(reference) > 0 {
		slog.Info("reconcile: справочные источники", "reference_files", len(reference))
	}
	next, err := applyAccepted(state, raw, *decision, rawBytes, decisionBytes)
	if err != nil {
		return staged, err
	}
	// A copied commit list: the caller's ledger keeps its backing array untouched.
	commits := append(append([]acceptedCommit{}, ledger.Commits...), acceptedCommit{string(rawBytes), string(decisionBytes)})
	staged.ledger = acceptedLedger{Version: ledger.Version, Commits: commits}
	data, err := json.MarshalIndent(staged.ledger, "", "  ")
	if err != nil || len(data)+1 > maxState {
		return staged, errors.New("журнал превышает 32 MiB; индекс не опубликован")
	}
	staged.state, staged.data = next, data
	slog.Debug("reconcile: staging", "operations", len(decision.Operations), "records_after", len(next.Records))
	return staged, nil
}

// acceptedSource is the view form of a source_set entry; the raw format (legacySource) stays unchanged.
type acceptedSource struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Reference bool   `json:"reference,omitempty"`
}

// acceptedFreshness is the single derivation shared by the read view and check (tool-spec §21).
func acceptedFreshness(ledger acceptedLedger, state acceptedState, files []SourceFile) string {
	if len(ledger.Commits) == 0 {
		return "uninitialized"
	}
	if acceptedFresh(state, files) {
		return "fresh"
	}
	return "stale"
}

func sourcesView(files []SourceFile) []acceptedSource {
	set := []acceptedSource{}
	for _, file := range files {
		if file.Kind == "spec" {
			set = append(set, acceptedSource{file.Path, file.SHA256, file.Reference})
		}
	}
	return set
}

func acceptedView(cfg Config, ledger acceptedLedger, state acceptedState) map[string]any {
	freshness := "unavailable"
	m, scanErr := scanSnapshot(cfg)
	set := []acceptedSource{}
	if scanErr == nil {
		freshness = acceptedFreshness(ledger, state, m.Files)
		set = sourcesView(m.Files)
	}
	return map[string]any{"base_index": state.Head, "source_set": set, "freshness": freshness,
		"records": state.Records, "history": state.History, "journal": ledger, "semantic_completeness_proven": false}
}

// tool-spec §21.1: hints pair a candidate with previous active records by shared exact citation lines.
// Threshold and limit come from the pilot replay accept-001→accept-002 (49 mapped candidates): Jaccard ranking put the
// true ID first in 48/49; at 0.1 the truth was within three hints in 49/49, at 0.5 in only 28/49 because added
// reference citations dilute the overlap. A hint is never semantic equivalence and never a decision.
const (
	matchHintThreshold = 0.1
	matchHintLimit     = 3
)

type matchHint struct {
	ID          string  `json:"id"`
	Revision    int     `json:"revision"`
	Overlap     float64 `json:"overlap"`
	SharedLines int     `json:"shared_lines"`
	FieldsEqual bool    `json:"fields_equal"`
}

type checkedCandidate struct {
	ID        string      `json:"id"`
	Clarity   string      `json:"clarity"`
	Normative bool        `json:"normative"`
	Matches   []matchHint `json:"matches"`
}

func quoteLines(citations []Citation) map[string]bool {
	lines := map[string]bool{}
	for _, cite := range citations {
		for _, line := range strings.Split(cite.Quote, "\n") {
			lines[line] = true
		}
	}
	return lines
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fieldsEqual: with the record's title/verification the candidate would hash to the same content (rebind possible).
func fieldsEqual(candidate legacyCandidate, req Requirement) bool {
	return req.Accepted != nil && candidate.Condition == req.Condition && candidate.Statement == req.Statement &&
		candidate.Clarity == req.Accepted.Clarity && sameStrings(candidate.Exceptions, req.Accepted.Exceptions) &&
		sameStrings(candidate.Unresolved, req.Accepted.Unresolved)
}

func matchHints(candidate legacyCandidate, records []AcceptedRecord) []matchHint {
	lines := quoteLines(candidate.Citations)
	hints := []matchHint{}
	for _, record := range records {
		if record.Status != "active" || record.Requirement.Accepted == nil {
			continue
		}
		other := quoteLines(record.Requirement.Accepted.Citations)
		shared := 0
		for line := range lines {
			if other[line] {
				shared++
			}
		}
		union := len(lines) + len(other) - shared
		if shared == 0 || union == 0 {
			continue
		}
		overlap := float64(shared) / float64(union)
		if overlap < matchHintThreshold {
			continue
		}
		hints = append(hints, matchHint{record.Requirement.ID, record.Revision, overlap, shared, fieldsEqual(candidate, record.Requirement)})
	}
	sort.Slice(hints, func(i, j int) bool {
		if hints[i].Overlap != hints[j].Overlap {
			return hints[i].Overlap > hints[j].Overlap
		}
		return hints[i].ID < hints[j].ID
	})
	if len(hints) > matchHintLimit {
		hints = hints[:matchHintLimit]
	}
	return hints
}

func checkedCandidates(raw legacyRaw, state acceptedState, reference map[string]bool) []checkedCandidate {
	result := []checkedCandidate{}
	hinted := 0
	for _, candidate := range raw.Candidates {
		normative := false
		for _, cite := range candidate.Citations {
			if !reference[cite.Path] {
				normative = true
				break
			}
		}
		matches := matchHints(candidate, state.Records)
		if len(matches) > 0 {
			hinted++
		}
		slog.Debug("check: подсказки сопоставления", "candidate", candidate.ID, "matches", len(matches))
		result = append(result, checkedCandidate{candidate.ID, candidate.Clarity, normative, matches})
	}
	slog.Debug("check: кандидаты с подсказками", "hinted", hinted, "candidates", len(result))
	return result
}

type checkedAssignment struct {
	Candidate     string   `json:"candidate"`
	RequirementID string   `json:"requirement_id"`
	Action        string   `json:"action"`
	Revision      int      `json:"revision"`
	Previous      []string `json:"previous"`
}

// checkAcceptance is the write-free twin of the apply path: the same checks in the same order, no lock, no files (REQ-SA-042).
func checkAcceptance(cfg Config, paths []string) (any, error) {
	if cfg.IndexMode != "accepted" {
		return nil, errors.New("check требует index_mode: accepted")
	}
	rawBytes, err := readPath(paths[0], maxResult)
	if err != nil {
		return nil, err
	}
	var decision *AcceptedDecision
	var decisionBytes []byte
	if len(paths) == 2 {
		if decisionBytes, err = readPath(paths[1], maxResult); err != nil {
			return nil, err
		}
		decision = &AcceptedDecision{}
		if err := legacyDecode(decisionBytes, decision); err != nil {
			return nil, err
		}
	}
	ledger, state, err := readAccepted(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	staged, err := stageAcceptance(cfg, ledger, state, decision, rawBytes, decisionBytes)
	if err != nil {
		return nil, err
	}
	if staged.duplicate {
		slog.Info("check: приёмка проверена", "candidates", 0, "decision", true, "duplicate", true)
		return map[string]any{"valid": true, "duplicate": true, "base_index": state.Head}, nil
	}
	candidates := checkedCandidates(staged.raw, state, staged.reference)
	view := map[string]any{"valid": true, "base_index": state.Head, "freshness": acceptedFreshness(ledger, state, staged.files),
		"source_set": sourcesView(staged.files), "candidates": candidates}
	if decision == nil {
		slog.Info("check: приёмка проверена", "candidates", len(candidates), "decision", false, "freshness", view["freshness"])
		return view, nil
	}
	operations := map[string]AcceptedOperation{}
	for _, operation := range decision.Operations {
		for _, target := range operation.Targets {
			operations[target.Candidate] = operation
		}
	}
	revisions := map[string]int{}
	retired := []string{}
	for i, record := range staged.state.Records {
		revisions[record.Requirement.ID] = record.Revision
		if record.Status == "retired" && (i >= len(state.Records) || state.Records[i].Status == "active") {
			retired = append(retired, record.Requirement.ID)
		}
	}
	assignments := []checkedAssignment{}
	for _, assignment := range staged.state.History[len(staged.state.History)-1].Assignments {
		operation := operations[assignment.Candidate]
		assignments = append(assignments, checkedAssignment{assignment.Candidate, assignment.RequirementID, operation.Action,
			revisions[assignment.RequirementID], append([]string{}, operation.Previous...)})
	}
	view["duplicate"], view["next_head"], view["assignments"], view["retired"] = false, staged.state.Head, assignments, retired
	slog.Info("check: приёмка проверена", "candidates", len(candidates), "decision", true, "assignments", len(assignments), "retired", len(retired), "duplicate", false)
	return view, nil
}
