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
	"sort"
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

func reviewV2Input(t *testing.T, config, id, concur string) ReviewDecisionV2 {
	t.Helper()
	view := runOK(t, "review", config, "review").(ReviewContext)
	verdicts := []ReviewVerdict{}
	for _, a := range view.Entries[0].Result.Assessments {
		verdicts = append(verdicts, ReviewVerdict{a.RequirementID, a.Specification, a.Implementation, a.Assertion, concur, "host: принимает свидетельства роли", []string{}})
	}
	return ReviewDecisionV2{2, id, "review", view.SnapshotID, view.BasisSHA256, "host-session", "Согласование по вердиктам", verdicts, countVerdicts(verdicts), []string{"Суждение хоста; не сертификат"}}
}

// tool-spec §23 / REQ-SA-044: a version 2 decision carries verdicts and adopts the named roles' evidence.
func TestReviewV2(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			// The redteam row for the first norm carries no test evidence, so adoption by role is observable.
			result.Assessments[0].Assertion, result.Assessments[0].Tests = "unknown", []TestCitation{}
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	journalPath := filepath.Join(base, "runs/review/host-reviews.json")
	write := func(decision ReviewDecisionV2) string {
		path := filepath.Join(base, "host-"+decision.ReviewID+".json")
		writeFixture(t, path, legacyMarshal(t, decision))
		return path
	}
	// A version 1 decision goes first, so the journal holds both forms (§23) and every later history count includes it.
	runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
	if v1 := runOK(t, "review", config, "review").(ReviewContext); v1.Form != "assessments" || v1.Concur != nil {
		t.Fatal("контекст после version 1", v1.Form, v1.Concur)
	}
	both := reviewV2Input(t, config, "v2-both", "both")
	bothPath := write(both)
	accepted := runOK(t, "review", config, "review", bothPath).(map[string]any)
	if accepted["accepted"] != true || accepted["review_state"] != "current" {
		t.Fatal("version 2 должна приниматься", accepted)
	}
	var journal reviewJournal
	if err := json.Unmarshal(readFixture(t, journalPath), &journal); err != nil || len(journal.Records) != 2 || journal.Records[1] != string(readFixture(t, bothPath)) {
		t.Fatal("журнал должен хранить точные байты version 2 после version 1", err)
	}
	view := runOK(t, "review", config, "review").(ReviewContext)
	if view.State != "current" || view.Form != "verdicts" || view.Concur["REQ-DEMO-001"] != "both" || view.Latest.Version != 2 {
		t.Fatal("контекст после version 2", view.State, view.Form, view.Concur)
	}
	first := view.Latest.Assessments[0]
	if first.RequirementID != "REQ-DEMO-001" || len(first.Spec) != 1 || len(first.Code) != 1 || len(first.Tests) != 1 || first.Statement != "host: принимает свидетельства роли" {
		t.Fatal("both должен объединить свидетельства ролей без повторов", first)
	}
	// concur: redteam adopts only the redteam evidence (no tests for the first norm).
	redteam := reviewV2Input(t, config, "v2-redteam", "redteam")
	runOK(t, "review", config, "review", write(redteam))
	view = runOK(t, "review", config, "review").(ReviewContext)
	if len(view.History) != 3 || len(view.Latest.Assessments[0].Tests) != 0 || len(view.Latest.Assessments[0].Code) != 1 || view.Concur["REQ-DEMO-002"] != "redteam" {
		t.Fatal("redteam должен давать только свои свидетельства", view.Latest.Assessments[0])
	}
	// Refusals: counts, concur, coverage, statement, version, stale basis — the journal stays as written.
	beforeJournal := readFixture(t, journalPath)
	refuse := func(name string, mutate func(d *ReviewDecisionV2), fragment string) {
		t.Helper()
		decision := reviewV2Input(t, config, "v2-"+name, "mapper")
		mutate(&decision)
		_, err := execute([]string{"review", config, "review", write(decision)})
		if err == nil || !strings.Contains(err.Error(), fragment) {
			t.Fatalf("%s: ожидался отказ %q, получено %v", name, fragment, err)
		}
	}
	refuse("counts", func(d *ReviewDecisionV2) { d.Counts.Relevant++ }, "counts.relevant")
	refuse("concur", func(d *ReviewDecisionV2) { d.Verdicts[0].Concur = "host" }, "concur")
	refuse("missing", func(d *ReviewDecisionV2) { d.Verdicts = d.Verdicts[1:]; d.Counts = countVerdicts(d.Verdicts) }, "ровно один вердикт")
	refuse("duplicate", func(d *ReviewDecisionV2) { d.Verdicts[1] = d.Verdicts[0]; d.Counts = countVerdicts(d.Verdicts) }, "повторной нормы")
	refuse("statement", func(d *ReviewDecisionV2) { d.Verdicts[2].Statement = " " }, "statement")
	refuse("version", func(d *ReviewDecisionV2) { d.Version = 4 }, "неверная версия")
	refuse("stale", func(d *ReviewDecisionV2) { d.BasisSHA256 = both.BasisSHA256 }, "текущая база")
	if !bytes.Equal(beforeJournal, readFixture(t, journalPath)) {
		t.Fatal("отказы изменили журнал")
	}
	// An accepted ambiguous norm cannot be judged clear in a verdict (same rule as §7).
	ambiguous := Requirement{ID: "REQ-AI-001", Accepted: &AcceptedDetails{Clarity: "ambiguous"}}
	if err := checkVerdict(ReviewVerdict{"REQ-AI-001", "clear", "unknown", "unknown", "mapper", "s", []string{}}, ambiguous); err == nil {
		t.Fatal("принятая неоднозначность требует ambiguous")
	}
	// Report renders the version 2 decision beside the adopted evidence; retry makes it outdated but readable.
	runOK(t, "report", config, "review")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.HostReview.State != "current" || report.HostReview.Form != "verdicts" || report.HostReview.Latest == nil || len(report.HostReview.Latest.Assessments) != 5 || len(report.HostReview.Latest.Assessments[1].Code) != 1 || report.HostReview.Concur["REQ-DEMO-003"] != "redteam" {
		t.Fatalf("отчёт должен показать решение version 2 с принятыми свидетельствами: state=%s form=%s", report.HostReview.State, report.HostReview.Form)
	}
	if report.Requirements[0].Navigation.HostConcur != "свидетельства redteam" || report.Requirements[4].Navigation.HostConcur != "свидетельства redteam" {
		t.Fatal("навигация должна называть принятые свидетельства", report.Requirements[0].Navigation.HostConcur)
	}
	// tool-spec §36.1: the host's states per norm are readable from report.json without walking host_review.latest.
	if a := report.HostReview.Latest.Assessments[0]; report.Requirements[0].Navigation.HostStates != a.Specification+"/"+a.Implementation+"/"+a.Assertion {
		t.Fatal("навигация должна нести состояния вердикта хоста", report.Requirements[0].Navigation.HostStates)
	}
	if html := readFixture(t, filepath.Join(base, "runs/review/report.html")); !bytes.Contains(html, []byte("Решение хоста опирается на свидетельства redteam")) || !bytes.Contains(html, []byte("Форма решения — вердикты (version 2)")) || !bytes.Contains(html, []byte("не согласие роли с вердиктом")) {
		t.Fatal("HTML без подписи concur/формы решения")
	}
	// §23.1: the derived role list mirrors entries without repeating their bodies.
	roles := view.Roles
	if len(roles) != 2 || roles[0].TaskID != batch.Tasks[0].TaskID || roles[0].Role != batch.Tasks[0].Role || roles[0].Scope != "all" || roles[0].Attempt != 1 || !roles[0].Submitted || roles[0].RawSHA256 == "" {
		t.Fatal("roles[] должен описывать задания", roles)
	}
	runOK(t, "retry", config, "review", batch.Tasks[0].TaskID)
	after := runOK(t, "review", config, "review").(ReviewContext)
	if after.State != "outdated" || len(after.History) != 3 || after.Latest == nil {
		t.Fatal("retry должен сделать решение outdated, сохранив историю", after.State, len(after.History))
	}
	if after.Roles[0].Submitted || after.Roles[0].Attempt != 2 || after.Roles[0].RawSHA256 != "" || !after.Roles[1].Submitted {
		t.Fatal("roles[] после retry", after.Roles)
	}
	// §14 answers still hold for version 2 while a role is pending: the same bytes are a duplicate, new bytes wait for delivery.
	journalAfterRetry := readFixture(t, journalPath)
	duplicate := runOK(t, "review", config, "review", bothPath).(map[string]any)
	if duplicate["duplicate"] != true || duplicate["review_state"] != "outdated" || duplicate["latest_review_id"] != redteam.ReviewID {
		t.Fatal("повтор тех же байт version 2 после retry — duplicate", duplicate)
	}
	pendingDecision := both
	pendingDecision.ReviewID, pendingDecision.BasisSHA256 = "v2-pending", after.BasisSHA256
	if _, err := execute([]string{"review", config, "review", write(pendingDecision)}); err == nil || !strings.Contains(err.Error(), "нужны все ответы ролей") {
		t.Fatal("version 2 при недоставленной роли должна ждать доставки", err)
	}
	if !bytes.Equal(journalAfterRetry, readFixture(t, journalPath)) {
		t.Fatal("журнал изменился после duplicate/отказа")
	}
	runOK(t, "report", config, "review")
}

// tool-spec §24.1: outcomes[] puts both roles' states side by side; citations stay in entries.
func TestReviewOutcomes(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			result.Assessments[0].Assertion = "weak"
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	view := runOK(t, "review", config, "review").(ReviewContext)
	if len(view.Outcomes) != len(view.Requirements) || view.Outcomes[0].RequirementID != view.Requirements[0].ID || view.Outcomes[0].Scope != "all" {
		t.Fatal("outcomes[] должен идти по нормам manifest со scope задания", view.Outcomes)
	}
	first := view.Outcomes[0]
	if first.Agree || first.Roles["mapper"].Assertion == "weak" || first.Roles["redteam"].Assertion != "weak" || first.Roles["redteam"].TaskID != batch.Tasks[1].TaskID || first.Roles["redteam"].Attempt != 1 {
		t.Fatal("расхождение по assertion должно давать agree:false с обеими тройками", first)
	}
	// tool-spec §27.2: identical citations leave only_here empty; the counts describe each role's evidence.
	for _, row := range view.Outcomes[1:] {
		if !row.Agree || len(row.Roles) != 2 {
			t.Fatal("совпадающие роли должны давать agree:true", row)
		}
		for role, outcome := range row.Roles {
			if len(outcome.OnlyHere.Spec)+len(outcome.OnlyHere.Code)+len(outcome.OnlyHere.Tests) != 0 || outcome.Citations.Spec != 1 || outcome.Citations.Code != 1 {
				t.Fatal("общие цитаты не попадают в only_here", role, outcome)
			}
		}
	}
	runOK(t, "retry", config, "review", batch.Tasks[0].TaskID)
	after := runOK(t, "review", config, "review").(ReviewContext)
	for _, row := range after.Outcomes {
		if _, ok := row.Roles["mapper"]; ok || row.Agree || row.Scope != "all" {
			t.Fatal("после retry роль без результата отсутствует и agree:false", row)
		}
		if redteam := row.Roles["redteam"]; len(redteam.OnlyHere.Code) != redteam.Citations.Code || len(redteam.OnlyHere.Spec) != redteam.Citations.Spec {
			t.Fatal("без другой роли only_here — все цитаты", redteam)
		}
	}
	if after.Roles[0].Submitted || !reflect.DeepEqual(after.Entries[1], view.Entries[1]) {
		t.Fatal("roles[] после retry; entries прежней формы", after.Roles)
	}
}

// tool-spec §24 / REQ-SA-045: draft prints an empty version 2 decision for the current basis and publishes nothing.
func TestReviewDraft(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	runFail(t, "draft", config, "review")
	for _, task := range batch.Tasks {
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	versionsPath := filepath.Join(base, "runs/review/tool-versions.json")
	versionsBefore := readFixture(t, versionsPath)
	draft := runOK(t, "draft", config, "review").(ReviewDecisionV3)
	view := runOK(t, "review", config, "review").(ReviewContext)
	if draft.Version != 3 || draft.ReviewID != "host-review-001" || draft.RunID != "review" || draft.SnapshotID != view.SnapshotID || draft.BasisSHA256 != view.BasisSHA256 || len(draft.Verdicts) != len(view.Requirements) || draft.Counts != (ReviewCounts{}) || draft.Reviewer != "" || draft.Summary != "" || draft.Limitations == nil {
		t.Fatal("черновик должен нести основание и все нормы с пустыми полями хоста", draft)
	}
	for i, verdict := range draft.Verdicts {
		if verdict.RequirementID != view.Requirements[i].ID || verdict.Specification != "" || verdict.Concur != "" || verdict.Statement != "" || verdict.Limitations == nil || verdict.Spec == nil || verdict.Code == nil || verdict.Tests == nil {
			t.Fatal("вердикт черновика", verdict)
		}
	}
	if !bytes.Equal(versionsBefore, readFixture(t, versionsPath)) {
		t.Fatal("draft не должен писать провенанс")
	}
	// The serialized draft is the exact form review accepts once filled; an unfilled one is refused without a record.
	raw := legacyMarshal(t, draft)
	var parsed ReviewDecisionV3
	if err := legacyDecode(raw, &parsed); err != nil || requiredJSON(raw, reflect.TypeOf(parsed)) != nil {
		t.Fatal("черновик должен проходить строгий разбор формы version 3", err)
	}
	draftPath := filepath.Join(base, "draft.json")
	writeFixture(t, draftPath, raw)
	runFail(t, "review", config, "review", draftPath)
	for i := range draft.Verdicts {
		row := view.Outcomes[i].Roles["mapper"]
		draft.Verdicts[i].Specification, draft.Verdicts[i].Implementation, draft.Verdicts[i].Assertion = row.Specification, row.Implementation, row.Assertion
		draft.Verdicts[i].Concur, draft.Verdicts[i].Statement = "both", "host: по совпадающим свидетельствам"
	}
	draft.Reviewer, draft.Summary, draft.Counts = "host", "Заполненный черновик", countVerdicts(baseVerdicts(draft.Verdicts))
	writeFixture(t, draftPath, legacyMarshal(t, draft))
	if accepted := runOK(t, "review", config, "review", draftPath).(map[string]any); accepted["accepted"] != true || accepted["review_state"] != "current" {
		t.Fatal("заполненный черновик должен приниматься", accepted)
	}
	second := runOK(t, "draft", config, "review").(ReviewDecisionV3)
	if second.ReviewID != "host-review-002" || second.BasisSHA256 == draft.BasisSHA256 || second.BasisSHA256 != runOK(t, "review", config, "review").(ReviewContext).BasisSHA256 {
		t.Fatal("второй черновик должен учитывать запись журнала", second.ReviewID)
	}
	runOK(t, "retry", config, "review", batch.Tasks[0].TaskID)
	runFail(t, "draft", config, "review")
	writeFixture(t, filepath.Join(base, "source/rules.md"), append(readFixture(t, filepath.Join(base, "source/rules.md")), []byte("\n<!-- stale -->\n")...))
	if _, err := execute([]string{"draft", config, "review"}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatal("draft на stale-снимке должен отказывать", err)
	}
	// A historical run without dispatch/ is served as before; no directories appear after the fact.
	config, base = fixture(t)
	writeFixture(t, config, bytes.Replace(readFixture(t, config), []byte("runtime: {kind: none}"), []byte("runtime: {kind: go, paths: ['.'], tests: [], timeout_seconds: 30}"), 1))
	for _, file := range []string{"manifest.json", "state.json"} {
		writeFixture(t, filepath.Join(base, "runs/blind-v1", file), readFixture(t, "../acceptance/declared-v01/"+file))
	}
	legacy := runOK(t, "review", config, "blind-v1").(ReviewContext)
	if len(legacy.Outcomes) != len(legacy.Requirements) || !legacy.Outcomes[0].Agree {
		t.Fatal("outcomes на историческом run", legacy.Outcomes)
	}
	if historical := runOK(t, "draft", config, "blind-v1").(ReviewDecisionV3); historical.ReviewID != "host-review-001" || historical.BasisSHA256 != legacy.BasisSHA256 {
		t.Fatal("draft на историческом run", historical.ReviewID)
	}
	if _, err := os.Stat(filepath.Join(base, "runs/blind-v1/dispatch")); !os.IsNotExist(err) {
		t.Fatal("dispatch/ не создаётся задним числом")
	}
}

func reviewV3Input(t *testing.T, config, id, concur string) ReviewDecisionV3 {
	t.Helper()
	view := runOK(t, "review", config, "review").(ReviewContext)
	verdicts := []ReviewVerdictV3{}
	for _, a := range view.Entries[0].Result.Assessments {
		verdicts = append(verdicts, ReviewVerdictV3{a.RequirementID, a.Specification, a.Implementation, a.Assertion, concur, "host: принимает свидетельства роли", []string{}, []Citation{}, []Citation{}, []TestCitation{}})
	}
	return ReviewDecisionV3{3, id, "review", view.SnapshotID, view.BasisSHA256, "host-session", "Согласование по вердиктам с цитатами хоста", verdicts, countVerdicts(baseVerdicts(verdicts)), []string{"Суждение хоста; не сертификат"}}
}

// tool-spec §25 / REQ-SA-046: version 3 carries the host's own citations under the §7 rules, merged with the roles' evidence.
func TestReviewV3(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			result.Assessments[0].Assertion, result.Assessments[0].Tests = "unknown", []TestCitation{}
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	journalPath := filepath.Join(base, "runs/review/host-reviews.json")
	write := func(decision ReviewDecisionV3) string {
		path := filepath.Join(base, "host-"+decision.ReviewID+".json")
		writeFixture(t, path, legacyMarshal(t, decision))
		return path
	}
	// A journal of three forms: version 1, version 2, then version 3.
	runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
	runOK(t, "review", config, "review", func() string {
		d := reviewV2Input(t, config, "v2-both", "both")
		p := filepath.Join(base, "host-v2.json")
		writeFixture(t, p, legacyMarshal(t, d))
		return p
	}())
	empty := reviewV3Input(t, config, "v3-empty", "both")
	runOK(t, "review", config, "review", write(empty))
	view := runOK(t, "review", config, "review").(ReviewContext)
	if view.State != "current" || view.Form != "verdicts" || view.Own != nil || view.Latest.Version != 3 || len(view.History) != 3 || len(view.Latest.Assessments[0].Tests) != 1 {
		t.Fatal("version 3 без собственных цитат должна равняться version 2", view.State, view.Form, view.Own, len(view.History))
	}
	// §27.2: the test citation only the mapper gave for the first norm shows up as its only_here.
	if mapper, redteam := view.Outcomes[0].Roles["mapper"], view.Outcomes[0].Roles["redteam"]; len(mapper.OnlyHere.Tests) != 1 || mapper.OnlyHere.Tests[0].Path != "source_test.go" || mapper.OnlyHere.Tests[0].TestID == "" || redteam.Citations.Tests != 0 || len(redteam.OnlyHere.Tests) != 0 {
		t.Fatal("разность цитат по tests", mapper.OnlyHere, redteam.Citations)
	}
	// The host's own citations: a sub-range of source.go no role cites and a test citation for a norm the redteam left without tests.
	source := readFixture(t, filepath.Join(base, "source/source.go"))
	ownQuote, err := lineQuote(source, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	ownCode := Citation{"source.go", 1, 2, ownQuote}
	testSource := readFixture(t, filepath.Join(base, "source/source_test.go"))
	testQuote, err := lineQuote(testSource, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	ownTest := TestCitation{"host-observed-test", Citation{"source_test.go", 1, 1, testQuote}}
	own := reviewV3Input(t, config, "v3-own", "redteam")
	own.Verdicts[0].Code, own.Verdicts[0].Tests = []Citation{ownCode}, []TestCitation{ownTest}
	own.Verdicts[1].Concur, own.Verdicts[1].Code = "both", []Citation{view.Entries[0].Result.Assessments[1].Code[0]} // duplicate of the mapper's citation
	if accepted := runOK(t, "review", config, "review", write(own)).(map[string]any); accepted["accepted"] != true || accepted["review_state"] != "current" {
		t.Fatal("version 3 с цитатами хоста должна приниматься", accepted)
	}
	view = runOK(t, "review", config, "review").(ReviewContext)
	first, second := view.Latest.Assessments[0], view.Latest.Assessments[1]
	if !view.Own["REQ-DEMO-001"] || !view.Own[second.RequirementID] || len(first.Code) != 2 || first.Code[1] != ownCode || len(first.Tests) != 1 || first.Tests[0] != ownTest || len(second.Code) != 1 {
		t.Fatal("свидетельства = роли ∪ хост без повторов", view.Own, first.Code, first.Tests, second.Code)
	}
	runOK(t, "report", config, "review")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/review/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.Requirements[0].Navigation.HostConcur != "свидетельства redteam и хоста" || report.Requirements[1].Navigation.HostConcur != "свидетельства обеих ролей и хоста" || report.Requirements[2].Navigation.HostConcur != "свидетельства redteam" {
		t.Fatal("подпись должна называть цитаты хоста", report.Requirements[0].Navigation.HostConcur, report.Requirements[1].Navigation.HostConcur)
	}
	if html := readFixture(t, filepath.Join(base, "runs/review/report.html")); !bytes.Contains(html, []byte("свидетельства redteam и хоста")) {
		t.Fatal("HTML без подписи «и хоста»")
	}
	// Refusals leave the journal as written.
	beforeJournal := readFixture(t, journalPath)
	refuse := func(name string, mutate func(d *ReviewDecisionV3), fragment string) {
		t.Helper()
		decision := reviewV3Input(t, config, "v3-"+name, "redteam")
		decision.Verdicts[0].Code, decision.Verdicts[0].Tests = []Citation{ownCode}, []TestCitation{ownTest}
		mutate(&decision)
		_, err := execute([]string{"review", config, "review", write(decision)})
		if err == nil || !strings.Contains(err.Error(), fragment) {
			t.Fatalf("%s: ожидался отказ %q, получено %v", name, fragment, err)
		}
	}
	refuse("quote", func(d *ReviewDecisionV3) { d.Verdicts[0].Code[0].Quote = "not the source" }, "цитата не совпадает")
	refuse("outside", func(d *ReviewDecisionV3) { d.Verdicts[0].Code[0].Path = "missing.go" }, "цитата вне группы code")
	refuse("group", func(d *ReviewDecisionV3) { d.Verdicts[0].Spec = []Citation{ownCode} }, "цитата вне группы spec")
	refuse("limit", func(d *ReviewDecisionV3) {
		for i := 0; i < 129; i++ {
			d.Verdicts[0].Code = append(d.Verdicts[0].Code, ownCode)
		}
	}, "слишком много цитат")
	refuse("tests", func(d *ReviewDecisionV3) { d.Verdicts[0].Tests = []TestCitation{} }, "требует тестовый источник")
	nullSpec := reviewV3Input(t, config, "v3-null", "both")
	raw := bytes.Replace(legacyMarshal(t, nullSpec), []byte(`"spec":[]`), []byte(`"spec":null`), 1)
	nullPath := filepath.Join(base, "host-null.json")
	writeFixture(t, nullPath, raw)
	runFail(t, "review", config, "review", nullPath)
	if !bytes.Equal(beforeJournal, readFixture(t, journalPath)) {
		t.Fatal("отказы изменили журнал")
	}
	runOK(t, "retry", config, "review", batch.Tasks[0].TaskID)
	after := runOK(t, "review", config, "review").(ReviewContext)
	if after.State != "outdated" || len(after.History) != 4 || after.Latest == nil || after.Latest.Version != 3 {
		t.Fatal("retry должен сделать решение outdated, сохранив историю трёх форм", after.State, len(after.History))
	}
	runOK(t, "report", config, "review")
}

// tool-spec §25.2: previous_host names the last host verdict of a sibling run on the same snapshot; nothing is carried over.
func TestReviewPreviousHost(t *testing.T) {
	config, base := fixture(t)
	submitAll := func(runID string) TaskBatch {
		batch := runOK(t, "prepare", config, runID).(TaskBatch)
		for _, task := range batch.Tasks {
			path := filepath.Join(base, runID+"-"+task.TaskID+".json")
			writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
			runOK(t, "submit", config, runID, task.TaskID, path)
		}
		return batch
	}
	decide := func(runID, reviewID string) {
		view := runOK(t, "review", config, runID).(ReviewContext)
		verdicts := []ReviewVerdict{}
		for _, a := range view.Entries[0].Result.Assessments {
			verdicts = append(verdicts, ReviewVerdict{a.RequirementID, a.Specification, a.Implementation, a.Assertion, "both", "host", []string{}})
		}
		verdicts[0].Implementation, verdicts[0].Assertion = "contradicted", "contradicts"
		decision := ReviewDecisionV2{2, reviewID, runID, view.SnapshotID, view.BasisSHA256, "host", "прошлое решение", verdicts, countVerdicts(verdicts), []string{}}
		path := filepath.Join(base, runID+"-"+reviewID+".json")
		writeFixture(t, path, legacyMarshal(t, decision))
		runOK(t, "review", config, runID, path)
	}
	submitAll("a")
	if view := runOK(t, "review", config, "a").(ReviewContext); view.Outcomes[0].PreviousHost != nil || len(view.Outcomes[0].Roles["mapper"].Limitations) != 0 {
		t.Fatal("без соседей previous_host отсутствует; limitations — пустой массив", view.Outcomes[0])
	}
	decide("a", "host-review-001")
	submitAll("b")
	writeFixture(t, filepath.Join(base, "runs/broken/manifest.json"), []byte("{not json"))
	view := runOK(t, "review", config, "b").(ReviewContext)
	prior := view.Outcomes[0].PreviousHost
	if prior == nil || prior.RunID != "a" || prior.ReviewID != "host-review-001" || prior.Implementation != "contradicted" || prior.Assertion != "contradicts" || prior.Statement != "host" || prior.Limitations == nil || len(prior.Limitations) != 0 || view.Outcomes[1].PreviousHost == nil || view.Outcomes[1].PreviousHost.Implementation != "supported" {
		t.Fatal("previous_host должен показать последний вердикт run a", prior)
	}
	if draft := runOK(t, "draft", config, "b").(ReviewDecisionV3); draft.Verdicts[0].Implementation != "" {
		t.Fatal("draft не копирует прошлый вердикт")
	}
	// A later decision on another sibling wins by its review time; a run on a different snapshot sees nothing.
	submitAll("d")
	decide("d", "later-review")
	if view := runOK(t, "review", config, "b").(ReviewContext); view.Outcomes[0].PreviousHost.RunID != "d" || view.Outcomes[0].PreviousHost.ReviewID != "later-review" {
		t.Fatal("выбирается последний по времени записи review", view.Outcomes[0].PreviousHost)
	}
	writeFixture(t, filepath.Join(base, "source/rules.md"), append(readFixture(t, filepath.Join(base, "source/rules.md")), []byte("\n<!-- другой snapshot -->\n")...))
	submitAll("c")
	if view := runOK(t, "review", config, "c").(ReviewContext); view.Outcomes[0].PreviousHost != nil {
		t.Fatal("другой snapshot — previous_host отсутствует", view.Outcomes[0].PreviousHost)
	}
}

// tool-spec §25.2 in accepted mode: the accepted head must match as well as the snapshot.
func TestReviewPreviousHostAccepted(t *testing.T) {
	config, base := acceptedFixture(t)
	c := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
	raw, decision := acceptedInputs(t, config, "initial", []legacyCandidate{c}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	whole := func(path string) Citation {
		data := readFixture(t, filepath.Join(base, "source", path))
		end := len(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"))
		quote, err := lineQuote(data, 1, end)
		if err != nil {
			t.Fatal(err)
		}
		return Citation{path, 1, end, quote}
	}
	submitAll := func(runID string) {
		batch := runOK(t, "prepare", config, runID).(TaskBatch)
		for _, task := range batch.Tasks {
			result := Result{TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope, Summary: "s", Assessments: []Assessment{}, Limitations: []string{}}
			for _, req := range task.Requirements {
				result.Assessments = append(result.Assessments, Assessment{RequirementID: req.ID, Specification: "clear", Implementation: "supported", Assertion: "relevant", Statement: "ok",
					Spec: req.Accepted.Citations, Code: []Citation{whole("source.go")}, Tests: []TestCitation{{"TestLimit", whole("source_test.go")}}, Limitations: []string{"граница проверена частично"}})
			}
			path := filepath.Join(base, runID+"-"+task.TaskID+".json")
			writeFixture(t, path, legacyMarshal(t, result))
			runOK(t, "submit", config, runID, task.TaskID, path)
		}
	}
	submitAll("a")
	view := runOK(t, "review", config, "a").(ReviewContext)
	if len(view.Outcomes[0].AmbiguousOverClear) != 0 {
		t.Fatal("роли не спорят с clarity — список пуст", view.Outcomes[0].AmbiguousOverClear)
	}
	verdicts := []ReviewVerdict{{view.Requirements[0].ID, "clear", "supported", "weak", "both", "host", []string{}}}
	path := filepath.Join(base, "a-decision.json")
	writeFixture(t, path, legacyMarshal(t, ReviewDecisionV2{2, "host-review-001", "a", view.SnapshotID, view.BasisSHA256, "host", "s", verdicts, countVerdicts(verdicts), []string{}}))
	runOK(t, "review", config, "a", path)
	submitAll("b")
	if view := runOK(t, "review", config, "b").(ReviewContext); view.Outcomes[0].PreviousHost == nil || view.Outcomes[0].PreviousHost.Assertion != "weak" || view.Outcomes[0].Roles["mapper"].Limitations[0] != "граница проверена частично" {
		t.Fatal("тот же snapshot и accepted head — previous_host есть, limitations ролей видны", view.Outcomes[0])
	}
	// A new acceptance on unchanged sources moves the head: the same snapshot no longer counts.
	revised := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ включительно", 2, 2)
	raw, decision = acceptedInputs(t, config, "revise", []legacyCandidate{revised}, acceptOperation("revise", []string{view.Requirements[0].ID}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	submitAll("c")
	if view := runOK(t, "review", config, "c").(ReviewContext); view.Outcomes[0].PreviousHost != nil {
		t.Fatal("другой accepted head — previous_host отсутствует", view.Outcomes[0].PreviousHost)
	}
	// tool-spec §40.1: a later package that keeps the norm (§39) changes the head but not the norm — the verdict of run c
	// carries over to the kept norm and not to the new one.
	view = runOK(t, "review", config, "c").(ReviewContext)
	verdicts = []ReviewVerdict{{view.Requirements[0].ID, "clear", "supported", "relevant", "both", "host", []string{}}}
	writeFixture(t, path, legacyMarshal(t, ReviewDecisionV2{2, "host-review-001", "c", view.SnapshotID, view.BasisSHA256, "host", "s", verdicts, countVerdicts(verdicts), []string{}}))
	runOK(t, "review", config, "c", path)
	second := candidateAt(t, base, "rules.md", "C001", "Название карточки обязательно", 3, 3)
	raw, decision = acceptedInputs(t, config, "pkg-2", []legacyCandidate{second}, acceptOperation("keep", []string{view.Requirements[0].ID}), acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, raw, decision)
	submitAll("d")
	kept := runOK(t, "review", config, "d").(ReviewContext)
	if len(kept.Outcomes) != 2 || kept.Outcomes[0].PreviousHost == nil || kept.Outcomes[0].PreviousHost.RunID != "c" || kept.Outcomes[0].PreviousHost.Assertion != "relevant" || kept.Outcomes[1].PreviousHost != nil {
		t.Fatal("после keep previous_host есть у прежней нормы и нет у новой", kept.Outcomes)
	}
	// tool-spec §41.2: a role calling an accepted clear norm ambiguous is accepted by validate and named in outcomes.
	redteam := kept.Entries[1]
	raised := *redteam.Result
	raised.Assessments = append([]Assessment{}, raised.Assessments...)
	raised.Assessments[1].Specification = "ambiguous"
	runOK(t, "retry", config, "d", redteam.Task.TaskID)
	raised.Attempt = 2
	raisedPath := filepath.Join(base, "d-raised.json")
	writeFixture(t, raisedPath, legacyMarshal(t, raised))
	runOK(t, "submit", config, "d", redteam.Task.TaskID, raisedPath)
	flagged := runOK(t, "review", config, "d").(ReviewContext)
	if !reflect.DeepEqual(flagged.Outcomes[1].AmbiguousOverClear, []string{"redteam"}) || flagged.Outcomes[1].Agree || len(flagged.Outcomes[0].AmbiguousOverClear) != 0 {
		t.Fatal("ambiguous над принятой clear назван по роли", flagged.Outcomes[1].AmbiguousOverClear, flagged.Outcomes[0].AmbiguousOverClear)
	}
	// Changed sources: no carry-over even for an identical norm.
	writeFixture(t, filepath.Join(base, "source/source.go"), append(readFixture(t, filepath.Join(base, "source/source.go")), []byte("\n// изменение\n")...))
	submitAll("e")
	if view := runOK(t, "review", config, "e").(ReviewContext); view.Outcomes[0].PreviousHost != nil {
		t.Fatal("другие файлы — previous_host отсутствует", view.Outcomes[0].PreviousHost)
	}
}

// tool-spec §27 / REQ-SA-047: validate … host runs review's checks without writing anything.
func TestReviewValidateHost(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	first := batch.Tasks[0]
	firstPath := filepath.Join(base, first.TaskID+".json")
	writeFixture(t, firstPath, legacyMarshal(t, sampleResult(t, first, filepath.Join(base, "source"))))
	runOK(t, "submit", config, "review", first.TaskID, firstPath)
	// Before full delivery the answer is review's refusal, not a false valid.
	early := reviewV2Input(t, config, "early", "mapper")
	earlyPath := filepath.Join(base, "early.json")
	writeFixture(t, earlyPath, legacyMarshal(t, early))
	if _, err := execute([]string{"validate", config, "review", "host", earlyPath}); err == nil || !strings.Contains(err.Error(), "нужны все ответы ролей") {
		t.Fatal("validate host до полной доставки", err)
	}
	for _, task := range batch.Tasks[1:] {
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	source := readFixture(t, filepath.Join(base, "source/source.go"))
	ownQuote, err := lineQuote(source, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	decision := reviewV3Input(t, config, "v3-checked", "both")
	decision.Verdicts[0].Code = []Citation{{"source.go", 1, 2, ownQuote}}
	write := func(d ReviewDecisionV3, name string) string {
		path := filepath.Join(base, name+".json")
		writeFixture(t, path, legacyMarshal(t, d))
		return path
	}
	journalPath := filepath.Join(base, "runs/review/host-reviews.json")
	versionsPath := filepath.Join(base, "runs/review/tool-versions.json")
	versionsBefore := readFixture(t, versionsPath)
	checked := runOK(t, "validate", config, "review", "host", write(decision, "ok")).(map[string]any)
	if checked["valid"] != true || checked["duplicate"] != false || checked["review_state"] != "current" || checked["version"] != 3 || checked["requirements"] != len(batch.Tasks[0].Requirements) || !reflect.DeepEqual(checked["own_citations"], []string{"REQ-DEMO-001"}) {
		t.Fatal("validate host должен обещать запись", checked)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatal("validate host не должен создавать журнал")
	}
	if !bytes.Equal(versionsBefore, readFixture(t, versionsPath)) {
		t.Fatal("validate host не должен писать провенанс")
	}
	// The same refusals as review, none of them writing.
	refuse := func(name string, mutate func(d *ReviewDecisionV3), fragment string) {
		t.Helper()
		d := reviewV3Input(t, config, "v3-"+name, "both")
		d.Verdicts[0].Code = []Citation{{"source.go", 1, 2, ownQuote}}
		mutate(&d)
		path := write(d, name)
		_, verr := execute([]string{"validate", config, "review", "host", path})
		_, rerr := execute([]string{"review", config, "review", path})
		if verr == nil || rerr == nil || verr.Error() != rerr.Error() || !strings.Contains(verr.Error(), fragment) {
			t.Fatalf("%s: validate=%v review=%v", name, verr, rerr)
		}
	}
	refuse("counts", func(d *ReviewDecisionV3) { d.Counts.Relevant++ }, "counts.relevant")
	refuse("quote", func(d *ReviewDecisionV3) { d.Verdicts[0].Code[0].Quote = "nope" }, "цитата не совпадает")
	refuse("stale", func(d *ReviewDecisionV3) { d.BasisSHA256 = strings.Repeat("0", 64) }, "текущая база")
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatal("отказы не должны создавать журнал")
	}
	// review accepts the validated bytes; afterwards validate reports a duplicate, and a changed body a conflict.
	okPath := filepath.Join(base, "ok.json")
	if accepted := runOK(t, "review", config, "review", okPath).(map[string]any); accepted["accepted"] != true || accepted["review_state"] != "current" {
		t.Fatal("проверенное решение должно записаться", accepted)
	}
	if again := runOK(t, "validate", config, "review", "host", okPath).(map[string]any); again["valid"] != true || again["duplicate"] != true || again["review_state"] != "current" {
		t.Fatal("те же байты после записи — duplicate", again)
	}
	decision.Summary = "другие байты"
	if _, err := execute([]string{"validate", config, "review", "host", write(decision, "conflict")}); err == nil || !strings.Contains(err.Error(), "конфликт review_id") {
		t.Fatal("тот же review_id с другими байтами — конфликт", err)
	}
	if _, err := execute([]string{"submit", config, "review", "host", okPath}); err == nil || !strings.Contains(err.Error(), "неизвестный TASK_ID") {
		t.Fatal("host — псевдо-задание только у validate", err)
	}
}

// tool-spec §28.3: an empty verdict statement is accepted only when the host merely concurs with both roles.
func TestReviewAgreedStatement(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			result.Assessments[1].Assertion, result.Assessments[1].Tests = "unknown", []TestCitation{} // the second norm disagrees between the roles
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	write := func(d ReviewDecisionV3, name string) string {
		path := filepath.Join(base, name+".json")
		writeFixture(t, path, legacyMarshal(t, d))
		return path
	}
	agreed := reviewV3Input(t, config, "agreed", "both")
	agreed.Verdicts[0].Statement = ""
	if checked := runOK(t, "validate", config, "review", "host", write(agreed, "agreed")).(map[string]any); checked["valid"] != true {
		t.Fatal("пустой statement при both и совпадении", checked)
	}
	runOK(t, "review", config, "review", filepath.Join(base, "agreed.json"))
	view := runOK(t, "review", config, "review").(ReviewContext)
	if view.Latest.Assessments[0].Statement != agreedStatement || view.Latest.Assessments[1].Statement == agreedStatement {
		t.Fatal("стандартный текст только у согласной нормы", view.Latest.Assessments[0].Statement, view.Latest.Assessments[1].Statement)
	}
	runOK(t, "report", config, "review")
	if html := readFixture(t, filepath.Join(base, "runs/review/report.html")); !bytes.Contains(html, []byte(agreedStatement)) {
		t.Fatal("HTML без стандартного statement")
	}
	journalBefore := readFixture(t, filepath.Join(base, "runs/review/host-reviews.json"))
	refuse := func(name string, mutate func(d *ReviewDecisionV3)) {
		t.Helper()
		d := reviewV3Input(t, config, "bad-"+name, "both")
		mutate(&d)
		if _, err := execute([]string{"review", config, "review", write(d, name)}); err == nil || !strings.Contains(err.Error(), "пустой statement допустим только") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	refuse("disagree", func(d *ReviewDecisionV3) { d.Verdicts[1].Statement = "" })                               // redteam said weak, verdict relevant
	refuse("concur", func(d *ReviewDecisionV3) { d.Verdicts[0].Statement, d.Verdicts[0].Concur = "", "mapper" }) // not both
	if !bytes.Equal(journalBefore, readFixture(t, filepath.Join(base, "runs/review/host-reviews.json"))) {
		t.Fatal("отказы изменили журнал")
	}
}

// tool-spec §29.1: review CONFIG RUN_ID REQ-ID reads one norm — both roles, the outcome row and the host's verdict.
func TestReviewRequirementView(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	runFail(t, "review", config, "review", "REQ-DEMO-999")
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			result.Assessments[0].Assertion, result.Assessments[0].Tests = "unknown", []TestCitation{}
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	versionsPath := filepath.Join(base, "runs/review/tool-versions.json")
	versionsBefore := readFixture(t, versionsPath)
	before := runOK(t, "review", config, "review", "REQ-DEMO-001").(RequirementView)
	if before.Host != nil || before.Requirement.ID != "REQ-DEMO-001" || before.Outcome.Agree || len(before.Roles) != 2 || before.Roles["mapper"].Statement == "" || len(before.Roles["mapper"].Tests) != 1 || len(before.Roles["redteam"].Tests) != 0 || before.Freshness != "fresh" {
		t.Fatal("чтение нормы до решения", before)
	}
	if !bytes.Equal(versionsBefore, readFixture(t, versionsPath)) {
		t.Fatal("чтение нормы не должно писать провенанс")
	}
	source := readFixture(t, filepath.Join(base, "source/source.go"))
	quote, err := lineQuote(source, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	decision := reviewV3Input(t, config, "v3-view", "redteam")
	decision.Verdicts[0].Code = []Citation{{"source.go", 1, 2, quote}}
	decision.Verdicts[0].Tests = before.Roles["mapper"].Tests
	path := filepath.Join(base, "v3-view.json")
	writeFixture(t, path, legacyMarshal(t, decision))
	accepted := runOK(t, "review", config, "review", path).(map[string]any)
	if accepted["version"] != 3 || accepted["requirements"] != len(batch.Tasks[0].Requirements) || !reflect.DeepEqual(accepted["own_citations"], []string{"REQ-DEMO-001"}) {
		t.Fatal("ответ review должен повторять version/requirements/own_citations (§29.2)", accepted)
	}
	after := runOK(t, "review", config, "review", "REQ-DEMO-001").(RequirementView)
	if after.Host == nil || after.Host.ReviewID != "v3-view" || after.Host.State != "current" || after.Host.Form != "verdicts" || !after.Host.OwnCitations || len(after.Host.Code) != 2 || len(after.Host.Tests) != 1 || after.Outcome.PreviousHost != nil {
		t.Fatal("чтение нормы после решения", after.Host)
	}
	if duplicate := runOK(t, "review", config, "review", path).(map[string]any); duplicate["duplicate"] != true || duplicate["version"] != 3 || !reflect.DeepEqual(duplicate["own_citations"], []string{"REQ-DEMO-001"}) {
		t.Fatal("duplicate-ответ тоже симметричен", duplicate)
	}
	// tool-spec §33.4: the text form carries the same facts in one readable block inside the JSON envelope.
	text := runOK(t, "review", config, "review", "REQ-DEMO-001", "text").(map[string]string)["text"]
	for _, want := range []string{"REQ-DEMO-001 — ", "[mapper] ", "[redteam] ", "[host] ", "v3-view", after.Roles["mapper"].Statement, "code source.go:1-2", "test " + after.Roles["mapper"].Tests[0].TestID, "agree=false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("текст нормы не содержит %q:\n%s", want, text)
		}
	}
	brief := runOK(t, "review", config, "review", "REQ-DEMO-001", "brief").(map[string]string)["text"]
	if strings.Contains(brief, "code source.go:1-2") || strings.Contains(brief, "запуск ") || !strings.Contains(brief, "цитаты: spec ") || !strings.Contains(brief, after.Roles["mapper"].Statement) || !strings.Contains(brief, "[host] ") || !strings.Contains(text, "запуск ") {
		t.Fatalf("brief держит суждения и считает цитаты:\n%s", brief)
	}
	runFail(t, "review", config, "review", "REQ-DEMO-001", "html")
}

// tool-spec §30.2: the citation index says who cites which lines under which norm; the host part is its own citations only.
func TestReviewCitations(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	if rows := runOK(t, "review", config, "review", "citations").([]CitationRow); len(rows) != 0 {
		t.Fatal("до доставки индекс пуст, но отвечает", rows)
	}
	runFail(t, "review", config, "review", "citations", "a", "b")
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			result.Assessments[0].Assertion, result.Assessments[0].Tests = "unknown", []TestCitation{}
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	versionsPath := filepath.Join(base, "runs/review/tool-versions.json")
	versionsBefore := readFixture(t, versionsPath)
	all := runOK(t, "review", config, "review", "citations").([]CitationRow)
	want := 0
	for _, entry := range runOK(t, "review", config, "review").(ReviewContext).Entries {
		for _, a := range entry.Result.Assessments {
			want += len(a.Spec) + len(a.Code) + len(a.Tests)
		}
	}
	if len(all) != want || !sort.SliceIsSorted(all, func(i, j int) bool {
		a, b := all[i], all[j]
		return a.Path < b.Path || a.Path == b.Path && (a.LineStart < b.LineStart || a.LineStart == b.LineStart && (a.LineEnd < b.LineEnd || a.LineEnd == b.LineEnd && (a.Role < b.Role || a.Role == b.Role && a.RequirementID < b.RequirementID)))
	}) {
		t.Fatal("индекс должен перечислить все цитаты ролей в заданном порядке", len(all), want)
	}
	if !bytes.Equal(versionsBefore, readFixture(t, versionsPath)) {
		t.Fatal("индекс цитат не должен писать провенанс")
	}
	source := runOK(t, "review", config, "review", "citations", "source.go").([]CitationRow)
	roles := map[string]int{}
	for _, row := range source {
		if row.Path != "source.go" || row.Kind != "code" || row.TestID != "" || row.TaskID == "" || row.RequirementID == "" {
			t.Fatal("строка индекса по файлу", row)
		}
		roles[row.Role]++
	}
	if roles["mapper"] == 0 || roles["redteam"] == 0 || roles["host"] != 0 {
		t.Fatal("по файлу — обе роли, хоста ещё нет", roles)
	}
	if rows := runOK(t, "review", config, "review", "citations", "nowhere.go").([]CitationRow); len(rows) != 0 {
		t.Fatal("неизвестный путь даёт пустой список", rows)
	}
	// tool-spec §33.2: the same argument filters by norm or by role.
	byNorm := runOK(t, "review", config, "review", "citations", "REQ-DEMO-001").([]CitationRow)
	byRole := runOK(t, "review", config, "review", "citations", "redteam").([]CitationRow)
	if len(byNorm) == 0 || len(byRole) == 0 || len(byNorm)+len(byRole) >= len(all) {
		t.Fatal("фильтры по норме и роли сужают индекс", len(byNorm), len(byRole), len(all))
	}
	for _, row := range byNorm {
		if row.RequirementID != "REQ-DEMO-001" {
			t.Fatal("фильтр по норме", row)
		}
	}
	for _, row := range byRole {
		if row.Role != "redteam" {
			t.Fatal("фильтр по роли", row)
		}
	}
	quote, err := lineQuote(readFixture(t, filepath.Join(base, "source/source.go")), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	decision := reviewV3Input(t, config, "v3-cite", "redteam")
	decision.Verdicts[0].Code = []Citation{{"source.go", 1, 2, quote}}
	decision.Verdicts[0].Tests = runOK(t, "review", config, "review", "REQ-DEMO-001").(RequirementView).Roles["mapper"].Tests
	path := filepath.Join(base, "v3-cite.json")
	writeFixture(t, path, legacyMarshal(t, decision))
	runOK(t, "review", config, "review", path)
	host := []CitationRow{}
	for _, row := range runOK(t, "review", config, "review", "citations").([]CitationRow) {
		if row.Role == "host" {
			host = append(host, row)
		}
	}
	if len(host) != 2 || host[0].Path != "source.go" || host[0].LineStart != 1 || host[0].LineEnd != 2 || host[0].RequirementID != "REQ-DEMO-001" || host[0].TaskID != "" || host[1].Kind != "tests" || host[1].TestID == "" {
		t.Fatal("хост в индексе — только собственные цитаты решения", host)
	}
}

// tool-spec §32.2: `review CONFIG RUN_ID summary` — outcomes, roles and the decision state without entries or bodies.
func TestReviewBrief(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	if brief := runOK(t, "review", config, "review", "summary").(ReviewBrief); brief.DeliveryComplete || len(brief.Roles) != len(batch.Tasks) || brief.State != "missing" || brief.Agree != 0 {
		t.Fatal("сводка до доставки", brief)
	}
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		if task.Role == "redteam" {
			result.Assessments[0].Assertion = "weak"
		}
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	versionsPath := filepath.Join(base, "runs/review/tool-versions.json")
	versionsBefore := readFixture(t, versionsPath)
	full := runOK(t, "review", config, "review").(ReviewContext)
	brief := runOK(t, "review", config, "review", "summary").(ReviewBrief)
	if !brief.DeliveryComplete || brief.RequirementsTotal != len(full.Requirements) || !reflect.DeepEqual(brief.Outcomes, full.Outcomes) || brief.Agree != len(full.Outcomes)-1 || !reflect.DeepEqual(brief.Disagree, []string{"REQ-DEMO-001"}) || brief.BasisSHA256 != full.BasisSHA256 {
		t.Fatal("сводка после доставки", brief.Agree, brief.Disagree)
	}
	raw, err := json.Marshal(brief)
	if err != nil || bytes.Contains(raw, []byte(`"entries"`)) || bytes.Contains(raw, []byte(`"quote"`)) {
		t.Fatal("сводка не несёт entries и цитат", err)
	}
	if !bytes.Equal(versionsBefore, readFixture(t, versionsPath)) {
		t.Fatal("сводка не должна писать провенанс")
	}
	runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
	if brief := runOK(t, "review", config, "review", "summary").(ReviewBrief); brief.State != "current" || len(brief.History) != 1 || brief.Form != "assessments" {
		t.Fatal("сводка после решения", brief.State, brief.Form)
	}
}

// tool-spec §43.1: a scope listing a sibling in `related` sees the sibling's contradicted code lines under its own norms.
func TestReviewContradictedElsewhere(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "review").(TaskBatch)
	for _, task := range batch.Tasks {
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
		runOK(t, "submit", config, "review", task.TaskID, path)
	}
	runOK(t, "review", config, "review", writeReviewInput(t, base, reviewInput(t, config)))
	first := runOK(t, "review", config, "review").(ReviewContext)
	contradicted := ""
	for _, a := range first.Latest.Assessments {
		if a.Implementation == "contradicted" {
			contradicted = a.RequirementID
		}
	}
	if contradicted == "" {
		t.Fatal("фикстура должна содержать contradicted-норму")
	}
	// A second scope over the same sources, related to the first; its roles cite the same code lines.
	other := filepath.Join(base, "other/config.yaml")
	writeFixture(t, other, []byte("version: 1\nproject_root: ../source\nspecs: {paths: [rules.md]}\ncode: {paths: [source.go, go.mod]}\ntests: {paths: [source_test.go]}\nreports_dir: runs\nruntime: {kind: none}\nscopes: []\nrelated: [../config.yaml]\n"))
	batch = runOK(t, "prepare", other, "sibling").(TaskBatch)
	for _, task := range batch.Tasks {
		path := filepath.Join(base, "other-"+task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, filepath.Join(base, "source"))))
		runOK(t, "submit", other, "sibling", task.TaskID, path)
	}
	view := runOK(t, "review", other, "sibling", "summary").(ReviewBrief)
	hits := map[string][]ElsewhereHit{}
	for _, row := range view.Outcomes {
		hits[row.RequirementID] = row.ContradictedElsewhere
	}
	if len(hits[contradicted]) == 0 || hits[contradicted][0].RequirementID != contradicted || hits[contradicted][0].RunID != "review" || !strings.HasSuffix(hits[contradicted][0].Config, "config.yaml") {
		t.Fatal("строки, contradicted в соседнем scope, названы под нормой", hits[contradicted])
	}
	for id, list := range hits {
		for _, hit := range list {
			if hit.RequirementID != contradicted {
				t.Fatal("подсказка только по contradicted-нормам соседа", id, hit)
			}
		}
	}
	text := runOK(t, "review", other, "sibling", contradicted, "brief").(map[string]string)["text"]
	if !strings.Contains(text, "уже contradicted в") {
		t.Fatalf("brief называет память соседнего scope:\n%s", text)
	}
	if plain := runOK(t, "review", config, "review", "summary").(ReviewBrief); len(plain.Outcomes[0].ContradictedElsewhere) != 0 {
		t.Fatal("без related список пуст", plain.Outcomes[0].ContradictedElsewhere)
	}
}
