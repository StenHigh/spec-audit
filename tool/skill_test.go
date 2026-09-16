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
	start := strings.Index(text, "\n## 14. ")
	end := strings.Index(text, "\n## 15. ")
	if start < 0 || end < start {
		t.Fatal("§14 не найден в tool-spec")
	}
	section := strings.TrimRight(text[start+1:end], "\n") + "\n"
	mirror, err := os.ReadFile(filepath.Join("..", skillSourceDir, "references", "host-decision.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(mirror), "\n## 14. ")
	if !ok || "## 14. "+body != section {
		t.Fatal("references/host-decision.md расходится с §14 tool-spec")
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
