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
	"regexp"
	"strings"
	"testing"
)

func navigationReport(t *testing.T, config, base, run string) Report {
	t.Helper()
	runOK(t, "report", config, run)
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs", run, "report.json")), &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestReportNavigationLifecycle(t *testing.T) {
	config, base, batch := reviewedFixture(t, false)
	decision := reviewInput(t, config)
	// This file has no role citations; only the host links it.
	data := readFixture(t, filepath.Join(base, "source/go.mod"))
	quote, err := lineQuote(data, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	decision.Assessments[0].Code = []Citation{{"go.mod", 1, 1, quote}}
	decision.Assessments[0].Implementation = "unknown"
	runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
	check := func(report Report, current bool) {
		t.Helper()
		if len(report.Navigation.Files) != len(batch.Files) {
			t.Fatal("inventory lost files")
		}
		for i, file := range report.Navigation.Files {
			if file.SourceFile != batch.Files[i] {
				t.Fatal("inventory drift")
			}
			if file.Path == "go.mod" {
				if file.Current != current || len(file.Evidence) != 1 || len(file.Evidence[0].Links) != 1 || file.Evidence[0].Links[0].Current != current {
					t.Fatal("host-only history mistaken for current link")
				}
			}
		}
	}
	report := navigationReport(t, config, base, "review")
	check(report, true)
	if report.Requirements[0].Navigation.Basis != "host-current" || !oneOf("unknown", report.Requirements[0].Navigation.Flags...) {
		t.Fatal("current host not used")
	}
	if len(report.Executions) != 0 || report.Requirements[0].Roles[0].Executions[0].State != "not_recorded" {
		t.Fatal("citation manufactured execution")
	}
	runOK(t, "retry", config, "review", batch.Tasks[0].TaskID)
	report = navigationReport(t, config, base, "review")
	check(report, false)
	if report.HostReview.State != "outdated" || report.Requirements[0].Navigation.Basis != "roles" || !oneOf("pending", report.Requirements[0].Navigation.Flags...) {
		t.Fatal("pending/outdated hidden")
	}
	writeFixture(t, filepath.Join(base, "source/go.mod"), []byte("changed\n"))
	report = navigationReport(t, config, base, "review")
	check(report, false)
	for _, file := range report.Navigation.Files {
		if file.Current {
			t.Fatal("stale has current link")
		}
		if file.Path == "go.mod" && file.Evidence[0].Citation.Quote != quote {
			t.Fatal("historical quote replaced by current source")
		}
	}
	if report.Requirements[0].Navigation.Basis != "history" {
		t.Fatal("stale basis hidden")
	}
}

func TestReportNavigationInventoryAndQuestions(t *testing.T) {
	config, base := fixture(t)
	runOK(t, "prepare", config, "pending")
	pending := navigationReport(t, config, base, "pending")
	for _, row := range pending.Requirements {
		if !reflect.DeepEqual(row.Navigation.Flags, []string{"pending", "unreviewed"}) {
			t.Fatal("absence of response invented a verdict", row.Navigation.Flags)
		}
	}
	// Projection must also retain accepted unresolved questions before any role responds.
	c := Citation{"ТЗ #1:.md", 1, 1, "<script>not executable</script>"}
	req := Requirement{ID: "REQ-NAV-001", Accepted: &AcceptedDetails{Clarity: "clear", Citations: []Citation{c}, Unresolved: []string{"Какой срок?"}}}
	m := Manifest{Files: []SourceFile{{Path: c.Path, Kind: "spec"}, {Path: "context.md", Kind: "spec"}}, Requirements: []Requirement{req}}
	// Same limits as the configured inventory; no quoted source contents copied wholesale.
	for i := 0; i < 24998; i++ {
		m.Files = append(m.Files, SourceFile{Path: fmt.Sprintf("src/file-%05d.go", i), Kind: "code"})
	}
	state := newState(m)
	r := makeReport("inventory", m, state, true)
	buildNavigation(&r, m)
	if len(r.Navigation.Files) != 25000 || r.Navigation.Groups[0].Unlinked != 1 || r.Navigation.Groups[1].Unlinked != 24998 || r.Navigation.Groups[2].Files != 0 {
		t.Fatal("unlinked/empty inventory incorrectly counted")
	}
	if !oneOf("spec_question", r.Requirements[0].Navigation.Flags...) {
		t.Fatal("accepted question hidden before assessment")
	}
	// One fragment can link to more than one requirement; duplicates from the same origin collapse.
	row := r.Requirements[0]
	row.Requirement.ID = "REQ-NAV-002"
	row.Scope = "another-scope"
	r.Requirements = append(r.Requirements, row)
	buildNavigation(&r, m)
	if len(r.Navigation.Files[0].Evidence) != 1 || len(r.Navigation.Files[0].Evidence[0].Links) != 2 {
		t.Fatal("shared fragment duplicated or reverse link lost")
	}
	if r.Requirements[1].Scope != "another-scope" {
		t.Fatal("file scope replaced requirement scope")
	}
}

func TestReportNavigationHTML(t *testing.T) {
	config, base, batch := reviewedFixture(t, false)
	for _, phase := range []string{"roles", "current", "outdated", "stale"} {
		switch phase {
		case "current":
			decision := reviewInput(t, config)
			runOK(t, "review", config, "review", writeReviewInput(t, base, decision))
		case "outdated":
			runOK(t, "retry", config, "review", batch.Tasks[0].TaskID)
		case "stale":
			writeFixture(t, filepath.Join(base, "source/source.go"), []byte("changed\n"))
		}
		navigationReport(t, config, base, "review")
		checkReportAnchors(t, readFixture(t, filepath.Join(base, "runs/review/report.html")))
	}
	// Quotes and paths may contain URL delimiters and markup, but never become URLs/JS.
	c := Citation{"ТЗ #\"':.md", 1, 1, "</pre><script>alert('payload')</script><img src=x onerror=alert(1)>"}
	m := Manifest{Files: []SourceFile{{Path: c.Path, Kind: "spec"}}, Requirements: []Requirement{{ID: "REQ-NAV-001", Source: c}}}
	r := makeReport("escape", m, newState(m), true)
	buildNavigation(&r, m)
	var page bytes.Buffer
	if err := reportTemplate.Execute(&page, r); err != nil {
		t.Fatal(err)
	}
	checkReportAnchors(t, page.Bytes())
	if !strings.Contains(page.String(), html.EscapeString(c.Quote)) || strings.Contains(page.String(), c.Quote) || strings.Contains(page.String(), "ZgotmplZ") {
		t.Fatal("missing/unsafe quoted source")
	}
	if strings.Count(page.String(), "<script>") != 1 || strings.Contains(page.String(), "src=\"") || strings.Contains(page.String(), "href=\"http") {
		t.Fatal("unexpected script or external resource")
	}
}

func checkReportAnchors(t *testing.T, page []byte) {
	t.Helper()
	ids := map[string]bool{}
	for _, match := range regexp.MustCompile(`\bid="([^"]+)"`).FindAllSubmatch(page, -1) {
		id := string(match[1])
		if ids[id] {
			t.Fatal("duplicate HTML id", id)
		}
		ids[id] = true
	}
	for _, match := range regexp.MustCompile(`href="#([^"]+)"`).FindAllSubmatch(page, -1) {
		if !ids[string(match[1])] {
			t.Fatal("broken internal anchor", string(match[1]))
		}
	}
}

func TestReportNavigationDisagreementBasis(t *testing.T) {
	config, _, _ := reviewedFixture(t, false)
	view := runOK(t, "review", config, "review").(ReviewContext)
	m := Manifest{Requirements: view.Requirements}
	state := State{Entries: view.Entries, Executions: []Receipt{}}
	state.Entries[1].Result.Assessments[0].Implementation = "contradicted"
	r := makeReport("basis", m, state, true)
	buildNavigation(&r, m)
	if !oneOf("disagreement", r.Requirements[0].Navigation.Flags...) || !oneOf("implementation_gap", r.Requirements[0].Navigation.Flags...) {
		t.Fatal("preliminary differences hidden")
	}
	r.HostReview = &ReviewSummary{State: "current", Latest: &ReviewDecision{ReviewID: "host", Assessments: state.Entries[0].Result.Assessments}}
	buildNavigation(&r, m)
	if !oneOf("disagreement", r.Requirements[0].Navigation.Flags...) || oneOf("implementation_gap", r.Requirements[0].Navigation.Flags...) || oneOf("unreviewed", r.Requirements[0].Navigation.Flags...) {
		t.Fatal("current decision either overwrote roles or ignored")
	}
}

func TestReportMetricsSeparateEvidenceAndFreshness(t *testing.T) {
	c := Citation{"TZ/section.md", 1, 3, "requirement"}
	m := Manifest{Config: Config{ProjectRoot: "/work/project"}, Files: []SourceFile{
		{Path: c.Path, Kind: "spec"}, {Path: "TZ/other.md", Kind: "spec"},
		{Path: "src/code.go", Kind: "code"}, {Path: "tests/code_test.go", Kind: "tests"},
	}}
	r := Report{Status: Status{Freshness: "fresh"}, HostReview: &ReviewSummary{
		State: "current", Latest: &ReviewDecision{ReviewID: "host"},
	}}
	for i, assertion := range []string{"relevant", "weak", "contradicts", "missing", "unknown", "relevant", "relevant"} {
		id := fmt.Sprintf("REQ-MET-00%d", i+1)
		req := Requirement{ID: id, Source: c}
		a := Assessment{RequirementID: id, Specification: "clear", Implementation: "supported", Assertion: assertion,
			Tests: []TestCitation{{TestID: "TestShared", Citation: Citation{"tests/code_test.go", 1, 1, "assert"}}},
		}
		if i == 2 {
			a.Implementation = "contradicted"
		}
		if i == 4 {
			a.Implementation = "unknown"
		}
		if i == 5 {
			a.Specification = "ambiguous"
		}
		if i == 6 {
			req.Accepted = &AcceptedDetails{Clarity: "clear", Unresolved: []string{"Threshold?"}, Citations: []Citation{c, {"TZ/other.md", 1, 1, "context"}}}
		}
		r.Requirements = append(r.Requirements, RequirementReport{Requirement: req})
		r.HostReview.Latest.Assessments = append(r.HostReview.Latest.Assessments, a)
	}
	// A shared green receipt must neither multiply counts nor upgrade weak/missing.
	r.Executions = []Receipt{{ID: "green", State: "passed", Tests: []ExecutedTest{{ID: "TestShared", State: "passed"}}}}
	buildNavigation(&r, m)
	want := AuditMetrics{Total: 7, Reviewed: 7, Supported: 5, Gaps: 1, Unknown: 1, Questions: 2,
		WithTests: 7, Relevant: 3, Weak: 1, Contradicts: 1, Missing: 1, TestUnknown: 1, Ready: 1}
	if r.Navigation.Metrics != want || r.Navigation.Sections[0].AuditMetrics != want || r.Navigation.Sections[1].Total != 0 {
		t.Fatal("counts must use primary sources, not citations or receipts", r.Navigation)
	}
	if r.Navigation.Editor != (EditorDefaults{CodeRoot: "/work/project", SpecRoot: "/work/project/TZ", SpecPrefix: "TZ/"}) {
		t.Fatal("spec root must strip its exact shared prefix once", r.Navigation.Editor)
	}
	if !oneOf("partial_test", r.Requirements[1].Navigation.Flags...) || !oneOf("missing_test", r.Requirements[3].Navigation.Flags...) {
		t.Fatal("test insufficiency filters lost")
	}
	decided := r.Navigation.Metrics
	for _, phase := range []string{"outdated", "missing"} {
		r.HostReview.State, r.HostReview.Complete = phase, false
		buildNavigation(&r, m)
		if r.Navigation.Metrics != (AuditMetrics{Total: 7, Unreviewed: 7}) {
			t.Fatal("historical or preliminary data promoted to current", phase, r.Navigation.Metrics)
		}
		var page bytes.Buffer
		if err := reportTemplate.Execute(&page, r); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(page.String(), "не оценено") || strings.Contains(page.String(), "0.0%") {
			t.Fatal("unreviewed represented as zero coverage")
		}
	}
	// tool-spec §65: a complete decision keeps its numbers when the snapshot goes stale afterwards — the page says so
	// instead of blanking them; the basis of every row names the stale snapshot.
	r.Freshness, r.HostReview.State, r.HostReview.Complete = "stale", "outdated", true
	buildNavigation(&r, m)
	if r.Navigation.Metrics != decided || !r.HostReviewComplete || r.Requirements[0].Navigation.Basis != "host-stale" || oneOf("unreviewed", r.Requirements[0].Navigation.Flags...) {
		t.Fatal("complete decision on a stale snapshot must keep its metrics", r.Navigation.Metrics, r.Requirements[0].Navigation)
	}
	var stalePage bytes.Buffer
	if err := reportTemplate.Execute(&stalePage, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stalePage.String(), "Snapshot изменился после решения хоста") || strings.Contains(stalePage.String(), "Нет полного согласования") {
		t.Fatal("stale page must explain the snapshot, not report «не оценено»")
	}
	for _, value := range []string{"relevant", "weak", "contradicts", "missing", "unknown"} {
		if assertionLabel(value) == value || assertionLabel(value) == "" {
			t.Fatal("unexplained assertion", value)
		}
	}
}

func TestReportEditorRootsAndEmptyMetrics(t *testing.T) {
	for _, tc := range []struct {
		paths  []string
		prefix string
	}{
		{[]string{"TZ/nested/a.md", "TZ/nested/b.md"}, "TZ/nested/"},
		{[]string{"TZ/a.md", "TZ-more/b.md"}, ""},
		{[]string{"rules.md", "TZ/a.md"}, ""},
		{[]string{"TZ/a.md", "docs/b.md"}, ""},
	} {
		m := Manifest{Config: Config{ProjectRoot: "/new/project"}}
		for _, p := range tc.paths {
			m.Files = append(m.Files, SourceFile{Kind: "spec", Path: p})
		}
		r := Report{}
		buildNavigation(&r, m)
		if r.Navigation.Editor.SpecPrefix != tc.prefix || r.Navigation.Editor.SpecRoot != filepath.Join("/new/project", tc.prefix) {
			t.Fatal("incorrect shared directory", r.Navigation.Editor)
		}
		var html bytes.Buffer
		if err := reportTemplate.Execute(&html, r); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(html.String(), "NaN") || strings.Contains(html.String(), "100.0%") {
			t.Fatal("empty section manufactured coverage")
		}
	}
}

// Optional output is a NEW synthetic page for the dev-only browser check, never a real audit.
func TestReportUIFixture(t *testing.T) {
	spec := Citation{"TZ/Цена #50% '\" &.md", 12, 13, "requirement <script>window.injected = true</script>"}
	code := Citation{"src/calc:9.go", 27, 27, "return price"}
	test := TestCitation{TestID: "TestPrice", Citation: Citation{"tests/check_test.go", 31, 31, "assert(price)"}}
	m := Manifest{Config: Config{ProjectRoot: "/workspace/My Project", Scopes: []Scope{{ID: "all"}}}, Files: []SourceFile{
		{Path: spec.Path, Kind: "spec"}, {Path: "TZ/other.md", Kind: "spec"},
		{Path: code.Path, Kind: "code"}, {Path: test.Citation.Path, Kind: "tests"},
	}}
	r := Report{Status: Status{RunID: "ui-fixture", Freshness: "fresh", HostReviewState: "current", RequirementsTotal: 2},
		HostReview: &ReviewSummary{State: "current", Latest: &ReviewDecision{ReviewID: "host"}}}
	for i, assertion := range []string{"weak", "relevant"} {
		id := fmt.Sprintf("REQ-UI-00%d", i+1)
		source := spec
		if i == 1 {
			source = Citation{"TZ/other.md", 1, 1, "another requirement"}
		}
		a := Assessment{RequirementID: id, Specification: "clear", Implementation: "supported", Assertion: assertion,
			Statement: "Проверяем код и существенные результаты отдельно.", Spec: []Citation{source}, Code: []Citation{code}, Tests: []TestCitation{test}}
		r.Requirements = append(r.Requirements, RequirementReport{Requirement: Requirement{ID: id, Title: "Тестовая норма", Source: source}, Scope: "all",
			Roles: []RoleReport{{Role: "mapper", Attempt: 1, Assessment: &a}, {Role: "redteam", Attempt: 1, Assessment: &a}}})
		r.HostReview.Latest.Assessments = append(r.HostReview.Latest.Assessments, a)
	}
	buildNavigation(&r, m)
	var page bytes.Buffer
	if err := reportTemplate.Execute(&page, r); err != nil {
		t.Fatal(err)
	}
	checkReportAnchors(t, page.Bytes())
	for _, text := range []string{assertionLabel("weak"), "50.0%", "section-filter", "prefers-color-scheme:dark", "Нет записанного исполнения."} {
		if !strings.Contains(page.String(), text) {
			t.Fatal("missing UI contract", text)
		}
	}
	if output := os.Getenv("SPEC_AUDIT_HTML_FIXTURE"); output != "" {
		if err := os.WriteFile(output, page.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
