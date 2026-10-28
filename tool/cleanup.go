//go:build darwin || linux

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type CleanupCandidate struct {
	Area      string   `json:"area"`
	Path      string   `json:"path"`
	SHA256    string   `json:"sha256"`
	Bytes     int64    `json:"bytes"`
	Class     string   `json:"class"`
	Reason    string   `json:"reason"`
	Consumers []string `json:"consumers"`
	Retained  string   `json:"retained"`
}
type CleanupPlan struct {
	PublicationBasis *PublicationCleanupBasis `json:"publication_basis,omitempty"`
	SchemaVersion    int                      `json:"schema_version"`
	ID               string                   `json:"id"`
	BundleID         string                   `json:"bundle_id"`
	ArchiveSHA256    string                   `json:"archive_sha256"`
	ToolVersion      string                   `json:"tool_version"`
	Candidates       []CleanupCandidate       `json:"candidates"`
	BytesBefore      int64                    `json:"bytes_before"`
	BytesAfter       int64                    `json:"bytes_after"`
	FreedBytes       int64                    `json:"expected_freed_bytes"`
}
type CleanupProof struct {
	SchemaVersion  int      `json:"schema_version"`
	PlanID         string   `json:"plan_id"`
	BundleID       string   `json:"bundle_id"`
	ArchiveSHA256  string   `json:"archive_sha256"`
	ToolVersion    string   `json:"tool_version"`
	VerifiedFiles  int      `json:"verified_files"`
	ConfigsChecked int      `json:"configs_checked"`
	Limitations    []string `json:"limitations"`
}

// PLAN includes the full live basis and can be larger than any individual state file.
// Keep this bound shared by schema dispatch, readers and the exact CLI output check.
const maxCleanupPlan = 256 << 20

type cleanupPlanSizeWriter struct {
	bytes int64
	limit int64
}

func (w *cleanupPlanSizeWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.limit-w.bytes {
		slog.Debug("[FIX:cleanup-plan] превышен лимит PLAN", "limit", w.limit)
		return 0, fmt.Errorf("cleanup: PLAN превышает лимит %d байт", w.limit)
	}
	w.bytes += int64(len(data))
	return len(data), nil
}

// Match main's Encoder, including indentation, HTML escaping and trailing LF.
func checkCleanupPlanSize(p CleanupPlan, limit int64) error {
	output := &cleanupPlanSizeWriter{limit: limit}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(p); err != nil {
		return err
	}
	slog.Debug("[FIX:cleanup-plan] размер PLAN проверен", "bytes", output.bytes, "limit", limit)
	return nil
}

func cleanupPlanID(p CleanupPlan) string { p.ID = ""; data, _ := json.Marshal(p); return digest(data) }

func cleanupCommand(args []string) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("cleanup plan ARCHIVE | verify ARCHIVE PLAN | apply ARCHIVE PLAN PROOF")
	}
	b, err := readBundle(args[1])
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	for _, area := range b.Index.Areas {
		if area.Kind == "reports" {
			dirs = append(dirs, area.OriginalRoot)
		}
	}
	unlock, err := lockWorkspaces(dirs, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if args[0] == "plan" && len(args) > 2 {
		return publicationCleanupCommand(args, b)
	}
	if args[0] != "plan" && len(args) >= 3 {
		raw, e := readExact(args[2], maxCleanupPlan)
		if e != nil {
			return nil, e
		}
		var header struct {
			SchemaVersion int `json:"schema_version"`
		}
		if e := json.Unmarshal(raw, &header); e != nil {
			return nil, e
		}
		if header.SchemaVersion == 2 {
			return publicationCleanupCommand(args, b)
		}
	}
	if len(args) == 2 && args[0] == "plan" {
		if err := compareCleanupBasis(b.Index, nil); err != nil {
			return nil, err
		}
		p, err := makeCleanupPlan(b)
		if err != nil {
			return nil, err
		}
		if err := checkCleanupPlanSize(p, maxCleanupPlan); err != nil {
			return nil, err
		}
		return p, nil
	}
	if len(args) < 3 {
		return nil, errors.New("cleanup: нужен PLAN")
	}
	raw, err := readExact(args[2], maxCleanupPlan)
	if err != nil {
		return nil, err
	}
	var p CleanupPlan
	if err := strictJSON(raw, &p); err != nil {
		return nil, err
	}
	expected, err := makeCleanupPlan(b)
	if err != nil {
		return nil, err
	}
	if p.SchemaVersion != 1 || p.ID != cleanupPlanID(p) || !reflect.DeepEqual(p, expected) {
		return nil, errors.New("cleanup: изменённый/чужой план или версия инструмента")
	}
	if err := compareCleanupBasis(b.Index, p.Candidates); err != nil {
		return nil, err
	}
	proof, err := proveBundleRestore(b, p)
	if err != nil {
		return nil, err
	}
	if len(args) == 3 && args[0] == "verify" {
		return proof, nil
	}
	if len(args) != 4 || args[0] != "apply" {
		return nil, errors.New("cleanup apply ARCHIVE PLAN PROOF")
	}
	raw, err = readExact(args[3], maxState)
	if err != nil {
		return nil, err
	}
	var supplied CleanupProof
	if err := strictJSON(raw, &supplied); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(supplied, proof) {
		return nil, errors.New("cleanup: нет подтверждённого восстановления точного плана")
	}
	// Restore proof precedes the final reread; a new reference or file invalidates it.
	if err := compareCleanupBasis(b.Index, p.Candidates); err != nil {
		return nil, err
	}
	removed := []string{}
	skipped := []string{}
	var freed int64
	finish := func(err error) (any, error) {
		problems := []string{}
		if err != nil {
			problems = append(problems, err.Error())
		}
		return map[string]any{"schema_version": 1, "plan_id": p.ID, "bundle_id": p.BundleID, "archive_sha256": b.SHA256, "removed": removed, "skipped": skipped, "freed_bytes": freed, "restore_proof": proof, "complete": err == nil, "errors": problems}, err
	}
	areas := map[string]BundleArea{}
	for _, area := range b.Index.Areas {
		areas[area.ID] = area
	}
	for _, candidate := range p.Candidates {
		if err := processContext.Err(); err != nil {
			return finish(err)
		}
		area := areas[candidate.Area]
		if area.Kind != "reports" {
			return finish(errors.New("cleanup: кандидат вне reports"))
		}
		root, err := os.OpenRoot(area.OriginalRoot)
		if err != nil {
			return finish(err)
		}
		data, err := readRoot(root, candidate.Path, maxFile)
		if os.IsNotExist(err) {
			root.Close()
			skipped = append(skipped, candidate.Area+"/"+candidate.Path)
			continue
		}
		if err != nil {
			root.Close()
			return finish(err)
		}
		if digest(data) != candidate.SHA256 || int64(len(data)) != candidate.Bytes {
			root.Close()
			return finish(errors.New("cleanup: кандидат изменился перед удалением"))
		}
		if err := root.Remove(candidate.Path); err != nil {
			root.Close()
			return finish(err)
		}
		root.Close()
		removed = append(removed, candidate.Area+"/"+candidate.Path)
		freed += candidate.Bytes
	}
	return finish(nil)
}

func makeCleanupPlan(b evidenceBundle) (CleanupPlan, error) {
	p := CleanupPlan{SchemaVersion: 1, BundleID: b.Index.ID, ArchiveSHA256: b.SHA256, ToolVersion: version, Candidates: []CleanupCandidate{}}
	areas := map[string]BundleArea{}
	for _, a := range b.Index.Areas {
		areas[a.ID] = a
	}
	files := map[string]BundleFile{}
	for _, f := range b.Index.Files {
		files[f.Area+"/"+f.Path] = f
		p.BytesBefore += f.Bytes
	}
	for _, f := range b.Index.Files {
		if areas[f.Area].Kind != "reports" {
			continue
		}
		parts := strings.Split(f.Path, "/")
		// Only a delivered task's optional scratch result is a known disposable duplicate.
		if len(parts) != 4 || parts[1] != "dispatch" || parts[3] != "result.json" {
			continue
		}
		stateFile, ok := files[f.Area+"/"+parts[0]+"/state.json"]
		if !ok {
			continue
		}
		var state State
		if strictJSON(b.Objects[stateFile.SHA256], &state) != nil {
			continue
		}
		complete := true
		for _, e := range state.Entries {
			if e.Result == nil {
				complete = false
			}
		}
		if !complete {
			continue
		}
		reportFile, ok := files[f.Area+"/"+parts[0]+"/report.json"]
		if !ok {
			continue
		}
		var report Report
		if strictJSON(b.Objects[reportFile.SHA256], &report) != nil || !report.HostReviewComplete {
			continue
		}
		mf, ok := files[f.Area+"/"+parts[0]+"/manifest.json"]
		if !ok {
			continue
		}
		jf, ok := files[f.Area+"/"+parts[0]+"/host-reviews.json"]
		if !ok {
			continue
		}
		var m Manifest
		var journal reviewJournal
		if strictJSON(b.Objects[mf.SHA256], &m) != nil || strictJSON(b.Objects[jf.SHA256], &journal) != nil {
			continue
		}
		valid := true
		for _, raw := range journal.Records {
			if _, err := validateReview([]byte(raw), parts[0], m, nil, false); err != nil {
				valid = false
			}
		}
		if !valid || !summarizeReviews(parts[0], m, state, false, journal).Complete {
			continue
		}
		for _, entry := range state.Entries {
			if entry.Task.TaskID != parts[2] || entry.RawSHA256 != f.SHA256 {
				continue
			}
			retained := fmt.Sprintf("%s/results/%s-attempt-%d.json", parts[0], entry.Task.TaskID, entry.Task.Attempt)
			raw, ok := files[f.Area+"/"+retained]
			if !ok || raw.SHA256 != f.SHA256 {
				continue
			}
			p.Candidates = append(p.Candidates, CleanupCandidate{f.Area, f.Path, f.SHA256, f.Bytes, "byte_duplicate", "Побайтовый дубль сданного raw завершённого run; оригинал и пакет сохранены", []string{parts[0] + "/state.json", parts[0] + "/host-reviews.json"}, retained})
		}
	}
	var err error
	p.Candidates, err = protectCleanupPhysicalFiles(b, nil, p.Candidates)
	if err != nil {
		return CleanupPlan{}, err
	}
	for _, candidate := range p.Candidates {
		p.FreedBytes += candidate.Bytes
	}
	p.BytesAfter = p.BytesBefore - p.FreedBytes
	p.ID = cleanupPlanID(p)
	return p, nil
}

func compareCleanupBasis(index BundleIndex, allowed []CleanupCandidate) error {
	request := BundleRequest{SchemaVersion: 1, Configs: []string{}, Sites: []string{}}
	if index.SchemaVersion == 3 {
		request.SchemaVersion = 2
	}
	cfgs := []Config{}
	for _, c := range index.Configs {
		request.Configs = append(request.Configs, c.OriginalPath)
		cfg, err := loadConfig(c.OriginalPath, true)
		if err != nil {
			return err
		}
		cfgs = append(cfgs, cfg)
	}
	for _, a := range index.Areas {
		if a.Kind == "site" && a.OriginalRoot != "" {
			request.Sites = append(request.Sites, a.OriginalRoot)
		}
	}
	current, err := collectBundle(request, cfgs)
	if err != nil {
		return err
	}
	missingAllowed := map[string]bool{}
	for _, c := range allowed {
		missingAllowed[c.Area+"/"+c.Path] = true
	}
	present := map[string]bool{}
	for _, f := range current.Index.Files {
		present[f.Area+"/"+f.Path] = true
	}
	expected := index
	expected.Files = []BundleFile{}
	for _, f := range index.Files {
		key := f.Area + "/" + f.Path
		if !present[key] && missingAllowed[key] {
			continue
		}
		expected.Files = append(expected.Files, f)
	}
	expected.ID = ""
	current.Index.ID = ""
	a, _ := json.Marshal(expected)
	c, _ := json.Marshal(current.Index)
	if !bytes.Equal(a, c) {
		return errors.New("cleanup: исходное состояние, ссылка или зависимость изменились; нужен новый пакет и план")
	}
	return nil
}

func proveBundleRestore(b evidenceBundle, p CleanupPlan) (CleanupProof, error) {
	proof := CleanupProof{1, p.ID, b.Index.ID, b.SHA256, version, len(b.Index.Files), 0, append([]string{}, b.Index.Limitations...)}
	temp, err := os.MkdirTemp("", "spec-audit-restore-proof-")
	if err != nil {
		return proof, err
	}
	defer os.RemoveAll(temp)
	if err := materializeBundle(b, temp, temp); err != nil {
		return proof, err
	}
	for _, f := range b.Index.Files {
		data, err := readExact(filepath.Join(temp, "areas", f.Area, f.Path), maxFile)
		if err != nil {
			return proof, err
		}
		if digest(data) != f.SHA256 || int64(len(data)) != f.Bytes {
			return proof, errors.New("cleanup: независимое восстановление bytes не прошло")
		}
	}
	if err := validateRestoredState(b, temp); err != nil {
		return proof, err
	}
	for _, c := range b.Index.Configs {
		cfg, err := loadConfig(filepath.Join(temp, "configs", c.ID+".yaml"), true)
		if err != nil {
			return proof, err
		}
		if cfg.IndexMode == "accepted" {
			if _, _, err := readAccepted(cfg.ReportsDir); err != nil {
				return proof, err
			}
		}
		if citationRetention(cfg) {
			// A citations package preserves validated history, not a checkout for analysis.
			proof.ConfigsChecked++
			continue
		}
		// Fresh current sources must remain usable; a preexisting stale/absent basis is
		// carried as an explicit limitation, never repaired by the restore proof.
		original, err := loadConfig(c.OriginalPath, true)
		if err != nil {
			return proof, err
		}
		beforeScan, beforeScanErr := scanSnapshot(original)
		afterScan, afterScanErr := scanSnapshot(cfg)
		if beforeScanErr == nil && (afterScanErr != nil || !reflect.DeepEqual(beforeScan.Files, afterScan.Files)) {
			return proof, errors.New("cleanup: потеряно исходное дерево при восстановлении")
		}
		before, beforeErr := snapshot(original)
		after, afterErr := snapshot(cfg)
		if beforeErr == nil && (afterErr != nil || before.SnapshotID != after.SnapshotID) {
			return proof, errors.New("cleanup: восстановленный snapshot/индекс не пригоден к продолжению")
		}
		if beforeErr == nil && len(before.Requirements) > 0 {
			configPath := filepath.Join(temp, "configs", c.ID+".yaml")
			if _, err := execute([]string{"prepare", configPath, "restore-proof-" + rand.Text()}); err != nil {
				return proof, err
			}
		}
		proof.ConfigsChecked++
	}
	return proof, nil
}
