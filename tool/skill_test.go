//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Launcher-authored files must be self-contained and free of pilot knowledge; bundled frozen docs are checked only for
// absolute paths and tracker IDs (their checkout navigation links are a documented owner decision, tool-spec §18).
func TestSkillFiles(t *testing.T) {
	files, err := skillFiles("dev")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	root := filepath.Join("..", skillSourceDir)
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		want[filepath.ToSlash(rel)] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range skillDocs {
		want["references/docs/"+name] = true
	}
	got := map[string]bool{}
	for rel := range files {
		got[rel] = true
	}
	if len(got) != len(want) {
		t.Fatalf("набор %v, ожидался %v", keys(got), keys(want))
	}
	for rel := range want {
		if !got[rel] {
			t.Fatalf("нет файла %s", rel)
		}
	}
	for _, rel := range []string{"references/protocol.txt", "references/host-decision.md"} {
		disk, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(disk, files[rel]) {
			t.Fatalf("%s должен копироваться байт-в-байт", rel)
		}
	}
	for _, name := range skillDocs {
		disk, err := os.ReadFile(filepath.Join("..", "docs", name))
		if err != nil || !bytes.Equal(disk, files["references/docs/"+name]) {
			t.Fatalf("docs/%s должен копироваться байт-в-байт", name)
		}
	}
	linkRE := regexp.MustCompile(`\]\(([^)]+)\)`)
	pilotRE := regexp.MustCompile(`(?i)smsplace`)
	absRE := regexp.MustCompile(`/Users/|/home/`)
	issueRE := regexp.MustCompile(`(^|\s)#\d+\b`)
	for rel, data := range files {
		text := string(data)
		if absRE.MatchString(text) || issueRE.MatchString(text) {
			t.Fatalf("%s содержит абсолютный путь или ID задачи", rel)
		}
		if strings.HasPrefix(rel, "references/docs/") {
			continue
		}
		if pilotRE.MatchString(text) {
			t.Fatalf("%s упоминает пилот", rel)
		}
		if strings.Contains(text, "](../") {
			t.Fatalf("%s ссылается вне копии", rel)
		}
		for _, m := range linkRE.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			if _, ok := files[path.Join(path.Dir(rel), target)]; !ok {
				t.Fatalf("%s: ссылка %s не существует в копии", rel, m[1])
			}
		}
	}
	if !strings.Contains(string(files["SKILL.md"]), "](references/docs/accepted-index.md)") {
		t.Fatal("ссылки SKILL.md не перенаправлены на встроенные документы")
	}
	receipt := skillReceipt(files, "1.2.3")
	if !bytes.Equal(receipt, skillReceipt(files, "1.2.3")) {
		t.Fatal("receipt недетерминирован")
	}
	var parsed SkillReceipt
	if err := strictJSON(receipt, &parsed); err != nil || parsed.SchemaVersion != skillReceiptSchema || parsed.Version != "1.2.3" || len(parsed.Files) != len(files) {
		t.Fatalf("receipt: %v %+v", err, parsed)
	}
	for rel, data := range files {
		if parsed.Files[rel] != digest(data) {
			t.Fatalf("receipt digest %s", rel)
		}
	}
	if _, ok := parsed.Files[skillReceiptName]; ok {
		t.Fatal("receipt не должен описывать сам себя")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(receipt, &raw); err != nil || len(raw) != 3 {
		t.Fatalf("receipt поля: %v", err)
	}
}

func TestHostDecisionMirror(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "docs", "tool-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec)
	section := func(from, to string) string {
		start := strings.Index(text, from)
		end := strings.Index(text, to)
		if start < 0 || end < start {
			t.Fatalf("раздел %q не найден в tool-spec", from)
		}
		return strings.TrimRight(text[start+1:end], "\n") + "\n"
	}
	// The delivered mirror carries §14 and §23 verbatim: the host decision contract and its light form.
	want := section("\n## 14. ", "\n## 15. ") + "\n" + section("\n## 23. ", "\n## 24. ")
	mirror, err := os.ReadFile(filepath.Join("..", skillSourceDir, "references", "host-decision.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(mirror), "\n## 14. ")
	if !ok || "## 14. "+body != want {
		t.Fatal("references/host-decision.md расходится с §14 и §23 tool-spec")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// path → kind plus link target or content digest; identical maps mean an identical tree.
func treeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "link:" + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			data, _ := os.ReadFile(p)
			out[rel] = "file:" + digest(data) + ":" + info.ModTime().String()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestSkill(t *testing.T) {
	files, err := skillFiles("dev")
	if err != nil {
		t.Fatal(err)
	}
	skillPath := func(dir string) string { return filepath.Join(dir, ".spec-audit", "skill") }
	install := func(t *testing.T, dir, host string) {
		t.Helper()
		if got, err := runSkill("install", dir, host, false, "0.1.0"); err != nil || got["updated"] != true {
			t.Fatalf("install: %v %v", err, got)
		}
	}
	write := func(t *testing.T, p string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(t *testing.T, target, p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	// Rewrite the receipt so the disk copy of rel looks like an older managed version; nil data = file absent in it.
	backdate := func(t *testing.T, dir, rel string, data []byte) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(skillPath(dir), skillReceiptName))
		if err != nil {
			t.Fatal(err)
		}
		var receipt SkillReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		if data == nil {
			if err := os.Remove(filepath.Join(skillPath(dir), filepath.FromSlash(rel))); err != nil {
				t.Fatal(err)
			}
			delete(receipt.Files, rel)
		} else {
			write(t, filepath.Join(skillPath(dir), filepath.FromSlash(rel)), data)
			receipt.Files[rel] = digest(data)
		}
		raw, _ = json.Marshal(receipt)
		write(t, filepath.Join(skillPath(dir), skillReceiptName), raw)
	}
	checkInstalled := func(t *testing.T, dir string, hosts ...string) {
		t.Helper()
		for rel, data := range files {
			got, err := os.ReadFile(filepath.Join(skillPath(dir), filepath.FromSlash(rel)))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s: %v", rel, err)
			}
		}
		raw, err := os.ReadFile(filepath.Join(skillPath(dir), skillReceiptName))
		if err != nil || !bytes.Equal(raw, skillReceipt(files, "0.1.0")) {
			t.Fatalf("receipt: %v", err)
		}
		for _, rel := range hosts {
			target, err := os.Readlink(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil || target != skillLinkTarget {
				t.Fatalf("%s → %q %v", rel, target, err)
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel), "SKILL.md")); err != nil {
				t.Fatalf("ссылка %s не разрешается: %v", rel, err)
			}
		}
	}
	codex, claude := ".agents/skills/spec-audit", ".claude/skills/spec-audit"

	cases := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		sub     string
		host    string
		replace bool
		fail    bool
		updated bool
		links   map[string]string
		check   func(t *testing.T, dir string)
	}{
		{name: "fresh_both", sub: "install", host: "both", updated: true, links: map[string]string{codex: "created", claude: "created"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, codex, claude)
		}},
		{name: "fresh_codex", sub: "install", host: "codex", updated: true, links: map[string]string{codex: "created"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, codex)
			if _, err := os.Lstat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
				t.Fatal(".claude создан без выбора host")
			}
		}},
		{name: "fresh_claude", sub: "install", host: "claude", updated: true, links: map[string]string{claude: "created"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, claude)
			if _, err := os.Lstat(filepath.Join(dir, ".agents")); !os.IsNotExist(err) {
				t.Fatal(".agents создан без выбора host")
			}
		}},
		{name: "install_twice_noop", prepare: func(t *testing.T, dir string) { install(t, dir, "both") }, sub: "install", host: "both", links: map[string]string{codex: "kept", claude: "kept"}},
		{name: "install_adds_missing_link", prepare: func(t *testing.T, dir string) { install(t, dir, "codex") }, sub: "install", host: "both", links: map[string]string{codex: "kept", claude: "created"}},
		{name: "symlink_no_replace", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "checkout", "SKILL.md"), []byte("manual"))
			link(t, "../checkout", skillPath(dir))
		}, sub: "install", host: "both", fail: true},
		{name: "symlink_replace", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "checkout", "SKILL.md"), []byte("manual"))
			link(t, "../checkout", skillPath(dir))
			link(t, "../../checkout", filepath.Join(dir, codex))
		}, sub: "install", host: "both", replace: true, updated: true, links: map[string]string{codex: "relinked", claude: "created"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, codex, claude)
			if data, err := os.ReadFile(filepath.Join(dir, "checkout", "SKILL.md")); err != nil || string(data) != "manual" {
				t.Fatal("цель прежней ссылки изменена")
			}
			if info, err := os.Lstat(skillPath(dir)); err != nil || !info.IsDir() {
				t.Fatal("skill не стал реальным каталогом")
			}
		}},
		{name: "foreign_dir", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(skillPath(dir), "SKILL.md"), []byte("someone else"))
		}, sub: "install", host: "both", replace: true, fail: true},
		{name: "foreign_receipt", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(skillPath(dir), skillReceiptName), []byte(`{"schema_version":"other/1"}`))
		}, sub: "install", host: "both", replace: true, fail: true},
		{name: "foreign_link", prepare: func(t *testing.T, dir string) {
			link(t, "../../elsewhere", filepath.Join(dir, codex))
		}, sub: "install", host: "codex", fail: true},
		{name: "foreign_link_replace", prepare: func(t *testing.T, dir string) {
			link(t, "../../elsewhere", filepath.Join(dir, codex))
		}, sub: "install", host: "codex", replace: true, updated: true, links: map[string]string{codex: "relinked"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, codex)
		}},
		{name: "real_path", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, claude, "SKILL.md"), []byte("real"))
		}, sub: "install", host: "both", replace: true, fail: true},
		{name: "update_noop", prepare: func(t *testing.T, dir string) { install(t, dir, "both") }, sub: "update", host: "both", links: map[string]string{codex: "kept", claude: "kept"}},
		{name: "update_stale_rewrites", prepare: func(t *testing.T, dir string) {
			install(t, dir, "both")
			backdate(t, dir, "references/protocol.txt", []byte("older protocol"))
			backdate(t, dir, "references/host-decision.md", nil)
		}, sub: "update", host: "both", updated: true, links: map[string]string{codex: "kept", claude: "kept"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, codex, claude)
		}},
		{name: "update_drift", prepare: func(t *testing.T, dir string) {
			install(t, dir, "both")
			write(t, filepath.Join(skillPath(dir), "SKILL.md"), []byte("edited locally"))
		}, sub: "update", host: "both", fail: true},
		{name: "update_deleted_is_drift", prepare: func(t *testing.T, dir string) {
			install(t, dir, "both")
			if err := os.Remove(filepath.Join(skillPath(dir), "SKILL.md")); err != nil {
				t.Fatal(err)
			}
		}, sub: "update", host: "both", fail: true},
		{name: "update_drift_replace", prepare: func(t *testing.T, dir string) {
			install(t, dir, "both")
			write(t, filepath.Join(skillPath(dir), "SKILL.md"), []byte("edited locally"))
			write(t, filepath.Join(skillPath(dir), "notes.md"), []byte("mine"))
			write(t, filepath.Join(skillPath(dir), "references", ".DS_Store"), []byte("finder"))
		}, sub: "update", host: "both", replace: true, updated: true, links: map[string]string{codex: "kept", claude: "kept"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, codex, claude)
			for _, extra := range []string{"notes.md", "references/.DS_Store"} {
				if _, err := os.Stat(filepath.Join(skillPath(dir), filepath.FromSlash(extra))); err != nil {
					t.Fatalf("чужой файл %s удалён", extra)
				}
			}
		}},
		{name: "update_needs_install_absent", sub: "update", host: "both", fail: true},
		{name: "update_replace_absent", sub: "update", host: "both", replace: true, fail: true},
		{name: "update_foreign_no_receipt", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(skillPath(dir), "SKILL.md"), []byte("someone else"))
		}, sub: "update", host: "both", replace: true, fail: true},
		{name: "install_replace_drift", prepare: func(t *testing.T, dir string) {
			install(t, dir, "both")
			write(t, filepath.Join(skillPath(dir), "SKILL.md"), []byte("edited locally"))
		}, sub: "install", host: "both", replace: true, updated: true, links: map[string]string{codex: "kept", claude: "kept"}, check: func(t *testing.T, dir string) {
			checkInstalled(t, dir, codex, claude)
		}},
		{name: "receipt_unknown_field_is_foreign", prepare: func(t *testing.T, dir string) {
			install(t, dir, "both")
			raw, _ := os.ReadFile(filepath.Join(skillPath(dir), skillReceiptName))
			write(t, filepath.Join(skillPath(dir), skillReceiptName), bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"extra":1,"schema_version"`), 1))
		}, sub: "update", host: "both", replace: true, fail: true},
		{name: "host_parent_is_file", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, ".agents", "skills"), []byte("not a dir"))
		}, sub: "install", host: "codex", fail: true},
		{name: "managed_path_symlink_refused", prepare: func(t *testing.T, dir string) {
			install(t, dir, "both")
			if err := os.Remove(filepath.Join(skillPath(dir), "SKILL.md")); err != nil {
				t.Fatal(err)
			}
			link(t, "references/protocol.txt", filepath.Join(skillPath(dir), "SKILL.md"))
		}, sub: "update", host: "both", replace: true, fail: true},
		{name: "update_needs_install_symlink", prepare: func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "checkout", "SKILL.md"), []byte("manual"))
			link(t, "../checkout", skillPath(dir))
		}, sub: "update", host: "both", replace: true, fail: true},
		{name: "escape_symlink", prepare: func(t *testing.T, dir string) {
			link(t, t.TempDir(), filepath.Join(dir, ".spec-audit"))
		}, sub: "install", host: "both", replace: true, fail: true},
		{name: "escape_host_parent", prepare: func(t *testing.T, dir string) {
			link(t, t.TempDir(), filepath.Join(dir, ".agents"))
		}, sub: "install", host: "codex", fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.prepare != nil {
				tc.prepare(t, dir)
			}
			before := treeSnapshot(t, dir)
			got, err := runSkill(tc.sub, dir, tc.host, tc.replace, "0.1.0")
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v got=%v", err, got)
			}
			if tc.fail {
				if !sameTree(before, treeSnapshot(t, dir)) {
					t.Fatal("отказ изменил дерево DIR")
				}
				return
			}
			if got["updated"] != tc.updated || got["dir"] != dir || got["skill"] != skillPath(dir) || got["version"] != "0.1.0" {
				t.Fatalf("%v", got)
			}
			links := got["links"].(map[string]string)
			if len(links) != len(tc.links) {
				t.Fatalf("links %v", links)
			}
			for rel, action := range tc.links {
				if links[rel] != action {
					t.Fatalf("links[%s]=%s, ожидалось %s", rel, links[rel], action)
				}
			}
			if !tc.updated && !sameTree(before, treeSnapshot(t, dir)) && len(tc.links) > 0 && !hasCreated(tc.links) {
				t.Fatal("no-op изменил дерево DIR")
			}
			if tc.check != nil {
				tc.check(t, dir)
			}
			if entries, _ := filepath.Glob(filepath.Join(skillPath(dir), ".tmp-*")); len(entries) != 0 {
				t.Fatalf("временные файлы: %v", entries)
			}
		})
	}
}

func hasCreated(links map[string]string) bool {
	for _, action := range links {
		if action != "kept" {
			return true
		}
	}
	return false
}

func TestSkillCommandFlags(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{}, {"remove"}, {"install"}, {"install", "--dir", dir}, {"install", "--dir", dir, "--host", "cursor"},
		{"install", "--dir", dir, "--host", "both", "--bogus"}, {"install", "--dir", dir, "--host", "both", "extra"},
		{"install", "--dir", filepath.Join(dir, "missing"), "--host", "both"},
	} {
		before := treeSnapshot(t, dir)
		if _, err := runSkillCommand(args, "dev"); err == nil || strings.Contains(err.Error(), "Usage") {
			t.Fatalf("%v: %v", args, err)
		}
		if !sameTree(before, treeSnapshot(t, dir)) {
			t.Fatalf("%v: отказ изменил дерево", args)
		}
	}
	got, err := runSkillCommand([]string{"install", "-dir", dir, "-host", "claude"}, "dev")
	if err != nil || got["updated"] != true || got["version"] != "dev" {
		t.Fatalf("%v %v", err, got)
	}
}
