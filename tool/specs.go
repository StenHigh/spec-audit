package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

const markdownProfile = "requirements/1;goldmark/1.8.6;protocol/1"

var requirementID = regexp.MustCompile(`^REQ-[A-Z0-9]+-[0-9]{3,}$`)
var requirementHeading = regexp.MustCompile(`^(REQ-[A-Z0-9]+-[0-9]{3,}) — (.+)$`)

type Requirement struct {
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Condition    string           `json:"condition"`
	Statement    string           `json:"statement"`
	Verification string           `json:"verification"`
	ContentHash  string           `json:"content_hash"`
	Source       Citation         `json:"source"`
	Accepted     *AcceptedDetails `json:"accepted,omitempty"`
}

func (req Requirement) containsSource(citation Citation) bool {
	sources := []Citation{req.Source}
	if req.Accepted != nil {
		sources = req.Accepted.Citations
	}
	for _, source := range sources {
		if citation.Path == source.Path && citation.LineStart >= source.LineStart && citation.LineEnd <= source.LineEnd {
			return true
		}
	}
	return false
}

func parseRequirements(path string, source []byte) ([]Requirement, error) {
	result := []Requirement{}
	if !utf8.Valid(source) {
		return result, fmt.Errorf("Markdown не UTF-8: %s", path)
	}
	tree := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	lines := strings.Split(strings.TrimSuffix(string(source), "\n"), "\n")
	lineAt := func(pos int) int { return bytes.Count(source[:pos], []byte("\n")) + 1 }
	for node := tree.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok || heading.Lines().Len() != 1 {
			continue
		}
		segment := heading.Lines().At(0)
		// Like the core pilot: use raw source ranges, not Segment.Value's synthetic bytes.
		label := strings.TrimSpace(string(source[segment.Start:segment.Stop]))
		if !strings.HasPrefix(label, "REQ-") {
			continue
		}
		match := requirementHeading.FindStringSubmatch(label)
		if heading.Level != 3 || match == nil {
			return nil, fmt.Errorf("неверный заголовок требования: %s:%d", path, lineAt(segment.Start))
		}
		req := Requirement{ID: match[1], Title: match[2]}
		start, end := lineAt(segment.Start), len(lines)
		fields := map[string]*string{"Условие:": &req.Condition, "Требование:": &req.Statement, "Проверка:": &req.Verification}
		for sibling := node.NextSibling(); sibling != nil; sibling = sibling.NextSibling() {
			if next, ok := sibling.(*ast.Heading); ok && next.Level <= 3 {
				end = lineAt(next.Lines().At(0).Start) - 1
				break
			}
			paragraph, ok := sibling.(*ast.Paragraph)
			if !ok {
				continue
			} // fences, blockquotes, nested examples cannot declare fields.
			hasField, hasContinuation := false, false
			for i := 0; i < paragraph.Lines().Len(); i++ {
				part := paragraph.Lines().At(i)
				line := strings.TrimSuffix(strings.TrimSuffix(string(source[part.Start:part.Stop]), "\n"), "\r")
				matched := false
				for prefix, value := range fields {
					if !strings.HasPrefix(line, prefix) {
						continue
					}
					matched, hasField = true, true
					body := strings.TrimSpace(strings.TrimPrefix(line, prefix))
					if *value != "" || body == "" {
						return nil, fmt.Errorf("повторное/пустое поле %s в %s", prefix, req.ID)
					}
					*value = body
				}
				if !matched && strings.TrimSpace(line) != "" {
					hasContinuation = true
				}
			}
			if hasField && hasContinuation {
				return nil, fmt.Errorf("многострочное поле в %s; отделите пояснение пустой строкой", req.ID)
			}
		}
		for name, value := range fields {
			if *value == "" {
				return nil, fmt.Errorf("нет поля %s в %s", name, req.ID)
			}
		}
		for end > start && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		quote, err := lineQuote(source, start, end)
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal([]string{req.ID, req.Title, req.Condition, req.Statement, req.Verification})
		req.ContentHash, req.Source = digest(payload), Citation{path, start, end, quote}
		result = append(result, req)
	}
	return result, nil
}

func assignRequirements(m *Manifest) error {
	if len(m.Requirements) > 512 {
		return errors.New("не более 512 требований")
	}
	sort.Slice(m.Requirements, func(i, j int) bool { return m.Requirements[i].ID < m.Requirements[j].ID })
	ids := map[string]bool{}
	all := []string{}
	for _, req := range m.Requirements {
		if ids[req.ID] {
			return errors.New("повторный requirement ID")
		}
		ids[req.ID] = true
		all = append(all, req.ID)
	}
	if len(m.Config.Scopes) == 0 && len(all) > 0 {
		if len(all) > 64 {
			return errors.New("более 64 требований: задайте явные scope")
		}
		m.Config.Scopes = []Scope{{ID: "all", Focus: "Все объявленные требования", Requirements: all}}
	}
	assigned := map[string]bool{}
	for _, scope := range m.Config.Scopes {
		unknown, duplicate := []string{}, []string{}
		for _, id := range scope.Requirements {
			switch {
			case !ids[id]:
				unknown = append(unknown, id)
			case assigned[id]:
				duplicate = append(duplicate, id)
			default:
				assigned[id] = true
			}
		}
		if len(unknown) > 0 || len(duplicate) > 0 {
			// tool-spec §22.2: after an acceptance that changed IDs, name the scope and the IDs so the host can fix `scopes`.
			return fmt.Errorf("scope %s: неизвестные ID %v; повторно назначенные %v", scope.ID, idList(unknown), idList(duplicate))
		}
	}
	if len(assigned) != len(ids) {
		unassigned := []string{}
		for _, id := range all {
			if !assigned[id] {
				unassigned = append(unassigned, id)
			}
		}
		return fmt.Errorf("не распределены по scope: %v", idList(unassigned))
	}
	return nil
}

// idList keeps diagnostics readable: sorted, at most scopeErrorIDs entries.
const scopeErrorIDs = 64

func idList(ids []string) []string {
	sort.Strings(ids)
	if len(ids) > scopeErrorIDs {
		ids = append(ids[:scopeErrorIDs:scopeErrorIDs], "…")
	}
	return ids
}

func initConfig(path string) error {
	// O_EXCL rejects an existing file AND a dangling symlink. Never mkdir in the audited tree.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(defaultConfig)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// scopeAdvisoryRequirements — рекомендуемый максимум норм на scope (tool-spec 20.1). Основание: 18 норм на 50 файлов
// прошли сравнительный run без затруднений, 49 норм на 2509 файлов заняли 27–36 минут и 0,5–0,6 M токенов на роль;
// 64 остаётся жёстким пределом §6. Эвристика по двум замерам, не измеренная граница качества.
const scopeAdvisoryRequirements = 24

// scopeAdvisories names every scope above the recommendation; the binary never splits norms itself (§6).
func scopeAdvisories(m Manifest) []string {
	advisories := []string{}
	for _, scope := range m.Config.Scopes {
		if len(scope.Requirements) > scopeAdvisoryRequirements {
			advisories = append(advisories, fmt.Sprintf("scope %s: %d норм, %d файлов; §6: слишком большой scope требует осознанного деления (рекомендация ≤ %d норм)",
				scope.ID, len(scope.Requirements), len(m.Files), scopeAdvisoryRequirements))
			slog.Warn("scope содержит много норм", "scope", scope.ID, "requirements", len(scope.Requirements), "files", len(m.Files), "advisory", scopeAdvisoryRequirements)
		}
	}
	return advisories
}

func indexConfig(cfg Config) (any, error) {
	m, err := snapshot(cfg)
	if err != nil {
		return nil, err
	}
	basis := "declared_requirements_only"
	if m.Accepted != nil {
		basis = "accepted_requirements_only"
	}
	index := map[string]any{"snapshot_id": m.SnapshotID, "profile": m.Profile, "requirements": m.Requirements, "files": m.Files,
		"completeness_basis": basis, "semantic_completeness_proven": false, "accepted": m.Accepted,
		"project_root": filepath.Clean(cfg.ProjectRoot)}
	if m.Accepted != nil {
		// tool-spec §26.1: snapshot refuses a stale accepted index, so a successful index is fresh by construction.
		index["freshness"] = "fresh"
	}
	// tool-spec §28.1: the list is always present, empty when nothing is advised.
	index["advisories"] = append(scopeAdvisories(m), anchorAdvisories(cfg.ProjectRoot, m.Files, requirementAnchorSources(m.Requirements))...)
	return index, nil
}

// tool-spec §26.2: a norm that names a section anchor (A-NNN, §N.N…) the snapshot's spec files never define may rest on
// a section outside scope. The check is a reading aid: mentions in prose do not count as definitions.
var (
	anchorRE        = regexp.MustCompile(`\bA-\d{2,4}\b|§\s?\d+(?:\.\d+)*[A-Za-zА-Яа-я]?`)
	anchorDefinedRE = regexp.MustCompile(`^\s*(?:#{1,6}\s+|[*-]\s+|\|\s*)?(?:\*\*)?(A-\d{2,4}|\d+(?:\.\d+)*[A-Za-zА-Яа-я]?)(?:\*\*)?(?:[\s:.)|*]|$)`)
)

type anchorSource struct {
	ID    string
	Texts []string
}

func requirementAnchorSources(reqs []Requirement) []anchorSource {
	sources := []anchorSource{}
	for _, req := range reqs {
		texts := []string{req.Condition, req.Statement, req.Verification}
		if req.Accepted != nil {
			texts = append(append(texts, req.Accepted.Exceptions...), req.Accepted.Unresolved...)
		}
		sources = append(sources, anchorSource{req.ID, texts})
	}
	return sources
}

func candidateAnchorSources(candidates []legacyCandidate) []anchorSource {
	sources := []anchorSource{}
	for _, candidate := range candidates {
		sources = append(sources, anchorSource{candidate.ID, append(append([]string{candidate.Condition, candidate.Statement}, candidate.Exceptions...), candidate.Unresolved...)})
	}
	return sources
}

// anchorKey normalizes a matched anchor: A-NNN stays as is, §1.6.10A becomes 1.6.10A.
func anchorKey(anchor string) string {
	return strings.TrimSpace(strings.TrimPrefix(anchor, "§"))
}

func anchorAdvisories(projectRoot string, files []SourceFile, sources []anchorSource) []string {
	named := map[string]bool{}
	for _, source := range sources {
		for _, text := range source.Texts {
			for _, anchor := range anchorRE.FindAllString(text, -1) {
				named[anchorKey(anchor)] = true
			}
		}
	}
	if len(named) == 0 {
		return []string{}
	}
	defined := map[string]bool{}
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		slog.Debug("index: spec-файл пропущен", "path", projectRoot, "error", err.Error())
		return []string{}
	}
	defer root.Close()
	for _, file := range files {
		if file.Kind != "spec" {
			continue
		}
		data, err := readRoot(root, file.Path, maxFile)
		if err != nil {
			slog.Debug("index: spec-файл пропущен", "path", file.Path, "error", err.Error())
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if match := anchorDefinedRE.FindStringSubmatch(line); match != nil && named[match[1]] {
				defined[match[1]] = true
			}
		}
	}
	advisories := []string{}
	total := 0
	for _, source := range sources {
		missing := []string{}
		seen := map[string]bool{}
		for _, text := range source.Texts {
			for _, anchor := range anchorRE.FindAllString(text, -1) {
				key := anchorKey(anchor)
				if defined[key] || seen[key] {
					continue
				}
				seen[key] = true
				missing = append(missing, strings.TrimSpace(anchor))
			}
		}
		if len(missing) > 0 {
			total += len(missing)
			advisories = append(advisories, fmt.Sprintf("%s: якоря %s не определены в spec-файлах snapshot (source_set+references); норма может опираться на раздел вне scope", source.ID, strings.Join(missing, ", ")))
		}
	}
	if len(advisories) > 0 {
		slog.Debug("index: якоря вне snapshot", "requirements", len(advisories), "anchors", total)
	}
	return advisories
}

const defaultConfig = `version: 1
project_root: .
specs:
  paths: [specs]
  include: ['*.md']
code:
  paths: [src]
  exclude: ['*_test.go']
tests:
  paths: [tests]
reports_dir: .spec-audit/reports
runtime:
  kind: none
  timeout_seconds: 30
scopes: []
`
