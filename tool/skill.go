//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	dist "github.com/StenHigh/spec-audit"
)

const (
	skillSourceDir     = "skills/spec-audit"
	skillReceiptName   = ".spec-audit-skill.json"
	skillReceiptSchema = "spec-audit-skill/1"
)

// Contracts SKILL.md links to; bundled byte-for-byte under references/docs/ so the installed copy needs no checkout.
var skillDocs = []string{"accepted-index.md", "legacy-extraction.md", "php-sdk-contract.md"}

type SkillReceipt struct {
	SchemaVersion string            `json:"schema_version"`
	Version       string            `json:"version"`
	Files         map[string]string `json:"files"`
}

// Materialise the installed launcher: SKILL.md links move to the bundled docs; everything else is copied unchanged.
func skillFiles(version string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(dist.Files, skillSourceDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(dist.Files, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, skillSourceDir+"/")
		if rel == "SKILL.md" {
			data = bytes.ReplaceAll(data, []byte("](../../docs/"), []byte("](references/docs/"))
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, name := range skillDocs {
		data, err := fs.ReadFile(dist.Files, "docs/"+name)
		if err != nil {
			return nil, err
		}
		files["references/docs/"+name] = data
	}
	slog.Debug("skill: набор файлов подготовлен", "files", len(files), "version", version)
	return files, nil
}

// json.Marshal sorts map keys, so the receipt is deterministic for a given set.
func skillReceipt(files map[string][]byte, version string) []byte {
	receipt := SkillReceipt{SchemaVersion: skillReceiptSchema, Version: version, Files: map[string]string{}}
	for rel, data := range files {
		receipt.Files[rel] = digest(data)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

const (
	skillInstallDir = ".spec-audit/skill"
	skillLinkTarget = "../../.spec-audit/skill"
	skillUsage      = "skill install|update --dir DIR --host codex|claude|both [--replace]"
)

var skillHosts = map[string][]string{
	"codex":  {".agents/skills/spec-audit"},
	"claude": {".claude/skills/spec-audit"},
	"both":   {".agents/skills/spec-audit", ".claude/skills/spec-audit"},
}

// Observed layout before any write. drift lists managed files edited locally (disk ≠ receipt and ≠ embedded);
// stale lists managed files whose disk bytes match the receipt but not the embedded set (an older version).
type skillState struct {
	kind  string // absent | symlink | foreign | ours
	drift []string
	stale []string
}

func runSkillCommand(args []string, current string) (map[string]any, error) {
	if len(args) == 0 || !oneOf(args[0], "install", "update") {
		return nil, errors.New(skillUsage)
	}
	flags := flag.NewFlagSet("skill", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "")
	host := flags.String("host", "", "")
	replace := flags.Bool("replace", false, "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return nil, errors.New(skillUsage)
	}
	if _, ok := skillHosts[*host]; !ok {
		return nil, errors.New("нужен --host codex|claude|both: каталоги агента создаются только по явному выбору")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return nil, err
	}
	root := skillProjectRoot(abs)
	if root != abs {
		slog.Debug("skill: корень проекта", "dir", root, "given", abs)
	}
	return runSkill(args[0], root, *host, *replace, current)
}

// skillProjectRoot maps DIR/.spec-audit or DIR/.spec-audit/skill back to DIR by directory names alone (tool-spec §24.5).
func skillProjectRoot(abs string) string {
	if filepath.Base(abs) == "skill" && filepath.Base(filepath.Dir(abs)) == ".spec-audit" {
		return filepath.Dir(filepath.Dir(abs))
	}
	if filepath.Base(abs) == ".spec-audit" {
		return filepath.Dir(abs)
	}
	return abs
}

func runSkill(sub, dir, host string, replace bool, current string) (map[string]any, error) {
	files, err := skillFiles(current)
	if err != nil {
		return nil, err
	}
	dirRoot, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer dirRoot.Close()
	state, err := readSkillState(dirRoot, files)
	if err != nil {
		return nil, err
	}
	links, err := planSkillLinks(dirRoot, skillHosts[host], replace)
	if err != nil {
		return nil, err
	}
	slog.Debug("skill: состояние", "skill_state", state.kind, "drift", len(state.drift), "stale", len(state.stale), "links", links)
	var write []string
	unlink := false
	switch {
	case state.kind == "absent" && sub == "install":
		write = sortedKeys(files)
	case state.kind == "symlink" && sub == "install" && replace:
		unlink, write = true, sortedKeys(files)
	case state.kind == "symlink" && sub == "install":
		return nil, errors.New("на месте .spec-audit/skill находится символическая ссылка; повторите с --replace, чтобы заменить её реальной копией")
	case state.kind == "foreign":
		return nil, errors.New("каталог .spec-audit/skill создан не этим инструментом (нет receipt или receipt не по контракту); он не заменяется")
	case state.kind != "ours":
		return nil, fmt.Errorf("каталог %s отсутствует или не установлен этим инструментом; --dir — корень проекта, содержащий .spec-audit/skill; сначала skill install", filepath.Join(dir, filepath.FromSlash(skillInstallDir)))
	case len(state.drift) > 0 && !replace:
		return nil, fmt.Errorf("локально изменённые файлы skill: %s; повторите с --replace, чтобы переписать только управляемые файлы", strings.Join(state.drift, ", "))
	default:
		write = append(append(write, state.stale...), state.drift...)
		sort.Strings(write)
	}
	if err := writeSkill(dirRoot, files, write, unlink, current); err != nil {
		return nil, err
	}
	if err := applySkillLinks(dirRoot, links); err != nil {
		return nil, err
	}
	switch {
	case len(write) == 0:
		slog.Info("skill: обновление не требуется", "version", current, "links", links)
	case sub == "install":
		slog.Info("skill: установлен", "version", current, "files", len(write), "links", links)
	default:
		slog.Info("skill: обновлён", "version", current, "files", len(write), "links", links)
	}
	return map[string]any{"dir": dir, "skill": filepath.Join(dir, filepath.FromSlash(skillInstallDir)), "version": current, "updated": len(write) > 0, "links": links}, nil
}

func readSkillState(dirRoot *os.Root, files map[string][]byte) (skillState, error) {
	info, err := dirRoot.Lstat(skillInstallDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return skillState{kind: "absent"}, nil
	case err != nil:
		return skillState{}, err
	case info.Mode()&fs.ModeSymlink != 0:
		return skillState{kind: "symlink"}, nil
	case !info.IsDir():
		return skillState{kind: "foreign"}, nil
	}
	skillRoot, err := dirRoot.OpenRoot(skillInstallDir)
	if err != nil {
		return skillState{}, err
	}
	defer skillRoot.Close()
	var receipt SkillReceipt
	raw, err := readRootFile(skillRoot, skillReceiptName)
	if err != nil || strictJSON(raw, &receipt) != nil || receipt.SchemaVersion != skillReceiptSchema {
		return skillState{kind: "foreign"}, nil
	}
	state := skillState{kind: "ours"}
	for _, rel := range sortedKeys(files) {
		disk, err := readRootFile(skillRoot, rel)
		recorded, known := receipt.Files[rel]
		switch {
		case err == nil && digest(disk) == digest(files[rel]):
		case errors.Is(err, fs.ErrNotExist) && !known:
			state.stale = append(state.stale, rel)
		case err == nil && known && digest(disk) == recorded:
			state.stale = append(state.stale, rel)
		case err == nil || errors.Is(err, fs.ErrNotExist):
			state.drift = append(state.drift, rel)
		default:
			return skillState{}, err
		}
	}
	return state, nil
}

// Managed paths are regular files: a symlink or FIFO in their place is a refusal, not a read.
func readRootFile(root *os.Root, name string) ([]byte, error) {
	return readRoot(root, name, maxConfig)
}

// Decide every link action before writing; a foreign real path is never replaced.
func planSkillLinks(dirRoot *os.Root, rels []string, replace bool) (map[string]string, error) {
	links := map[string]string{}
	for _, rel := range rels {
		info, err := dirRoot.Lstat(rel)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Existing ancestors must be directories, or MkdirAll would fail after the skill files are written.
			for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
				if info, err := dirRoot.Stat(parent); err == nil && !info.IsDir() {
					return nil, fmt.Errorf("%s существует и не является каталогом; ссылка %s не создаётся", parent, rel)
				} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return nil, err
				}
			}
			links[rel] = "created"
		case err != nil:
			return nil, err
		case info.Mode()&fs.ModeSymlink == 0:
			return nil, fmt.Errorf("%s существует и не является символической ссылкой; он не заменяется", rel)
		default:
			target, err := dirRoot.Readlink(rel)
			if err != nil {
				return nil, err
			}
			switch {
			case target == skillLinkTarget:
				links[rel] = "kept"
			case replace:
				links[rel] = "relinked"
			default:
				return nil, fmt.Errorf("%s указывает на другой skill; повторите с --replace, чтобы перелинковать", rel)
			}
		}
	}
	return links, nil
}

func writeSkill(dirRoot *os.Root, files map[string][]byte, write []string, unlink bool, current string) error {
	if len(write) == 0 {
		return nil
	}
	if unlink {
		if err := dirRoot.Remove(skillInstallDir); err != nil {
			return err
		}
	}
	if err := dirRoot.MkdirAll(skillInstallDir, 0755); err != nil {
		return err
	}
	skillRoot, err := dirRoot.OpenRoot(skillInstallDir)
	if err != nil {
		return err
	}
	defer skillRoot.Close()
	for _, rel := range write {
		if parent := path.Dir(rel); parent != "." {
			if err := skillRoot.MkdirAll(parent, 0755); err != nil {
				return err
			}
		}
		if err := atomicWrite(skillRoot, rel, files[rel], 0644); err != nil {
			return err
		}
	}
	return atomicWrite(skillRoot, skillReceiptName, skillReceipt(files, current), 0644)
}

func applySkillLinks(dirRoot *os.Root, links map[string]string) error {
	for _, rel := range sortedKeys(links) {
		switch links[rel] {
		case "kept":
			continue
		case "relinked":
			if err := dirRoot.Remove(rel); err != nil {
				return err
			}
		default:
			if err := dirRoot.MkdirAll(path.Dir(rel), 0755); err != nil {
				return err
			}
		}
		if err := dirRoot.Symlink(skillLinkTarget, rel); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
