package main

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
)

// Promoted unchanged from the frozen extraction control: provenance, not semantic truth.
type legacyCandidate struct {
	ID         string     `json:"id"`
	Condition  string     `json:"condition"`
	Statement  string     `json:"statement"`
	Exceptions []string   `json:"exceptions"`
	Clarity    string     `json:"clarity"`
	Unresolved []string   `json:"unresolved"`
	Citations  []Citation `json:"citations"`
}

type legacySource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type legacyRaw struct {
	Version     int               `json:"version"`
	SourceSet   []legacySource    `json:"source_set"`
	Candidates  []legacyCandidate `json:"candidates"`
	Limitations []string          `json:"limitations"`
}

func legacyDecode(data []byte, out any) error {
	if len(data) > maxResult {
		return errors.New("ответ больше 4 MiB")
	}
	if err := strictJSON(data, out); err != nil {
		return err
	}
	return requiredJSON(data, reflect.TypeOf(out).Elem())
}

func legacyValidate(data []byte, sources map[string][]byte) (legacyRaw, error) {
	var raw legacyRaw
	if err := legacyDecode(data, &raw); err != nil {
		return raw, err
	}
	if err := legacyShape(raw); err != nil {
		return raw, err
	}
	if len(raw.SourceSet) != len(sources) {
		return raw, errors.New("неверная версия, набор источников или число кандидатов")
	}
	for _, source := range raw.SourceSet {
		content, ok := sources[source.Path]
		if !ok || digest(content) != source.SHA256 {
			return raw, errors.New("неверный путь, повторный источник или stale hash")
		}
	}
	for _, candidate := range raw.Candidates {
		for _, cite := range candidate.Citations {
			content := sources[cite.Path]
			quote, err := lineQuote(content, cite.LineStart, cite.LineEnd)
			if err != nil || quote != cite.Quote {
				return raw, errors.New("цитата не совпадает с выбранным источником")
			}
		}
	}
	return raw, nil
}

// History checks its own structure; old source bytes are not claimed to be available.
func legacyShape(raw legacyRaw) error {
	if raw.Version != 1 || len(raw.Candidates) > 64 {
		return errors.New("неверная версия или число кандидатов")
	}
	sources := map[string]bool{}
	shaPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, source := range raw.SourceSet {
		if !localPath(source.Path) || sources[source.Path] || !shaPattern.MatchString(source.SHA256) {
			return errors.New("неверный путь, повторный источник или hash")
		}
		sources[source.Path] = true
	}
	seen := map[string]bool{}
	idPattern := regexp.MustCompile(`^C[0-9]{3}$`)
	for _, candidate := range raw.Candidates {
		if !idPattern.MatchString(candidate.ID) || seen[candidate.ID] || strings.TrimSpace(candidate.Condition) == "" || strings.TrimSpace(candidate.Statement) == "" {
			return errors.New("неверный/повторный ID или пустая норма")
		}
		seen[candidate.ID] = true
		if (candidate.Clarity != "clear" && candidate.Clarity != "ambiguous") || (candidate.Clarity == "ambiguous") != (len(candidate.Unresolved) > 0) {
			return errors.New("clarity и unresolved не согласованы")
		}
		for _, list := range [][]string{candidate.Exceptions, candidate.Unresolved} {
			for _, value := range list {
				if strings.TrimSpace(value) == "" {
					return errors.New("пустое исключение или вопрос")
				}
			}
		}
		if len(candidate.Citations) < 1 || len(candidate.Citations) > 16 {
			return errors.New("нужно от 1 до 16 цитат")
		}
		for _, cite := range candidate.Citations {
			if !sources[cite.Path] || cite.LineStart < 1 || cite.LineEnd < cite.LineStart {
				return errors.New("недопустимая цитата")
			}
		}
	}
	return nil
}
