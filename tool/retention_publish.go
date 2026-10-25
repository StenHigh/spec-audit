//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A citation-only publication starts in a fresh directory. In particular it
// cannot inherit full source resources from an older archive-mode website.
func publishWithRetention(out string, paths []string, controls ...*CorpusControlFile) (any, error) {
	citations, archives := false, false
	for _, path := range paths {
		cfg, err := loadConfig(path, true)
		if err != nil {
			return nil, err
		}
		if citationRetention(cfg) {
			citations = true
		} else {
			archives = true
		}
	}
	if !citations {
		return publish(out, paths, controls...)
	}
	if archives {
		return nil, errors.New("publish: citations и archive требуют отдельные сайты, чтобы полные источники не попали в результаты citations")
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	abs, err = canonicalPath(abs)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		cfg, err := loadConfig(path, true)
		if err != nil {
			return nil, err
		}
		if within(cfg.ReportsDir, abs) || within(abs, cfg.ReportsDir) || within(cfg.ProjectRoot, abs) || within(abs, cfg.ProjectRoot) {
			return nil, errors.New("publish: OUT пересекается с рабочей областью/исходниками")
		}
	}
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return nil, errors.New("publish citations: нужен новый отсутствующий OUT_DIR; прежняя публикация сохраняется")
	}
	if err := mkdirExact(filepath.Dir(abs), 0755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(abs), ".citation-site-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	value, err := publish(stage, paths, controls...)
	if err != nil {
		return nil, err
	}
	// Recheck before the publication point: a concurrently created destination
	// must not be silently replaced, even if it is an empty directory.
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return nil, errors.New("publish citations: OUT появился во время сборки")
	}
	if err := os.Rename(stage, abs); err != nil {
		return nil, fmt.Errorf("publish citations: %w", err)
	}
	result := value.(map[string]any)
	result["dir"], result["index"] = abs, filepath.Join(abs, "index.html")
	result["source_retention"] = "citations"
	return result, nil
}
