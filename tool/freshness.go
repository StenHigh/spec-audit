//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

const savedSnapshotConclusion = "Сохранённое решение действительно по снимку этого run; текущая актуальность не проверена. Само отсутствие источников не требует нового аудита."

type sourceObservation struct {
	Freshness    string
	Availability string
	Reason       string
}

// Legacy callers with an explicit comparison retain their existing behavior.
func observationFor(m Manifest, fresh bool) sourceObservation {
	if m.observation != nil {
		return *m.observation
	}
	if fresh {
		return sourceObservation{"fresh", "available", "Текущее основание совпадает со снимком run."}
	}
	return sourceObservation{"stale", "available", "Текущее основание отличается от снимка run."}
}

func observeSnapshot(cfg Config, saved, current Manifest, snapshotErr error) sourceObservation {
	o := sourceObservation{"fresh", "available", "Текущее основание совпадает со снимком run."}
	if snapshotErr == nil {
		if current.SnapshotID != saved.SnapshotID {
			o.Freshness, o.Reason = "stale", "Обнаружено изменение источников или основания снимка."
		}
	} else {
		o = sourceObservation{"unchecked", "unavailable", fmt.Sprintf("Текущая актуальность не проверена: %v", snapshotErr)}
		if releasedSnapshotSources(cfg) {
			o.Availability, o.Reason = "released", "Временные источники штатно освобождены; текущая актуальность не проверена."
		}
		changed := errors.Is(snapshotErr, errRuntimeSourcesChanged)
		proof := ""
		change := func(reason string) {
			changed = true
			if proof == "" {
				proof = reason
			}
		}
		if changed {
			proof = "runtime code/tests"
		}
		previous := map[string]SourceFile{}
		for _, f := range saved.Files {
			previous[f.Path] = f
		}
		// scanSnapshot records a hash before parsing Markdown. A parse failure
		// after different bytes were read is still positive change evidence.
		for _, f := range current.Files {
			if was, ok := previous[f.Path]; !ok || was != f {
				change("байтов или классификации файла " + f.Path)
			}
		}
		if root, err := os.OpenRoot(cfg.ProjectRoot); err == nil {
			available := true
			for _, f := range saved.Files {
				if noSymlinks(root, f.Path) != nil {
					available = false
					continue
				}
				data, err := readRoot(root, f.Path, maxFile)
				if err != nil {
					available = false
					continue
				}
				if len(data) != f.Bytes || digest(data) != f.SHA256 {
					change("байтов файла " + f.Path)
				}
			}
			root.Close()
			if cfg.External != nil && runtimeSourceRoot(cfg) != cfg.ProjectRoot {
				if err := runtimeSourcesCurrent(cfg); err != nil && !errors.Is(err, errRuntimeSourcesChanged) {
					available = false
				}
			}
			if available {
				o.Availability = "available"
			}
		}
		if saved.Accepted != nil && current.Accepted != nil && saved.Accepted.Head != current.Accepted.Head {
			change("принятого индекса")
		}
		a, b := saved.Config, cfg
		if len(b.Scopes) == 0 {
			projected := Manifest{Config: b, Requirements: append([]Requirement{}, saved.Requirements...)}
			if assignRequirements(&projected) == nil {
				b = projected.Config
			}
		}
		a.ProjectRoot, a.ReportsDir, b.ProjectRoot, b.ReportsDir = "", "", "", ""
		oldConfig, oldErr := configObservationBasis(a)
		newConfig, newErr := configObservationBasis(b)
		if oldErr == nil && newErr == nil && !bytes.Equal(oldConfig, newConfig) {
			change("основания CONFIG")
		}
		if cfg.External != nil && runtimeSourceRoot(cfg) != cfg.ProjectRoot && errors.Is(runtimeSourcesCurrent(cfg), errRuntimeSourcesChanged) {
			change("runtime code/tests")
		}
		if changed {
			o.Freshness, o.Reason = "stale", fmt.Sprintf("Обнаружено изменение %s; полная сверка не завершена: %v", proof, snapshotErr)
		}
	}
	slog.Debug("snapshot: наблюдение источников", "freshness", o.Freshness, "source_availability", o.Availability, "reason", o.Reason)
	return o
}

// JSON drops runtime-only bindings; YAML treats nil/empty source lists alike,
// as a restored CONFIG does. Encoding differences are not configuration changes.
func configObservationBasis(cfg Config) ([]byte, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var basis Config
	if err := json.Unmarshal(raw, &basis); err != nil {
		return nil, err
	}
	return yaml.Marshal(basis)
}

func releasedSnapshotSources(cfg Config) bool {
	if !citationRetention(cfg) || !managedExternalRoot(cfg) {
		return false
	}
	raw, err := readExact(filepath.Join(filepath.Dir(cfg.ProjectRoot), "release.json"), maxConfig)
	var receipt externalReleaseReceipt
	if err != nil || strictJSON(raw, &receipt) != nil || receipt.SchemaVersion != 1 || !receipt.Complete {
		return false
	}
	x, _, err := savedExternalInput(cfg)
	if err != nil || receipt.ManifestHash != digest(x.manifestRaw) || receipt.CodeHash != codeDigest(x.code) {
		return false
	}
	for _, name := range []string{"tree", "corpus"} {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(cfg.ProjectRoot), name)); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func observedExecutions(receipts []Receipt, snapshotID, freshness string) []Receipt {
	result := append([]Receipt{}, receipts...)
	for i := range result {
		if freshness == "stale" || result[i].SnapshotID != snapshotID {
			result[i].State = "stale"
		}
	}
	return result
}
