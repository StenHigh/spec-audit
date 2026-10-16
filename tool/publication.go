//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
)

type PublicationFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}
type PublicationSnapshot struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Files         []PublicationFile `json:"files"`
}

func publicationID(p PublicationSnapshot) string {
	p.ID = ""
	data, _ := json.Marshal(p)
	return digest(data)
}

// Retain each successful external publication independently of its hosting path.
// Later export/restore needs neither the host directory nor regenerated HTML.
func recordPublication(out string, paths []string) error {
	roots := []*os.Root{}
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	for _, path := range paths {
		cfg, err := loadConfig(path, true)
		if err != nil {
			return err
		}
		if cfg.External == nil {
			continue
		}
		root, err := os.OpenRoot(cfg.ReportsDir)
		if err != nil {
			return err
		}
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		return nil
	}
	p := PublicationSnapshot{SchemaVersion: 1, Files: []PublicationFile{}}
	if err := walkBundleArea(out, func(path string, data []byte, _ uint32) error {
		for _, root := range roots {
			if err := putObject(root, data); err != nil {
				return err
			}
		}
		p.Files = append(p.Files, PublicationFile{path, digest(data), len(data)})
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	p.ID = publicationID(p)
	for _, root := range roots {
		if err := root.MkdirAll("._publications", 0700); err != nil {
			return err
		}
		if err := noSymlinks(root, "._publications"); err != nil {
			return err
		}
		if err := writeJSON(root, "._publications/"+p.ID+".json", p, 0400); err != nil {
			return err
		}
	}
	return nil
}
func validatePublication(p PublicationSnapshot) error {
	if p.SchemaVersion != 1 || p.ID != publicationID(p) || !validDigest(p.ID) || len(p.Files) > 200000 {
		return errors.New("publication: неверная схема/основание")
	}
	seen := map[string]bool{}
	for _, file := range p.Files {
		if !localPath(file.Path) || seen[file.Path] || !validDigest(file.SHA256) || file.Bytes < 0 || file.Bytes > maxFile {
			return errors.New("publication: неверный файл")
		}
		seen[file.Path] = true
	}
	return nil
}
