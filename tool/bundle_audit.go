//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Determine only known derived files. Unknown files are retained, never inferred
// disposable from an extension or an unreferenced object name.
func auditBundleOmissions(dir string, reportRoots ...string) (map[string]bool, error) {
	excluded, err := nestedReportPaths(dir, reportRoots)
	if err != nil {
		return nil, err
	}
	omit := map[string]bool{}
	sourceObjects := map[string]bool{}
	publicationObjects := map[string]bool{}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || !slugRE.MatchString(e.Name()) || excluded[e.Name()] {
			continue
		}
		raw, err := readRoot(root, e.Name()+"/manifest.json", maxState)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var m Manifest
		if err := strictJSON(raw, &m); err != nil {
			return nil, err
		}
		run, err := root.OpenRoot(e.Name())
		if err != nil {
			return nil, err
		}
		inv, err := readSourceInventory(root, run, m)
		run.Close()
		if err != nil {
			return nil, err
		}
		var report Report
		raw, err = readRoot(root, e.Name()+"/report.json", maxState)
		if err == nil {
			if err := strictJSON(raw, &report); err != nil {
				return nil, err
			}
			if report.RunID != e.Name() || report.SnapshotID != m.SnapshotID {
				return nil, errors.New("bundle: неверное основание производного отчёта")
			}
			omit[e.Name()+"/report.html"] = true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		for _, s := range inv.Sources {
			if !s.Full {
				continue
			}
			sourceObjects[s.SHA256] = true
			key := e.Name() + "/sources/" + sourcePageKey(s)
			omit[key+".html"], omit[key+".bin"] = true, true
		}
	}
	err = fs.WalkDir(root.FS(), "._publications", func(path string, e fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && path == "._publications" {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if e.IsDir() {
			if excluded[path] {
				return fs.SkipDir
			}
			if path == "._publications" {
				return nil
			}
			// A directory may only provide the path to a selected child root.
			for child := range excluded {
				if strings.HasPrefix(child, path+"/") {
					return nil
				}
			}
			return errors.New("bundle: неизвестный каталог publications")
		}
		raw, err := readRoot(root, path, maxState)
		if err != nil {
			return err
		}
		var p PublicationSnapshot
		if err := strictJSON(raw, &p); err != nil {
			return err
		}
		if err := validatePublication(p); err != nil {
			return err
		}
		if path != "._publications/"+p.ID+".json" {
			return errors.New("bundle: имя publication не совпадает с ID")
		}
		omit[path] = true
		for _, f := range p.Files {
			object, err := readRoot(root, "._objects/"+f.SHA256, maxFile)
			if err != nil {
				return err
			}
			if digest(object) != f.SHA256 || len(object) != f.Bytes {
				return fmt.Errorf("bundle: повреждён объект публикации %s", p.ID)
			}
			publicationObjects[f.SHA256] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for hash := range publicationObjects {
		if !sourceObjects[hash] {
			omit["._objects/"+hash] = true
		}
	}
	return omit, nil
}

func auditBundlePathAllowed(area BundleArea, path string) bool {
	if area.Kind == "site" {
		return false
	}
	return area.Kind != "reports" || !strings.HasPrefix(filepath.ToSlash(path), "._publications/")
}

// Match the new export traversal during live capture and resumed cleanup.
// Schemas 1/2 retain their historical overlapping-area behavior.
func auditReportRoots(index BundleIndex) []string {
	if index.SchemaVersion < 3 {
		return nil
	}
	roots := []string{}
	for _, area := range index.Areas {
		if area.Kind == "reports" {
			roots = append(roots, area.OriginalRoot)
		}
	}
	return roots
}
