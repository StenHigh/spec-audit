//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func reviewedFixture(t *testing.T, runtime bool) (string, string, TaskBatch) {
	t.Helper()
	config, base := fixture(t)
	if runtime {
		writeFixture(t, config, bytes.Replace(readFixture(t, config), []byte("runtime: {kind: none}"), []byte("runtime: {kind: go, paths: ['.'], tests: [], timeout_seconds: 30}"), 1))
	}
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		body := legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source")))
		body = append([]byte(" \r\n"), append(bytes.ReplaceAll(body, []byte("\n"), []byte("\r\n")), []byte("\r\n\t")...)...)
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, body)
		runOK(t, "submit", config, "review", task.TaskID, path)
		stored := readFixture(t, filepath.Join(base, "runs/review/results", task.TaskID+"-attempt-1.json"))
		if !bytes.Equal(body, stored) {
			t.Fatal("initial raw changed")
		}
	}
	return config, base, batch
}
func reviewInput(t *testing.T, config string) ReviewDecision {
	t.Helper()
	view := runOK(t, "review", config, "review").(ReviewContext)
	rows := append([]Assessment{}, view.Entries[0].Result.Assessments...)
	return ReviewDecision{1, "decision-1", "review", view.SnapshotID, view.BasisSHA256, "host-session", "Обоснованное согласование", rows, []string{"Суждение хоста; не сертификат"}}
}
func writeReviewInput(t *testing.T, base string, decision ReviewDecision) string {
	t.Helper()
	path := filepath.Join(base, "host-"+decision.ReviewID+".json")
	writeFixture(t, path, legacyMarshal(t, decision))
	return path
}
func TestReviewLifecycle(t *testing.T) {
	config, base, batch := reviewedFixture(t, false)
	beforeState := readFixture(t, filepath.Join(base, "runs/review/state.json"))
	decision := reviewInput(t, config)
	decision.Summary = "<script>not executable</script>"
	path := writeReviewInput(t, base, decision)
	raw := append([]byte(" \n"), append(readFixture(t, path), []byte("\r\n")...)...)
	writeFixture(t, path, raw)
	runOK(t, "review", config, "review", path)
	runOK(t, "report", config, "review")
	view := runOK(t, "review", config, "review").(ReviewContext)
	if view.State != "current" || len(view.History) != 1 || view.History[0].RawSHA256 != digest(raw) {
		t.Fatal("review not current/exact")
	}
	var journal reviewJournal
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/host-reviews.json")), &journal); err != nil || journal.Records[0] != string(raw) {
		t.Fatal("review bytes lost", err)
	}
	if !bytes.Equal(beforeState, readFixture(t, filepath.Join(base, "runs/review/state.json"))) {
		t.Fatal("review changed role state")
	}
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.HostReconciliationRequired || report.SemanticCompletenessProven || report.HostReview.State != "current" || len(report.RawProvenance) != 2 {
		t.Fatal("report hides or overclaims review")
	}
	html := readFixture(t, filepath.Join(base, "runs/review/report.html"))
	if bytes.Contains(html, []byte("<script>not executable</script>")) || !bytes.Contains(html, []byte("&lt;script&gt;not executable&lt;/script&gt;")) {
		t.Fatal("unsafe/missing host HTML")
	}
	beforeJournal := readFixture(t, filepath.Join(base, "runs/review/host-reviews.json"))
	duplicate := runOK(t, "review", config, "review", path).(map[string]any)
	if duplicate["duplicate"] != true || !bytes.Equal(beforeJournal, readFixture(t, filepath.Join(base, "runs/review/host-reviews.json"))) {
		t.Fatal("review replay changed journal")
	}
	// Equivalent role JSON must not replace the first byte representation.
	task := batch.Tasks[0]
	resultPath := filepath.Join(base, task.TaskID+".json")
	original := readFixture(t, resultPath)
	writeFixture(t, resultPath, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
	runOK(t, "submit", config, "review", task.TaskID, resultPath)
	if !bytes.Equal(original, readFixture(t, filepath.Join(base, "runs/review/results", task.TaskID+"-attempt-1.json"))) {
		t.Fatal("duplicate replaced first raw")
	}
	runOK(t, "retry", config, "review", task.TaskID)
	if got := runOK(t, "status", config, "review").(Status); got.HostReviewState != "outdated" {
		t.Fatal("retry retained current review")
	}
	runOK(t, "report", config, "review")
	if _, err := os.Stat(filepath.Join(base, "runs/review/results", task.TaskID+"-attempt-1.json")); err != nil {
		t.Fatal("retry lost old raw")
	}
}
func TestReviewRejects(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "foreign", "basis", "run", "snapshot", "citation", "summary", "statement", "null", "unknown_field", "case", "pending", "same_id_conflict"} {
		t.Run(kind, func(t *testing.T) {
			config, base, batch := reviewedFixture(t, false)
			decision := reviewInput(t, config)
			path := filepath.Join(base, "decision.json")
			switch kind {
			case "missing":
				decision.Assessments = decision.Assessments[:len(decision.Assessments)-1]
			case "duplicate":
				decision.Assessments[1] = decision.Assessments[0]
			case "foreign":
				decision.Assessments[0].RequirementID = "REQ-OTHER-001"
			case "basis":
				decision.BasisSHA256 = strings.Repeat("0", 64)
			case "run":
				decision.RunID = "other"
			case "snapshot":
				decision.SnapshotID = strings.Repeat("0", 64)
			case "citation":
				decision.Assessments[0].Spec[0].Quote = "not source"
			case "summary":
				decision.Summary = " "
			case "statement":
				decision.Assessments[0].Statement = " \n"
			case "pending":
				runOK(t, "retry", config, "review", batch.Tasks[0].TaskID)
			case "same_id_conflict":
				runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
				decision.Summary = "Changed bytes"
			}
			data := legacyMarshal(t, decision)
			switch kind {
			case "null":
				// Avoid relying on pretty formatting.
				var object map[string]any
				_ = json.Unmarshal(data, &object)
				object["limitations"] = nil
				data = legacyMarshal(t, object)
			case "unknown_field":
				var object map[string]any
				_ = json.Unmarshal(data, &object)
				object["extra"] = true
				data = legacyMarshal(t, object)
			case "case":
				data = bytes.Replace(data, []byte("\"reviewer\""), []byte("\"Reviewer\""), 1)
			}
			writeFixture(t, path, data)
			journalPath := filepath.Join(base, "runs/review/host-reviews.json")
			before, _ := os.ReadFile(journalPath)
			runFail(t, "review", config, "review", path)
			after, _ := os.ReadFile(journalPath)
			if !bytes.Equal(before, after) {
				t.Fatal("rejection changed review journal")
			}
		})
	}
}
func TestReviewConcurrency(t *testing.T) {
	config, base, _ := reviewedFixture(t, false)
	first := reviewInput(t, config)
	second := first
	second.ReviewID = "decision-2"
	paths := []string{writeReviewInput(t, base, first), writeReviewInput(t, base, second)}
	errorsOut := make(chan error, 2)
	var group sync.WaitGroup
	for _, path := range paths {
		group.Add(1)
		go func(path string) {
			defer group.Done()
			_, err := execute([]string{"review", config, "review", path})
			errorsOut <- err
		}(path)
	}
	group.Wait()
	close(errorsOut)
	success := 0
	for err := range errorsOut {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("CAS winners=%d", success)
	}
	if view := runOK(t, "review", config, "review").(ReviewContext); len(view.History) != 1 || view.State != "current" {
		t.Fatal("lost concurrent decision")
	}
}
func TestReviewDrift(t *testing.T) {
	for _, kind := range []string{"source", "receipt", "raw_tamper", "journal_tamper"} {
		t.Run(kind, func(t *testing.T) {
			config, base, batch := reviewedFixture(t, kind == "receipt")
			decision := reviewInput(t, config)
			runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
			journalPath := filepath.Join(base, "runs/review/host-reviews.json")
			beforeJournal := readFixture(t, journalPath)
			switch kind {
			case "source":
				path := filepath.Join(base, "source/source.go")
				writeFixture(t, path, append(readFixture(t, path), []byte("\n// changed\n")...))
			case "receipt":
				runOK(t, "test", config, "review", "observed")
			case "raw_tamper":
				path := filepath.Join(base, "runs/review/results", batch.Tasks[0].TaskID+"-attempt-1.json")
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, path, []byte("{}"))
			case "journal_tamper":
				writeFixture(t, filepath.Join(base, "runs/review/host-reviews.json"), []byte("{\"version\":1}"))
			}
			if strings.HasSuffix(kind, "tamper") {
				runFail(t, "report", config, "review")
				return
			}
			view := runOK(t, "review", config, "review").(ReviewContext)
			if view.State != "outdated" || len(view.History) != 1 {
				t.Fatal("drift lost history or retained current")
			}
			if !bytes.Equal(beforeJournal, readFixture(t, journalPath)) || !reflect.DeepEqual(view.Latest, &decision) {
				t.Fatal("drift changed historical decision")
			}
			runOK(t, "report", config, "review")
			var report Report
			if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/report.json")), &report); err != nil || report.HostReview.State != "outdated" || !report.HostReconciliationRequired || !reflect.DeepEqual(report.HostReview.Latest, &decision) {
				t.Fatal("outdated JSON lost decision or requires no review", err)
			}
			page := readFixture(t, filepath.Join(base, "runs/review/report.html"))
			for _, want := range []string{"Согласование хоста — outdated", decision.ReviewID, decision.Summary, decision.Assessments[0].Statement} {
				if !bytes.Contains(page, []byte(html.EscapeString(want))) {
					t.Fatal("outdated HTML lost history/state", want)
				}
			}
		})
	}
}

func TestReviewRetryAndDisagreement(t *testing.T) {
	config, base, _ := reviewedFixture(t, false)
	first := reviewInput(t, config)
	firstPath := writeReviewInput(t, base, first)
	runOK(t, "review", config, "review", firstPath)
	journalPath := filepath.Join(base, "runs/review/host-reviews.json")
	firstJournal := readFixture(t, journalPath)
	task := runOK(t, "retry", config, "review", "all-redteam").(Task)
	redteam := sampleResult(t, task, filepath.Join(base, "source"))
	// Synthetic differing judgments exercise projection, not a new semantic verdict.
	redteam.Assessments[0].Assertion = "weak"
	redteam.Assessments[0].Statement = "redteam: mechanical disagreement sentinel"
	redPath := filepath.Join(base, "redteam-new.json")
	writeFixture(t, redPath, legacyMarshal(t, redteam))
	runOK(t, "submit", config, "review", task.TaskID, redPath)
	view := runOK(t, "review", config, "review").(ReviewContext)
	if !view.DeliveryComplete || view.State != "outdated" || !reflect.DeepEqual(view.Latest, &first) || !bytes.Equal(firstJournal, readFixture(t, journalPath)) {
		t.Fatal("redelivery reused old review or changed history")
	}
	duplicate := runOK(t, "review", config, "review", firstPath).(map[string]any)
	if duplicate["duplicate"] != true || duplicate["review_state"] != "outdated" || !bytes.Equal(firstJournal, readFixture(t, journalPath)) {
		t.Fatal("historical replay restored current review")
	}
	second := reviewInput(t, config)
	second.ReviewID = "decision-2"
	second.Assessments[0].Implementation, second.Assessments[0].Assertion = "unknown", "unknown"
	second.Assessments[0].Statement = "host: <independent choice>"
	secondPath := writeReviewInput(t, base, second)
	beforeState := readFixture(t, filepath.Join(base, "runs/review/state.json"))
	runOK(t, "review", config, "review", secondPath)
	secondJournal := readFixture(t, journalPath)
	duplicate = runOK(t, "review", config, "review", firstPath).(map[string]any)
	if duplicate["duplicate"] != true || duplicate["latest_review_id"] != second.ReviewID || !bytes.Equal(secondJournal, readFixture(t, journalPath)) {
		t.Fatal("old replay displaced latest decision")
	}
	var journal reviewJournal
	if err := json.Unmarshal(secondJournal, &journal); err != nil || len(journal.Records) != 2 || journal.Records[0] != string(readFixture(t, firstPath)) || journal.Records[1] != string(readFixture(t, secondPath)) {
		t.Fatal("history bytes changed", err)
	}
	runOK(t, "report", config, "review")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.HostReview.State != "current" || report.HostReconciliationRequired || report.SemanticCompletenessProven || !reflect.DeepEqual(report.HostReview.Latest, &second) || len(report.HostReview.History) != 2 {
		t.Fatal("new host judgment lost or overclaimed")
	}
	for i, row := range report.Requirements {
		if len(row.Roles) != len(view.Entries) {
			t.Fatal("report lost role")
		}
		for j, role := range row.Roles {
			if role.Role != view.Entries[j].Task.Role || !reflect.DeepEqual(role.Assessment, &view.Entries[j].Result.Assessments[i]) {
				t.Fatal("host decision rewrote a role")
			}
			for _, execution := range role.Executions {
				if execution.State != "not_recorded" {
					t.Fatal("invented runtime")
				}
			}
		}
	}
	if !bytes.Equal(beforeState, readFixture(t, filepath.Join(base, "runs/review/state.json"))) {
		t.Fatal("review/report changed state")
	}
	page := readFixture(t, filepath.Join(base, "runs/review/report.html"))
	for _, want := range []string{first.ReviewID, second.ReviewID, "Согласование хоста — current", "weak", "unknown", "not_recorded", view.Entries[0].Result.Assessments[0].Statement, redteam.Assessments[0].Statement, second.Assessments[0].Statement} {
		if !bytes.Contains(page, []byte(html.EscapeString(want))) {
			t.Fatal("HTML lost disagreement/history", want)
		}
	}
	if bytes.Contains(page, []byte("<independent choice>")) {
		t.Fatal("host statement not escaped")
	}
}
func TestReviewOrphanRaw(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	task := batch.Tasks[0]
	result := sampleResult(t, task, filepath.Join(base, "source"))
	original := append([]byte(" \r\n"), legacyMarshal(t, result)...)
	stored := filepath.Join(base, "runs/review/results", task.TaskID+"-attempt-1.json")
	writeFixture(t, stored, original)
	submitted := filepath.Join(base, "result.json")
	writeFixture(t, submitted, legacyMarshal(t, result))
	runOK(t, "submit", config, "review", task.TaskID, submitted)
	if !bytes.Equal(original, readFixture(t, stored)) {
		t.Fatal("orphan first bytes changed")
	}
	view := runOK(t, "review", config, "review").(ReviewContext)
	if view.Entries[0].RawSHA256 != digest(original) {
		t.Fatal("orphan hash lost")
	}
}
func TestReviewAcceptedAmbiguity(t *testing.T) {
	config, base := acceptedFixture(t)
	candidate := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
	candidate.Clarity, candidate.Unresolved = "ambiguous", []string{"Срок не согласован"}
	raw, decision := acceptedInputs(t, config, "seed", []legacyCandidate{candidate}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		req := task.Requirements[0]
		result := Result{task.TaskID, 1, task.SnapshotID, task.Role, task.Scope, "Недостаточно данных", []Assessment{{req.ID, "ambiguous", "unknown", "unknown", "Неизвестный срок остаётся открытым", req.Accepted.Citations, []Citation{}, []TestCitation{}, []string{"Реализация не проверена"}}}, []string{}}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	review := reviewInput(t, config)
	review.Assessments[0].Specification = "clear"
	runFail(t, "review", config, "review", writeReviewInput(t, base, review))
	review.Assessments[0].Specification = "ambiguous"
	runOK(t, "review", config, "review", writeReviewInput(t, base, review))
	raw, decision = acceptedInputs(t, config, "rebind", []legacyCandidate{candidate}, acceptOperation("rebind", []string{batch.Tasks[0].Requirements[0].ID}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	if got := runOK(t, "review", config, "review").(ReviewContext); got.State != "outdated" || got.Freshness != "stale" {
		t.Fatal("accepted-head drift hidden")
	}
}

func TestReviewJournalByteBoundary(t *testing.T) {
	config, base, _ := reviewedFixture(t, false)
	prototype := reviewInput(t, config)
	journal := reviewJournal{1, []string{}}
	// Synthetic historical records exercise serialized bounds, not observed model work.
	for i := 0; i < 10; i++ {
		record := prototype
		record.ReviewID = fmt.Sprintf("seed-%d", i)
		record.Summary = strings.Repeat("x", 3<<20)
		journal.Records = append(journal.Records, string(legacyMarshal(t, record)))
	}
	journalPath := filepath.Join(base, "runs/review/host-reviews.json")
	writeFixture(t, journalPath, legacyMarshal(t, journal))
	proposal := reviewInput(t, config)
	proposal.ReviewID = "boundary"
	encode := func(padding int) []byte {
		record := proposal
		record.Summary = strings.Repeat("x", padding)
		return legacyMarshal(t, record)
	}
	probe := reviewJournal{1, append(append([]string{}, journal.Records...), string(encode(0)))}
	body, err := json.MarshalIndent(probe, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	padding := maxState - len(body) - 1
	if padding <= 0 || len(encode(padding)) > maxResult {
		t.Fatal("invalid boundary fixture")
	}
	before := readFixture(t, journalPath)
	input := filepath.Join(base, "boundary.json")
	writeFixture(t, input, encode(padding+1))
	runFail(t, "review", config, "review", input)
	if !bytes.Equal(before, readFixture(t, journalPath)) {
		t.Fatal("oversize review published")
	}
	writeFixture(t, input, encode(padding))
	runOK(t, "review", config, "review", input)
	info, err := os.Stat(journalPath)
	if err != nil || info.Size() != maxState {
		t.Fatal("exact journal boundary not tested", err)
	}
}

func TestReviewHistoricalCanonical(t *testing.T) {
	config, base := fixture(t)
	writeFixture(t, config, bytes.Replace(readFixture(t, config), []byte("runtime: {kind: none}"), []byte("runtime: {kind: go, paths: ['.'], tests: [], timeout_seconds: 30}"), 1))
	for _, file := range []string{"manifest.json", "state.json"} {
		writeFixture(t, filepath.Join(base, "runs/blind-v1", file), readFixture(t, "../acceptance/declared-v01/"+file))
	}
	view := runOK(t, "review", config, "blind-v1").(ReviewContext)
	decision := ReviewDecision{1, "legacy-review", "blind-v1", view.SnapshotID, view.BasisSHA256, "host", "Historical canonical evidence only", view.Entries[0].Result.Assessments, []string{"Original raw formatting unavailable"}}
	path := writeReviewInput(t, base, decision)
	runOK(t, "review", config, "blind-v1", path)
	runOK(t, "report", config, "blind-v1")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/blind-v1/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.HostReview.State != "current" || len(report.RawProvenance) != 2 {
		t.Fatal("legacy review missing")
	}
	for _, p := range report.RawProvenance {
		if p.Kind != "legacy_canonical" || p.SHA256 != "" {
			t.Fatal("invented original raw")
		}
	}
}

func TestReviewLimits(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		config, base, _ := reviewedFixture(t, false)
		for i := 0; i < 64; i++ {
			decision := reviewInput(t, config)
			decision.ReviewID = fmt.Sprintf("decision-%d", i)
			runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
		}
		before := readFixture(t, filepath.Join(base, "runs/review/host-reviews.json"))
		decision := reviewInput(t, config)
		decision.ReviewID = "overflow"
		runFail(t, "review", config, "review", writeReviewInput(t, base, decision))
		if !bytes.Equal(before, readFixture(t, filepath.Join(base, "runs/review/host-reviews.json"))) {
			t.Fatal("overflow changed journal")
		}
	})
}
