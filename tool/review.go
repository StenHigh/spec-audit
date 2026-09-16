//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
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
}
type ReviewContext struct {
	RunID            string `json:"run_id"`
	SnapshotID       string `json:"snapshot_id"`
	DeliveryComplete bool   `json:"delivery_complete"`
	Freshness        string `json:"freshness"`
	ReviewSummary
	Requirements []Requirement `json:"requirements"`
	Entries      []Entry       `json:"entries"`
	Executions   []Receipt     `json:"executions"`
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
func validateReview(data []byte, runID string, m Manifest, checkSources bool) (ReviewDecision, error) {
	var decision ReviewDecision
	if err := legacyDecode(data, &decision); err != nil {
		return decision, err
	}
	if decision.Version != 1 || !slugRE.MatchString(decision.ReviewID) || decision.RunID != runID || decision.SnapshotID != m.SnapshotID || !validDigest(decision.BasisSHA256) || strings.TrimSpace(decision.Reviewer) == "" {
		return decision, errors.New("неверная версия/идентичность host review")
	}
	task := Task{TaskID: "host-review", Attempt: 1, SnapshotID: m.SnapshotID, Role: "host", Scope: "host", Requirements: m.Requirements}
	result := Result{task.TaskID, 1, m.SnapshotID, task.Role, task.Scope, decision.Summary, decision.Assessments, decision.Limitations}
	return decision, validateResult(result, task, m, checkSources)
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
		decision, err := validateReview([]byte(raw), runID, m, false)
		if err != nil {
			return journal, err
		}
		if seen[decision.ReviewID] {
			return journal, errors.New("повторный review_id в журнале")
		}
		seen[decision.ReviewID] = true
	}
	return journal, nil
}
func summarizeReviews(runID string, m Manifest, state State, fresh bool, journal reviewJournal) ReviewSummary {
	summary := ReviewSummary{State: "missing", History: []ReviewHistory{}}
	previous := ""
	for _, raw := range journal.Records {
		var decision ReviewDecision
		_ = json.Unmarshal([]byte(raw), &decision) // readReviews already validated every record.
		summary.History = append(summary.History, ReviewHistory{decision.ReviewID, decision.Reviewer, digest([]byte(raw)), decision.BasisSHA256, decision.Summary})
		summary.Latest = &decision
		summary.State = "outdated"
		if fresh && len(pending(state)) == 0 && decision.BasisSHA256 == reviewBasis(m.SnapshotID, state, previous) {
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
	return ReviewContext{runID, m.SnapshotID, status.DeliveryComplete, status.Freshness, summarizeReviews(runID, m, state, fresh, journal), m.Requirements, state.Entries, state.Executions}, nil
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
	decision, err := validateReview(data, runID, m, true)
	if err != nil {
		return nil, err
	}
	view := summarizeReviews(runID, m, state, true, journal)
	for _, raw := range journal.Records {
		var prior ReviewDecision
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
	slog.Info("согласование сохранено", "run_id", runID, "review_id", decision.ReviewID, "requirements", len(decision.Assessments))
	return map[string]any{"accepted": true, "duplicate": false, "review_id": decision.ReviewID, "review_state": "current"}, nil
}
