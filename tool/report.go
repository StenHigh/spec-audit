//go:build darwin || linux

package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"sort"
	"strings"
)

//go:embed report.html
var reportHTML string

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"citationID": citationID,
	"join":       strings.Join,
}).Parse(reportHTML))

// Navigation is a projection, never an additional audit or a persisted decision.
type RequirementNavigation struct {
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
	Groups  []NavigationGroup  `json:"groups"`
	Files   []NavigationFile   `json:"files"`
	Filters []NavigationFilter `json:"filters"`
	Scopes  []Scope            `json:"scopes"`
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
		Filters: []NavigationFilter{
			{ID: "implementation_gap", Label: "Противоречие реализации"},
			{ID: "test_gap", Label: "Недостаточно проверок в тестах"},
			{ID: "spec_question", Label: "Вопросы к ТЗ"},
			{ID: "unknown", Label: "Неизвестность"},
			{ID: "pending", Label: "Ожидается ответ роли"},
			{ID: "disagreement", Label: "Различаются оценки ролей"},
			{ID: "unreviewed", Label: "Нет текущего согласования"},
		},
	}
	files := map[string]int{}
	evidence := map[string]int{}
	for _, file := range m.Files {
		files[file.Path] = len(nav.Files)
		nav.Files = append(nav.Files, NavigationFile{SourceFile: file, ID: "f-" + digest([]byte(file.Path)), Evidence: []NavigationEvidence{}})
	}
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
		row.Navigation = RequirementNavigation{Basis: "roles", Flags: []string{}, Host: host[req.ID], HostExecutions: []TestExecution{}}
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
