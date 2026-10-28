//go:build darwin || linux

package main

import (
	"encoding/json"
	"os"
	"path"
)

// This is evidence of a saved run's identity, never validation of its contents.
// Keep a damaged audit distinct from unrelated metadata at a store root.
func savedRunEvidence(root *os.Root, dir string, manifest []byte) (bool, error) {
	if auditJSONFields(manifest, "snapshot_id") || auditJSONFields(manifest, "config", "files") {
		return true, nil
	}
	// A generated site also contains report.json. It cannot identify a run.
	for _, name := range []string{"state.json", "sources.json", toolVersionsFile} {
		raw, err := readRoot(root, path.Join(dir, name), maxState)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		switch name {
		case "state.json":
			if auditJSONFields(raw, "entries", "executions") {
				return true, nil
			}
		case "sources.json":
			var inv sourceInventory
			if strictJSON(raw, &inv) == nil && (inv.SchemaVersion == 1 || inv.SchemaVersion == 2) && validDigest(inv.SnapshotID) {
				return true, nil
			}
		case toolVersionsFile:
			var journal toolVersionJournal
			if strictJSON(raw, &journal) == nil && journal.Version == 1 && len(journal.Records) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

func auditJSONFields(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil {
		return false
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}
