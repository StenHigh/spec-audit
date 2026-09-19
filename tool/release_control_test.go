//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type releaseNoteAssessment struct {
	NoteID    string   `json:"note_id"`
	Kind      string   `json:"kind"`
	Supported bool     `json:"supported"`
	GoldIDs   []string `json:"gold_ids"`
	Reason    string   `json:"reason"`
}
type releaseNotesReview struct {
	Version        int                     `json:"version"`
	NotesSHA256    string                  `json:"notes_sha256"`
	GoldSHA256     string                  `json:"gold_sha256"`
	Notes          []releaseNoteAssessment `json:"notes"`
	GoldObjections []string                `json:"gold_objections"`
}

// Counts explicit reviewer judgments, not meaning inferred from matching words.
// Additional source-supported notes do not expand the normative denominator.
func releaseCountNotes(data, rawBytes, notesBytes, goldBytes []byte, sources map[string][]byte, required map[string]string) (map[string]int, error) {
	notes, err := contextValidateNotes(notesBytes, rawBytes, sources)
	if err != nil {
		return nil, err
	}
	var review releaseNotesReview
	if err := legacyDecode(data, &review); err != nil {
		return nil, err
	}
	if review.Version != 1 || review.NotesSHA256 != digest(notesBytes) || review.GoldSHA256 != digest(goldBytes) || len(review.Notes) != len(notes.Notes) {
		return nil, errors.New("неверная база/версия или неполный notes review")
	}
	actual := map[string]contextNote{}
	for _, note := range notes.Notes {
		actual[note.ID] = note
	}
	counts := map[string]int{"notes": len(notes.Notes), "required": len(required), "covered": 0, "additional": 0, "unsupported": 0, "kind_mismatch": 0, "gold_objections": len(review.GoldObjections)}
	seen, covered := map[string]bool{}, map[string]bool{}
	for _, row := range review.Notes {
		note, ok := actual[row.NoteID]
		if !ok || seen[row.NoteID] || strings.TrimSpace(row.Reason) == "" || !oneOf(row.Kind, "context", "open_question") {
			return nil, errors.New("неверная/повторная notes оценка")
		}
		seen[row.NoteID] = true
		if !row.Supported {
			counts["unsupported"]++
		}
		if row.Kind != note.Kind {
			counts["kind_mismatch"]++
		}
		if len(row.GoldIDs) == 0 {
			counts["additional"]++
		}
		links := map[string]bool{}
		for _, id := range row.GoldIDs {
			kind, ok := required[id]
			if !ok || links[id] {
				return nil, errors.New("неверная/повторная notes gold связь")
			}
			links[id] = true
			if kind != row.Kind {
				counts["kind_mismatch"]++
			}
			if row.Supported && kind == row.Kind && row.Kind == note.Kind {
				covered[id] = true
			}
		}
	}
	counts["covered"] = len(covered)
	return counts, nil
}

func TestReleaseNotesAccounting(t *testing.T) {
	sources := map[string][]byte{"notes.md": []byte("Система обязана записать сообщение.\nДля разбора ошибок.\nСрок не обсуждался.\n")}
	raw := legacyRaw{1, []legacySource{{"notes.md", digest(sources["notes.md"])}}, []legacyCandidate{{"C001", "Всегда", "Записать сообщение", []string{}, "clear", []string{}, []Citation{{"notes.md", 1, 1, "Система обязана записать сообщение."}}}}, []string{}}
	rawBytes := legacyMarshal(t, raw)
	notes := contextNotes{1, digest(rawBytes), []contextNote{
		{"N001", "context", "Цель — разбор ошибок", []string{"C001"}, []Citation{{"notes.md", 2, 2, "Для разбора ошибок."}}},
		{"N002", "open_question", "Срок не обсуждался", []string{}, []Citation{{"notes.md", 3, 3, "Срок не обсуждался."}}},
	}, []string{}}
	notesBytes, goldBytes := legacyMarshal(t, notes), readFixture(t, "../acceptance/release-control.json")
	review := releaseNotesReview{1, digest(notesBytes), digest(goldBytes), []releaseNoteAssessment{
		{"N001", "context", true, []string{"GN01"}, "Заданный synthetic judgment"},
		{"N002", "open_question", true, []string{}, "Дополнительное пояснение подтверждено отдельной строкой"},
	}, []string{}}
	required := map[string]string{"GN01": "context"}
	counts, err := releaseCountNotes(legacyMarshal(t, review), rawBytes, notesBytes, goldBytes, sources, required)
	if err != nil || counts["covered"] != 1 || counts["additional"] != 1 || counts["unsupported"] != 0 {
		t.Fatalf("notes accounting: %v %v", counts, err)
	}
	for _, field := range []string{"unsupported", "kind", "missing", "hash", "objection"} {
		t.Run(field, func(t *testing.T) {
			var changed releaseNotesReview
			if err := json.Unmarshal(legacyMarshal(t, review), &changed); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "unsupported":
				changed.Notes[1].Supported = false
			case "kind":
				changed.Notes[0].Kind = "open_question"
			case "missing":
				changed.Notes = changed.Notes[:1]
			case "hash":
				changed.GoldSHA256 = strings.Repeat("0", 64)
			case "objection":
				changed.GoldObjections = []string{"Эталон оспорен"}
			}
			c, e := releaseCountNotes(legacyMarshal(t, changed), rawBytes, notesBytes, goldBytes, sources, required)
			if e == nil && c["unsupported"] == 0 && c["kind_mismatch"] == 0 && c["covered"] == c["required"] && c["gold_objections"] == 0 {
				t.Fatal("negative counted as pass")
			}
		})
	}
}

type releaseAuditCase struct {
	ID       string            `json:"id"`
	Files    map[string]string `json:"files"`
	Expected []struct {
		ID             string `json:"id"`
		Specification  string `json:"specification"`
		Implementation string `json:"implementation"`
		Assertion      string `json:"assertion"`
	} `json:"expected"`
}

func releaseCases(t *testing.T) []releaseAuditCase {
	t.Helper()
	data := readFixture(t, "../acceptance/release-control.json")
	if digest(data) != "a707645d3f7731612dbf235e2af9b486c170fecba86fb74e6ff25e2dc2cd0805" {
		t.Fatal("frozen release control changed")
	}
	var control struct {
		AuditCases []releaseAuditCase `json:"audit_cases"`
	}
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatal(err)
	}
	return control.AuditCases
}

func releaseWriteCase(t *testing.T, base string, c releaseAuditCase) string {
	t.Helper()
	for path, body := range c.Files {
		writeFixture(t, filepath.Join(base, "source", path), []byte(body))
	}
	config := filepath.Join(base, "config.yaml")
	writeFixture(t, config, []byte("version: 1\nproject_root: source\nspecs: {paths: [rules.md]}\ncode: {paths: [source.go, go.mod]}\ntests: {paths: [source_test.go]}\nreports_dir: reports\nruntime: {kind: none}\nscopes: []\n"))
	return config
}

func TestReleasePairSources(t *testing.T) {
	for _, c := range releaseCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			config := releaseWriteCase(t, t.TempDir(), c)
			batch := runOK(t, "prepare", config, "pair").(TaskBatch)
			if len(batch.Tasks) != 2 || len(batch.Tasks[0].Requirements) != len(c.Expected) {
				t.Fatal("неполная пара")
			}
			// Fixed labels check mechanics only; independent role dispatch is separate.
			result := Result{batch.Tasks[0].TaskID, 1, batch.SnapshotID, "mapper", "all", "Synthetic mechanical control", []Assessment{}, []string{}}
			for _, req := range batch.Tasks[0].Requirements {
				var label *struct {
					ID             string `json:"id"`
					Specification  string `json:"specification"`
					Implementation string `json:"implementation"`
					Assertion      string `json:"assertion"`
				}
				for i := range c.Expected {
					if c.Expected[i].ID == req.ID {
						label = &c.Expected[i]
					}
				}
				if label == nil {
					t.Fatal("нет frozen ожидания")
				}
				whole := func(path string) Citation {
					body := c.Files[path]
					return Citation{path, 1, len(strings.Split(strings.TrimSuffix(body, "\n"), "\n")), strings.TrimSuffix(body, "\n")}
				}
				result.Assessments = append(result.Assessments, Assessment{req.ID, label.Specification, label.Implementation, label.Assertion, "Fixed expectation for mechanical validation", []Citation{req.Source}, []Citation{whole("source.go")}, []TestCitation{{"fixture::test", whole("source_test.go")}}, []string{}})
			}
			cfg, err := loadConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			m, err := snapshot(cfg)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(m.Config.ProjectRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := validateResult(result, batch.Tasks[0], m, newSourceCache(root)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Opt-in output preparation; create only a new external directory, never overwrite a run.
func TestExternalReleasePrepare(t *testing.T) {
	base := os.Getenv("SPEC_AUDIT_RELEASE_OUTPUT")
	if base == "" {
		t.Skip("external output not requested")
	}
	if !filepath.IsAbs(base) {
		t.Fatal("absolute output path required")
	}
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	for i, c := range releaseCases(t) {
		config := releaseWriteCase(t, filepath.Join(base, []string{"a", "b"}[i]), c)
		batch := runOK(t, "prepare", config, "control").(TaskBatch)
		for _, task := range batch.Tasks {
			writeFixture(t, filepath.Join(filepath.Dir(config), task.Role+".task.json"), legacyMarshal(t, task))
		}
		writeFixture(t, filepath.Join(filepath.Dir(config), "files.json"), legacyMarshal(t, batch.Files))
	}
	t.Log("prepared two frozen source variants, no model judgments generated")
}

// Opt-in validation of independent raw; never manufactures a semantic judgment.
func TestExternalReleaseReview(t *testing.T) {
	base := os.Getenv("SPEC_AUDIT_RELEASE_REVIEW")
	if base == "" {
		t.Skip("external review not requested")
	}
	if !filepath.IsAbs(base) {
		t.Fatal("absolute review directory required")
	}
	_ = releaseCases(t) // The same pre-dispatch frozen pin, not an adjustable denominator.
	goldBytes := readFixture(t, "../acceptance/release-control.json")
	var control struct {
		Extraction struct {
			Files         map[string]string `json:"files"`
			ExpectedNorms []struct {
				ID string `json:"id"`
			} `json:"expected_norms"`
			ExpectedNotes []struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"expected_notes"`
		} `json:"extraction"`
	}
	if err := json.Unmarshal(goldBytes, &control); err != nil {
		t.Fatal(err)
	}
	sources := map[string][]byte{}
	for path, expected := range control.Extraction.Files {
		body := readFixture(t, filepath.Join(base, "source", path))
		if string(body) != expected {
			t.Fatal("external source differs from frozen control")
		}
		sources[path] = body
	}
	rawBytes := readFixture(t, filepath.Join(base, "extractor.raw.json"))
	notesBytes := readFixture(t, filepath.Join(base, "notes.raw.json"))
	raw, err := legacyValidate(rawBytes, sources)
	if err != nil {
		t.Fatal(err)
	}
	var review struct {
		Version   int                `json:"version"`
		Normative legacyReview       `json:"normative"`
		Notes     releaseNotesReview `json:"notes"`
	}
	if err := legacyDecode(readFixture(t, filepath.Join(base, "review.json")), &review); err != nil || review.Version != 1 {
		t.Fatalf("review schema/version: %v", err)
	}
	// legacyCount consumes IDs only; the source/facets remain the independent reviewer's input.
	var gold legacyGold
	if err := json.Unmarshal(legacyMarshal(t, map[string]any{"requirements": control.Extraction.ExpectedNorms}), &gold); err != nil {
		t.Fatal(err)
	}
	norms, err := legacyCount(legacyMarshal(t, review.Normative), rawBytes, goldBytes, raw, gold)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]string{}
	for _, note := range control.Extraction.ExpectedNotes {
		required[note.ID] = note.Kind
	}
	notes, err := releaseCountNotes(legacyMarshal(t, review.Notes), rawBytes, notesBytes, goldBytes, sources, required)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reviewer_counts=%s", legacyMarshal(t, map[string]any{"normative": norms, "notes": notes}))
	if !contextPassed(norms) || notes["covered"] != notes["required"] || notes["unsupported"] != 0 || notes["kind_mismatch"] != 0 || notes["gold_objections"] != 0 {
		t.Fatal("semantic control failed according to independent reviewer")
	}
}
