//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Publication selection is command-local. It is never an audit decision or a registry.
type publishOptions struct {
	Paths              []string
	From, Run, Control string
}
type publishStore struct {
	Path   string
	Config Config
}
type savedPublicationRun struct {
	ID       string
	Manifest Manifest
	Report   *Report
	View     RunOverview
	Code     []ContradictedCitation
}

func parsePublish(args []string) (publishOptions, error) {
	o := publishOptions{Paths: []string{}}
	options := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if options && a == "--" {
			options = false
			continue
		}
		if options && oneOf(a, "--from", "--run", "--corpus-control") {
			if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				return o, fmt.Errorf("%s: нужно одно значение", a)
			}
			var dest *string
			switch a {
			case "--from":
				dest = &o.From
			case "--run":
				dest = &o.Run
			default:
				dest = &o.Control
			}
			if *dest != "" {
				return o, fmt.Errorf("%s: повтор параметра", a)
			}
			i++
			*dest = args[i]
			continue
		}
		if options && strings.HasPrefix(a, "--") {
			return o, fmt.Errorf("publish: неизвестный параметр %s", a)
		}
		o.Paths = append(o.Paths, a)
	}
	if o.From != "" && len(o.Paths) > 0 || o.Run != "" && (o.From != "" || len(o.Paths) != 1 || !slugRE.MatchString(o.Run)) {
		return o, errors.New("publish: --run требует один CONFIG и несовместим с --from; --from не принимает CONFIG")
	}
	return o, nil
}

func publicationSkip(name string) bool {
	return oneOf(name, "._objects", "._inputs", "._publications", ".git", ".hg", ".svn", "node_modules", "vendor") || strings.HasPrefix(name, ".citation-site-") || strings.HasPrefix(name, ".publish-")
}

func discoverPublicationStores(from, out string) ([]publishStore, error) {
	if from == "" {
		from = "."
	}
	base, err := filepath.Abs(from)
	if err != nil {
		return nil, err
	}
	base = exactSystemPath(base)
	guard, _, err := openExact(base)
	if err != nil {
		return nil, err
	}
	guard.Close()
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	stores := map[string]bool{}
	configs := []string{}
	type disputedMetadata struct {
		path string
		err  error
	}
	disputed := []disputedMetadata{}
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := processContext.Err(); err != nil {
			return err
		}
		count++
		if count > 200000 {
			return errors.New("publish: обнаружение превышает 200000 записей")
		}
		abs := filepath.Join(base, filepath.FromSlash(path))
		if e.IsDir() && path != "." && (publicationSkip(e.Name()) || abs == out) {
			return fs.SkipDir
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("publish: симлинк внутри каталога аудитов: %s", path)
		}
		if !e.IsDir() {
			if oneOf(e.Name(), "config.yaml", "config.yml", "audit.yaml", ".spec-audit.yaml") || filepath.Base(filepath.Dir(abs)) == "configs" && strings.HasSuffix(e.Name(), ".yaml") {
				configs = append(configs, abs)
			}
			return nil
		}
		if !slugRE.MatchString(filepath.Base(abs)) {
			return nil
		}
		// Recognize the run before descending: its resources may sort before
		// manifest.json and are never independent audit stores.
		data, err := readRoot(root, filepath.ToSlash(filepath.Join(path, "manifest.json")), maxState)
		if os.IsNotExist(err) {
			evidence, evidenceErr := savedRunEvidence(root, path, nil)
			if evidenceErr != nil {
				return evidenceErr
			}
			if evidence {
				slog.Debug("[FIX:audit-roots] сохранённый run без manifest", "run", abs)
				return fmt.Errorf("publish: manifest сохранённого run %s: %w", path, err)
			}
			return nil
		}
		if err != nil {
			return err
		}
		_, stateErr := root.Lstat(filepath.ToSlash(filepath.Join(path, "state.json")))
		if stateErr != nil && !os.IsNotExist(stateErr) {
			return stateErr
		}
		var m Manifest
		classificationErr := strictJSON(data, &m)
		if classificationErr == nil && (m.Version != 1 || !validDigest(m.SnapshotID)) {
			classificationErr = errors.New("неверное основание run")
		}
		if classificationErr != nil {
			evidence, err := savedRunEvidence(root, path, data)
			if err != nil {
				return err
			}
			problem := fmt.Errorf("publish: manifest %s: %w", path, classificationErr)
			if evidence {
				return problem
			}
			if os.IsNotExist(stateErr) {
				return nil
			}
			// Ownership is not yet known. A direct valid run may prove this
			// directory is a store whose root metadata is unrelated to auditing.
			disputed = append(disputed, disputedMetadata{path, problem})
			return nil
		}
		if path == "." || !within(base, filepath.Dir(abs)) {
			return errors.New("publish: AUDIT_ROOT должен содержать reports_dir, а не только один run")
		}
		stores[filepath.Dir(abs)] = true
		slog.Debug("[FIX:publication] run распознан, ресурсы исключены из поиска", "run", abs)
		return fs.SkipDir
	})
	if err != nil {
		return nil, err
	}
	for _, candidate := range disputed {
		if !stores[filepath.Join(base, filepath.FromSlash(candidate.path))] {
			return nil, candidate.err
		}
		slog.Debug("[FIX:audit-roots] корневые metadata подтверждённого хранилища исключены из run", "root", filepath.Join(base, candidate.path))
	}
	bindings := map[string]publishStore{}
	// Only CONFIGs owning an already discovered store are bindings, not candidates.
	for _, p := range configs {
		cfg, err := loadConfig(p, true)
		if err == nil && stores[cfg.ReportsDir] {
			old, ok := bindings[cfg.ReportsDir]
			if ok {
				same, err := samePublicationBinding(old.Config, cfg)
				if err != nil {
					return nil, err
				}
				if !same {
					slog.Debug("[FIX:publication] несовместимые привязки CONFIG", "reports_dir", cfg.ReportsDir, "first_config", old.Path, "second_config", p)
					return nil, fmt.Errorf("publish: несовместимые CONFIG %s и %s для reports_dir %s", old.Path, p, cfg.ReportsDir)
				}
			}
			if !ok || p < old.Path {
				bindings[cfg.ReportsDir] = publishStore{p, cfg}
			}
		}
	}
	result := []publishStore{}
	dirs := []string{}
	for dir := range stores {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		if b, ok := bindings[dir]; ok {
			result = append(result, b)
			continue
		}
		runs, err := readPublicationRuns(dir, "", dirs...)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			continue
		}
		last := runs[len(runs)-1]
		cfg := last.Manifest.Config
		cfg.ReportsDir = dir
		path := dir
		if cfg.External != nil {
			key := filepath.Base(filepath.Dir(last.Manifest.Config.ProjectRoot))
			working := filepath.Join(dir, "._inputs", key, "config.yaml")
			if c, e := loadConfig(working, true); e == nil && c.ReportsDir == dir {
				cfg, path = c, working
			} else {
				ext := *cfg.External
				ext.Input = filepath.Join(dir, "._inputs", key, "audit-input.json")
				ext.CodeRoot = filepath.Join(dir, "._unavailable")
				cfg.External = &ext
				cfg.ProjectRoot = filepath.Join(dir, "._inputs", key, "tree")
			}
		} else {
			cfg.ProjectRoot = filepath.Join(dir, "._unavailable")
		}
		result = append(result, publishStore{path, cfg})
	}
	if len(result) == 0 {
		return nil, errors.New("publish: в выбранном каталоге нет сохранённых областей аудита")
	}
	slog.Debug("publish: обнаружены области", "root", base, "stores", len(result))
	return result, nil
}

// loadConfig normalizes roots, defaults and source ordering. YAML also treats
// nil and empty collections alike, while retaining external roots omitted by JSON.
func samePublicationBinding(a, b Config) (bool, error) {
	first, err := yaml.Marshal(a)
	if err != nil {
		return false, err
	}
	second, err := yaml.Marshal(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(first, second), nil
}

func resolvePublicationStores(o publishOptions, out string) ([]publishStore, error) {
	if len(o.Paths) == 0 {
		return discoverPublicationStores(o.From, out)
	}
	result := []publishStore{}
	seen := map[string]bool{}
	for _, p := range o.Paths {
		cfg, err := loadConfig(p, true)
		if err != nil {
			return nil, err
		}
		if seen[cfg.ReportsDir] {
			return nil, errors.New("publish: одна reports_dir выбрана несколько раз")
		}
		seen[cfg.ReportsDir] = true
		result = append(result, publishStore{p, cfg})
	}
	return result, nil
}

func readPublicationRuns(dir, runID string, reportRoots ...string) ([]savedPublicationRun, error) {
	if runID != "" && !slugRE.MatchString(runID) {
		return nil, errors.New("publish: недопустимый RUN_ID")
	}
	reports, err := os.OpenRoot(dir)
	if os.IsNotExist(err) {
		return []savedPublicationRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer reports.Close()
	// Historical selection must not open unrelated manifests or version journals.
	names := []string{runID}
	if runID == "" {
		excluded, err := nestedReportPaths(dir, reportRoots)
		if err != nil {
			return nil, err
		}
		names = []string{}
		entries, err := fs.ReadDir(reports.FS(), ".")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && slugRE.MatchString(e.Name()) && !excluded[e.Name()] {
				names = append(names, e.Name())
			}
		}
	}
	slog.Debug("[FIX:publication] metadata run выбраны для чтения", "run_id", runID, "runs", len(names))
	result := []savedPublicationRun{}
	for _, name := range names {
		raw, err := readRoot(reports, name+"/manifest.json", maxState)
		if os.IsNotExist(err) {
			evidence, evidenceErr := savedRunEvidence(reports, name, nil)
			if evidenceErr != nil {
				return nil, evidenceErr
			}
			if evidence {
				slog.Debug("[FIX:publication] сохранённый run без manifest", "run_id", name)
				return nil, fmt.Errorf("publish: manifest сохранённого run %s: %w", name, err)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		var m Manifest
		if err := strictJSON(raw, &m); err != nil {
			return nil, err
		}
		if m.Version != 1 || !validDigest(m.SnapshotID) {
			return nil, fmt.Errorf("publish: run %s: неверный manifest", name)
		}
		run, err := reports.OpenRoot(name)
		if err != nil {
			return nil, err
		}
		versions, err := readToolVersions(run)
		run.Close()
		if err != nil {
			return nil, err
		}
		v := RunOverview{RunID: name}
		if len(versions.Records) > 0 {
			v.PreparedAt = versions.Records[0].RecordedAt
		}
		result = append(result, savedPublicationRun{ID: name, Manifest: m, View: v})
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].View, result[j].View
		return a.PreparedAt < b.PreparedAt || a.PreparedAt == b.PreparedAt && a.RunID < b.RunID
	})
	return result, nil
}

func loadPublicationRun(store publishStore, s savedPublicationRun, historical bool, current Manifest, snapshotErr error) (savedPublicationRun, error) {
	reports, err := os.OpenRoot(store.Config.ReportsDir)
	if err != nil {
		return s, err
	}
	defer reports.Close()
	run, err := reports.OpenRoot(s.ID)
	if err != nil {
		return s, err
	}
	defer run.Close()
	// Validate delivery and decision journals even when no printable report exists.
	state, err := readRunState(run, s.Manifest, false)
	if err != nil {
		return s, err
	}
	journal, err := readReviews(run, s.ID, s.Manifest)
	if err != nil {
		return s, err
	}
	summary := summarizeReviews(s.ID, s.Manifest, state, false, journal)
	v, code, err := runOverview(reports, s.ID, store.Config, current, snapshotErr)
	if err != nil {
		return s, err
	}
	s.View, s.Code = v, code
	if s.Manifest.External != nil {
		if _, err := readReviewChecks(run); err != nil {
			return s, err
		}
	}
	raw, err := readRoot(run, "report.json", maxState)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	var report Report
	if err := strictJSON(raw, &report); err != nil {
		return s, err
	}
	if report.RunID != s.ID || report.SnapshotID != s.Manifest.SnapshotID || !reportRetentionMatches(report, store.Config) || !samePublicationJSON(report.External, s.Manifest.External) {
		return s, errors.New("publish: report и manifest имеют разное основание")
	}
	expectedReport := makeReport(s.ID, s.Manifest, state, false)
	if len(report.Requirements) != len(expectedReport.Requirements) || !samePublicationJSON(report.Accepted, expectedReport.Accepted) || !samePublicationJSON(report.RawProvenance, expectedReport.RawProvenance) {
		return s, errors.New("publish: report потерял нормы или raw provenance")
	}
	for i, row := range report.Requirements {
		want := expectedReport.Requirements[i]
		if !samePublicationJSON(row.Requirement, want.Requirement) || row.Scope != want.Scope || len(row.Roles) != len(want.Roles) {
			return s, errors.New("publish: нормы/роли отчёта отличаются от manifest/state")
		}
		for j, role := range row.Roles {
			expected := want.Roles[j]
			if role.Role != expected.Role || role.Attempt != expected.Attempt || !samePublicationJSON(role.Assessment, expected.Assessment) {
				return s, errors.New("publish: оценка роли отличается от сохранённого raw")
			}
		}
	}
	if !summary.Complete || !v.DeliveryComplete {
		return s, nil
	}
	if report.HostReview == nil || !reflect.DeepEqual(summary.Latest, report.HostReview.Latest) || !reflect.DeepEqual(summary.History, report.HostReview.History) {
		slog.Debug("[FIX:publication] report не связан с точными raw-байтами решения", "run_id", s.ID)
		return s, errors.New("publish: report не отражает полное решение; сформируйте report заново")
	}
	// Receipt lookup and presentation derive from the validated journal, never cached hashes.
	report.HostReview = &summary
	slog.Debug("[FIX:publication] точные raw-байты решения проверены", "run_id", s.ID, "history", len(summary.History))
	inv, err := readSourceInventory(reports, run, s.Manifest)
	if err != nil {
		return s, err
	}
	if s.Manifest.External != nil {
		if store.Config.External == nil || !sameCorpusContext(CorpusControl{Project: store.Config.External.Project, Corpus: store.Config.External.Corpus, Line: store.Config.External.Line}, *s.Manifest.External) || store.Config.External.Scope != s.Manifest.External.Scope {
			return s, errors.New("publish: CONFIG принадлежит другой внешней области")
		}
		if err := validateExternalRunMetadata(store.Config, s.Manifest, inv); err != nil {
			return s, err
		}
	}
	if len(report.Sources) == 0 && s.Manifest.External == nil && !citationRetention(store.Config) {
		report.Sources = inv.Sources
	}
	if len(inv.Sources) != len(report.Sources) {
		return s, errors.New("publish: report потерял опись источников")
	}
	for i, src := range inv.Sources {
		was := report.Sources[i]
		was.URL, was.RawURL, was.LiveURL = "", "", ""
		if !samePublicationJSON(was, src) {
			return s, errors.New("publish: report.sources отличается от сохранённой описи")
		}
	}
	if s.Manifest.External != nil {
		check, err := checkForReport(run, report)
		if err != nil {
			return s, err
		}
		if report.IndependentReview != nil && !reflect.DeepEqual(check, report.IndependentReview) {
			return s, errors.New("publish: review-check отчёта не совпадает с точной сохранённой проверкой")
		}
		if check == nil || check.Conclusion != "confirmed" {
			s.View.PublicationPending = "Ожидается независимая проверка решения."
			if check != nil {
				s.View.PublicationPending = "Независимая проверка требует доработки решения."
			}
			slog.Debug("[FIX:publication] run ещё не пригоден для публикации", "run_id", s.ID, "reason", s.View.PublicationPending)
			return s, nil
		}
		if report.IndependentReview == nil {
			return s, errors.New("publish: нужна confirmed review-check точного решения и новый report")
		}
	}
	observation := observationForPublication(store.Config, s.Manifest, current, snapshotErr)
	if historical {
		observation = sourceObservation{"unchecked", observation.Availability, "Исторический аудит выбран явно; текущая актуальность не проверяется."}
	}
	report.Freshness, report.SourceAvailability, report.FreshnessReason = observation.Freshness, observation.Availability, observation.Reason
	report.HostReview.Complete = true
	report.HostReview.State = "current"
	if observation.Freshness == "unchecked" {
		report.HostReview.State = "saved"
	}
	if observation.Freshness == "stale" {
		report.HostReview.State = "outdated"
	}
	report.HostReviewComplete = true
	report.HostReviewState = report.HostReview.State
	report.HostReconciliationRequired = false
	switch observation.Freshness {
	case "fresh":
		report.Conclusion = "Хост согласовал результаты по текущим свидетельствам. Это его обоснованная оценка, не автоматическое соответствие или доказательство полноты ТЗ."
	case "unchecked":
		report.Conclusion = savedSnapshotConclusion
	case "stale":
		report.Conclusion = "Обнаружено изменение текущего основания; сохранённый аудит относится к указанному снимку run."
	}
	// Persisted report receipts may already contain a stale presentation.
	// Derive each observation from authoritative state, preserving original stale receipts.
	report.Executions = observedExecutions(state.Executions, report.SnapshotID, report.Freshness)
	slog.Debug("[FIX:publication-observation] представление сохранённого исполнения пересчитано", "run_id", s.ID, "freshness", report.Freshness, "host_state", report.HostReviewState, "executions", len(state.Executions))
	// Rebuild links and metrics from validated saved data, preserving SDK hints
	// which were already materialized in JSON rather than runtime-only facts.
	sdk := map[string][]NavigationHint{}
	for _, f := range report.Navigation.Files {
		sdk[f.Path] = f.SDK
	}
	buildNavigation(&report, s.Manifest)
	for i := range report.Navigation.Files {
		report.Navigation.Files[i].SDK = sdk[report.Navigation.Files[i].Path]
	}
	for i, src := range inv.Sources {
		if src.Full {
			src.URL = sourceRangeURL(sourcePageKey(src), 1, 1)
			src.RawURL = "sources/" + sourcePageKey(src) + ".bin"
			var origin struct {
				SourceURL string `json:"source_url"`
			}
			_ = json.Unmarshal(src.Origin, &origin)
			if safeExternalURL(origin.SourceURL) {
				src.LiveURL = origin.SourceURL
			}
		}
		report.Sources[i] = src
	}
	linkReportSources(&report)
	hydrateSavedReport(&report)
	s.Report = &report
	s.View.Freshness = report.Freshness
	s.View.SourceAvailability = report.SourceAvailability
	s.View.FreshnessReason = report.FreshnessReason
	s.View.HostReviewState = report.HostReviewState
	return s, nil
}

// Host is intentionally absent from JSON. Hydrate it without rebuilding the saved
// file/evidence/SDK navigation from runtime-only facts that are no longer present.
func hydrateSavedReport(r *Report) {
	host := map[string]*Assessment{}
	if r.HostReview != nil && r.HostReview.Latest != nil {
		for i := range r.HostReview.Latest.Assessments {
			a := &r.HostReview.Latest.Assessments[i]
			host[a.RequirementID] = a
		}
	}
	for i := range r.Requirements {
		row := &r.Requirements[i]
		row.Navigation.Host = host[row.Requirement.ID]
		for j := range row.Roles {
			if row.Roles[j].Assessment != nil {
				row.Roles[j].Executions = assessmentExecutions(*row.Roles[j].Assessment, r.Executions)
			}
		}
		if row.Navigation.Host != nil {
			row.Navigation.HostExecutions = assessmentExecutions(*row.Navigation.Host, r.Executions)
		}
		if r.Freshness == "unchecked" {
			row.Navigation.Basis = "host-saved"
		} else if r.Freshness == "stale" {
			row.Navigation.Basis = "host-stale"
		}
	}
}

func publicationOverview(stores []publishStore, runID string, control *CorpusControlFile) (Overview, []*savedPublicationRun, error) {
	view := Overview{Scopes: []ScopeOverview{}, Totals: OverviewTotals{Implementation: map[string]int{}, Assertion: map[string]int{}}, CorpusControl: control, ContradictedCode: []ContradictedCitation{}}
	selected := []*savedPublicationRun{}
	identities := map[string]bool{}
	reportRoots := []string{}
	for _, store := range stores {
		reportRoots = append(reportRoots, store.Config.ReportsDir)
	}
	for _, store := range stores {
		runs, err := readPublicationRuns(store.Config.ReportsDir, runID, reportRoots...)
		if err != nil {
			return view, nil, err
		}
		scope := ScopeOverview{Name: scopeName(store.Path), Config: store.Path, IndexMode: "declared", Freshness: "n/a", History: []RunRef{}, Questions: []ScopeQuestion{}, Runs: len(runs)}
		if store.Config.IndexMode == "accepted" {
			scope.IndexMode = "accepted"
		}
		if store.Config.External != nil {
			scope.Name = store.Config.External.Scope
		}
		if len(runs) > 0 {
			last := runs[len(runs)-1].Manifest
			scope.Requirements = len(last.Requirements)
			if last.Accepted != nil {
				scope.Head = last.Accepted.Head
			}
		}
		identity := store.Config.ReportsDir
		if b := store.Config.External; b != nil {
			raw, _ := json.Marshal([]any{b.Project, b.Corpus, b.Line, b.Scope})
			identity = string(raw)
		}
		if identities[identity] {
			return view, nil, errors.New("publish: неоднозначная логическая область в нескольких хранилищах")
		}
		identities[identity] = true
		var chosen *savedPublicationRun
		complete := []savedPublicationRun{}
		var current Manifest
		var snapshotErr error
		if runID == "" {
			current, snapshotErr = snapshot(store.Config)
		} else {
			o := sourceObservation{"unchecked", "unavailable", "Исторический аудит выбран явно; текущая актуальность не проверяется."}
			if releasedSnapshotSources(store.Config) {
				o.Availability = "released"
			}
			current = Manifest{observation: &o}
			snapshotErr = errors.New(o.Reason)
		}
		for _, s := range runs {
			if runID != "" && s.ID != runID {
				continue
			}
			loaded, err := loadPublicationRun(store, s, runID != "", current, snapshotErr)
			if err != nil {
				return view, nil, fmt.Errorf("publish: %s / %s: %w", store.Path, s.ID, err)
			}
			copyView := loaded.View
			scope.Latest = &copyView
			if loaded.Report != nil {
				v := loaded
				complete = append(complete, loaded)
				chosen = &v
				scope.History = append(scope.History, RunRef{loaded.ID, loaded.View.PreparedAt, loaded.View.ReviewID, loaded.View.HostReviewState, loaded.View.Gap.Total, ""})
			}
		}
		if runID != "" && chosen == nil {
			return view, nil, fmt.Errorf("publish: run %s отсутствует или не имеет полного проверенного отчёта", runID)
		}
		if chosen != nil {
			slog.Debug("publish: выбран сохранённый аудит", "config", store.Path, "run_id", chosen.ID, "historical", runID != "")
			v := chosen.View
			scope.Decided = &v
			scope.External = chosen.Manifest.External
			if scope.External == nil {
				scope.History = []RunRef{}
			}
			scope.Requirements = len(chosen.Manifest.Requirements)
			scope.Questions = specQuestions(chosen.Manifest, &v)
			if chosen.Manifest.Accepted != nil {
				scope.Head = chosen.Manifest.Accepted.Head
			}
			if scope.External != nil {
				scope.Name = scope.External.Scope
			}
			if len(complete) >= 2 {
				baseline := complete[len(complete)-2].View
				for _, prior := range complete[:len(complete)-1] {
					if prior.ID == store.Config.BaselineRun {
						baseline = prior.View
					}
				}
				scope.Delta = scopeDelta(baseline, v)
				scope.Delta.Pinned = store.Config.BaselineRun != "" && baseline.RunID == store.Config.BaselineRun
				if store.Config.BaselineRun != "" && !scope.Delta.Pinned {
					slog.Warn("baseline_run не найден среди решённых run; сравнение с предыдущим", "config", store.Path, "baseline_run", store.Config.BaselineRun)
				}
				view.Totals.Closed += len(scope.Delta.Closed)
				view.Totals.Opened += len(scope.Delta.Opened)
				view.Totals.Worsened += len(scope.Delta.Worsened)
			}
			scope.DriftUnchecked = "Текущая актуальность отображается отдельно от сохранённого решения."
			if snapshotErr == nil && runID == "" {
				root, err := os.OpenRoot(store.Config.ReportsDir)
				if err != nil {
					return view, nil, err
				}
				scope.Drift = scopeDrift(root, chosen.ID, current)
				root.Close()
				if scope.Drift != nil {
					scope.DriftUnchecked = ""
					view.Totals.Drifted += scope.Drift.Assessed
				}
			}
			for _, c := range chosen.Code {
				c.Config = store.Path
				view.ContradictedCode = append(view.ContradictedCode, c)
			}
			for k, n := range v.Implementation {
				view.Totals.Implementation[k] += n
			}
			for k, n := range v.Assertion {
				view.Totals.Assertion[k] += n
			}
			view.Totals.Gap.Total += v.Gap.Total
			view.Totals.Gap.Implementation += v.Gap.Implementation
			view.Totals.Gap.Verification += v.Gap.Verification
			view.Totals.Gap.Specification += v.Gap.Specification
			if scope.DriftUnchecked != "" {
				view.Totals.Unchecked++
			}
			if v.Freshness == "stale" {
				view.Totals.StaleScopes++
			}
		} else {
			scope.Error = "Аудит не завершён: полного сохранённого отчёта нет."
		}
		scope.CorpusRevision = compareCorpusRevision(scope, control)
		view.Scopes = append(view.Scopes, scope)
		selected = append(selected, chosen)
		view.Totals.Scopes++
		view.Totals.Requirements += scope.Requirements
		view.Totals.Runs += scope.Runs
		view.Totals.Questions += len(scope.Questions)
	}
	return view, selected, nil
}

// Saved source roots restrict writes without authorizing reads of an old checkout.
// Keep these boundaries separate from a synthetic unavailable observation root.
func guardPublicationOutput(cfg Config, out string) error {
	if within(cfg.ReportsDir, out) || within(out, cfg.ReportsDir) || (cfg.External != nil || citationRetention(cfg)) && (within(cfg.ProjectRoot, out) || within(out, cfg.ProjectRoot)) {
		slog.Debug("[FIX:publication] OUT пересекается с рабочей областью/исходниками", "dir", out)
		return errors.New("publish: OUT пересекается с рабочей областью/исходниками")
	}
	if cfg.External == nil && !citationRetention(cfg) {
		groups := []Sources{cfg.Specs, cfg.Code, cfg.Tests}
		if cfg.References != nil {
			groups = append(groups, *cfg.References)
		}
		for _, group := range groups {
			for _, path := range group.Paths {
				source := filepath.Join(cfg.ProjectRoot, filepath.FromSlash(path))
				if within(source, out) || within(out, source) {
					slog.Debug("[FIX:publication] OUT пересекается с выбранным источником", "dir", out, "source", path)
					return fmt.Errorf("publish: OUT пересекается с выбранным источником %s", path)
				}
			}
		}
		if within(cfg.ProjectRoot, out) {
			slog.Debug("[FIX:publication] локальный archive OUT вне выбранных источников разрешён", "dir", out)
		}
	}
	return nil
}

func publishCommand(out string, args []string) (any, error) {
	o, err := parsePublish(args)
	if err != nil {
		return nil, err
	}
	control, err := readCorpusControl(o.Control)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	abs = exactSystemPath(abs)
	stores, err := resolvePublicationStores(o, abs)
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	for _, s := range stores {
		dirs = append(dirs, s.Config.ReportsDir)
	}
	unlock, err := lockWorkspaces(dirs, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	again, err := resolvePublicationStores(o, abs)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(stores, again) {
		return nil, errors.New("publish: состав областей изменился; повторите команду")
	}
	for _, store := range stores {
		if err := guardPublicationOutput(store.Config, abs); err != nil {
			return nil, err
		}
		runs, err := readPublicationRuns(store.Config.ReportsDir, o.Run, dirs...)
		if err != nil {
			return nil, err
		}
		for _, saved := range runs {
			boundary := saved.Manifest.Config
			// ReportsDir is a live binding; ProjectRoot remains a historical deny boundary.
			boundary.ReportsDir = store.Config.ReportsDir
			if err := guardPublicationOutput(boundary, abs); err != nil {
				return nil, fmt.Errorf("publish: run %s: %w", saved.ID, err)
			}
		}
	}
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return nil, errors.New("publish: нужен новый отсутствующий OUT_DIR; прежняя публикация сохраняется")
	}
	view, chosen, err := publicationOverview(stores, o.Run, control)
	if err != nil {
		return nil, err
	}
	if err := mkdirExact(filepath.Dir(abs), 0755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(abs), ".publish-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	copied := 0
	targets := map[string]bool{}
	citations := true
	for i, s := range chosen {
		citations = citations && citationRetention(stores[i].Config)
		if s == nil {
			continue
		}
		name := scopeName(stores[i].Path)
		if s.Manifest.External != nil {
			name = externalScopePath(stores[i].Config.External)
			if basis := s.Manifest.External; basis.Line != nil {
				raw, _ := json.Marshal([]any{basis.Project, basis.Corpus, basis.Line})
				name = filepath.Join("external", digest(raw)[:32], basis.Scope)
			}
		}
		if targets[name] && s.Manifest.External == nil {
			name += "-" + digest([]byte(stores[i].Config.ReportsDir))[:12]
		}
		if targets[name] {
			return nil, errors.New("publish: конфликт адресов областей")
		}
		targets[name] = true
		link := filepath.ToSlash(filepath.Join(name, s.ID, "report.html"))
		target := filepath.Join(stage, name, s.ID)
		if err := renderPublicationRun(target, stores[i], *s, o.Run != ""); err != nil {
			return nil, err
		}
		view.Scopes[i].Decided.Report = link
		// History is data, not an implicit collection of generated historical pages.
		for j := range view.Scopes[i].History {
			if view.Scopes[i].History[j].RunID == s.ID {
				view.Scopes[i].History[j].Report = link
			}
		}
		copied++
	}
	html, err := renderCorpus(view)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "index.html"), html, 0644); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, "overview.json"), append(raw, '\n'), 0644); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return nil, errors.New("publish: OUT появился во время сборки")
	}
	if err := os.Rename(stage, abs); err != nil {
		return nil, err
	}
	mode := "latest"
	if o.Run != "" {
		mode = "historical"
	}
	slog.Info("сайт сформирован из сохранённых отчётов", "dir", abs, "scopes", len(stores), "reports", copied, "selection", mode)
	result := map[string]any{"dir": abs, "index": filepath.Join(abs, "index.html"), "reports": copied, "scopes": len(stores), "selection": mode}
	if citations {
		result["source_retention"] = "citations"
	}
	return result, nil
}

func renderPublicationRun(target string, store publishStore, s savedPublicationRun, historical bool) error {
	if err := mkdirExact(target, 0755); err != nil {
		return err
	}
	report := *s.Report
	for _, src := range report.Sources {
		if !src.Full {
			continue
		}
		raw, err := readExact(filepath.Join(store.Config.ReportsDir, "._objects", src.SHA256), maxFile)
		if err != nil {
			return err
		}
		if digest(raw) != src.SHA256 || len(raw) != src.Bytes {
			return errors.New("publish: повреждён сохранённый source-объект")
		}
		page, err := renderSource(raw, src)
		if err != nil {
			return err
		}
		if err := mkdirExact(filepath.Join(target, "sources"), 0755); err != nil {
			return err
		}
		for ext, data := range map[string][]byte{".html": page, ".bin": raw} {
			if err := os.WriteFile(filepath.Join(target, "sources", sourcePageKey(src)+ext), data, 0644); err != nil {
				return err
			}
		}
	}
	var html bytes.Buffer
	if err := reportTemplate.Execute(&html, report); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(target, "report.json"), raw, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, "report.html"), html.Bytes(), 0644); err != nil {
		return err
	}
	if report.External != nil {
		result := compactResult(report, raw, historical)
		if err := validateSiteResult(result, raw); err != nil {
			return err
		}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, "result.json"), append(data, '\n'), 0644); err != nil {
			return err
		}
	}
	return nil
}

func observationForPublication(cfg Config, saved, current Manifest, snapshotErr error) sourceObservation {
	if current.observation != nil {
		return *current.observation
	}
	return observeSnapshot(cfg, saved, current, snapshotErr)
}

func samePublicationJSON(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}
