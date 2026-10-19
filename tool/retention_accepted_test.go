//go:build darwin || linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCitationAcceptedRevisionsAndOfflineHistory(t *testing.T) {
	config, base := acceptedFixture(t)
	citationsConfig(t, config)
	mod := filepath.Join(base, "source/go.mod")
	marker := []byte("UNQUOTED_SOURCE_MARKER_ACCEPTED")
	writeFixture(t, mod, append(readFixture(t, mod), append([]byte("\n// "), marker...)...))
	accept := func(decision, action string, previous []string) {
		candidate := candidateAt(t, base, "rules.md", "C001", "Лимит одного файла", 2, 2)
		raw, choice := acceptedInputs(t, config, decision, []legacyCandidate{candidate}, acceptOperation(action, previous, "C001"))
		runOK(t, "reconcile", config, raw, choice)
	}
	accept("initial", "accept", []string{})
	first := acceptedRead(t, config)
	id := first.Records[0].Requirement.ID
	deliver := func(run string, incremental bool) {
		args := []string{"prepare", config, run}
		if incremental {
			args = append(args, "since", "A")
		}
		batch := runOK(t, args...).(TaskBatch)
		for _, task := range batch.Tasks {
			r := Result{TaskID: task.TaskID, Attempt: task.Attempt, SnapshotID: task.SnapshotID, Role: task.Role, Scope: task.Scope, Summary: "Детерминированная проверка механики accepted, не аудит продукта", Assessments: []Assessment{}, Limitations: []string{}}
			for _, req := range task.Requirements {
				r.Assessments = append(r.Assessments, Assessment{RequirementID: req.ID, Specification: "clear", Implementation: "unknown", Assertion: "unknown", Statement: "Для test data свидетельства реализации не предоставлены", Spec: req.Accepted.Citations, Code: []Citation{}, Tests: []TestCitation{}, Limitations: []string{"Механический fixture"}})
			}
			path := filepath.Join(base, run+"-"+task.TaskID+".json")
			writeFixture(t, path, mustJSON(t, r))
			runOK(t, "submit", config, run, task.TaskID, path)
		}
		view := runOK(t, "review", config, run).(ReviewContext)
		rows := []Assessment{}
		for _, entry := range view.Entries {
			if entry.Task.Role == "mapper" {
				rows = append(rows, entry.Result.Assessments...)
			}
		}
		decision := ReviewDecision{1, "decision-" + run, run, view.SnapshotID, view.BasisSHA256, "test-host", "Детерминированная история", rows, []string{}}
		path := filepath.Join(base, "host-"+run+".json")
		writeFixture(t, path, mustJSON(t, decision))
		runOK(t, "review", config, run, path)
		runOK(t, "report", config, run)
	}
	deliver("A", false)
	rules := filepath.Join(base, "source/rules.md")
	writeFixture(t, rules, bytes.Replace(readFixture(t, rules), []byte("8 МиБ"), []byte("16 МиБ"), 1))
	// A changed statement is explicitly accepted as a new revision of the same ID.
	candidate := candidateAt(t, base, "rules.md", "C001", "Лимит одного файла — 16 МиБ включительно", 2, 2)
	raw, choice := acceptedInputs(t, config, "revision", []legacyCandidate{candidate}, acceptOperation("revise", []string{id}, "C001"))
	runOK(t, "reconcile", config, raw, choice)
	second := acceptedRead(t, config)
	if second.Records[0].Requirement.ID != id || second.Records[0].Requirement.Accepted.Revision != 2 || second.Head == first.Head {
		t.Fatal("accepted identity/revision not preserved")
	}
	deliver("B", true)
	before := runOK(t, "overview", config).(Overview)
	if len(before.Scopes[0].History) != 2 || before.Scopes[0].Decided.Gap.Total != 1 {
		t.Fatal("new spec revision closed a gap or lost history", before)
	}
	site := filepath.Join(base, "site")
	runOK(t, "publish", site, config)
	archive := exportTestBundle(t, config, base, site)
	if err := os.RemoveAll(filepath.Join(base, "source")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "restored")
	restored := runOK(t, "bundle", "restore", archive, destination).(map[string]any)
	cfg := restored["configs"].([]string)[0]
	for _, run := range []string{"A", "B"} {
		view := runOK(t, "review", cfg, run).(ReviewContext)
		if !view.Complete || view.Latest.Assessments[0].RequirementID != id {
			t.Fatal("offline history lost accepted ID")
		}
		runOK(t, "review", cfg, run, "citations")
	}
	state, err := loadConfig(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	_, accepted, err := readAccepted(state.ReportsDir)
	if err != nil || accepted.Head != second.Head {
		t.Fatal("offline accepted ledger lost", err)
	}
	if err := walkBundleArea(destination, func(path string, data []byte, _ uint32) error {
		if bytes.Contains(data, marker) {
			t.Fatalf("unquoted bytes in restored %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runFail(t, "prepare", cfg, "requires-sources")
}

func TestCitationAcceptedExternalReleaseAndRestore(t *testing.T) {
	f := newExternalFixture(t)
	writeFixture(t, f.config, append(readFixture(t, f.config), []byte("source_retention: citations\nindex_mode: accepted\n")...))
	config := f.install(t)
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	set, _, _, err := acceptedSources(cfg)
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateAt(t, f.base, "rules.md", "C001", "Результат должен быть логическим отрицанием входного значения", 9, 9)
	raw := legacyMarshal(t, legacyRaw{1, set, []legacyCandidate{candidate}, []string{}})
	_, prior, err := readAccepted(cfg.ReportsDir)
	if err != nil {
		t.Fatal(err)
	}
	decision := AcceptedDecision{1, "initial", prior.Head, digest(raw), []AcceptedOperation{acceptOperation("accept", []string{}, "C001")}, false, nil}
	rawPath, decisionPath := filepath.Join(f.base, "accepted-raw.json"), filepath.Join(f.base, "accepted-decision.json")
	writeFixture(t, rawPath, raw)
	writeFixture(t, decisionPath, legacyMarshal(t, decision))
	runOK(t, "reconcile", config, rawPath, decisionPath)
	accepted := acceptedRead(t, config)
	completeExternal(t, config, "accepted")
	runOK(t, "external", "release", config)
	runOK(t, "report", config, "accepted")
	result := runOK(t, "external", "result", config, "accepted").(ExternalResult)
	if result.SchemaVersion != 2 || result.AcceptedHead != accepted.Head {
		t.Fatal("external result lost accepted head")
	}
	archive := exportTestBundle(t, config, f.base)
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(f.base, "restored")).(map[string]any)
	restoredConfig := restored["configs"].([]string)[0]
	view := runOK(t, "review", restoredConfig, "accepted").(ReviewContext)
	if !view.Complete || view.Requirements[0].ID != accepted.Records[0].Requirement.ID {
		t.Fatal("external accepted history lost")
	}
	runFail(t, "prepare", restoredConfig, "without-sources")
}
