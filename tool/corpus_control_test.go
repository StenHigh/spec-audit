//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCorpusControlReleasedScopes(t *testing.T) {
	f := citationLifecycleFixture(t)
	f.manifest.SpecVersion = "z-old"
	f.save(t)
	a1 := f.install(t)
	completeExternal(t, a1, "A1")
	other := filepath.Join(f.base, "other/config.yaml")
	original, err := loadConfig(f.config)
	if err != nil {
		t.Fatal(err)
	}
	original.ReportsDir = filepath.Join(f.base, "other-runs")
	ext := *original.External
	ext.Scope = "second-scope"
	original.External = &ext
	raw, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, other, raw)
	b1 := runOK(t, "external", "import", other, f.input).(map[string]any)["config"].(string)
	completeExternal(t, b1, "B1")
	runOK(t, "external", "release", a1)
	runOK(t, "external", "release", b1)
	f.manifest.SpecVersion = "a-control"
	previous := "z-old"
	f.manifest.PreviousSpecVersion = &previous
	f.save(t)
	a2 := f.install(t)
	control := filepath.Join(f.base, "control.json")
	writeFixture(t, control, mustJSON(t, map[string]any{"schema_version": 1, "controls": []any{map[string]any{"project": f.manifest.Project, "corpus": f.manifest.Corpus, "line": nil, "spec_version": "a-control"}}}))
	importOnly := runOK(t, "overview", "--corpus-control", control, a2, b1).(Overview)
	if importOnly.Scopes[0].CorpusRevision.AuditedVersion != "z-old" || importOnly.Scopes[0].CorpusRevision.Status != "not_checked" {
		t.Fatal("new import was mistaken for an audit of that revision")
	}
	completeExternal(t, a2, "A2")
	runOK(t, "external", "release", a2)
	cfgB, err := loadConfig(b1, true)
	if err != nil {
		t.Fatal(err)
	}
	before := publicationBytes(t, filepath.Join(cfgB.ReportsDir, "B1"))
	view := runOK(t, "overview", "--corpus-control", control, a2, b1).(Overview)
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Scopes []struct {
			Revision struct {
				Status  string `json:"status"`
				Audited string `json:"audited_version"`
				Control string `json:"control_version"`
			} `json:"corpus_revision"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Scopes[0].Revision.Status != "checked" || decoded.Scopes[1].Revision.Status != "not_checked" || decoded.Scopes[1].Revision.Audited != "z-old" || decoded.Scopes[1].Revision.Control != "a-control" {
		t.Fatalf("control revision ignored: %s", data)
	}
	site := filepath.Join(f.base, "controlled-site")
	runOK(t, "publish", site, "--corpus-control", control, a2, b1)
	var published Overview
	if err := json.Unmarshal(readFixture(t, filepath.Join(site, "overview.json")), &published); err != nil {
		t.Fatal(err)
	}
	if published.CorpusControl == nil || published.Scopes[0].CorpusRevision.Status != "checked" || published.Scopes[1].CorpusRevision.Status != "not_checked" {
		t.Fatal("publication lost the explicit control")
	}
	for _, scope := range published.Scopes {
		if scope.Decided.HostReviewState != "saved" || scope.Decided.Freshness != "unchecked" || scope.Decided.SourceAvailability != "released" {
			t.Fatal("revision comparison replaced snapshot/source state")
		}
	}
	html := string(readFixture(t, filepath.Join(site, "index.html")))
	for _, text := range []string{"Контрольные редакции корпусов", "проверено на z-old; на a-control не проверено", "data-corpus-revision=\"checked\""} {
		if !strings.Contains(html, text) {
			t.Fatal("missing control display", text)
		}
	}
	request := filepath.Join(f.base, "control-bundle.json")
	writeFixture(t, request, mustJSON(t, map[string]any{"schema_version": 2, "configs": []string{a2, b1}}))
	archive := filepath.Join(f.base, "control.tar.gz")
	runOK(t, "bundle", "export", request, archive)
	runOK(t, "bundle", "verify", archive)
	restored := runOK(t, "bundle", "restore", archive, filepath.Join(f.base, "restored-control")).(map[string]any)
	restoredControl := filepath.Join(f.base, "restored-control", "control.json")
	if len(restored["sites"].([]string)) != 0 {
		t.Fatal("new bundle retained sites")
	}
	// The caller selects the same control again; it is not an implicit audit setting.
	writeFixture(t, restoredControl, readFixture(t, control))
	if err := os.Remove(control); err != nil {
		t.Fatal(err)
	}
	restoredConfigs := restored["configs"].([]string)
	republished := filepath.Join(f.base, "restored-site")
	args := append([]string{"publish", republished, "--corpus-control", restoredControl}, restoredConfigs...)
	runOK(t, args...)
	var restoredView Overview
	if err := json.Unmarshal(readFixture(t, filepath.Join(republished, "overview.json")), &restoredView); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, scope := range restoredView.Scopes {
		states[scope.Name] = scope.CorpusRevision.Status
	}
	if states["fixture-scope"] != "checked" || states["second-scope"] != "not_checked" {
		t.Fatal("restore/republication changed comparison", states)
	}
	if output := os.Getenv("SPEC_AUDIT_CORPUS_CONTROL_UI"); output != "" {
		copyTestTree(t, republished, output)
	}
	if !reflect.DeepEqual(before, publicationBytes(t, filepath.Join(cfgB.ReportsDir, "B1"))) {
		t.Fatal("control publication rewrote B evidence or tasks")
	}
	// A later audit of the historical revision must not select the control.
	f.manifest.SpecVersion, f.manifest.PreviousSpecVersion = "z-old", nil
	f.save(t)
	historical := f.install(t)
	completeExternal(t, historical, "zz-historical")
	runOK(t, "external", "release", historical)
	late := runOK(t, "overview", "--corpus-control", restoredControl, historical, b1).(Overview)
	if late.Scopes[0].Decided.RunID != "zz-historical" || late.CorpusControl.Controls[0].SpecVersion != "a-control" || late.Scopes[0].CorpusRevision.Status != "not_checked" {
		t.Fatal("a historical audit selected or downgraded the control")
	}
	if !reflect.DeepEqual(before, publicationBytes(t, filepath.Join(cfgB.ReportsDir, "B1"))) {
		t.Fatal("historical audit comparison rewrote B")
	}
	plain := runOK(t, "overview", historical, b1).(Overview)
	if plain.CorpusControl != nil || plain.Scopes[0].CorpusRevision != nil || plain.Scopes[1].CorpusRevision != nil {
		t.Fatal("control was silently inferred or retained for another command")
	}
}

func TestCorpusControlContexts(t *testing.T) {
	line, otherLine, empty := "main", "other", ""
	control := &CorpusControlFile{1, []CorpusControl{{"project", "corpus", &line, "a-control"}}}
	for _, tc := range []struct {
		name, project, corpus, version, status string
		line                                   *string
		complete                               bool
	}{
		{"same", "project", "corpus", "a-control", "checked", &line, true},
		{"different", "project", "corpus", "z-old", "not_checked", &line, true},
		{"other_project", "other", "corpus", "a-control", "incomparable", &line, true},
		{"other_corpus", "project", "other", "a-control", "incomparable", &line, true},
		{"other_line", "project", "corpus", "a-control", "incomparable", &otherLine, true},
		{"null_line", "project", "corpus", "a-control", "incomparable", nil, true},
		{"empty_line", "project", "corpus", "a-control", "incomparable", &empty, true},
		{"missing_project", "", "corpus", "a-control", "incomparable", &line, true},
		{"missing_version", "project", "corpus", "", "incomparable", &line, true},
		{"changed_review_basis", "project", "corpus", "a-control", "incomparable", &line, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := ScopeOverview{Decided: &RunOverview{RunID: "saved", HostReviewComplete: tc.complete, External: &ExternalBasis{Project: tc.project, Corpus: tc.corpus, Line: tc.line, SpecVersion: tc.version}}}
			r := compareCorpusRevision(scope, control)
			if r.Status != tc.status || r.Reason == "" {
				t.Fatal(r)
			}
			if tc.name == "other_line" && r.ControlVersion != "" {
				t.Fatal("borrowed a version from another line")
			}
		})
	}
}

func TestCorpusControlArchiveAndCodeDrift(t *testing.T) {
	f := newExternalFixture(t)
	f.manifest.SpecVersion = "<old>&"
	f.save(t)
	config := f.install(t)
	completeExternal(t, config, "archive")
	cfg, err := loadConfig(config, true)
	if err != nil {
		t.Fatal(err)
	}
	before := publicationBytes(t, filepath.Join(cfg.ReportsDir, "archive"))
	control := filepath.Join(f.base, "control.json")
	value := map[string]any{"schema_version": 1, "controls": []any{map[string]any{"project": f.manifest.Project, "corpus": f.manifest.Corpus, "line": nil, "spec_version": "</li><script>bad()</script>"}}}
	writeFixture(t, control, mustJSON(t, value))
	site := filepath.Join(f.base, "archive-site")
	runOK(t, "publish", site, "--corpus-control", control, config)
	html := string(readFixture(t, filepath.Join(site, "index.html")))
	if strings.Contains(html, "<script>bad()") || !strings.Contains(html, "&lt;script&gt;") || !strings.Contains(html, "data-corpus-revision=\"not_checked\"") {
		t.Fatal("opaque version rendered as markup or lost comparison")
	}
	view := runOK(t, "overview", "--corpus-control", control, config).(Overview)
	if view.Scopes[0].Decided.Freshness != "fresh" || view.Scopes[0].CorpusRevision.Status != "not_checked" {
		t.Fatal("version difference changed snapshot freshness")
	}
	// Matching corpus versions do not hide a proven change to the current code.
	value["controls"].([]any)[0].(map[string]any)["spec_version"] = f.manifest.SpecVersion
	writeFixture(t, control, mustJSON(t, value))
	code := filepath.Join(f.base, "source/source.go")
	writeFixture(t, code, append(readFixture(t, code), []byte("\n// changed after audit\n")...))
	changed := runOK(t, "overview", "--corpus-control", control, config).(Overview)
	if changed.Scopes[0].CorpusRevision.Status != "checked" || changed.Scopes[0].Decided.Freshness != "stale" || changed.Scopes[0].Decided.HostReviewState != "outdated" {
		t.Fatal("corpus revision and code freshness were conflated")
	}
	if !reflect.DeepEqual(before, publicationBytes(t, filepath.Join(cfg.ReportsDir, "archive"))) {
		t.Fatal("presentation changed saved evidence")
	}
}

func TestCorpusControlValidationAndLocalCompatibility(t *testing.T) {
	config, base := fixture(t)
	valid := `{"schema_version":1,"controls":[{"project":"p","corpus":"c","line":null,"spec_version":"R2"}]}`
	file := filepath.Join(base, "control.json")
	out := filepath.Join(base, "site")
	for _, tc := range []struct{ name, raw string }{
		{"null", `null`},
		{"empty", `{"schema_version":1,"controls":[]}`},
		{"version", strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1)},
		{"missing_line", strings.Replace(valid, `"line":null,`, ``, 1)},
		{"empty_line", strings.Replace(valid, `"line":null`, `"line":" "`, 1)},
		{"null_project", strings.Replace(valid, `"project":"p"`, `"project":null`, 1)},
		{"missing_version", strings.Replace(valid, `,"spec_version":"R2"`, ``, 1)},
		{"unknown", strings.Replace(valid, `"line":null`, `"line":null,"guess":true`, 1)},
		{"duplicate_key", strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)},
		{"duplicate_context", `{"schema_version":1,"controls":[{"project":"p","corpus":"c","line":null,"spec_version":"R2"},{"project":"p","corpus":"c","line":null,"spec_version":"R1"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFixture(t, file, []byte(tc.raw))
			runFail(t, "publish", out, "--corpus-control", file, config)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("invalid control created publication")
			}
		})
	}
	writeFixture(t, file, []byte(valid))
	for _, args := range [][]string{
		{"overview", "--corpus-control"},
		{"overview", "--corpus-control", file},
		{"overview", "--corpus-control", file, "--corpus-control", file, config},
		{"overview", "--unknown", config},
	} {
		runFail(t, args...)
	}
	plain := runOK(t, "overview", config).(Overview)
	encoded, _ := json.Marshal(plain)
	if bytes.Contains(encoded, []byte("corpus_control")) || bytes.Contains(encoded, []byte("corpus_revision")) {
		t.Fatal("ordinary input changed without control")
	}
	local := runOK(t, "overview", config, "--corpus-control", file).(Overview)
	if local.Scopes[0].CorpusRevision.Status != "incomparable" || local.Scopes[0].CorpusRevision.AuditedVersion != "" {
		t.Fatal("local input acquired a made-up version")
	}
	runOK(t, "corpus", filepath.Join(base, "map.html"), "--corpus-control", file, config)
	link := filepath.Join(base, "control-link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	runFail(t, "publish", out, "--corpus-control", link, config)
	// No external integration is needed for the option or for ordinary publication.
	runOK(t, "publish", out, "--corpus-control", file, config)
}
