//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
)

type ExternalResult struct {
	SchemaVersion    int               `json:"schema_version"`
	ToolVersion      string            `json:"tool_version"`
	Scope            string            `json:"scope"`
	RunID            string            `json:"run_id"`
	SnapshotID       string            `json:"snapshot_id"`
	AcceptedHead     string            `json:"accepted_head"`
	Basis            *ExternalBasis    `json:"basis"`
	Decision         *ReviewSummary    `json:"decision"`
	Report           Report            `json:"report"`
	Paths            map[string]string `json:"paths"`
	SourceRetention  string            `json:"source_retention,omitempty"`
	ReportBasis      string            `json:"report_basis,omitempty"`
	CurrentFreshness *ResultFreshness  `json:"current_freshness,omitempty"`
}

type ResultFreshness struct {
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	SourceAvailability string `json:"source_availability"`
}

func resultFromReport(report Report, paths map[string]string) ExternalResult {
	r := ExternalResult{SchemaVersion: 3, ToolVersion: version, RunID: report.RunID, SnapshotID: report.SnapshotID, Basis: report.External, Decision: report.HostReview, Report: report, Paths: paths, SourceRetention: "archive", ReportBasis: "saved_run"}
	if report.SourceRetention == "citations" {
		r.SourceRetention = "citations"
	}
	r.CurrentFreshness = &ResultFreshness{Status: report.Freshness, Reason: report.FreshnessReason, SourceAvailability: report.SourceAvailability}
	if r.CurrentFreshness.SourceAvailability == "" {
		r.CurrentFreshness = &ResultFreshness{Status: "unchecked", Reason: "Сохранённый отчёт без текущего наблюдения источников.", SourceAvailability: "unavailable"}
		r.ReportBasis = "historical_publication"
	}
	if report.External != nil {
		r.Scope = report.External.Scope
	}
	if report.Accepted != nil {
		r.AcceptedHead = report.Accepted.Head
	}
	return r
}

func reportRetentionMatches(report Report, cfg Config) bool {
	return oneOf(report.SourceRetention, "", "archive", "citations") && citationRetention(cfg) == (report.SourceRetention == "citations")
}

func externalResult(config, runID string) (any, error) {
	if !slugRE.MatchString(runID) {
		return nil, errors.New("external result: недопустимый RUN_ID")
	}
	cfg, err := loadConfig(config, true)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := readRoot(root, runID+"/report.json", maxState)
	if err != nil {
		return nil, err
	}
	var report Report
	if err := strictJSON(data, &report); err != nil {
		return nil, err
	}
	if report.RunID != runID || report.External == nil || cfg.External == nil || report.External.Project != cfg.External.Project || report.External.Corpus != cfg.External.Corpus || report.External.Scope != cfg.External.Scope {
		return nil, errors.New("external result: чужой scope/run или отсутствует внешнее основание")
	}
	if !reportRetentionMatches(report, cfg) {
		return nil, errors.New("external result: политика хранения отчёта не совпадает с CONFIG")
	}
	report, err = projectSavedReport(config, runID, report)
	if err != nil {
		return nil, err
	}
	return resultFromReport(report, map[string]string{"report_json": filepath.Join(cfg.ReportsDir, runID, "report.json"), "report_html": filepath.Join(cfg.ReportsDir, runID, "report.html"), "sources": filepath.Join(cfg.ReportsDir, runID, "sources.json")}), nil
}

// projectSavedReport reuses the validated run reader and the existing renderer;
// it updates presentation only, never saved report/raw/journal bytes.
func projectSavedReport(config, runID string, report Report) (Report, error) {
	value, err := executeCommand([]string{"review", config, runID})
	if err != nil {
		return Report{}, err
	}
	view := value.(ReviewContext)
	if report.Freshness == view.Freshness && report.SourceAvailability == view.SourceAvailability && report.FreshnessReason == view.FreshnessReason && report.HostReview != nil && report.HostReview.State == view.State && report.HostReview.Complete == view.Complete && reflect.DeepEqual(report.HostReview.Latest, view.Latest) {
		return report, nil
	}
	cfg, err := loadConfig(config, true)
	if err != nil {
		return Report{}, err
	}
	reports, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return Report{}, err
	}
	defer reports.Close()
	raw, err := readRoot(reports, runID+"/manifest.json", maxState)
	var m Manifest
	if err != nil {
		return Report{}, err
	}
	if err := strictJSON(raw, &m); err != nil {
		return Report{}, err
	}
	o := sourceObservation{view.Freshness, view.SourceAvailability, view.FreshnessReason}
	m.observation = &o
	report.Status = makeStatus(runID, m, State{Entries: view.Entries}, view.Freshness == "fresh")
	valid := report.HostReview != nil && view.Complete && reflect.DeepEqual(report.HostReview.Latest, view.Latest)
	if report.HostReview != nil {
		summary := *report.HostReview
		report.HostReview = &summary
		report.HostReview.Complete = valid
		report.HostReview.State = "outdated"
		if valid {
			report.HostReview.State = view.State
		}
		report.HostReviewState = report.HostReview.State
	}
	report.HostReviewComplete = valid
	report.HostReconciliationRequired = !valid || view.State == "outdated"
	report.Executions = observedExecutions(view.Executions, m.SnapshotID, report.Freshness)
	report.Requirements = append([]RequirementReport{}, report.Requirements...)
	for i := range report.Requirements {
		report.Requirements[i].Roles = append([]RoleReport{}, report.Requirements[i].Roles...)
		for j := range report.Requirements[i].Roles {
			role := &report.Requirements[i].Roles[j]
			if role.Assessment != nil {
				role.Executions = assessmentExecutions(*role.Assessment, report.Executions)
			}
		}
	}
	switch report.Freshness {
	case "unchecked":
		report.Conclusion = "Текущая актуальность не проверена; источники освобождены или недоступны. Сохранены свидетельства по снимку run."
		if valid {
			report.Conclusion = savedSnapshotConclusion
		}
	case "stale":
		report.Conclusion = "STALE: обнаружено изменение источников или основания снимка; оценки относятся к сохранённому run."
	case "fresh":
		if valid {
			report.Conclusion = "Хост согласовал результаты по текущим свидетельствам. Это его обоснованная оценка, не автоматическое соответствие или доказательство полноты ТЗ."
		} else {
			report.Conclusion = "Сохранённый отчёт требует нового согласования или формирования отчёта по актуальному основанию run."
		}
	}
	buildNavigation(&report, m)
	linkReportSources(&report)
	return report, nil
}

// SiteResult v4 references the single report in a generated run directory.
// The standalone external result command intentionally remains schema v3.
type ReportReference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}
type DecisionReference struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
}
type SiteResult struct {
	SchemaVersion    int                `json:"schema_version"`
	ToolVersion      string             `json:"tool_version"`
	Scope            string             `json:"scope"`
	RunID            string             `json:"run_id"`
	SnapshotID       string             `json:"snapshot_id"`
	AcceptedHead     string             `json:"accepted_head"`
	Basis            *ExternalBasis     `json:"basis"`
	SourceRetention  string             `json:"source_retention"`
	ReportBasis      string             `json:"report_basis"`
	CurrentFreshness *ResultFreshness   `json:"current_freshness"`
	Paths            map[string]string  `json:"paths"`
	ReportRef        ReportReference    `json:"report_ref"`
	DecisionRef      *DecisionReference `json:"decision_ref"`
}

func compactResult(report Report, raw []byte, historical bool) SiteResult {
	full := resultFromReport(report, map[string]string{"report_html": "report.html", "report_json": "report.json", "site": "../../../../index.html"})
	if historical {
		full.ReportBasis = "historical_publication"
		full.CurrentFreshness = &ResultFreshness{Status: "unchecked", SourceAvailability: report.SourceAvailability, Reason: "Исторический аудит выбран явно."}
	}
	r := SiteResult{4, full.ToolVersion, full.Scope, full.RunID, full.SnapshotID, full.AcceptedHead, full.Basis, full.SourceRetention, full.ReportBasis, full.CurrentFreshness, full.Paths, ReportReference{"report.json", digest(raw), len(raw)}, nil}
	if report.HostReview != nil {
		r.DecisionRef = &DecisionReference{"report.json", "/host_review"}
	}
	return r
}
func validateSiteResult(r SiteResult, raw []byte) error {
	if r.SchemaVersion != 4 || r.ReportRef.Path != "report.json" || r.ReportRef.SHA256 != digest(raw) || r.ReportRef.Bytes != len(raw) {
		return errors.New("result v4: неверная ссылка или hash/bytes отчёта")
	}
	var report Report
	if err := strictJSON(raw, &report); err != nil {
		return err
	}
	head := ""
	if report.Accepted != nil {
		head = report.Accepted.Head
	}
	if r.RunID != report.RunID || r.SnapshotID != report.SnapshotID || r.AcceptedHead != head || !samePublicationJSON(r.Basis, report.External) || report.External == nil || r.Scope != report.External.Scope || r.SourceRetention != resultFromReport(report, nil).SourceRetention {
		return errors.New("result v4: чужое основание отчёта")
	}
	if report.HostReview == nil {
		if r.DecisionRef != nil {
			return errors.New("result v4: отсутствует цель decision_ref")
		}
	} else if r.DecisionRef == nil || r.DecisionRef.Path != "report.json" || r.DecisionRef.Pointer != "/host_review" {
		return errors.New("result v4: неверная ссылка на решение")
	}
	if !oneOf(r.ReportBasis, "saved_run", "historical_publication") || r.CurrentFreshness == nil || !oneOf(r.CurrentFreshness.Status, "fresh", "stale", "unchecked") || !oneOf(r.CurrentFreshness.SourceAvailability, "available", "released", "unavailable") || r.CurrentFreshness.Reason == "" {
		return errors.New("result v4: неверное наблюдение")
	}
	return nil
}
