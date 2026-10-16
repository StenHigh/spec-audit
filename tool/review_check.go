//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"syscall"
)

// ReviewCheck records the independent session's claim and conclusion. The CLI
// verifies its basis; it cannot establish independence or perform semantic review.
type ReviewCheck struct {
	SchemaVersion int      `json:"schema_version"`
	RunID         string   `json:"run_id"`
	SnapshotID    string   `json:"snapshot_id"`
	ReviewID      string   `json:"review_id"`
	ReviewSHA256  string   `json:"review_sha256"`
	BasisSHA256   string   `json:"basis_sha256"`
	Reviewer      string   `json:"reviewer"`
	ContextRef    string   `json:"context_ref"`
	Conclusion    string   `json:"conclusion"`
	Summary       string   `json:"summary"`
	Limitations   []string `json:"limitations"`
}
type reviewCheckJournal struct {
	SchemaVersion int      `json:"schema_version"`
	Records       []string `json:"records"`
}

func parseReviewCheck(raw []byte) (ReviewCheck, error) {
	var c ReviewCheck
	if err := strictJSON(raw, &c); err != nil {
		return c, err
	}
	if err := requiredJSON(raw, reflect.TypeOf(c)); err != nil {
		return c, err
	}
	if c.SchemaVersion != 1 || !slugRE.MatchString(c.RunID) || !slugRE.MatchString(c.ReviewID) || !validDigest(c.SnapshotID) || !validDigest(c.ReviewSHA256) || !validDigest(c.BasisSHA256) || !nonempty(c.Reviewer, c.ContextRef, c.Summary) || !oneOf(c.Conclusion, "confirmed", "changes_required") {
		return c, errors.New("review-check: неверная форма, основание или conclusion")
	}
	for _, text := range c.Limitations {
		if strings.TrimSpace(text) == "" {
			return c, errors.New("review-check: пустое limitation")
		}
	}
	return c, nil
}
func readReviewChecks(run *os.Root) (reviewCheckJournal, error) {
	journal := reviewCheckJournal{1, []string{}}
	data, err := readRoot(run, "review-checks.json", maxState)
	if os.IsNotExist(err) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	if err := strictJSON(data, &journal); err != nil {
		return journal, err
	}
	if err := requiredJSON(data, reflect.TypeOf(journal)); err != nil {
		return journal, err
	}
	if journal.SchemaVersion != 1 || len(journal.Records) > 256 {
		return journal, errors.New("review-checks: неподдерживаемый журнал")
	}
	for _, raw := range journal.Records {
		if _, err := parseReviewCheck([]byte(raw)); err != nil {
			return journal, err
		}
	}
	return journal, nil
}
func checkForReport(run *os.Root, report Report) (*ReviewCheck, error) {
	journal, err := readReviewChecks(run)
	if err != nil {
		return nil, err
	}
	if report.HostReview == nil || report.HostReview.Latest == nil || len(report.HostReview.History) == 0 {
		return nil, nil
	}
	latest := report.HostReview.Latest
	hash := report.HostReview.History[len(report.HostReview.History)-1].RawSHA256
	var found *ReviewCheck
	for _, raw := range journal.Records {
		c, err := parseReviewCheck([]byte(raw))
		if err != nil {
			return nil, err
		}
		if c.RunID == report.RunID && c.SnapshotID == report.SnapshotID && c.ReviewID == latest.ReviewID && c.ReviewSHA256 == hash && c.BasisSHA256 == latest.BasisSHA256 {
			copy := c
			found = &copy
		}
	}
	return found, nil
}

func reviewCheckCommand(args []string) (any, error) {
	if len(args) != 2 && len(args) != 3 {
		return nil, errors.New("review-check CONFIG RUN_ID [RECEIPT]")
	}
	if !slugRE.MatchString(args[1]) {
		return nil, errors.New("review-check: неверный RUN_ID")
	}
	cfg, err := loadConfig(args[0], true)
	if err != nil {
		return nil, err
	}
	reports, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	defer reports.Close()
	lock, err := reports.OpenFile("."+args[1]+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	run, err := reports.OpenRoot(args[1])
	if err != nil {
		return nil, err
	}
	defer run.Close()
	if _, err := readToolVersions(run); err != nil {
		return nil, err
	}
	raw, err := readRoot(run, "manifest.json", maxState)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := strictJSON(raw, &m); err != nil {
		return nil, err
	}
	raw, err = readRoot(run, "state.json", maxState)
	if err != nil {
		return nil, err
	}
	var state State
	if err := strictJSON(raw, &state); err != nil {
		return nil, err
	}
	current, snapshotErr := snapshot(cfg)
	fresh := snapshotErr == nil && current.SnapshotID == m.SnapshotID
	view, err := reviewContext(reports, run, args[1], m, state, fresh, cfg.Related)
	if err != nil {
		return nil, err
	}
	report := Report{Status: Status{RunID: args[1], SnapshotID: m.SnapshotID}, HostReview: &view.ReviewSummary}
	if len(args) == 2 {
		check, err := checkForReport(run, report)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run_id": args[1], "host_review_state": view.State, "check": check}, nil
	}
	if view.State != "current" || view.Latest == nil || len(view.History) == 0 {
		return nil, errors.New("review-check: нужно текущее полное решение хоста")
	}
	raw, err = readExact(args[2], maxResult)
	if err != nil {
		return nil, err
	}
	c, err := parseReviewCheck(raw)
	if err != nil {
		return nil, err
	}
	latest := view.Latest
	if c.RunID != args[1] || c.SnapshotID != m.SnapshotID || c.ReviewID != latest.ReviewID || c.ReviewSHA256 != view.History[len(view.History)-1].RawSHA256 || c.BasisSHA256 != latest.BasisSHA256 || c.Reviewer == latest.Reviewer {
		return nil, errors.New("review-check: чужое решение/основание либо reviewer совпадает с хостом")
	}
	journal, err := readReviewChecks(run)
	if err != nil {
		return nil, err
	}
	for _, stored := range journal.Records {
		if stored == string(raw) {
			return map[string]any{"recorded": false, "check": c}, nil
		}
	}
	if len(journal.Records) >= 256 {
		return nil, errors.New("review-check: журнал достиг лимита")
	}
	journal.Records = append(journal.Records, string(raw))
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil || len(body)+1 > maxState {
		return nil, errors.New("review-check: журнал превышает 32 MiB")
	}
	versions, err := prepareToolVersion(run, "review-check", version)
	if err != nil {
		return nil, err
	}
	if err := writeJSON(run, "review-checks.json", journal, 0600); err != nil {
		return nil, err
	}
	publishToolVersion(run, versions)
	return map[string]any{"recorded": true, "check": c}, nil
}
