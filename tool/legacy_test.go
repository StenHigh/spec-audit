//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Test-only experiment: exact provenance is mechanical; semantic labels come from an independent reviewer.
type legacyCandidate struct {
	ID         string     `json:"id"`
	Condition  string     `json:"condition"`
	Statement  string     `json:"statement"`
	Exceptions []string   `json:"exceptions"`
	Clarity    string     `json:"clarity"`
	Unresolved []string   `json:"unresolved"`
	Citations  []Citation `json:"citations"`
}

type legacyRaw struct {
	Version   int `json:"version"`
	SourceSet []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"source_set"`
	Candidates  []legacyCandidate `json:"candidates"`
	Limitations []string          `json:"limitations"`
}

type legacyGold struct {
	Version      int    `json:"version"`
	Provenance   string `json:"provenance"`
	Requirements []struct {
		ID      string   `json:"id"`
		Facets  []string `json:"facets"`
		MustNot []string `json:"must_not"`
		Sources []struct {
			Path      string `json:"path"`
			LineStart int    `json:"line_start"`
			LineEnd   int    `json:"line_end"`
		} `json:"sources"`
	} `json:"requirements"`
}

type legacyReview struct {
	Version    int    `json:"version"`
	RawSHA256  string `json:"raw_sha256"`
	GoldSHA256 string `json:"gold_sha256"`
	Gold       []struct {
		ID           string   `json:"id"`
		CandidateIDs []string `json:"candidate_ids"`
		State        string   `json:"state"`
		Reason       string   `json:"reason"`
	} `json:"gold"`
	Candidates []struct {
		ID      string   `json:"id"`
		GoldIDs []string `json:"gold_ids"`
		State   string   `json:"state"`
		Reason  string   `json:"reason"`
	} `json:"candidates"`
	GoldObjections []string `json:"gold_objections"`
	Limitations    []string `json:"limitations"`
}

func legacyDecode(data []byte, out any) error {
	if len(data) > 4<<20 {
		return errors.New("ответ больше 4 MiB")
	}
	if err := strictJSON(data, out); err != nil {
		return err
	}
	return requiredJSON(data, reflect.TypeOf(out).Elem())
}

func legacyValidate(data []byte, sources map[string][]byte) (legacyRaw, error) {
	var raw legacyRaw
	if err := legacyDecode(data, &raw); err != nil {
		return raw, err
	}
	if raw.Version != 1 || len(raw.SourceSet) != len(sources) || len(raw.Candidates) > 64 {
		return raw, errors.New("неверная версия, набор источников или число кандидатов")
	}
	seen := map[string]bool{}
	for _, source := range raw.SourceSet {
		content, ok := sources[source.Path]
		if !ok || seen[source.Path] || digest(content) != source.SHA256 {
			return raw, errors.New("неверный путь, повторный источник или stale hash")
		}
		seen[source.Path] = true
	}
	seen = map[string]bool{}
	idPattern := regexp.MustCompile(`^C[0-9]{3}$`)
	for _, candidate := range raw.Candidates {
		if !idPattern.MatchString(candidate.ID) || seen[candidate.ID] || strings.TrimSpace(candidate.Condition) == "" || strings.TrimSpace(candidate.Statement) == "" {
			return raw, errors.New("неверный/повторный ID или пустая норма")
		}
		seen[candidate.ID] = true
		if (candidate.Clarity != "clear" && candidate.Clarity != "ambiguous") || (candidate.Clarity == "ambiguous") != (len(candidate.Unresolved) > 0) {
			return raw, errors.New("clarity и unresolved не согласованы")
		}
		for _, list := range [][]string{candidate.Exceptions, candidate.Unresolved} {
			for _, value := range list {
				if strings.TrimSpace(value) == "" {
					return raw, errors.New("пустое исключение или вопрос")
				}
			}
		}
		if len(candidate.Citations) < 1 || len(candidate.Citations) > 16 {
			return raw, errors.New("нужно от 1 до 16 цитат")
		}
		for _, cite := range candidate.Citations {
			content, ok := sources[cite.Path]
			quote, err := lineQuote(content, cite.LineStart, cite.LineEnd)
			if !ok || err != nil || quote != cite.Quote {
				return raw, errors.New("цитата не совпадает с выбранным источником")
			}
		}
	}
	return raw, nil
}

func legacyCount(data, rawBytes, goldBytes []byte, raw legacyRaw, gold legacyGold) (map[string]int, error) {
	var review legacyReview
	if err := legacyDecode(data, &review); err != nil {
		return nil, err
	}
	if review.Version != 1 || review.RawSHA256 != digest(rawBytes) || review.GoldSHA256 != digest(goldBytes) || len(review.Gold) != len(gold.Requirements) || len(review.Candidates) != len(raw.Candidates) {
		return nil, errors.New("неверный hash/версия или неполный учёт оценок")
	}
	goldIDs, candidateIDs := map[string]bool{}, map[string]bool{}
	for _, requirement := range gold.Requirements {
		goldIDs[requirement.ID] = true
	}
	for _, candidate := range raw.Candidates {
		candidateIDs[candidate.ID] = true
	}
	counts := map[string]int{"gold_total": len(goldIDs), "candidates_total": len(candidateIDs), "preserved": 0, "partial": 0, "missing": 0, "supported": 0, "overstated": 0, "unsupported": 0, "duplicate": 0, "gold_objections": len(review.GoldObjections)}
	forward, reverse := map[[2]string]bool{}, map[[2]string]bool{}
	seen := map[string]bool{}
	for _, row := range review.Gold {
		if !goldIDs[row.ID] || seen[row.ID] || strings.TrimSpace(row.Reason) == "" || (row.State != "preserved" && row.State != "partial" && row.State != "missing") || (row.State == "missing") != (len(row.CandidateIDs) == 0) {
			return nil, errors.New("неверная или повторная оценка gold")
		}
		seen[row.ID] = true
		counts[row.State]++
		for _, id := range row.CandidateIDs {
			edge := [2]string{row.ID, id}
			if !candidateIDs[id] || forward[edge] {
				return nil, errors.New("чужая или повторная связь gold → candidate")
			}
			forward[edge] = true
		}
	}
	seen = map[string]bool{}
	for _, row := range review.Candidates {
		if !candidateIDs[row.ID] || seen[row.ID] || strings.TrimSpace(row.Reason) == "" || (row.State != "supported" && row.State != "overstated" && row.State != "unsupported" && row.State != "duplicate") || (row.State == "unsupported") != (len(row.GoldIDs) == 0) {
			return nil, errors.New("неверная или повторная оценка кандидата")
		}
		seen[row.ID] = true
		counts[row.State]++
		for _, id := range row.GoldIDs {
			edge := [2]string{id, row.ID}
			if !goldIDs[id] || reverse[edge] {
				return nil, errors.New("чужая или повторная связь candidate → gold")
			}
			reverse[edge] = true
		}
	}
	if !maps.Equal(forward, reverse) {
		return nil, errors.New("связи оценок не взаимны")
	}
	return counts, nil
}

func legacyFixture(t *testing.T) (map[string][]byte, []byte, legacyGold) {
	t.Helper()
	baseline := strings.Split(strings.TrimSpace(string(readFixture(t, "../acceptance/legacy/baseline.sha256"))), "\n")
	if len(baseline) != 5 {
		t.Fatal("неполный frozen baseline")
	}
	seen := map[string]bool{}
	for _, line := range baseline {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 || seen[fields[1]] || digest(readFixture(t, "../"+fields[1])) != fields[0] {
			t.Fatal("frozen baseline изменён или повреждён")
		}
		seen[fields[1]] = true
	}
	sources := map[string][]byte{}
	for _, name := range []string{"legacy.md", "clarifications.md", "tool-operations.md"} {
		sources[name] = readFixture(t, "../acceptance/legacy/corpus/"+name)
	}
	data := readFixture(t, "../acceptance/legacy/gold.json")
	var gold legacyGold
	if err := legacyDecode(data, &gold); err != nil {
		t.Fatal(err)
	}
	return sources, data, gold
}

func legacyMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func legacyExternal(t *testing.T, key string) []byte {
	t.Helper()
	path := os.Getenv(key)
	if path == "" {
		t.Skip("внешний ответ не задан: " + key)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, err := readRoot(root, filepath.Base(path), 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLegacyBaseline(t *testing.T) {
	sources, _, gold := legacyFixture(t)
	ids := map[string]bool{}
	for _, requirement := range gold.Requirements {
		if ids[requirement.ID] || requirement.ID == "" || len(requirement.Facets) == 0 || len(requirement.Sources) == 0 {
			t.Fatal("неверное ожидание gold")
		}
		ids[requirement.ID] = true
		for _, source := range requirement.Sources {
			if _, err := lineQuote(sources[source.Path], source.LineStart, source.LineEnd); err != nil {
				t.Fatal(err)
			}
		}
	}
	for path, data := range sources {
		declared, err := parseRequirements(path, data)
		if err != nil || len(declared) != 0 {
			t.Fatal("контроль должен содержать обычный Markdown, без объявленных REQ")
		}
	}
	t.Logf("frozen sources=%d, gold=%d; declared REQ=0 не означает отсутствие обязанностей", len(sources), len(ids))
}

func TestLegacyChecks(t *testing.T) {
	sources := map[string][]byte{"example.md": []byte("Карточка показывает название.\n")}
	base := []byte(`{"version":1,"source_set":[{"path":"example.md","sha256":"HASH"}],"candidates":[{"id":"C001","condition":"Для карточки","statement":"Показывает название","exceptions":[],"clarity":"clear","unresolved":[],"citations":[{"path":"example.md","line_start":1,"line_end":1,"quote":"Карточка показывает название."}]}],"limitations":[]}`)
	base = bytes.ReplaceAll(base, []byte("HASH"), []byte(digest(sources["example.md"])))
	raw, err := legacyValidate(base, sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{`"version":1`, `"version":1,"extra":1`}, {`"version":1`, `"version":1,"version":1`}, {`"version":1`, `"Version":1`},
		{`"exceptions":[]`, `"exceptions":null`}, {`"exceptions":[],`, ``}, {`"line_start":1`, `"line_start":0`},
		{`"line_end":1`, `"line_end":2`}, {`Карточка показывает название.`, `Выдуманная цитата`},
		{`"path":"example.md"`, `"path":"../example.md"`}, {`"clarity":"clear"`, `"clarity":"ambiguous"`},
		{digest(sources["example.md"]), strings.Repeat("0", 64)},
	} {
		if _, err := legacyValidate(bytes.Replace(base, []byte(pair[0]), []byte(pair[1]), 1), sources); err == nil {
			t.Fatal("принят некорректный ответ")
		}
	}
	for _, invalid := range [][]byte{[]byte("{"), append(append([]byte{}, base...), base...), bytes.Repeat([]byte(" "), (4<<20)+1)} {
		if _, err := legacyValidate(invalid, sources); err == nil {
			t.Fatal("принят malformed/повторный/слишком большой JSON")
		}
	}
	for _, mutate := range []func(*legacyRaw){
		func(r *legacyRaw) { r.Candidates = append(r.Candidates, r.Candidates[0]) },
		func(r *legacyRaw) { r.SourceSet = append(r.SourceSet, r.SourceSet[0]) },
		func(r *legacyRaw) { r.Candidates[0].Citations[0].Path = "../example.md" },
		func(r *legacyRaw) { r.Candidates[0].Citations = []Citation{} },
		func(r *legacyRaw) {
			r.Candidates[0].Citations = append(r.Candidates[0].Citations, make([]Citation, 16)...)
		},
		func(r *legacyRaw) { r.Candidates[0].Statement = " " },
		func(r *legacyRaw) { r.Candidates[0].Unresolved = []string{"не согласовано"} },
	} {
		var changed legacyRaw
		if err := legacyDecode(base, &changed); err != nil {
			t.Fatal(err)
		}
		mutate(&changed)
		if _, err := legacyValidate(legacyMarshal(t, changed), sources); err == nil {
			t.Fatal("принят неверный кандидат/источник")
		}
	}
	// A tiny synthetic review tests accounting only, not the frozen semantic acceptance.
	goldBytes := []byte(`{"version":1,"provenance":"unit test","requirements":[{"id":"G01","facets":["Название"],"must_not":[],"sources":[{"path":"example.md","line_start":1,"line_end":1}]}]}`)
	var gold legacyGold
	if err := legacyDecode(goldBytes, &gold); err != nil {
		t.Fatal(err)
	}
	review := []byte(`{"version":1,"raw_sha256":"RAW","gold_sha256":"GOLD","gold":[{"id":"G01","candidate_ids":["C001"],"state":"preserved","reason":"Название сохранено"}],"candidates":[{"id":"C001","gold_ids":["G01"],"state":"supported","reason":"Норма источника"}],"gold_objections":[],"limitations":[]}`)
	review = bytes.ReplaceAll(bytes.ReplaceAll(review, []byte("RAW"), []byte(digest(base))), []byte("GOLD"), []byte(digest(goldBytes)))
	counts, err := legacyCount(review, base, goldBytes, raw, gold)
	if err != nil || counts["preserved"] != 1 || counts["supported"] != 1 {
		t.Fatalf("учёт: %v %v", counts, err)
	}
	for _, pair := range [][2]string{
		{`"id":"G01"`, `"id":"G02"`}, {`"candidate_ids":["C001"]`, `"candidate_ids":["C001","C001"]`},
		{`"candidate_ids":["C001"]`, `"candidate_ids":[]`}, {`"gold_ids":["G01"]`, `"gold_ids":["G02"]`},
		{`"candidates":[`, `"extra":[],"candidates":[`}, {`"gold_objections":[]`, `"gold_objections":null`},
		{`"state":"preserved"`, `"state":"PASS"`}, {digest(base), strings.Repeat("0", 64)},
	} {
		if _, err := legacyCount(bytes.Replace(review, []byte(pair[0]), []byte(pair[1]), 1), base, goldBytes, raw, gold); err == nil {
			t.Fatal("принята неверная оценка")
		}
	}
	var changed legacyReview
	if err := legacyDecode(review, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Candidates = changed.Candidates[:0]
	if _, err := legacyCount(legacyMarshal(t, changed), base, goldBytes, raw, gold); err == nil {
		t.Fatal("потерян кандидат reviewer")
	}
	changed.Gold[0].State = "missing"
	changed.Gold[0].CandidateIDs = []string{}
	raw.Candidates = []legacyCandidate{}
	emptyRaw := legacyMarshal(t, raw)
	changed.RawSHA256 = digest(emptyRaw)
	if _, err := legacyValidate(emptyRaw, sources); err != nil {
		t.Fatal("число кандидатов не является семантическим знаменателем")
	}
	counts, err = legacyCount(legacyMarshal(t, changed), emptyRaw, goldBytes, raw, gold)
	if err != nil || counts["missing"] != 1 || counts["preserved"] != 0 {
		t.Fatal("пустое извлечение ошибочно засчитано как полное")
	}
	raw, err = legacyValidate(base, sources)
	if err != nil {
		t.Fatal(err)
	}
	partial := bytes.Replace(review, []byte(`"state":"preserved"`), []byte(`"state":"partial"`), 1)
	counts, err = legacyCount(partial, base, goldBytes, raw, gold)
	if err != nil || counts["partial"] != 1 || counts["preserved"] != 0 {
		t.Fatal("частичная норма ошибочно засчитана как полная")
	}
}

func TestLegacyRaw(t *testing.T) {
	data := legacyExternal(t, "SPEC_AUDIT_LEGACY_RAW")
	sources, _, _ := legacyFixture(t)
	raw, err := legacyValidate(data, sources)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("структурно принят raw_sha256=%s, candidates=%d; смысловая полнота не доказана", digest(data), len(raw.Candidates))
}

func TestLegacyReview(t *testing.T) {
	data := legacyExternal(t, "SPEC_AUDIT_LEGACY_REVIEW")
	rawBytes := legacyExternal(t, "SPEC_AUDIT_LEGACY_RAW")
	sources, goldBytes, gold := legacyFixture(t)
	raw, err := legacyValidate(rawBytes, sources)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := legacyCount(data, rawBytes, goldBytes, raw, gold)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reviewer_counts=%s", legacyMarshal(t, counts))
	if counts["preserved"] != counts["gold_total"] || counts["supported"] != counts["candidates_total"] || counts["gold_objections"] != 0 {
		t.Fatal("смысловой контроль не пройден по оценкам reviewer; это не ошибка JSON")
	}
}
