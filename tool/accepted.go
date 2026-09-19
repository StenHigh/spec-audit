package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"unicode"
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
	// Narrowed is the host's narrowed statement from a version 2 decision (REQ-SA-049): only words of the candidate's
	// statement may remain. It lives in the journal's decision bytes, not in the derived state.
	Narrowed string `json:"-"`
}

// Decision version 2 (tool-spec §46): version 1 plus narrowed_statement on every target ("" when unused).
type acceptedTargetV2 struct {
	Candidate         string `json:"candidate"`
	Title             string `json:"title"`
	Verification      string `json:"verification"`
	NarrowedStatement string `json:"narrowed_statement"`
}

type acceptedOperationV2 struct {
	Action   string             `json:"action"`
	Previous []string           `json:"previous"`
	Targets  []acceptedTargetV2 `json:"targets"`
	Reason   string             `json:"reason"`
}

type acceptedDecisionV2 struct {
	Version    int                   `json:"version"`
	DecisionID string                `json:"decision_id"`
	BaseIndex  string                `json:"base_index"`
	RawSHA256  string                `json:"raw_sha256"`
	Operations []acceptedOperationV2 `json:"operations"`
}

// decodeDecision reads a DECISION of version 1 or 2 into the internal form; other versions are refused before any
// source is read. The version is peeked first so the strict field check matches the declared form.
func decodeDecision(data []byte, out *AcceptedDecision) error {
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return errors.New("недопустимый JSON решения")
	}
	if head.Version != 2 {
		return legacyDecode(data, out)
	}
	var v2 acceptedDecisionV2
	if err := legacyDecode(data, &v2); err != nil {
		return err
	}
	*out = AcceptedDecision{Version: 1, DecisionID: v2.DecisionID, BaseIndex: v2.BaseIndex, RawSHA256: v2.RawSHA256, Operations: []AcceptedOperation{}}
	for _, op := range v2.Operations {
		targets := []AcceptedTarget{}
		for _, t := range op.Targets {
			targets = append(targets, AcceptedTarget{t.Candidate, t.Title, t.Verification, t.NarrowedStatement})
		}
		out.Operations = append(out.Operations, AcceptedOperation{op.Action, op.Previous, targets, op.Reason})
	}
	return nil
}

// narrowable is the REQ-SA-049 guard: the narrowed statement is non-empty, differs from the candidate's and contains
// only words (letters/digits, case-insensitive) that the candidate's statement contains — meaning can be removed,
// never added; the exact original stays in the raw inside the journal.
func narrowable(original, narrowed string) error {
	if strings.TrimSpace(narrowed) == "" || narrowed == original {
		return errors.New("narrowed_statement должен быть непустым и отличаться от statement кандидата")
	}
	allowed := textTokens("", original)
	for token := range textTokens("", narrowed) {
		if !allowed[token] {
			return fmt.Errorf("narrowed_statement добавляет слово %q, которого нет в statement кандидата; сужение может только убирать смысл", token)
		}
	}
	return nil
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

// acceptedCommit is one package of the ledger. Ledger version 2 (tool-spec §55) records which source_set files were
// references when the package was applied; version 1 commits have no classification and are replayed as
// `classified: false` — freshness then compares bytes only, as before.
type acceptedCommit struct {
	Raw        string   `json:"raw"`
	Decision   string   `json:"decision"`
	References []string `json:"references"`
	Classified bool     `json:"classified"`
}

type acceptedCommitV1 struct {
	Raw      string `json:"raw"`
	Decision string `json:"decision"`
}

type acceptedLedger struct {
	Version int              `json:"version"`
	Commits []acceptedCommit `json:"commits"`
}

type acceptedLedgerV1 struct {
	Version int                `json:"version"`
	Commits []acceptedCommitV1 `json:"commits"`
}

const ledgerVersion = 2

// decodeLedger reads a version 1 or 2 ledger into the current form; any other version is refused.
func decodeLedger(data []byte) (acceptedLedger, error) {
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return acceptedLedger{}, errors.New("недопустимый JSON журнала индекса")
	}
	switch head.Version {
	case 1:
		var old acceptedLedgerV1
		if err := strictJSON(data, &old); err != nil {
			return acceptedLedger{}, err
		}
		if err := requiredJSON(data, reflect.TypeOf(old)); err != nil {
			return acceptedLedger{}, err
		}
		ledger := acceptedLedger{Version: 1, Commits: []acceptedCommit{}}
		for _, c := range old.Commits {
			ledger.Commits = append(ledger.Commits, acceptedCommit{c.Raw, c.Decision, []string{}, false})
		}
		return ledger, nil
	case ledgerVersion:
		var ledger acceptedLedger
		if err := strictJSON(data, &ledger); err != nil {
			return acceptedLedger{}, err
		}
		if err := requiredJSON(data, reflect.TypeOf(ledger)); err != nil {
			return acceptedLedger{}, err
		}
		return ledger, nil
	}
	return acceptedLedger{}, errors.New("неверная версия или размер журнала индекса")
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
	// References/Classified come from the last package's commit (§55): which source_set files were references then.
	References []string `json:"references"`
	Classified bool     `json:"classified"`
	nextID     int
}

func emptyAccepted() acceptedState {
	return acceptedState{AcceptedSummary: AcceptedSummary{digest([]byte("accepted-index/1")), []AcceptedHistory{}},
		Records: []AcceptedRecord{}, SourceSet: []legacySource{}, References: []string{}, nextID: 1}
}

func acceptedRequirement(candidate legacyCandidate, target AcceptedTarget) Requirement {
	meta := &AcceptedDetails{1, candidate.Exceptions, candidate.Clarity, candidate.Unresolved, candidate.Citations, []string{}}
	statement := candidate.Statement
	if target.Narrowed != "" {
		statement = target.Narrowed
	}
	req := Requirement{Title: target.Title, Condition: candidate.Condition, Statement: statement,
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
		case "retire", "keep":
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
			if operation.Action == "keep" {
				// REQ-SA-048: a norm outside this raw continues unchanged only while every file it cites is byte-identical
				// to the source set of the package that accepted it; otherwise the host must rebind/revise it with a candidate.
				if err := keepable(state.Records[position].Requirement, state.SourceSet, raw.SourceSet); err != nil {
					return state, fmt.Errorf("keep %s: %w", id, err)
				}
				state.Records[position].Reason = operation.Reason
				slog.Debug("reconcile: keep", "id", id)
				continue
			}
			state.Records[position].Status, state.Records[position].Reason = "retired", operation.Reason
		}
		for _, target := range operation.Targets {
			candidate, ok := candidates[target.Candidate]
			if !ok || seenCandidates[target.Candidate] {
				return state, errors.New("неизвестный или повторно решённый кандидат")
			}
			seenCandidates[target.Candidate] = true
			if oneOf(operation.Action, "reject", "defer", "reanchor") && target.Narrowed != "" {
				return state, errors.New("narrowed_statement допустим только при accept/revise/split/merge")
			}
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
			if target.Narrowed != "" {
				if err := narrowable(candidate.Statement, target.Narrowed); err != nil {
					return state, fmt.Errorf("%s: %w", target.Candidate, err)
				}
				slog.Debug("reconcile: statement сужен хостом", "candidate", target.Candidate)
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
	if ledger, err = decodeLedger(data); err != nil {
		return ledger, state, err
	}
	if len(ledger.Commits) > 128 {
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
		if err := decodeDecision([]byte(commit.Decision), &decision); err != nil {
			return ledger, state, err
		}
		state, err = applyAccepted(state, raw, decision, []byte(commit.Raw), []byte(commit.Decision))
		if err == nil {
			state.References, state.Classified = commit.References, commit.Classified
		}
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
	selected, references := map[string]string{}, []string{}
	for _, file := range files {
		if file.Kind == "spec" {
			selected[file.Path] = file.SHA256
			if file.Reference {
				references = append(references, file.Path)
			}
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
	// §55: a file moved between specs and references with the same bytes changes what the norms may rest on.
	if state.Classified {
		sort.Strings(references)
		if !slices.Equal(references, state.References) {
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
	if err := decodeDecision(decisionBytes, &decision); err != nil {
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
	view["deferred"], view["rejected"], view["kept"] = decidedCandidates(decision, "defer"), decidedCandidates(decision, "reject"), keptIDs(decision)
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
	// §55: the package records which source_set files were references; a later regrouping makes the index stale.
	references := []string{}
	for _, source := range raw.SourceSet {
		if reference[source.Path] {
			references = append(references, source.Path)
		}
	}
	sort.Strings(references)
	next.References, next.Classified = references, true
	// A copied commit list: the caller's ledger keeps its backing array untouched; the file is always written in the
	// current ledger version (older commits stay unclassified).
	commits := append(append([]acceptedCommit{}, ledger.Commits...), acceptedCommit{string(rawBytes), string(decisionBytes), references, true})
	staged.ledger = acceptedLedger{Version: ledgerVersion, Commits: commits}
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
	view := map[string]any{"base_index": state.Head, "source_set": set, "freshness": freshness,
		"records": state.Records, "history": state.History, "journal": ledger, "semantic_completeness_proven": false}
	view["advisories"] = []string{}
	if scanErr == nil {
		view["advisories"] = anchorAdvisories(cfg.ProjectRoot, m.Files, requirementAnchorSources(recordRequirements(state.Records)))
	}
	return view
}

func recordRequirements(records []AcceptedRecord) []Requirement {
	reqs := []Requirement{}
	for _, record := range records {
		reqs = append(reqs, record.Requirement)
	}
	return reqs
}

// tool-spec §21.1/§22.1: hints pair a candidate with previous active records by shared exact citation lines.
// Threshold and limit come from two pilot replays (49 and 50 mapped candidates): Jaccard ranking put the true ID first
// in 48/49 and 46/50; at 0.1 the truth was within four hints in 49/49 and 50/50 (three: 49/49 and 49/50), at 0.5 in only
// 28/49 because added reference citations dilute the overlap. Frequency weighting or dropping shared lines lowered the
// first replay to 43/49, so ranking stays plain Jaccard and unique_shared exposes header-only matches instead.
// A hint is never semantic equivalence and never a decision.
const (
	matchHintThreshold     = 0.1
	matchHintLimit         = 4
	likelyDuplicateOverlap = 0.5 // §41.3: a candidate sharing half its lines with an accepted norm is probably the same text
	likelyDuplicateText    = 0.5 // §42.1: token Jaccard of condition+statement at which a restatement counts as a duplicate
)

type matchHint struct {
	ID           string  `json:"id"`
	Revision     int     `json:"revision"`
	Overlap      float64 `json:"overlap"`
	SharedLines  int     `json:"shared_lines"`
	UniqueShared int     `json:"unique_shared"`
	FieldsEqual  bool    `json:"fields_equal"`
	// LikelyDuplicate marks a hint whose candidate repeats an accepted norm almost verbatim (tool-spec §41.3): the
	// usual outcome is defer/merge/reject, never a second accept. A hint, not a decision.
	LikelyDuplicate bool `json:"likely_duplicate"`
	// TextSimilarity is the token Jaccard of condition+statement (tool-spec §42.1): a digest section restating an
	// accepted norm in its own lines shares no citation lines but most of its words.
	TextSimilarity float64 `json:"text_similarity"`
}

// textTokens lowers and splits a norm's condition and statement into a word set for the §42.1 similarity.
func textTokens(condition, statement string) map[string]bool {
	tokens := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(condition+" "+statement), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(word)) > 2 {
			tokens[word] = true
		}
	}
	return tokens
}

func tokenJaccard(a, b map[string]bool) float64 {
	shared := 0
	for token := range a {
		if b[token] {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

// lineFrequency counts, per exact citation line, how many active records cite it; frequency 1 marks a line unique to one norm.
func lineFrequency(records []AcceptedRecord) map[string]int {
	frequency := map[string]int{}
	for _, record := range records {
		if record.Status != "active" || record.Requirement.Accepted == nil {
			continue
		}
		for line := range quoteLines(record.Requirement.Accepted.Citations) {
			frequency[line]++
		}
	}
	return frequency
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

func sameStrings(a, b []string) bool { return slices.Equal(a, b) }

// fieldsEqual: with the record's title/verification the candidate would hash to the same content (rebind possible).
func fieldsEqual(candidate legacyCandidate, req Requirement) bool {
	return req.Accepted != nil && candidate.Condition == req.Condition && candidate.Statement == req.Statement &&
		candidate.Clarity == req.Accepted.Clarity && sameStrings(candidate.Exceptions, req.Accepted.Exceptions) &&
		sameStrings(candidate.Unresolved, req.Accepted.Unresolved)
}

func matchHints(candidate legacyCandidate, records []AcceptedRecord, frequency map[string]int) []matchHint {
	lines := quoteLines(candidate.Citations)
	tokens := textTokens(candidate.Condition, candidate.Statement)
	hints := []matchHint{}
	for _, record := range records {
		if record.Status != "active" || record.Requirement.Accepted == nil {
			continue
		}
		other := quoteLines(record.Requirement.Accepted.Citations)
		shared, unique := 0, 0
		for line := range lines {
			if other[line] {
				shared++
				if frequency[line] == 1 {
					unique++
				}
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
		similarity := tokenJaccard(tokens, textTokens(record.Requirement.Condition, record.Requirement.Statement))
		hints = append(hints, matchHint{record.Requirement.ID, record.Revision, overlap, shared, unique, fieldsEqual(candidate, record.Requirement), overlap >= likelyDuplicateOverlap || similarity >= likelyDuplicateText, similarity})
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
	// tool-spec §42.1: records with no shared lines but near-identical wording are appended after the line hints.
	hinted := map[string]bool{}
	for _, hint := range hints {
		hinted[hint.ID] = true
	}
	textual := []matchHint{}
	for _, record := range records {
		if record.Status != "active" || hinted[record.Requirement.ID] {
			continue
		}
		if similarity := tokenJaccard(tokens, textTokens(record.Requirement.Condition, record.Requirement.Statement)); similarity >= likelyDuplicateText {
			textual = append(textual, matchHint{record.Requirement.ID, record.Revision, 0, 0, 0, fieldsEqual(candidate, record.Requirement), true, similarity})
		}
	}
	sort.Slice(textual, func(i, j int) bool {
		if textual[i].TextSimilarity != textual[j].TextSimilarity {
			return textual[i].TextSimilarity > textual[j].TextSimilarity
		}
		return textual[i].ID < textual[j].ID
	})
	hints = append(hints, textual...)
	if len(hints) > matchHintLimit {
		hints = hints[:matchHintLimit]
	}
	return hints
}

func checkedCandidates(raw legacyRaw, state acceptedState, reference map[string]bool) []checkedCandidate {
	result := []checkedCandidate{}
	hinted := 0
	frequency := lineFrequency(state.Records)
	for _, candidate := range raw.Candidates {
		normative := false
		for _, cite := range candidate.Citations {
			if !reference[cite.Path] {
				normative = true
				break
			}
		}
		matches := matchHints(candidate, state.Records, frequency)
		unique := 0
		if len(matches) > 0 {
			hinted++
			unique = matches[0].UniqueShared
		}
		slog.Debug("check: подсказки сопоставления", "candidate", candidate.ID, "matches", len(matches), "unique_shared", unique)
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
		if err := decodeDecision(decisionBytes, decision); err != nil {
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
		view["advisories"] = anchorAdvisories(cfg.ProjectRoot, staged.files, candidateAnchorSources(staged.raw.Candidates))
		slog.Info("check: приёмка проверена", "candidates", len(candidates), "decision", false, "freshness", view["freshness"])
		return view, nil
	}
	view["advisories"] = anchorAdvisories(cfg.ProjectRoot, staged.files, requirementAnchorSources(recordRequirements(staged.state.Records)))
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
	// §22.3: on success the decision's base_index equals the current head by construction (applyAccepted enforced it).
	view["duplicate"], view["base_index_current"], view["next_head"], view["assignments"], view["retired"] = false, true, staged.state.Head, assignments, retired
	// tool-spec §38.1: the remainder is named, not inferred from the assignments' absence.
	view["deferred"], view["rejected"], view["kept"] = decidedCandidates(*decision, "defer"), decidedCandidates(*decision, "reject"), keptIDs(*decision)
	slog.Info("check: приёмка проверена", "candidates", len(candidates), "decision", true, "assignments", len(assignments), "retired", len(retired), "duplicate", false)
	return view, nil
}

// decidedCandidates lists the candidates a decision handled with the given action (tool-spec §38.1).
func decidedCandidates(decision AcceptedDecision, action string) []string {
	ids := []string{}
	for _, operation := range decision.Operations {
		if operation.Action != action {
			continue
		}
		for _, target := range operation.Targets {
			ids = append(ids, target.Candidate)
		}
	}
	return ids
}

// keepable is the §39 guard: the kept norm's cited files must be unchanged since the accepting package.
func keepable(req Requirement, previous, current []legacySource) error {
	before, after := map[string]string{}, map[string]string{}
	for _, source := range previous {
		before[source.Path] = source.SHA256
	}
	for _, source := range current {
		after[source.Path] = source.SHA256
	}
	paths := map[string]bool{req.Source.Path: true}
	if req.Accepted != nil {
		for _, c := range req.Accepted.Citations {
			paths[c.Path] = true
		}
	}
	for path := range paths {
		if before[path] == "" || before[path] != after[path] {
			return fmt.Errorf("источник %s изменился или отсутствует в raw; норму нужно перепривязать кандидатом (rebind/revise/reanchor)", path)
		}
	}
	return nil
}

// keptIDs lists the norms a decision carries over with keep (tool-spec §39).
func keptIDs(decision AcceptedDecision) []string {
	ids := []string{}
	for _, operation := range decision.Operations {
		if operation.Action == "keep" {
			ids = append(ids, operation.Previous...)
		}
	}
	return ids
}
