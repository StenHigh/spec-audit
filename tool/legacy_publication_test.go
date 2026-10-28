//go:build darwin || linux

package main

import (
	"os"
	"sort"
)

// Frozen legacy publication writer used only for migration fixtures.
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
