//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
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
