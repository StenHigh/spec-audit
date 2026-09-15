//go:build darwin || linux

package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed report.html
var reportHTML string

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"citationID":     citationID,
	"join":           strings.Join,
	"assertionLabel": assertionLabel,
	"percent": func(count, total, reviewed int) string {
		if total == 0 {
			return "—"
		}
		if reviewed == 0 {
			return "не оценено"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(count)/float64(total))
	},
}).Parse(reportHTML))

// Navigation is a projection, never an additional audit or a persisted decision.
type RequirementNavigation struct {
	Section        string          `json:"section"`
	Basis          string          `json:"basis"`
	Flags          []string        `json:"flags"`
	Host           *Assessment     `json:"-"`
	HostExecutions []TestExecution `json:"host_executions"`
}

type NavigationLink struct {
	RequirementID string `json:"requirement_id"`
	Origin        string `json:"origin"`
	Current       bool   `json:"current"`
	TestID        string `json:"test_id,omitempty"`
}

type NavigationEvidence struct {
	ID       string           `json:"id"`
	Citation Citation         `json:"citation"`
	Links    []NavigationLink `json:"links"`
}

type NavigationFile struct {
	SourceFile
	ID       string               `json:"id"`
	Current  bool                 `json:"has_current_links"`
	Evidence []NavigationEvidence `json:"evidence"`
}

type NavigationGroup struct {
	Kind      string  `json:"kind"`
	Selection Sources `json:"selection"`
	Files     int     `json:"files"`
	Unlinked  int     `json:"without_current_links"`
}

type NavigationFilter struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type ReportNavigation struct {
	Groups   []NavigationGroup  `json:"groups"`
	Files    []NavigationFile   `json:"files"`
	Filters  []NavigationFilter `json:"filters"`
	Scopes   []Scope            `json:"scopes"`
	Metrics  AuditMetrics       `json:"metrics"`
	Sections []AuditSection     `json:"sections"`
	Editor   EditorDefaults     `json:"editor"`
}

type EditorDefaults struct {
	CodeRoot   string `json:"code_root"`
	SpecRoot   string `json:"spec_root"`
	SpecPrefix string `json:"spec_prefix"`
}

type AuditSection struct {
	Path   string `json:"path"`
	FileID string `json:"file_id"`
	AuditMetrics
}

// Each counter counts requirements, never citations, roles, test cases or issues.
type AuditMetrics struct {
	Total       int `json:"total"`
	Reviewed    int `json:"reviewed"`
	Unreviewed  int `json:"unreviewed"`
	Supported   int `json:"supported"`
	Gaps        int `json:"gaps"`
	Unknown     int `json:"unknown"`
	Questions   int `json:"questions"`
	WithTests   int `json:"with_tests"`
	Relevant    int `json:"relevant"`
	Weak        int `json:"weak"`
	Contradicts int `json:"contradicts"`
	Missing     int `json:"missing"`
	TestUnknown int `json:"test_unknown"`
	Ready       int `json:"ready"`
}

func (counts *AuditMetrics) add(row RequirementReport, current bool) {
	counts.Total++
	a := row.Navigation.Host
	if !current || a == nil {
		counts.Unreviewed++
		return
	}
	counts.Reviewed++
	switch a.Implementation {
	case "supported":
		counts.Supported++
	case "contradicted":
		counts.Gaps++
	default:
		counts.Unknown++
	}
	if len(a.Tests) != 0 {
		counts.WithTests++
	}
	switch a.Assertion {
	case "relevant":
		counts.Relevant++
	case "weak":
		counts.Weak++
	case "contradicts":
		counts.Contradicts++
	case "missing":
		counts.Missing++
	default:
		counts.TestUnknown++
	}
	question := oneOf("spec_question", row.Navigation.Flags...)
	if question {
		counts.Questions++
	}
	if a.Specification == "clear" && a.Implementation == "supported" && a.Assertion == "relevant" && !question {
		counts.Ready++
	}
}

func assertionLabel(value string) string {
	switch value {
	case "relevant":
		return "Достаточность подтверждена автором оценки"
	case "weak":
		return "Тесты есть, но проверка частичная или слабая"
	case "contradicts":
		return "Тестовая проверка противоречит требованию"
	case "missing":
		return "Подходящая проверка не найдена"
	default:
		return "Достаточность тестов неизвестна"
	}
}

func citationID(c Citation) string {
	data, _ := json.Marshal(c)
	return "e-" + digest(data)
}

func assessmentExecutions(assessment Assessment, receipts []Receipt) []TestExecution {
	result := []TestExecution{}
	for _, test := range assessment.Tests {
		latest := TestExecution{TestID: test.TestID, State: "not_recorded"}
		for _, receipt := range receipts {
			for _, executed := range receipt.Tests {
				if executed.ID == test.TestID {
					status := executed.State
					if receipt.State == "stale" {
						status = "stale"
					}
					latest = TestExecution{test.TestID, receipt.ID, status}
				}
			}
		}
		result = append(result, latest)
	}
	return result
}

func buildNavigation(report *Report, m Manifest) {
	slog.Debug("начало карты отчёта", "files", len(m.Files), "requirements", len(report.Requirements))
	nav := ReportNavigation{
		Groups: []NavigationGroup{{Kind: "spec", Selection: m.Config.Specs}, {Kind: "code", Selection: m.Config.Code}, {Kind: "tests", Selection: m.Config.Tests}},
		Files:  []NavigationFile{}, Scopes: m.Config.Scopes,
		Sections: []AuditSection{},
		Editor:   EditorDefaults{CodeRoot: m.Config.ProjectRoot},
		Filters: []NavigationFilter{
			{ID: "implementation_gap", Label: "Противоречие реализации"},
			{ID: "test_gap", Label: "Недостаточно проверок в тестах"},
			{ID: "partial_test", Label: "Тесты есть, но проверка частичная / слабая"},
			{ID: "missing_test", Label: "Подходящая проверка не найдена"},
			{ID: "spec_question", Label: "Вопросы к ТЗ"},
			{ID: "unknown", Label: "Неизвестность"},
			{ID: "pending", Label: "Ожидается ответ роли"},
			{ID: "disagreement", Label: "Различаются оценки ролей"},
			{ID: "unreviewed", Label: "Нет текущего согласования"},
		},
	}
	files := map[string]int{}
	sections := map[string]int{}
	specDir := ""
	evidence := map[string]int{}
	for _, file := range m.Files {
		files[file.Path] = len(nav.Files)
		nav.Files = append(nav.Files, NavigationFile{SourceFile: file, ID: "f-" + digest([]byte(file.Path)), Evidence: []NavigationEvidence{}})
		if file.Kind == "spec" {
			sections[file.Path] = len(nav.Sections)
			nav.Sections = append(nav.Sections, AuditSection{Path: file.Path, FileID: nav.Files[len(nav.Files)-1].ID})
			if specDir == "" {
				specDir = path.Dir(file.Path)
			}
			for specDir != "." && !strings.HasPrefix(file.Path, specDir+"/") {
				specDir = path.Dir(specDir)
			}
		}
	}
	if specDir != "" && specDir != "." {
		nav.Editor.SpecPrefix = specDir + "/"
	}
	nav.Editor.SpecRoot = filepath.Join(m.Config.ProjectRoot, specDir)
	add := func(c Citation, link NavigationLink) {
		index, ok := files[c.Path]
		if !ok { // Only manifest files can be navigated, including in historical reports.
			return
		}
		file := &nav.Files[index]
		file.Current = file.Current || link.Current
		id := citationID(c)
		position, exists := evidence[id]
		if !exists {
			position = len(file.Evidence)
			evidence[id] = position
			file.Evidence = append(file.Evidence, NavigationEvidence{ID: id, Citation: c, Links: []NavigationLink{}})
		}
		item := &file.Evidence[position]
		for _, prior := range item.Links {
			if prior == link {
				return
			}
		}
		item.Links = append(item.Links, link)
	}
	fresh := report.Freshness == "fresh"
	currentHost := fresh && report.HostReview != nil && report.HostReview.State == "current"
	host := map[string]*Assessment{}
	if report.HostReview != nil && report.HostReview.Latest != nil {
		for i := range report.HostReview.Latest.Assessments {
			a := &report.HostReview.Latest.Assessments[i]
			host[a.RequirementID] = a
		}
	}
	for i := range report.Requirements {
		row := &report.Requirements[i]
		req := row.Requirement
		row.Navigation = RequirementNavigation{Section: req.Source.Path, Basis: "roles", Flags: []string{}, Host: host[req.ID], HostExecutions: []TestExecution{}}
		flags := map[string]bool{"unreviewed": !currentHost}
		if !fresh {
			row.Navigation.Basis = "history"
		} else if currentHost {
			row.Navigation.Basis = "host-current"
		}
		citations := []Citation{req.Source}
		if req.Accepted != nil {
			citations = req.Accepted.Citations
			flags["spec_question"] = req.Accepted.Clarity == "ambiguous" || len(req.Accepted.Unresolved) != 0
		}
		for _, c := range citations {
			add(c, NavigationLink{RequirementID: req.ID, Origin: "requirement", Current: fresh})
		}
		assess := func(a Assessment, origin string, current, basis bool) {
			link := NavigationLink{RequirementID: req.ID, Origin: origin, Current: current}
			for _, c := range a.Spec {
				add(c, link)
			}
			for _, c := range a.Code {
				add(c, link)
			}
			for _, test := range a.Tests {
				link.TestID = test.TestID
				add(test.Citation, link)
			}
			if basis {
				flags["implementation_gap"] = flags["implementation_gap"] || a.Implementation == "contradicted"
				flags["test_gap"] = flags["test_gap"] || oneOf(a.Assertion, "weak", "missing", "contradicts")
				flags["partial_test"] = flags["partial_test"] || a.Assertion == "weak"
				flags["missing_test"] = flags["missing_test"] || a.Assertion == "missing"
				flags["spec_question"] = flags["spec_question"] || a.Specification == "ambiguous"
				flags["unknown"] = flags["unknown"] || a.Implementation == "unknown" || a.Assertion == "unknown"
			}
		}
		previous := ""
		for _, role := range row.Roles {
			if role.Assessment == nil {
				flags["pending"] = true
				continue
			}
			a := *role.Assessment
			states := a.Specification + "/" + a.Implementation + "/" + a.Assertion
			flags["disagreement"] = flags["disagreement"] || previous != "" && previous != states
			previous = states
			assess(a, fmt.Sprintf("%s · attempt %d", role.Role, role.Attempt), fresh, !currentHost)
		}
		if a := row.Navigation.Host; a != nil {
			assess(*a, "host · "+report.HostReview.Latest.ReviewID+" · "+report.HostReview.State, currentHost, currentHost)
			row.Navigation.HostExecutions = assessmentExecutions(*a, report.Executions)
		}
		for j := range nav.Filters {
			if flags[nav.Filters[j].ID] {
				row.Navigation.Flags = append(row.Navigation.Flags, nav.Filters[j].ID)
				nav.Filters[j].Count++
			}
		}
		nav.Metrics.add(*row, currentHost)
		if section, ok := sections[row.Navigation.Section]; ok {
			nav.Sections[section].AuditMetrics.add(*row, currentHost)
		}
	}
	for i := range nav.Files {
		file := &nav.Files[i]
		sort.Slice(file.Evidence, func(a, b int) bool {
			x, y := file.Evidence[a], file.Evidence[b]
			if x.Citation.LineStart != y.Citation.LineStart {
				return x.Citation.LineStart < y.Citation.LineStart
			}
			return x.ID < y.ID
		})
		for j := range nav.Groups {
			if nav.Groups[j].Kind == file.Kind {
				nav.Groups[j].Files++
				if !file.Current {
					nav.Groups[j].Unlinked++
				}
			}
		}
	}
	report.Navigation = nav
	slog.Debug("карта отчёта построена", "files", len(nav.Files), "fragments", len(evidence))
}
