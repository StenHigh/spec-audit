package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// rawFile writes a raw extraction for the fixture's current source_set and returns its path.
func rawFile(t *testing.T, config, name string, candidates []legacyCandidate) string {
	t.Helper()
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	set, _, _, err := acceptedSources(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(config), name+"-raw.json")
	writeFixture(t, path, legacyMarshal(t, legacyRaw{1, set, candidates, []string{}}))
	return path
}

func checkView(t *testing.T, args ...string) map[string]any {
	t.Helper()
	return runOK(t, append([]string{"check"}, args...)...).(map[string]any)
}

// tool-spec §21 / REQ-SA-042: check runs the apply checks without writing anything; §21.1 adds match hints.
func TestCheckAcceptance(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		config, base := fixture(t)
		raw := filepath.Join(base, "raw.json")
		writeFixture(t, raw, []byte("{}"))
		if _, err := execute([]string{"check", config, raw}); err == nil || !strings.Contains(err.Error(), "index_mode") {
			t.Fatal("declared-профиль должен быть отклонён", err)
		}
		runFail(t, "check", config)
	})
	t.Run("raw", func(t *testing.T) {
		config, base := referenceFixture(t)
		reports := filepath.Join(base, "runs")
		mixed := candidateAt(t, base, "rules.md", "C001", "Уведомить при задержке", 4, 4)
		mixed.Citations = append(mixed.Citations, candidateAt(t, base, "clarification.md", "C001", "", 3, 3).Citations[0])
		only := candidateAt(t, base, "clarification.md", "C002", "Срок по договору", 3, 3)
		raw := rawFile(t, config, "ok", []legacyCandidate{mixed, only})
		view := checkView(t, config, raw)
		if view["valid"] != true || view["freshness"] != "uninitialized" || view["base_index"] != emptyAccepted().Head {
			t.Fatal("check без журнала", view)
		}
		if _, err := os.Stat(reports); !os.IsNotExist(err) {
			t.Fatal("check не должен создавать reports_dir")
		}
		if _, has := view["base_index_current"]; has {
			t.Fatal("без DECISION признака base_index_current нет")
		}
		candidates := view["candidates"].([]checkedCandidate)
		if len(candidates) != 2 || !candidates[0].Normative || candidates[1].Normative || len(candidates[0].Matches) != 0 {
			t.Fatal("признак normative и пустые подсказки", candidates)
		}
		flags := map[string]bool{}
		for _, source := range view["source_set"].([]acceptedSource) {
			flags[source.Path] = source.Reference
		}
		if !flags["clarification.md"] || flags["rules.md"] {
			t.Fatal("source_set должен нести признак reference", flags)
		}
		// Every raw defect yields the same error text as apply; the apply attempt itself creates reports_dir, so it runs last.
		_, decision := acceptedInputs(t, config, "parity", []legacyCandidate{mixed, only}, acceptOperation("accept", []string{}, "C001"), acceptOperation("defer", []string{}, "C002"))
		valid := legacyRaw{}
		if err := legacyDecode(readFixture(t, raw), &valid); err != nil {
			t.Fatal(err)
		}
		mutations := map[string]func(r *legacyRaw) []byte{
			"stale_hash": func(r *legacyRaw) []byte {
				r.SourceSet[0].SHA256 = strings.Repeat("0", 64)
				return legacyMarshal(t, *r)
			},
			"wrong_quote": func(r *legacyRaw) []byte { r.Candidates[0].Citations[0].Quote += "x"; return legacyMarshal(t, *r) },
			"extra_source": func(r *legacyRaw) []byte {
				r.SourceSet = append(r.SourceSet, legacySource{"other.md", strings.Repeat("a", 64)})
				return legacyMarshal(t, *r)
			},
			"duplicate_id": func(r *legacyRaw) []byte { r.Candidates[1].ID = r.Candidates[0].ID; return legacyMarshal(t, *r) },
			"too_many": func(r *legacyRaw) []byte {
				for len(r.Candidates) < 65 {
					r.Candidates = append(r.Candidates, r.Candidates[0])
				}
				return legacyMarshal(t, *r)
			},
			"trailing_junk": func(r *legacyRaw) []byte { return append(legacyMarshal(t, *r), '{') },
		}
		for name, mutate := range mutations {
			var copied legacyRaw
			if err := legacyDecode(readFixture(t, raw), &copied); err != nil {
				t.Fatal(err)
			}
			broken := filepath.Join(base, name+"-raw.json")
			writeFixture(t, broken, mutate(&copied))
			_, errCheck := execute([]string{"check", config, broken})
			_, errApply := execute([]string{"reconcile", config, broken, decision})
			if errCheck == nil || errApply == nil || errCheck.Error() != errApply.Error() {
				t.Fatalf("%s: check и apply должны отказывать одинаково: %v / %v", name, errCheck, errApply)
			}
		}
		// A corrupt ledger is refused before anything else, like apply.
		writeFixture(t, filepath.Join(reports, acceptedFile), []byte("{\"version\":1,"))
		_, errCheck := execute([]string{"check", config, raw})
		_, errApply := execute([]string{"reconcile", config, raw, decision})
		if errCheck == nil || errApply == nil || errCheck.Error() != errApply.Error() {
			t.Fatal("повреждённый журнал", errCheck, errApply)
		}
	})
	t.Run("hints", func(t *testing.T) {
		config, base := acceptedFixture(t)
		rules := filepath.Join(base, "source/rules.md")
		first := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
		raw, decision := acceptedInputs(t, config, "first", []legacyCandidate{first}, acceptOperation("accept", []string{}, "C001"))
		runOK(t, "reconcile", config, raw, decision)
		ledgerBytes := readFixture(t, filepath.Join(base, "runs", acceptedFile))
		// The specification gains a line: citations shift, the quote lines stay the same.
		writeFixture(t, rules, append([]byte("Вводная строка.\n"), readFixture(t, rules)...))
		same := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 3, 3)
		changed := candidateAt(t, base, "rules.md", "C002", "Лимит изменён", 3, 3)
		context := candidateAt(t, base, "rules.md", "C003", "Лимит 8 МиБ", 1, 4)
		unrelated := candidateAt(t, base, "rules.md", "C004", "Пример", 6, 6)
		view := checkView(t, config, rawFile(t, config, "shifted", []legacyCandidate{same, changed, context, unrelated}))
		if view["freshness"] != "stale" {
			t.Fatal("правка ТЗ должна давать stale", view["freshness"])
		}
		candidates := view["candidates"].([]checkedCandidate)
		want := matchHint{"REQ-AI-001", 1, 1, 1, 1, true, true, 1}
		if len(candidates[0].Matches) != 1 || candidates[0].Matches[0] != want {
			t.Fatal("сдвиг строк не должен мешать подсказке", candidates[0].Matches)
		}
		if len(candidates[1].Matches) != 1 || candidates[1].Matches[0].FieldsEqual || candidates[1].Matches[0].Overlap != 1 {
			t.Fatal("иной statement — та же цитата, fields_equal=false", candidates[1].Matches)
		}
		// tool-spec §41.3/§42.1: a quarter of the lines is a hint only by lines; the identical statement still marks a duplicate by words.
		if len(candidates[2].Matches) != 1 || candidates[2].Matches[0].Overlap != 0.25 || candidates[2].Matches[0].SharedLines != 1 || !candidates[2].Matches[0].LikelyDuplicate || candidates[2].Matches[0].TextSimilarity != 1 {
			t.Fatal("добавленный контекст снижает overlap, но подсказка остаётся", candidates[2].Matches)
		}
		if len(candidates[3].Matches) != 0 {
			t.Fatal("без общих строк и слов подсказок нет", candidates[3].Matches)
		}
		// tool-spec §42.1: a restatement in its own lines shares no citation lines but most words → text hint.
		restated := candidateAt(t, base, "rules.md", "C005", "Лимит одного файла 8 МиБ включительно", 5, 5)
		restated.Condition = "В указанной области"
		digest := checkView(t, config, rawFile(t, config, "digest", []legacyCandidate{restated}))
		textual := digest["candidates"].([]checkedCandidate)[0].Matches
		if len(textual) != 1 || textual[0].ID != "REQ-AI-001" || textual[0].SharedLines != 0 || !textual[0].LikelyDuplicate || textual[0].TextSimilarity < likelyDuplicateText {
			t.Fatal("свод своими строками ловится по словам", textual)
		}
		if !bytes.Equal(ledgerBytes, readFixture(t, filepath.Join(base, "runs", acceptedFile))) {
			t.Fatal("check изменил журнал")
		}
	})
	t.Run("hint_limit", func(t *testing.T) {
		config, base := acceptedFixture(t)
		candidates, ops := []legacyCandidate{}, []AcceptedOperation{}
		for i, statement := range []string{"Первая", "Вторая", "Третья", "Четвёртая", "Пятая"} {
			id := "C00" + string(rune('1'+i))
			candidates = append(candidates, candidateAt(t, base, "rules.md", id, statement, 2, 3))
			ops = append(ops, acceptOperation("accept", []string{}, id))
		}
		raw, decision := acceptedInputs(t, config, "five", candidates, ops...)
		runOK(t, "reconcile", config, raw, decision)
		view := checkView(t, config, rawFile(t, config, "probe", []legacyCandidate{candidateAt(t, base, "rules.md", "C001", "Первая", 2, 3)}))
		matches := view["candidates"].([]checkedCandidate)[0].Matches
		if len(matches) != matchHintLimit || matchHintLimit != 4 || matches[0].ID != "REQ-AI-001" || !matches[0].FieldsEqual || matches[1].FieldsEqual || matches[0].UniqueShared != 0 {
			t.Fatal("лимит подсказок, порядок по ID при равном overlap, общие строки не уникальны", matches)
		}
	})
	t.Run("unique_shared", func(t *testing.T) {
		config, base := acceptedFixture(t)
		// Two norms share line 2; line 3 belongs to the first norm alone.
		wide := candidateAt(t, base, "rules.md", "C001", "Лимит и название", 2, 3)
		narrow := candidateAt(t, base, "rules.md", "C002", "Только лимит", 2, 2)
		raw, decision := acceptedInputs(t, config, "pair", []legacyCandidate{wide, narrow}, acceptOperation("accept", []string{}, "C001"), acceptOperation("accept", []string{}, "C002"))
		runOK(t, "reconcile", config, raw, decision)
		view := checkView(t, config, rawFile(t, config, "probe", []legacyCandidate{
			candidateAt(t, base, "rules.md", "C001", "Лимит и название", 2, 3),
			candidateAt(t, base, "rules.md", "C002", "Только лимит", 2, 2)}))
		candidates := view["candidates"].([]checkedCandidate)
		byID := func(matches []matchHint, id string) matchHint {
			for _, match := range matches {
				if match.ID == id {
					return match
				}
			}
			t.Fatal("нет подсказки", id, matches)
			return matchHint{}
		}
		if first := byID(candidates[0].Matches, "REQ-AI-001"); first.SharedLines != 2 || first.UniqueShared != 1 {
			t.Fatal("уникальная строка 3 должна учитываться один раз", first)
		}
		if second := byID(candidates[0].Matches, "REQ-AI-002"); second.SharedLines != 1 || second.UniqueShared != 0 {
			t.Fatal("общая строка 2 не уникальна", second)
		}
		if only := byID(candidates[1].Matches, "REQ-AI-002"); only.UniqueShared != 0 || only.Overlap != 1 {
			t.Fatal("кандидат из одной общей строки", only)
		}
	})
	t.Run("decision", func(t *testing.T) {
		config, base := acceptedFixture(t)
		reports := filepath.Join(base, "runs")
		assignmentsOf := func(view map[string]any) []checkedAssignment { return view["assignments"].([]checkedAssignment) }
		// accept: predicted ID, head and no files; apply then confirms the prediction byte for byte.
		first := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)
		raw, decision := acceptedInputs(t, config, "accept", []legacyCandidate{first}, acceptOperation("accept", []string{}, "C001"))
		view := checkView(t, config, raw, decision)
		want := []checkedAssignment{{"C001", "REQ-AI-001", "accept", 1, []string{}}}
		if view["valid"] != true || view["duplicate"] != false || view["base_index_current"] != true || !reflect.DeepEqual(assignmentsOf(view), want) || len(view["retired"].([]string)) != 0 || view["next_head"] == view["base_index"] {
			t.Fatal("dry-run accept", view)
		}
		if _, err := os.Stat(reports); !os.IsNotExist(err) {
			t.Fatal("check с DECISION не должен создавать reports_dir")
		}
		applied := runOK(t, "reconcile", config, raw, decision).(map[string]any)
		if applied["base_index"] != view["next_head"] {
			t.Fatal("apply должен дать предсказанный head", applied["base_index"], view["next_head"])
		}
		state := acceptedRead(t, config)
		if !reflect.DeepEqual(state.History[0].Assignments, []AcceptedAssignment{{"C001", "REQ-AI-001"}}) {
			t.Fatal("assignments apply и check расходятся", state.History[0].Assignments)
		}
		ledgerBytes := readFixture(t, filepath.Join(reports, acceptedFile))
		// Exact replay: duplicate without freshness in both answers; same id with other bytes: refused by both.
		replay := checkView(t, config, raw, decision)
		if replay["duplicate"] != true || replay["valid"] != true {
			t.Fatal("точный повтор — duplicate", replay)
		}
		if _, has := replay["freshness"]; has {
			t.Fatal("duplicate не подтверждает свежесть")
		}
		if _, has := replay["base_index_current"]; has {
			t.Fatal("duplicate не утверждает актуальность base_index")
		}
		again := runOK(t, "reconcile", config, raw, decision).(map[string]any)
		if _, has := again["freshness"]; has || again["duplicate"] != true {
			t.Fatal("apply duplicate без freshness", again)
		}
		conflict := filepath.Join(base, "conflict-decision.json")
		writeFixture(t, conflict, bytes.Replace(readFixture(t, decision), []byte("Решение хоста по источникам"), []byte("Другая причина"), 1))
		_, errCheck := execute([]string{"check", config, raw, conflict})
		_, errApply := execute([]string{"reconcile", config, raw, conflict})
		if errCheck == nil || errApply == nil || errCheck.Error() != errApply.Error() {
			t.Fatal("повтор id с другими байтами", errCheck, errApply)
		}
		// rebind keeps ID and revision; revise raises it; split retires the parent and issues new IDs.
		raw, decision = acceptedInputs(t, config, "rebind", []legacyCandidate{first}, acceptOperation("rebind", []string{"REQ-AI-001"}, "C001"))
		if got := assignmentsOf(checkView(t, config, raw, decision)); !reflect.DeepEqual(got, []checkedAssignment{{"C001", "REQ-AI-001", "rebind", 1, []string{"REQ-AI-001"}}}) {
			t.Fatal("rebind", got)
		}
		revised := candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ включительно", 2, 2)
		raw, decision = acceptedInputs(t, config, "revise", []legacyCandidate{revised}, acceptOperation("revise", []string{"REQ-AI-001"}, "C001"))
		if got := assignmentsOf(checkView(t, config, raw, decision)); !reflect.DeepEqual(got, []checkedAssignment{{"C001", "REQ-AI-001", "revise", 2, []string{"REQ-AI-001"}}}) {
			t.Fatal("revise", got)
		}
		second := candidateAt(t, base, "rules.md", "C002", "Название обязательно", 3, 3)
		raw, decision = acceptedInputs(t, config, "split", []legacyCandidate{first, second}, acceptOperation("split", []string{"REQ-AI-001"}, "C001", "C002"))
		split := checkView(t, config, raw, decision)
		if got := assignmentsOf(split); !reflect.DeepEqual(got, []checkedAssignment{{"C001", "REQ-AI-002", "split", 1, []string{"REQ-AI-001"}}, {"C002", "REQ-AI-003", "split", 1, []string{"REQ-AI-001"}}}) || !reflect.DeepEqual(split["retired"], []string{"REQ-AI-001"}) {
			t.Fatal("split", split)
		}
		// Mutations refuse with the apply text; the ledger stays as applied.
		twin := candidateAt(t, base, "rules.md", "C002", "Лимит 8 МиБ", 2, 2)
		for name, scenario := range map[string]struct {
			candidates []legacyCandidate
			ops        []AcceptedOperation
			text       string
		}{
			"rebind_changed":   {[]legacyCandidate{revised}, []AcceptedOperation{acceptOperation("rebind", []string{"REQ-AI-001"}, "C001")}, "rebind сохраняет содержание"},
			"revise_unchanged": {[]legacyCandidate{twin}, []AcceptedOperation{acceptOperation("revise", []string{"REQ-AI-001"}, "C002")}, "rebind сохраняет содержание"},
			"incomplete":       {[]legacyCandidate{first, second}, []AcceptedOperation{acceptOperation("accept", []string{}, "C001")}, "полный учёт"},
		} {
			raw, decision = acceptedInputs(t, config, name, scenario.candidates, scenario.ops...)
			_, errCheck := execute([]string{"check", config, raw, decision})
			_, errApply := execute([]string{"reconcile", config, raw, decision})
			if errCheck == nil || errApply == nil || errCheck.Error() != errApply.Error() || !strings.Contains(errCheck.Error(), scenario.text) {
				t.Fatalf("%s: %v / %v", name, errCheck, errApply)
			}
		}
		stale := filepath.Join(base, "stale-decision.json")
		writeFixture(t, stale, bytes.Replace(readFixture(t, decision), []byte(state.Head), []byte(strings.Repeat("0", 64)), 1))
		junk := filepath.Join(base, "junk-decision.json")
		writeFixture(t, junk, append(readFixture(t, decision), '{'))
		for _, broken := range []string{stale, junk} {
			_, errCheck := execute([]string{"check", config, raw, broken})
			_, errApply := execute([]string{"reconcile", config, raw, broken})
			if errCheck == nil || errApply == nil || errCheck.Error() != errApply.Error() {
				t.Fatal(broken, errCheck, errApply)
			}
		}
		if !bytes.Equal(ledgerBytes, readFixture(t, filepath.Join(reports, acceptedFile))) {
			t.Fatal("отказы изменили журнал")
		}
		// merge, retire, defer on a two-norm index.
		config, base = acceptedFixture(t)
		raw, decision = acceptedInputs(t, config, "two", []legacyCandidate{first, second}, acceptOperation("accept", []string{}, "C001"), acceptOperation("accept", []string{}, "C002"))
		runOK(t, "reconcile", config, raw, decision)
		merged := candidateAt(t, base, "rules.md", "C001", "Лимит и название", 2, 3)
		raw, decision = acceptedInputs(t, config, "merge", []legacyCandidate{merged}, acceptOperation("merge", []string{"REQ-AI-001", "REQ-AI-002"}, "C001"))
		merge := checkView(t, config, raw, decision)
		if got := assignmentsOf(merge); !reflect.DeepEqual(got, []checkedAssignment{{"C001", "REQ-AI-003", "merge", 1, []string{"REQ-AI-001", "REQ-AI-002"}}}) || !reflect.DeepEqual(merge["retired"], []string{"REQ-AI-001", "REQ-AI-002"}) {
			t.Fatal("merge", merge)
		}
		raw, decision = acceptedInputs(t, config, "retire", []legacyCandidate{second, candidateAt(t, base, "rules.md", "C003", "Пример", 5, 5)},
			acceptOperation("retire", []string{"REQ-AI-001"}), acceptOperation("rebind", []string{"REQ-AI-002"}, "C002"), acceptOperation("defer", []string{}, "C003"))
		retire := checkView(t, config, raw, decision)
		if got := assignmentsOf(retire); !reflect.DeepEqual(got, []checkedAssignment{{"C002", "REQ-AI-002", "rebind", 1, []string{"REQ-AI-002"}}}) || !reflect.DeepEqual(retire["retired"], []string{"REQ-AI-001"}) {
			t.Fatal("retire/rebind/defer", retire)
		}
		// tool-spec §38.1: the remainder is named in the dry-run, the apply answer and the index summary.
		if !reflect.DeepEqual(retire["deferred"], []string{"C003"}) || !reflect.DeepEqual(retire["rejected"], []string{}) {
			t.Fatal("deferred/rejected в ответе check", retire["deferred"], retire["rejected"])
		}
		applied = runOK(t, "reconcile", config, raw, decision).(map[string]any)
		if !reflect.DeepEqual(applied["deferred"], []string{"C003"}) || !reflect.DeepEqual(applied["rejected"], []string{}) {
			t.Fatal("deferred/rejected в ответе reconcile", applied["deferred"])
		}
		if accepted := runOK(t, "index", config, "summary").(map[string]any)["accepted"].(map[string]any); !reflect.DeepEqual(accepted["last_deferred"], []string{"C003"}) || accepted["history_total"] != 2 {
			t.Fatal("index summary называет остаток последнего пакета", accepted)
		}
		// Reference-only target: the guard text is shared with apply.
		config, base = referenceFixture(t)
		only := candidateAt(t, base, "clarification.md", "C001", "Срок по договору", 3, 3)
		raw, decision = acceptedInputs(t, config, "guard", []legacyCandidate{only}, acceptOperation("accept", []string{}, "C001"))
		_, errCheck = execute([]string{"check", config, raw, decision})
		_, errApply = execute([]string{"reconcile", config, raw, decision})
		if errCheck == nil || errApply == nil || errCheck.Error() != errApply.Error() || !strings.Contains(errCheck.Error(), "C001") {
			t.Fatal("guard", errCheck, errApply)
		}
	})
	t.Run("process", func(t *testing.T) {
		if testing.Short() {
			t.Skip("нативная сборка и процессы")
		}
		config, base := acceptedFixture(t)
		raw := rawFile(t, config, "ok", []legacyCandidate{candidateAt(t, base, "rules.md", "C001", "Лимит 8 МиБ", 2, 2)})
		binary := filepath.Join(base, "spec-audit")
		if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
			t.Fatalf("сборка: %v: %s", err, output)
		}
		run := func(args ...string) (string, string, error) {
			cmd := exec.Command(binary, args...)
			cmd.Dir, cmd.Env = base, []string{"PATH=", "LOG_LEVEL=error"}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			return stdout.String(), stderr.String(), err
		}
		stdout, stderr, err := run("check", "config.yaml", filepath.Base(raw))
		if err != nil || !json.Valid([]byte(stdout)) || stderr != "" {
			t.Fatal("check через процесс", err, stdout, stderr)
		}
		if _, stderr, err := run("check", "config.yaml", "missing.json"); err == nil || !json.Valid([]byte(stderr)) {
			t.Fatal("отказ должен быть JSON в stderr", err, stderr)
		}
	})
}
