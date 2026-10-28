//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNestedReportsBundleRestoreAndPublicationCleanup(t *testing.T) {
	f := newExternalFixture(t)
	parent := f.install(t)
	completeExternal(t, parent, "A")
	parentCfg, err := loadConfig(parent, true)
	if err != nil {
		t.Fatal(err)
	}
	template, err := loadConfig(f.config, true)
	if err != nil {
		t.Fatal(err)
	}
	template.ReportsDir = filepath.Join(parentCfg.ReportsDir, "nested", "child-runs")
	ext := *template.External
	ext.Scope = "child-scope"
	template.External = &ext
	raw, err := yaml.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	childTemplate := filepath.Join(f.base, "child.yaml")
	writeFixture(t, childTemplate, raw)
	child := runOK(t, "external", "import", childTemplate, f.input).(map[string]any)["config"].(string)
	completeExternal(t, child, "A")
	childCfg, err := loadConfig(child, true)
	if err != nil {
		t.Fatal(err)
	}
	configs := []string{parent, child}
	publicationView(t, filepath.Join(f.base, "both-before-cleanup"), configs...)
	// Unknown files remain part of each owned audit area.
	writeFixture(t, filepath.Join(parentCfg.ReportsDir, "notes/operator.txt"), []byte("parent retained notes"))
	writeFixture(t, filepath.Join(childCfg.ReportsDir, "notes/operator.txt"), []byte("child retained notes"))
	legacy := filepath.Join(f.base, "legacy-cache")
	writeFixture(t, filepath.Join(legacy, "index.html"), []byte("selected legacy page"))
	writeFixture(t, filepath.Join(legacy, "shared.txt"), []byte("shared publication resource"))
	writeFixture(t, filepath.Join(legacy, "source.bin"), readFixture(t, filepath.Join(parentCfg.ProjectRoot, "source.go")))
	if err := recordPublication(legacy, configs); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(parentCfg.ReportsDir, "._publications/*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	selectedID := strings.TrimSuffix(filepath.Base(paths[0]), ".json")
	writeFixture(t, filepath.Join(legacy, "index.html"), []byte("retained child page"))
	if err := recordPublication(legacy, []string{child}); err != nil {
		t.Fatal(err)
	}
	childPublications, err := filepath.Glob(filepath.Join(childCfg.ReportsDir, "._publications/*.json"))
	if err != nil || len(childPublications) != 2 {
		t.Fatal(childPublications, err)
	}
	var unselectedPath string
	for _, path := range childPublications {
		if strings.TrimSuffix(filepath.Base(path), ".json") != selectedID {
			unselectedPath = path
		}
	}
	unselectedRaw := readFixture(t, unselectedPath)
	var unselected PublicationSnapshot
	if err := strictJSON(unselectedRaw, &unselected); err != nil {
		t.Fatal(err)
	}
	var backup string
	var bundle evidenceBundle
	for i, order := range [][]string{configs, {child, parent}} {
		request := filepath.Join(f.base, fmt.Sprintf("request-%d.json", i))
		writeFixture(t, request, mustJSON(t, map[string]any{"schema_version": 2, "configs": order}))
		archive := filepath.Join(f.base, fmt.Sprintf("audit-%d.tar.gz", i))
		runOK(t, "bundle", "export", request, archive)
		b, err := readBundle(archive)
		if err != nil {
			t.Fatal(err)
		}
		var parentID, childID string
		for _, area := range b.Index.Areas {
			if area.Kind == "reports" {
				if area.OriginalRoot == parentCfg.ReportsDir {
					parentID = area.ID
				}
				if area.OriginalRoot == childCfg.ReportsDir {
					childID = area.ID
				}
			}
		}
		relative, err := filepath.Rel(parentCfg.ReportsDir, childCfg.ReportsDir)
		if err != nil {
			t.Fatal(err)
		}
		childPrefix := filepath.ToSlash(relative) + "/"
		sawChildNotes := false
		for _, file := range b.Index.Files {
			if file.Area == parentID && strings.HasPrefix(file.Path, childPrefix) {
				t.Fatal("child file was counted inside parent audit area", file.Path)
			}
			if strings.Contains(file.Path, "._publications/") || strings.HasSuffix(file.Path, "/report.html") {
				t.Fatal("derived publication remained in audit-only bundle", file.Path)
			}
			if file.Area == childID && file.Path == "notes/operator.txt" {
				sawChildNotes = true
			}
		}
		if parentID == "" || childID == "" || !sawChildNotes {
			t.Fatal("owned areas or unknown retained data missing")
		}
		if i == 0 {
			backup, bundle = archive, b
		}
	}
	restored := runOK(t, "bundle", "restore", backup, filepath.Join(f.base, "restored")).(map[string]any)
	restoredConfigs := restored["configs"].([]string)
	publicationView(t, filepath.Join(f.base, "restored-site"), restoredConfigs...)
	for i, config := range restoredConfigs {
		publicationView(t, filepath.Join(f.base, fmt.Sprintf("restored-history-%d", i)), "--run", "A", config)
	}
	plan := runOK(t, "cleanup", "plan", backup, "--publications", selectedID).(CleanupPlan)
	// No owned file in the live basis is also included below the parent's child path.
	for _, area := range bundle.Index.Areas {
		if area.Kind == "reports" && area.OriginalRoot == parentCfg.ReportsDir {
			for _, file := range plan.PublicationBasis.Files {
				if file.Area == area.ID && strings.HasPrefix(file.Path, "nested/child-runs/") {
					t.Fatal("live basis duplicates physical child data")
				}
			}
		}
	}
	pp, pf := filepath.Join(f.base, "plan.json"), filepath.Join(f.base, "proof.json")
	writeFixture(t, pp, mustJSON(t, plan))
	proof := runOK(t, "cleanup", "verify", backup, pp).(CleanupProof)
	writeFixture(t, pf, mustJSON(t, proof))
	// All selected area locks and the child live basis must be checked before deletion.
	func() {
		unlock, err := lockWorkspaces([]string{childCfg.ReportsDir}, false)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		request := filepath.Join(f.base, "request-0.json")
		for _, args := range [][]string{{"bundle", "export", request, filepath.Join(f.base, "busy.tar.gz")}, {"cleanup", "apply", backup, pp, pf}} {
			if _, err := execute(args); err == nil || !strings.Contains(err.Error(), "занята") {
				t.Fatal("child writer did not block operation", args, err)
			}
		}
	}()
	dependency := filepath.Join(childCfg.ReportsDir, "new-dependency.txt")
	writeFixture(t, dependency, []byte("new dependency"))
	runFail(t, "cleanup", "apply", backup, pp, pf)
	if err := os.Remove(dependency); err != nil {
		t.Fatal(err)
	}
	areas := map[string]BundleArea{}
	for _, area := range bundle.Index.Areas {
		areas[area.ID] = area
	}
	type originalFile struct {
		data []byte
		mode os.FileMode
	}
	originals := map[string]originalFile{}
	for _, candidate := range plan.Candidates {
		path := filepath.Join(areas[candidate.Area].OriginalRoot, filepath.FromSlash(candidate.Path))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = originalFile{readFixture(t, path), info.Mode().Perm()}
	}
	if len(plan.Candidates) < 3 {
		t.Fatal("test lost physical deletion prefixes")
	}
	for prefix := 0; prefix <= len(plan.Candidates); prefix++ {
		for path, file := range originals {
			if err := os.Chmod(path, 0600); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			writeFixture(t, path, file.data)
			if err := os.Chmod(path, file.mode); err != nil {
				t.Fatal(err)
			}
		}
		for _, candidate := range plan.Candidates[:prefix] {
			if err := os.Remove(filepath.Join(areas[candidate.Area].OriginalRoot, filepath.FromSlash(candidate.Path))); err != nil {
				t.Fatal(err)
			}
		}
		for repeat := 0; repeat < 2; repeat++ {
			result := runOK(t, "cleanup", "apply", backup, pp, pf).(map[string]any)
			if result["complete"] != true {
				t.Fatal("nested cleanup was not safely resumable", prefix, repeat)
			}
		}
		for _, file := range bundle.Index.Files {
			if areas[file.Area].Kind != "reports" {
				continue
			}
			path := filepath.Join(areas[file.Area].OriginalRoot, filepath.FromSlash(file.Path))
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal("retained audit file missing", path, err)
			}
			if digest(readFixture(t, path)) != file.SHA256 || uint32(info.Mode().Perm()) != file.Mode {
				t.Fatal("retained audit bytes/mode changed", path)
			}
		}
		if !reflect.DeepEqual(unselectedRaw, readFixture(t, unselectedPath)) {
			t.Fatal("unselected child publication changed")
		}
		for _, file := range unselected.Files {
			if digest(readFixture(t, filepath.Join(childCfg.ReportsDir, "._objects", file.SHA256))) != file.SHA256 {
				t.Fatal("unselected child publication object changed")
			}
		}
	}
	publicationView(t, filepath.Join(f.base, "after-cleanup"), configs...)
	for i, config := range configs {
		publicationView(t, filepath.Join(f.base, fmt.Sprintf("history-after-cleanup-%d", i)), "--run", "A", config)
	}
}

func TestNestedReportsPreserveChildRootMetadata(t *testing.T) {
	for _, relative := range []string{"child", "._publications", "._publications/child", "._publications/group/child"} {
		t.Run(strings.ReplaceAll(relative, "/", "-"), func(t *testing.T) {
			testNestedReportsPreserveChildRootMetadata(t, relative)
		})
	}
}

func testNestedReportsPreserveChildRootMetadata(t *testing.T, relative string) {
	f := newExternalFixture(t)
	parent := f.install(t)
	completeExternal(t, parent, "A")
	parentCfg, err := loadConfig(parent, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(f.config, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReportsDir = filepath.Join(parentCfg.ReportsDir, filepath.FromSlash(relative))
	ext := *cfg.External
	ext.Scope = "child-scope"
	cfg.External = &ext
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(f.base, "child.yaml")
	writeFixture(t, template, raw)
	child := runOK(t, "external", "import", template, f.input).(map[string]any)["config"].(string)
	completeExternal(t, child, "A")
	// This is an unknown file in the child store, not a run manifest of the parent.
	metadata := []byte("{\"owner\":\"child\",\"note\":\"retained root metadata\"}\n")
	writeFixture(t, filepath.Join(cfg.ReportsDir, "manifest.json"), metadata)
	// Both selected boundaries must also apply to live publication readers.
	explicit := publicationView(t, filepath.Join(f.base, "nested-explicit-site"), parent, child)
	if len(explicit.Scopes) != 2 {
		t.Fatal("explicit publication lost a selected nested scope")
	}
	publicationView(t, filepath.Join(f.base, "parent-history"), "--run", "A", parent)
	if relative == "child" {
		// No recognized working CONFIG: force discovery's saved-manifest fallback.
		if err := os.Rename(f.config, filepath.Join(f.base, "template-only.yaml")); err != nil {
			t.Fatal(err)
		}
		view := publicationView(t, filepath.Join(f.base, "nested-discovered-site"), "--from", parentCfg.ReportsDir)
		if len(view.Scopes) != 2 {
			t.Fatal("nested discovery lost a selected scope")
		}
	}
	backup := exportAuditTestBundle(t, []string{parent, child}, f.base)
	bundle, err := readBundle(backup)
	if err != nil {
		t.Fatal(err)
	}
	var childArea string
	for _, area := range bundle.Index.Areas {
		if area.Kind == "reports" && area.OriginalRoot == cfg.ReportsDir {
			childArea = area.ID
		}
	}
	found := false
	for _, file := range bundle.Index.Files {
		if file.Area == childArea && file.Path == "manifest.json" {
			found = true
			if !reflect.DeepEqual(metadata, bundle.Objects[file.SHA256]) {
				t.Fatal("child metadata changed")
			}
		}
	}
	if !found {
		t.Fatal("unknown child root metadata was omitted")
	}
	selectedManifest := filepath.Join(parentCfg.ReportsDir, "A", "manifest.json")
	if err := os.Chmod(selectedManifest, 0600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, selectedManifest, []byte("{broken"))
	for i, args := range [][]string{{parent, child}, {"--run", "A", parent}} {
		out := filepath.Join(f.base, fmt.Sprintf("corrupt-parent-%d", i))
		if _, err := execute(append([]string{"publish", out}, args...)); err == nil {
			t.Fatal("actual corrupt run was hidden by nested metadata filtering")
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("corrupt selected run exposed OUT")
		}
	}
}
