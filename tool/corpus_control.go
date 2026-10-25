//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// CorpusControlFile is caller-selected presentation metadata, never run evidence.
type CorpusControlFile struct {
	SchemaVersion int             `json:"schema_version"`
	Controls      []CorpusControl `json:"controls"`
}

type CorpusControl struct {
	Project     string  `json:"project"`
	Corpus      string  `json:"corpus"`
	Line        *string `json:"line"`
	SpecVersion string  `json:"spec_version"`
}

type CorpusRevision struct {
	Status         string `json:"status"`
	AuditedVersion string `json:"audited_version,omitempty"`
	ControlVersion string `json:"control_version,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Reason         string `json:"reason"`
}

// Parse options separately so workspace locking sees only CONFIG paths.
func corpusArguments(args []string) ([]string, string, error) {
	paths := []string{}
	control := ""
	options := true
	for i := 0; i < len(args); i++ {
		switch {
		case options && args[i] == "--":
			options = false
		case options && args[i] == "--corpus-control":
			if control != "" || i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				return nil, "", errors.New("--corpus-control: нужен один FILE")
			}
			i++
			control = args[i]
		case options && strings.HasPrefix(args[i], "--"):
			return nil, "", errors.New("неизвестный параметр карты корпуса: " + args[i])
		default:
			paths = append(paths, args[i])
		}
	}
	if len(paths) == 0 {
		return nil, "", errors.New("карта корпуса: нужен хотя бы один CONFIG")
	}
	return paths, control, nil
}

func readCorpusControl(path string) (*CorpusControlFile, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := readExact(path, maxConfig)
	if err != nil {
		return nil, fmt.Errorf("corpus-control: %w", err)
	}
	var control CorpusControlFile
	if err := strictJSON(raw, &control); err != nil {
		return nil, fmt.Errorf("corpus-control: %w", err)
	}
	if err := requiredKeys(raw, "schema_version", "controls"); err != nil {
		return nil, fmt.Errorf("corpus-control: %w", err)
	}
	if control.SchemaVersion != 1 || len(control.Controls) == 0 || len(control.Controls) > 1024 {
		return nil, errors.New("corpus-control: нужны schema_version=1 и 1–1024 controls")
	}
	var entries []json.RawMessage
	_ = json.Unmarshal(rawField(raw, "controls"), &entries)
	seen := map[string]bool{}
	for i, c := range control.Controls {
		if err := requiredKeys(entries[i], "project", "corpus", "line", "spec_version"); err != nil {
			return nil, fmt.Errorf("corpus-control.controls[%d]: %w", i, err)
		}
		if !nonempty(c.Project, c.Corpus, c.SpecVersion) || (c.Line != nil && !nonempty(*c.Line)) {
			return nil, fmt.Errorf("corpus-control.controls[%d]: пустая идентичность или spec_version", i)
		}
		identity, _ := json.Marshal(struct {
			Project string
			Corpus  string
			Line    *string
		}{c.Project, c.Corpus, c.Line})
		if seen[string(identity)] {
			return nil, errors.New("corpus-control: повтор контекста project/corpus/line")
		}
		seen[string(identity)] = true
	}
	return &control, nil
}

func sameCorpusContext(c CorpusControl, b ExternalBasis) bool {
	return c.Project == b.Project && c.Corpus == b.Corpus &&
		((c.Line == nil && b.Line == nil) || (c.Line != nil && b.Line != nil && *c.Line == *b.Line))
}

func compareCorpusRevision(scope ScopeOverview, control *CorpusControlFile) *CorpusRevision {
	if control == nil {
		return nil
	}
	r := &CorpusRevision{Status: "incomparable", Reason: "Нет завершённого решения с внешней версией корпуса."}
	d := scope.Decided
	if d == nil {
		return r
	}
	r.RunID = d.RunID
	if d.External == nil {
		return r
	}
	b := *d.External
	r.AuditedVersion = b.SpecVersion
	if !nonempty(b.Project, b.Corpus, b.SpecVersion) || (b.Line != nil && !nonempty(*b.Line)) {
		r.Reason = "Недостаточно сведений о контексте или версии основания аудита."
		return r
	}
	r.Reason = "Контрольная редакция для контекста этого аудита не задана."
	for _, c := range control.Controls {
		if !sameCorpusContext(c, b) {
			continue
		}
		r.ControlVersion = c.SpecVersion
		if !d.HostReviewComplete {
			r.Reason = "Основание решения изменилось; полного решения по текущим свидетельствам run нет."
			return r
		}
		r.Status, r.Reason = "not_checked", "Решение относится к другой редакции; проверка на контрольной редакции не выполнена. Различие не доказывает влияние правки."
		if b.SpecVersion == c.SpecVersion {
			r.Status, r.Reason = "checked", "Версия основания решения совпадает с выбранной контрольной редакцией. Это не проверка текущего кода."
		}
		break
	}
	slog.Debug("corpus: сравнение редакции", "scope", scope.Name, "run_id", r.RunID, "status", r.Status)
	return r
}
