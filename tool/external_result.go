//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func resultFromReport(report Report, paths map[string]string) ExternalResult {
	r := ExternalResult{SchemaVersion: 1, ToolVersion: version, RunID: report.RunID, SnapshotID: report.SnapshotID, Basis: report.External, Decision: report.HostReview, Report: report, Paths: paths}
	if report.SourceRetention == "citations" {
		r.SchemaVersion, r.SourceRetention, r.ReportBasis = 2, "citations", "saved_run"
		r.CurrentFreshness = &ResultFreshness{Status: "unchecked", Reason: "Текущая актуальность не проверена; report и decision содержат сохранённую оценку на момент формирования отчёта."}
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
	return resultFromReport(report, map[string]string{"report_json": filepath.Join(cfg.ReportsDir, runID, "report.json"), "report_html": filepath.Join(cfg.ReportsDir, runID, "report.html"), "sources": filepath.Join(cfg.ReportsDir, runID, "sources.json")}), nil
}

// Published external runs retain their relative addresses and exact representations.
func publishExternalScope(out string, scope *ScopeOverview, cfg Config) (int, error) {
	name := externalScopePath(cfg.External)
	if !slugRE.MatchString(cfg.External.Scope) {
		return 0, errors.New("external.scope: недопустимый slug")
	}
	root, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if scope.External == nil && scope.Decided != nil {
		scope.External = scope.Decided.External
	}
	copied := 0
	for i, history := range scope.History {
		if !slugRE.MatchString(history.RunID) {
			return copied, errors.New("publish: неверный исторический run")
		}
		source := history.RunID + "/report.html"
		html, err := readRoot(root, source, maxFile)
		if os.IsNotExist(err) {
			scope.History[i].Report = ""
			continue
		}
		if err != nil {
			return copied, err
		}
		target := filepath.Join(out, name, history.RunID)
		if err := mkdirExact(target, 0755); err != nil {
			return copied, err
		}
		if err := publishImmutable(filepath.Join(target, "report.html"), html); err != nil {
			return copied, err
		}
		data, err := readRoot(root, history.RunID+"/report.json", maxState)
		if err == nil {
			var report Report
			if err := strictJSON(data, &report); err != nil {
				return copied, err
			}
			if !reportRetentionMatches(report, cfg) {
				return copied, errors.New("publish: политика хранения отчёта не совпадает с CONFIG")
			}
			if report.External != nil && (report.IndependentReview == nil || report.IndependentReview.Conclusion != "confirmed") {
				return copied, errors.New("publish: внешнему run нужна независимая review-check с conclusion confirmed и новый report")
			}
			if report.External != nil {
				run, err := root.OpenRoot(history.RunID)
				if err != nil {
					return copied, err
				}
				manifestData, err := readRoot(run, "manifest.json", maxState)
				if err != nil {
					run.Close()
					return copied, err
				}
				var m Manifest
				if err := strictJSON(manifestData, &m); err != nil {
					run.Close()
					return copied, err
				}
				stateData, err := readRoot(run, "state.json", maxState)
				if err != nil {
					run.Close()
					return copied, err
				}
				var state State
				if err := strictJSON(stateData, &state); err != nil {
					run.Close()
					return copied, err
				}
				reviews, err := readReviews(run, history.RunID, m)
				if err != nil {
					run.Close()
					return copied, err
				}
				current := summarizeReviews(history.RunID, m, state, false, reviews)
				if !current.Complete || report.HostReview == nil || !reflect.DeepEqual(current.Latest, report.HostReview.Latest) {
					run.Close()
					return copied, errors.New("publish: report не отражает текущее полное решение; нужны review-check и новый report")
				}
				check, checkErr := checkForReport(run, report)
				run.Close()
				if checkErr != nil {
					return copied, checkErr
				}
				if check == nil || !reflect.DeepEqual(check, report.IndependentReview) {
					return copied, errors.New("publish: перепроверка report не совпадает с сохранённым основанием")
				}
			}
			if err := publishImmutable(filepath.Join(target, "report.json"), data); err != nil {
				return copied, err
			}
			result := resultFromReport(report, map[string]string{"report_html": "report.html", "report_json": "report.json", "site": "../../../../index.html"})
			resultRaw, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return copied, err
			}
			if err := publishImmutable(filepath.Join(target, "result.json"), append(resultRaw, '\n')); err != nil {
				return copied, err
			}
		} else if !os.IsNotExist(err) {
			return copied, err
		}
		if !citationRetention(cfg) {
			sourcesDir := filepath.Join(cfg.ReportsDir, history.RunID, "sources")
			if _, err := os.Lstat(sourcesDir); err == nil {
				if err := walkBundleArea(sourcesDir, func(path string, data []byte, _ uint32) error {
					if err := mkdirExact(filepath.Join(target, "sources", filepath.Dir(path)), 0755); err != nil {
						return err
					}
					return publishImmutable(filepath.Join(target, "sources", path), data)
				}); err != nil {
					return copied, err
				}
			} else if !os.IsNotExist(err) {
				return copied, err
			}
		}
		link := filepath.ToSlash(filepath.Join(name, history.RunID, "report.html"))
		scope.History[i].Report = link
		if scope.Decided != nil && scope.Decided.RunID == history.RunID {
			scope.Decided.Report = link
		}
		copied++
	}
	return copied, nil
}

func publishImmutable(path string, data []byte) error {
	guard, _, err := openExact(filepath.Dir(path))
	if err != nil {
		return err
	}
	guard.Close()
	old, err := readExact(path, maxFile)
	if err == nil {
		if !bytes.Equal(old, data) {
			return fmt.Errorf("публикация уже содержит другую редакцию %s; используйте новый каталог", filepath.Base(path))
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return atomicWrite(root, filepath.Base(path), data, 0644)
}
