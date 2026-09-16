package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
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
		case "rebind", "revise":
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
func acceptedSources(cfg Config) ([]legacySource, map[string][]byte, error) {
	cfg.Scopes = nil
	m, err := scanSnapshot(cfg)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	set, contents := []legacySource{}, map[string][]byte{}
	for _, file := range m.Files {
		if file.Kind != "spec" {
			continue
		}
		data, err := readRoot(root, file.Path, maxFile)
		if err != nil || digest(data) != file.SHA256 {
			return nil, nil, errors.New("источник изменился во время чтения")
		}
		set = append(set, legacySource{file.Path, file.SHA256})
		contents[file.Path] = data
	}
	return set, contents, nil
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
	for i, history := range state.History {
		if history.Decision.DecisionID == decision.DecisionID {
			commit := ledger.Commits[i]
			if commit.Raw != string(rawBytes) || commit.Decision != string(decisionBytes) {
				return nil, errors.New("decision_id уже принят с другими байтами")
			}
			return map[string]any{"accepted": true, "duplicate": true, "base_index": state.Head, "freshness_checked": false}, nil
		}
	}
	if len(ledger.Commits) >= 128 {
		return nil, errors.New("лимит 128 пакетов; история не усекается")
	}
	_, sources, err := acceptedSources(cfg)
	if err != nil {
		return nil, err
	}
	raw, err := legacyValidate(rawBytes, sources)
	if err != nil {
		return nil, err
	}
	state, err = applyAccepted(state, raw, decision, rawBytes, decisionBytes)
	if err != nil {
		return nil, err
	}
	ledger.Commits = append(ledger.Commits, acceptedCommit{string(rawBytes), string(decisionBytes)})
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil || len(data)+1 > maxState {
		return nil, errors.New("журнал превышает 32 MiB; индекс не опубликован")
	}
	_, sources, err = acceptedSources(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := legacyValidate(rawBytes, sources); err != nil {
		return nil, err
	}
	if err := atomicWrite(reports, acceptedFile, append(data, '\n'), 0600); err != nil {
		return nil, err
	}
	// Same view as the read-only call; freshness is measured again under the held lock, not assumed.
	view := acceptedView(cfg, ledger, state)
	view["accepted"], view["duplicate"] = true, false
	slog.Debug("reconcile: view после apply", "freshness", view["freshness"], "records", len(state.Records))
	return view, nil
}

func acceptedView(cfg Config, ledger acceptedLedger, state acceptedState) map[string]any {
	freshness := "unavailable"
	m, scanErr := scanSnapshot(cfg)
	set := []legacySource{}
	if scanErr == nil {
		freshness = "stale"
		if len(ledger.Commits) == 0 {
			freshness = "uninitialized"
		} else if acceptedFresh(state, m.Files) {
			freshness = "fresh"
		}
		for _, file := range m.Files {
			if file.Kind == "spec" {
				set = append(set, legacySource{file.Path, file.SHA256})
			}
		}
	}
	return map[string]any{"base_index": state.Head, "source_set": set, "freshness": freshness,
		"records": state.Records, "history": state.History, "journal": ledger, "semantic_completeness_proven": false}
}
