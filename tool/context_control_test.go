//go:build darwin || linux

package main

// Experimental control only; legacy/accepted wire formats and the CLI stay unchanged.
import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type contextNote struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Statement    string     `json:"statement"`
	CandidateIDs []string   `json:"candidate_ids"`
	Citations    []Citation `json:"citations"`
}

type contextNotes struct {
	Version     int           `json:"version"`
	RawSHA256   string        `json:"raw_sha256"`
	Notes       []contextNote `json:"notes"`
	Limitations []string      `json:"limitations"`
}

type contextGold struct {
	Version   int        `json:"version"`
	Normative legacyGold `json:"normative"`
	Notes     legacyGold `json:"notes"`
	NoteKinds []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	} `json:"note_kinds"`
}

type contextReview struct {
	Version   int          `json:"version"`
	Normative legacyReview `json:"normative"`
	Notes     legacyReview `json:"notes"`
}

func contextValidateNotes(data, rawBytes []byte, sources map[string][]byte) (contextNotes, error) {
	var notes contextNotes
	raw, err := legacyValidate(rawBytes, sources)
	if err != nil {
		return notes, err
	}
	if err := legacyDecode(data, &notes); err != nil {
		return notes, err
	}
	if notes.Version != 1 || notes.RawSHA256 != digest(rawBytes) || len(notes.Notes) > 64 {
		return notes, errors.New("неверный notes version/hash/лимит")
	}
	candidates, seen := map[string]bool{}, map[string]bool{}
	for _, candidate := range raw.Candidates {
		candidates[candidate.ID] = true
	}
	idPattern := regexp.MustCompile(`^N[0-9]{3}$`)
	for _, note := range notes.Notes {
		if !idPattern.MatchString(note.ID) || seen[note.ID] || !oneOf(note.Kind, "context", "open_question") || strings.TrimSpace(note.Statement) == "" || len(note.Citations) < 1 || len(note.Citations) > 16 {
			return notes, errors.New("неверная notes строка")
		}
		seen[note.ID] = true
		links := map[string]bool{}
		for _, id := range note.CandidateIDs {
			if !candidates[id] || links[id] {
				return notes, errors.New("неразрешимая/повторная notes связь")
			}
			links[id] = true
		}
		for _, cite := range note.Citations {
			content, ok := sources[cite.Path]
			quote, err := lineQuote(content, cite.LineStart, cite.LineEnd)
			if !ok || err != nil || quote != cite.Quote {
				return notes, errors.New("notes цитата не совпадает с источником")
			}
		}
	}
	for _, value := range notes.Limitations {
		if strings.TrimSpace(value) == "" {
			return notes, errors.New("пустое notes ограничение")
		}
	}
	return notes, nil
}

func contextCount(reviewBytes, rawBytes, notesBytes, goldBytes []byte, sources map[string][]byte) (map[string]int, map[string]int, error) {
	notes, err := contextValidateNotes(notesBytes, rawBytes, sources)
	if err != nil {
		return nil, nil, err
	}
	var raw legacyRaw
	var gold contextGold
	var review contextReview
	for _, input := range []struct {
		data []byte
		out  any
	}{{rawBytes, &raw}, {goldBytes, &gold}, {reviewBytes, &review}} {
		if err := legacyDecode(input.data, input.out); err != nil {
			return nil, nil, err
		}
	}
	if gold.Version != 1 || review.Version != 1 {
		return nil, nil, errors.New("неверная версия контроля")
	}
	goldNoteIDs := map[string]bool{}
	for groupIndex, group := range []legacyGold{gold.Normative, gold.Notes} {
		if group.Version != 1 || strings.TrimSpace(group.Provenance) == "" {
			return nil, nil, errors.New("неполный gold")
		}
		seen := map[string]bool{}
		for _, row := range group.Requirements {
			if row.ID == "" || seen[row.ID] || len(row.Facets) == 0 || len(row.Sources) == 0 {
				return nil, nil, errors.New("неверная/повторная gold строка")
			}
			seen[row.ID] = true
			if groupIndex == 1 {
				goldNoteIDs[row.ID] = true
			}
			for _, value := range append(slices.Clone(row.Facets), row.MustNot...) {
				if strings.TrimSpace(value) == "" {
					return nil, nil, errors.New("пустой gold facet")
				}
			}
			for _, source := range row.Sources {
				content, ok := sources[source.Path]
				if _, err := lineQuote(content, source.LineStart, source.LineEnd); !ok || err != nil {
					return nil, nil, errors.New("gold источник вне корпуса")
				}
			}
		}
	}
	goldKinds, actualKinds := map[string]string{}, map[string]string{}
	if len(gold.NoteKinds) != len(goldNoteIDs) {
		return nil, nil, errors.New("неполный учёт gold kinds")
	}
	for _, row := range gold.NoteKinds {
		if !goldNoteIDs[row.ID] || goldKinds[row.ID] != "" || !oneOf(row.Kind, "context", "open_question") {
			return nil, nil, errors.New("неверный gold kind")
		}
		goldKinds[row.ID] = row.Kind
	}
	// These views reuse accounting only; notes are never sent to reconcile as obligations.
	noteView := legacyRaw{Version: 1, Candidates: []legacyCandidate{}}
	for _, note := range notes.Notes {
		actualKinds[note.ID] = note.Kind
		noteView.Candidates = append(noteView.Candidates, legacyCandidate{ID: note.ID})
	}
	normReview, err := json.Marshal(review.Normative)
	if err != nil {
		return nil, nil, err
	}
	noteReview, err := json.Marshal(review.Notes)
	if err != nil {
		return nil, nil, err
	}
	normCounts, err := legacyCount(normReview, rawBytes, goldBytes, raw, gold.Normative)
	if err != nil {
		return nil, nil, err
	}
	noteCounts, err := legacyCount(noteReview, notesBytes, goldBytes, noteView, gold.Notes)
	if err != nil {
		return nil, nil, err
	}
	noteCounts["kind_mismatch"] = 0
	for _, row := range review.Notes.Gold {
		for _, id := range row.CandidateIDs {
			if goldKinds[row.ID] != actualKinds[id] {
				noteCounts["kind_mismatch"]++
			}
		}
	}
	return normCounts, noteCounts, nil
}

func contextPassed(counts map[string]int) bool {
	return counts["gold_total"] == counts["preserved"] && counts["candidates_total"] == counts["supported"] && counts["gold_objections"] == 0 && counts["kind_mismatch"] == 0
}

type contextPacket struct {
	Version        int            `json:"version"`
	OriginalSHA256 string         `json:"original_sha256"`
	Task           map[string]any `json:"task"`
	QuotePool      []string       `json:"quote_pool"`
}

// ponytail: test-only, <=4 MiB; linear quote lookup is enough until this control shows a real bottleneck.
func contextQuotes(value any, pool *[]string, expand bool, used []bool) error {
	switch node := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(node)) {
			switch key {
			case "quote":
				quote, ok := node[key].(string)
				if expand || !ok {
					return errors.New("ожидалась точная строка quote")
				}
				index := slices.Index(*pool, quote)
				if index < 0 {
					index = len(*pool)
					*pool = append(*pool, quote)
				}
				delete(node, key)
				node["quote_ref"] = index
			case "quote_ref":
				index, ok := node[key].(float64)
				if !expand || !ok || index < 0 || index >= float64(len(*pool)) || index != float64(int(index)) {
					return errors.New("неверная quote_ref")
				}
				if _, exists := node["quote"]; exists {
					return errors.New("quote и quote_ref одновременно")
				}
				used[int(index)] = true
				delete(node, key)
				node["quote"] = (*pool)[int(index)]
			default:
				if err := contextQuotes(node[key], pool, expand, used); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, item := range node {
			if err := contextQuotes(item, pool, expand, used); err != nil {
				return err
			}
		}
	case nil:
		return errors.New("null в задании")
	}
	return nil
}

func contextPack(task Task) (contextPacket, error) {
	body, err := json.Marshal(task)
	packet := contextPacket{Version: 1, OriginalSHA256: digest(body), QuotePool: []string{}}
	if err != nil {
		return packet, err
	}
	if len(body) > maxResult {
		return packet, errors.New("задание больше 4 MiB")
	}
	if err := strictJSON(body, &packet.Task); err != nil {
		return packet, err
	}
	if err := contextQuotes(packet.Task, &packet.QuotePool, false, nil); err != nil {
		return packet, err
	}
	packed, err := json.Marshal(packet)
	if err != nil {
		return packet, err
	}
	if len(packed) > maxResult {
		return packet, errors.New("пакет больше 4 MiB")
	}
	return packet, nil
}

func contextUnpack(data []byte) (Task, error) {
	var packet contextPacket
	var task Task
	if err := legacyDecode(data, &packet); err != nil {
		return task, err
	}
	if packet.Version != 1 {
		return task, errors.New("неверная версия пакета")
	}
	used := make([]bool, len(packet.QuotePool))
	if err := contextQuotes(packet.Task, &packet.QuotePool, true, used); err != nil {
		return task, err
	}
	for i, quote := range packet.QuotePool {
		if !used[i] || slices.Index(packet.QuotePool, quote) != i {
			return task, errors.New("лишняя/повторная quote_pool запись")
		}
	}
	body, err := json.Marshal(packet.Task)
	if err != nil {
		return task, err
	}
	if err := strictJSON(body, &task); err != nil {
		return task, err
	}
	canonical, err := json.Marshal(task)
	if err != nil {
		return task, err
	}
	if digest(canonical) != packet.OriginalSHA256 {
		return task, errors.New("содержимое задания изменилось")
	}
	return task, nil
}

func TestReleaseControlBaseline(t *testing.T) {
	if digest(readFixture(t, "../acceptance/release-control.json")) != "a707645d3f7731612dbf235e2af9b486c170fecba86fb74e6ff25e2dc2cd0805" {
		t.Fatal("изменён frozen release контроль")
	}
}

func TestContextPacket(t *testing.T) {
	pin := "a95cb5c2628c757a6bf86f9e7f3c7e66548f2a20641520d0279732c4c9d0ffed"
	if digest(readFixture(t, "../acceptance/context-control.json")) != pin {
		t.Fatal("изменён frozen контроль")
	}
	config, base := acceptedFixture(t)
	candidate := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
	candidate.Clarity, candidate.Unresolved = "ambiguous", []string{"Срок не согласован"}
	candidate.Exceptions = []string{"Без изменения чужих требований"}
	candidate.Citations = append(candidate.Citations, candidate.Citations[0])
	rawPath, decisionPath := acceptedInputs(t, config, "context-control", []legacyCandidate{candidate}, acceptOperation("accept", []string{}, "C001"))
	runOK(t, "reconcile", config, rawPath, decisionPath)
	task := runOK(t, "prepare", config, "context-control").(TaskBatch).Tasks[0]
	packet, err := contextPack(task)
	if err != nil {
		t.Fatal(err)
	}
	body := legacyMarshal(t, packet)
	restored, err := contextUnpack(body)
	if err != nil || !reflect.DeepEqual(task, restored) {
		t.Fatalf("потеря содержания: %v", err)
	}
	if len(packet.QuotePool) != 1 {
		t.Fatal("одинаковые quote не разделены")
	}
	t.Logf("canonical task bytes=%d, packet bytes=%d", len(legacyMarshal(t, task)), len(body))
	for name, mutate := range map[string]func(*contextPacket){
		"wrong_ref": func(p *contextPacket) {
			p.Task["requirements"].([]any)[0].(map[string]any)["source"].(map[string]any)["quote_ref"] = 999
		},
		"fraction_ref": func(p *contextPacket) {
			p.Task["requirements"].([]any)[0].(map[string]any)["source"].(map[string]any)["quote_ref"] = 0.5
		},
		"lost_requirement": func(p *contextPacket) { p.Task["requirements"] = []any{} },
		"changed_condition": func(p *contextPacket) {
			p.Task["requirements"].([]any)[0].(map[string]any)["condition"] = "Всегда"
		},
		"changed_quote":   func(p *contextPacket) { p.QuotePool[0] += "!" },
		"unused_quote":    func(p *contextPacket) { p.QuotePool = append(p.QuotePool, "unused") },
		"duplicate_quote": func(p *contextPacket) { p.QuotePool = append(p.QuotePool, p.QuotePool[0]) },
		"unknown_field":   func(p *contextPacket) { p.Task["guess"] = true },
		"stale_snapshot":  func(p *contextPacket) { p.Task["snapshot_id"] = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			var changed contextPacket
			if err := legacyDecode(body, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if _, err := contextUnpack(legacyMarshal(t, changed)); err == nil {
				t.Fatal("повреждение принято")
			}
		})
	}
}

func TestContextExistingEvidence(t *testing.T) {
	config, base := fixture(t)
	batch := runOK(t, "prepare", config, "context").(TaskBatch)
	for _, original := range batch.Tasks {
		packed, err := contextPack(original)
		if err != nil {
			t.Fatal(err)
		}
		task, err := contextUnpack(legacyMarshal(t, packed))
		if err != nil || !reflect.DeepEqual(original, task) {
			t.Fatalf("потеря Task: %v", err)
		}
		result := sampleResult(t, task, filepath.Join(base, "source"))
		path := filepath.Join(base, task.TaskID+".json")
		for name, mutate := range map[string]func(*Result){
			"quote":       func(r *Result) { r.Assessments[0].Code[0].Quote += "!" },
			"range":       func(r *Result) { r.Assessments[0].Code[0].LineStart++ },
			"category":    func(r *Result) { r.Assessments[0].Code[0] = r.Assessments[0].Spec[0] },
			"requirement": func(r *Result) { r.Assessments[0].Spec[0] = r.Assessments[1].Spec[0] },
		} {
			t.Run(task.Role+"_"+name, func(t *testing.T) {
				changed := sampleResult(t, task, filepath.Join(base, "source"))
				mutate(&changed)
				writeFixture(t, path, legacyMarshal(t, changed))
				runFail(t, "submit", config, "context", task.TaskID, path)
			})
		}
		writeFixture(t, path, legacyMarshal(t, result))
		runOK(t, "submit", config, "context", task.TaskID, path)
	}
	runOK(t, "report", config, "context")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/context/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if !report.DeliveryComplete || report.Requirements[1].Roles[0].Assessment.Assertion != "weak" || report.Requirements[2].Roles[0].Assessment.Assertion != "contradicts" || report.Requirements[3].Roles[0].Assessment.Assertion != "missing" || len(report.Executions) != 0 || report.Requirements[0].Roles[0].Executions[0].State != "not_recorded" {
		t.Fatal("подменена сила свидетельства/исполнение")
	}
	// A change outside any evidence range must still invalidate the run.
	path := filepath.Join(base, "source/source.go")
	writeFixture(t, path, append(readFixture(t, path), []byte("\n// unrelated change\n")...))
	status := runOK(t, "status", config, "context").(Status)
	if status.Freshness != "stale" {
		t.Fatal("изменение вне цитат не замечено")
	}
	t.Log("раздельные состояния DEMO и stale сохранены; это не новый смысловой аудит")
}

func TestExternalContextReview(t *testing.T) {
	configPath := os.Getenv("SPEC_AUDIT_CONTEXT_CONFIG")
	if configPath == "" {
		t.Skip("внешний конфиг не задан")
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, sources, _, err := acceptedSources(cfg)
	if err != nil {
		t.Fatal(err)
	}
	norm, notes, err := contextCount(legacyExternal(t, "SPEC_AUDIT_CONTEXT_REVIEW"), legacyExternal(t, "SPEC_AUDIT_LEGACY_RAW"), legacyExternal(t, "SPEC_AUDIT_CONTEXT_NOTES"), legacyExternal(t, "SPEC_AUDIT_CONTEXT_GOLD"), sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("normative=%s notes=%s", legacyMarshal(t, norm), legacyMarshal(t, notes))
	if !contextPassed(norm) || !contextPassed(notes) {
		t.Fatal("новый смысловой контроль не пройден по оценкам reviewer")
	}
}

func TestExternalContextPacket(t *testing.T) {
	body := legacyExternal(t, "SPEC_AUDIT_CONTEXT_TASK")
	var task Task
	if err := strictJSON(body, &task); err != nil {
		t.Fatal(err)
	}
	packet, err := contextPack(task)
	if err != nil {
		t.Fatal(err)
	}
	packed := legacyMarshal(t, packet)
	restored, err := contextUnpack(packed)
	if err != nil || !reflect.DeepEqual(task, restored) {
		t.Fatalf("потеря Task: %v", err)
	}
	// Explicit opt-in create-only artifact; the regular suite writes nothing outside t.TempDir.
	if name := os.Getenv("SPEC_AUDIT_CONTEXT_PACKET"); name != "" {
		if !localPath(name) || filepath.Base(name) != name {
			t.Fatal("нужно простое имя файла пакета")
		}
		cfg, err := loadConfig(os.Getenv("SPEC_AUDIT_CONTEXT_CONFIG"))
		if err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(cfg.ReportsDir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(packed)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("canonical_bytes=%d packed_bytes=%d quote_pool=%d", len(legacyMarshal(t, task)), len(packed), len(packet.QuotePool))
}

func TestContextTypedAccounting(t *testing.T) {
	sources := map[string][]byte{"sample.md": []byte("Уведомить при задержке; срок не согласован.\nХост обязан одобрить результат.\nНапример: уведомление MUST отправляться через две минуты.\nЖизненный цикл не обсуждался.\n")}
	raw := legacyRaw{1, []legacySource{{"sample.md", digest(sources["sample.md"])}}, []legacyCandidate{}, []string{}}
	for i, statement := range []string{"Уведомить при задержке", "Хост обязан одобрить результат"} {
		end := i + 1
		if i == 0 {
			end = 3
		}
		quote, err := lineQuote(sources["sample.md"], i+1, end)
		if err != nil {
			t.Fatal(err)
		}
		raw.Candidates = append(raw.Candidates, legacyCandidate{fmt.Sprintf("C%03d", i+1), "В указанной области", statement, []string{}, "clear", []string{}, []Citation{{"sample.md", i + 1, end, quote}}})
	}
	raw.Candidates[0].Clarity, raw.Candidates[0].Unresolved = "ambiguous", []string{"Срок не согласован"}
	rawBytes := legacyMarshal(t, raw)
	notes := contextNotes{1, digest(rawBytes), []contextNote{
		{"N001", "context", "Пример, не обязательный срок", []string{"C001"}, []Citation{{"sample.md", 3, 3, "Например: уведомление MUST отправляться через две минуты."}}},
		{"N002", "open_question", "Жизненный цикл не согласован", []string{}, []Citation{{"sample.md", 4, 4, "Жизненный цикл не обсуждался."}}},
	}, []string{}}
	notesBytes := legacyMarshal(t, notes)
	goldBytes := []byte("{\"version\":1,\"normative\":{\"version\":1,\"provenance\":\"Fixed unit-test expectations\",\"requirements\":[{\"id\":\"G01\",\"facets\":[\"Уведомить при задержке, сохранив неизвестный срок\"],\"must_not\":[\"Не прятать обязанность только в вопросах\"],\"sources\":[{\"path\":\"sample.md\",\"line_start\":1,\"line_end\":1}]},{\"id\":\"G02\",\"facets\":[\"Хост обязан одобрить результат\"],\"must_not\":[\"Не удалять ручную обязанность\"],\"sources\":[{\"path\":\"sample.md\",\"line_start\":2,\"line_end\":2}]}]},\"notes\":{\"version\":1,\"provenance\":\"Separate denominator\",\"requirements\":[{\"id\":\"GN01\",\"facets\":[\"Пример срока, не нормативное значение\"],\"must_not\":[],\"sources\":[{\"path\":\"sample.md\",\"line_start\":3,\"line_end\":3}]},{\"id\":\"GN02\",\"facets\":[\"Жизненный цикл не согласован\"],\"must_not\":[],\"sources\":[{\"path\":\"sample.md\",\"line_start\":4,\"line_end\":4}]}]},\"note_kinds\":[{\"id\":\"GN01\",\"kind\":\"context\"},{\"id\":\"GN02\",\"kind\":\"open_question\"}]}")
	// Fixed evaluations exercise accounting, not a keyword-based semantic classifier.
	reviewBytes := []byte(strings.NewReplacer("GOLD_HASH", digest(goldBytes), "RAW_HASH", digest(rawBytes), "NOTES_HASH", digest(notesBytes)).Replace("{\"version\":1,\"normative\":{\"version\":1,\"raw_sha256\":\"RAW_HASH\",\"gold_sha256\":\"GOLD_HASH\",\"gold\":[{\"id\":\"G01\",\"candidate_ids\":[\"C001\"],\"state\":\"preserved\",\"reason\":\"Fixed synthetic mapping\"},{\"id\":\"G02\",\"candidate_ids\":[\"C002\"],\"state\":\"preserved\",\"reason\":\"Fixed synthetic mapping\"}],\"candidates\":[{\"id\":\"C001\",\"gold_ids\":[\"G01\"],\"state\":\"supported\",\"reason\":\"Fixed synthetic mapping\"},{\"id\":\"C002\",\"gold_ids\":[\"G02\"],\"state\":\"supported\",\"reason\":\"Fixed synthetic mapping\"}],\"gold_objections\":[],\"limitations\":[]},\"notes\":{\"version\":1,\"raw_sha256\":\"NOTES_HASH\",\"gold_sha256\":\"GOLD_HASH\",\"gold\":[{\"id\":\"GN01\",\"candidate_ids\":[\"N001\"],\"state\":\"preserved\",\"reason\":\"Fixed synthetic mapping\"},{\"id\":\"GN02\",\"candidate_ids\":[\"N002\"],\"state\":\"preserved\",\"reason\":\"Fixed synthetic mapping\"}],\"candidates\":[{\"id\":\"N001\",\"gold_ids\":[\"GN01\"],\"state\":\"supported\",\"reason\":\"Fixed synthetic mapping\"},{\"id\":\"N002\",\"gold_ids\":[\"GN02\"],\"state\":\"supported\",\"reason\":\"Fixed synthetic mapping\"}],\"gold_objections\":[],\"limitations\":[]}}"))
	normCounts, noteCounts, err := contextCount(reviewBytes, rawBytes, notesBytes, goldBytes, sources)
	if err != nil || !contextPassed(normCounts) || !contextPassed(noteCounts) || normCounts["gold_total"] != 2 || noteCounts["gold_total"] != 2 {
		t.Fatalf("раздельный учёт: %v %v %v", normCounts, noteCounts, err)
	}
	for name, mutate := range map[string]func(*contextNotes){
		"wrong_hash":          func(n *contextNotes) { n.RawSHA256 = strings.Repeat("0", 64) },
		"invalid_kind":        func(n *contextNotes) { n.Notes[0].Kind = "requirement" },
		"invalid_id":          func(n *contextNotes) { n.Notes[0].ID = "C001" },
		"duplicate_id":        func(n *contextNotes) { n.Notes[1].ID = "N001" },
		"empty_statement":     func(n *contextNotes) { n.Notes[0].Statement = " " },
		"foreign_candidate":   func(n *contextNotes) { n.Notes[0].CandidateIDs = []string{"C999"} },
		"duplicate_candidate": func(n *contextNotes) { n.Notes[0].CandidateIDs = []string{"C001", "C001"} },
		"no_citation":         func(n *contextNotes) { n.Notes[0].Citations = []Citation{} },
		"foreign_source":      func(n *contextNotes) { n.Notes[0].Citations[0].Path = "../sample.md" },
		"range":               func(n *contextNotes) { n.Notes[0].Citations[0].LineEnd = 999 },
		"quote":               func(n *contextNotes) { n.Notes[0].Citations[0].Quote = "Выдумка" },
	} {
		t.Run(name, func(t *testing.T) {
			var changed contextNotes
			if err := legacyDecode(notesBytes, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if _, err := contextValidateNotes(legacyMarshal(t, changed), rawBytes, sources); err == nil {
				t.Fatal("неверная заметка принята")
			}
		})
	}
	for _, pair := range [][2]string{
		{"\"version\":1", "\"version\":1,\"version\":1"},
		{"\"version\":1", "\"version\":1,\"extra\":1"},
		{"\"limitations\":[]", "\"limitations\":null"},
		{",\"candidate_ids\":[]", ""},
	} {
		if _, err := contextValidateNotes([]byte(strings.Replace(string(notesBytes), pair[0], pair[1], 1)), rawBytes, sources); err == nil {
			t.Fatal("schema bypass")
		}
	}
	for _, data := range [][]byte{append(slices.Clone(notesBytes), notesBytes...), []byte(strings.Repeat(" ", maxResult+1))} {
		if _, err := contextValidateNotes(data, rawBytes, sources); err == nil {
			t.Fatal("trailing document/size bypass")
		}
	}
	boundary := notes
	boundary.Notes = []contextNote{}
	for i := 0; i < 64; i++ {
		note := notes.Notes[0]
		note.ID = fmt.Sprintf("N%03d", i+1)
		note.Citations = slices.Repeat(note.Citations, 16)
		boundary.Notes = append(boundary.Notes, note)
	}
	if _, err := contextValidateNotes(legacyMarshal(t, boundary), rawBytes, sources); err != nil {
		t.Fatal("64 notes/16 citations rejected:", err)
	}
	boundary.Notes = append(boundary.Notes, contextNote{"N065", "context", "Пример", []string{}, notes.Notes[0].Citations})
	if _, err := contextValidateNotes(legacyMarshal(t, boundary), rawBytes, sources); err == nil {
		t.Fatal("65 notes accepted")
	}
	boundary.Notes = boundary.Notes[:64]
	boundary.Notes[0].Citations = append(boundary.Notes[0].Citations, notes.Notes[0].Citations[0])
	if _, err := contextValidateNotes(legacyMarshal(t, boundary), rawBytes, sources); err == nil {
		t.Fatal("17 citations accepted")
	}

	t.Run("frozen_gold", func(t *testing.T) {
		changed := append(slices.Clone(goldBytes), '\n')
		if _, _, err := contextCount(reviewBytes, rawBytes, notesBytes, changed, sources); err == nil {
			t.Fatal("gold silently changed")
		}
	})
	for _, scenario := range []string{"kind_mismatch", "truncated_context", "missing_norm", "gold_objection", "asymmetric_links"} {
		t.Run(scenario, func(t *testing.T) {
			var changedNotes contextNotes
			var changedRaw legacyRaw
			var changedReview contextReview
			for _, input := range []struct {
				data []byte
				out  any
			}{{rawBytes, &changedRaw}, {notesBytes, &changedNotes}, {reviewBytes, &changedReview}} {
				if err := legacyDecode(input.data, input.out); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "kind_mismatch":
				changedNotes.Notes[1].Kind = "context"
			case "truncated_context":
				// Exact shorter quote passes provenance; the review still reports semantic loss.
				changedRaw.Candidates[0].Citations[0].LineEnd = 1
				changedRaw.Candidates[0].Citations[0].Quote, _ = lineQuote(sources["sample.md"], 1, 1)
				changedReview.Normative.Gold[0].State = "partial"
				changedReview.Normative.Gold[0].Reason = "Недостаточный контекст по оценке reviewer"
			case "missing_norm":
				changedRaw.Candidates = changedRaw.Candidates[1:]
				changedNotes.Notes[0].CandidateIDs = []string{}
				changedReview.Normative.Gold[0].State = "missing"
				changedReview.Normative.Gold[0].CandidateIDs = []string{}
				changedReview.Normative.Candidates = changedReview.Normative.Candidates[1:]
			case "gold_objection":
				changedReview.Normative.GoldObjections = []string{"Ошибка эталона требует новой версии, не зелёного счёта"}
			case "asymmetric_links":
				changedReview.Normative.Candidates[0].GoldIDs = []string{"G02"}
			}
			changedRawBytes := legacyMarshal(t, changedRaw)
			changedNotes.RawSHA256 = digest(changedRawBytes)
			changedNotesBytes := legacyMarshal(t, changedNotes)
			changedReview.Normative.RawSHA256 = digest(changedRawBytes)
			changedReview.Notes.RawSHA256 = digest(changedNotesBytes)
			norm, notes, err := contextCount(legacyMarshal(t, changedReview), changedRawBytes, changedNotesBytes, goldBytes, sources)
			if scenario == "asymmetric_links" {
				if err == nil {
					t.Fatal("asymmetric links accepted")
				}
				return
			}
			if err != nil || norm["gold_total"] != 2 || notes["gold_total"] != 2 || (contextPassed(norm) && contextPassed(notes)) {
				t.Fatalf("отрицательный результат потерян: %v %v %v", norm, notes, err)
			}
			if scenario == "kind_mismatch" && notes["kind_mismatch"] != 1 {
				t.Fatal("kind mismatch lost")
			}
		})
	}
}
