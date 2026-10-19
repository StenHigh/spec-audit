//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
)

const retentionFile = "._source-retention.json"

type retentionPolicy struct {
	SchemaVersion int    `json:"schema_version"`
	Retention     string `json:"source_retention"`
}

func citationRetention(cfg Config) bool { return cfg.SourceRetention == "citations" }

// A citations workspace starts empty. Old archives are never migrated by changing
// CONFIG: they may have source bytes in publications or historical objects.
func validateRetention(cfg Config) error {
	root, err := os.OpenRoot(cfg.ReportsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := readRoot(root, retentionFile, maxConfig)
	if err == nil {
		var policy retentionPolicy
		if strictJSON(raw, &policy) != nil || policy.SchemaVersion != 1 || !oneOf(policy.Retention, "citations", "archive") {
			return errors.New("source_retention: повреждена политика рабочей области")
		}
		if citationRetention(cfg) != (policy.Retention == "citations") {
			return errors.New("source_retention: рабочая область закреплена за " + policy.Retention + "; смена политики запрещена")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if !citationRetention(cfg) {
		return nil
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if transientBundleFile(entry.Name()) && !entry.IsDir() {
			continue
		}
		return errors.New("source_retention: citations требует новый пустой reports_dir; прежние архивы не преобразуются")
	}
	return nil
}

func ensureRetention(cfg Config) error {
	root, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := root.OpenFile(".source-retention.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := validateRetention(cfg); err != nil {
		return err
	}
	if _, err := readRoot(root, retentionFile, maxConfig); err == nil {
		return nil
	}
	policy := "archive"
	if citationRetention(cfg) {
		policy = "citations"
	}
	slog.Debug("retention: закрепление политики", "source_retention", policy)
	return writeJSON(root, retentionFile, retentionPolicy{1, policy}, 0400)
}

// Metadata is retained even after release. This verifies provenance without
// claiming to verify absent source bytes or their current freshness.
func savedExternalInput(cfg Config) (externalInput, []SourceFile, error) {
	var x externalInput
	if cfg.External == nil || !managedExternalRoot(cfg) {
		return x, nil, errors.New("external: CONFIG не принадлежит управляемому входу")
	}
	dir := filepath.Dir(cfg.External.Input)
	var err error
	x.raw, err = readExact(cfg.External.Input, maxResult)
	if err != nil {
		return x, nil, err
	}
	x.request, err = parseAuditInput(x.raw)
	if err != nil {
		return x, nil, err
	}
	x.manifestRaw, err = readExact(filepath.Join(dir, "source-manifest.json"), maxState)
	if err != nil {
		return x, nil, err
	}
	x.manifest, err = parseExternalManifest(x.manifestRaw)
	if err != nil {
		return x, nil, err
	}
	x.profileRaw, err = readExact(filepath.Join(dir, "profile.json"), maxConfig)
	if err != nil {
		return x, nil, err
	}
	m := x.manifest
	if digest(x.manifestRaw) != x.request.ManifestSHA256 || digest(x.profileRaw) != m.Profile.SHA256 || m.Project != cfg.External.Project || m.Corpus != cfg.External.Corpus || !reflect.DeepEqual(m.Line, cfg.External.Line) || m.Profile.ID != cfg.External.ProfileID || m.Profile.Version != cfg.External.ProfileVersion {
		return x, nil, errors.New("external metadata: hash/идентичность/profile изменились")
	}
	codeRaw, err := readExact(filepath.Join(dir, "code-files.json"), maxState)
	if err != nil {
		return x, nil, err
	}
	if err := strictJSON(codeRaw, &x.code); err != nil {
		return x, nil, err
	}
	if !validDigest(cfg.External.CodeSHA256) || codeDigest(x.code) != cfg.External.CodeSHA256 {
		return x, nil, errors.New("external metadata: изменена опись code/tests")
	}
	seen := map[string]bool{}
	for _, file := range x.code {
		if !localPath(file.Path) || !validDigest(file.SHA256) || file.Bytes < 0 || file.Bytes > maxFile || !oneOf(file.Kind, "code", "tests") || file.Reference || seen[file.Path] {
			return x, nil, errors.New("external metadata: неверный файл code/tests")
		}
		seen[file.Path] = true
	}
	for _, file := range m.Files {
		if seen[file.Path] {
			return x, nil, errors.New("external metadata: пересечение corpus/code")
		}
	}
	// Snapshot basis binds code bytes through codeDigest; Git basis was checked
	// against the authorized checkout at import and is retained as provenance.
	var basis struct {
		Kind   string `json:"kind"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(x.request.CodeBasis, &basis); err != nil {
		return x, nil, err
	}
	if basis.Kind == "snapshot" && basis.SHA256 != codeDigest(x.code) {
		return x, nil, errors.New("external metadata: code_basis не совпадает с описью")
	}
	x.scope = cfg.External.Scope
	expected := append(externalSourceFiles(m), x.code...)
	sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
	return x, expected, nil
}

func validateExternalMetadata(cfg Config) error {
	if cfg.External == nil {
		return nil
	}
	configs, err := retainedInputConfigs(cfg)
	if err != nil {
		return err
	}
	for _, c := range configs {
		if _, _, err := savedExternalInput(c); err != nil {
			return err
		}
	}
	return nil
}

type externalReleaseReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	ManifestHash  string `json:"manifest_sha256"`
	CodeHash      string `json:"code_sha256"`
	Complete      bool   `json:"complete"`
}

// All input versions in one workspace are owned by external import; their
// configs and metadata remain after release so historical revisions can be read.
func retainedInputConfigs(cfg Config) ([]Config, error) {
	root, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), "._inputs")
	if os.IsNotExist(err) && cfg.External == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	configs := []Config{}
	for _, entry := range entries {
		if !entry.IsDir() || !validDigest(entry.Name()) {
			return nil, fmt.Errorf("external: неизвестный временный вход %s; автоматическое удаление запрещено", entry.Name())
		}
		path := filepath.Join(cfg.ReportsDir, "._inputs", entry.Name(), "config.yaml")
		children, err := fs.ReadDir(root.FS(), "._inputs/"+entry.Name())
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if oneOf(child.Name(), "tree", "corpus") || strings.HasPrefix(child.Name(), ".rehydrate-") && child.IsDir() {
				continue
			}
			if child.Type().IsRegular() && oneOf(child.Name(), "source-manifest.json", "profile.json", "audit-input.json", "code-files.json", "original-config.yaml", "config.yaml", "release.json") {
				continue
			}
			return nil, fmt.Errorf("external: неизвестный файл временного входа %s/%s; удаление запрещено", entry.Name(), child.Name())
		}
		raw, err := readExact(path, maxConfig)
		if err != nil {
			return nil, err
		}
		var c Config
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			return nil, err
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return nil, errors.New("external: нужен один YAML-документ")
		}
		if c.External != nil && c.External.Input == "audit-input.json" {
			c.External.Input = filepath.Join(filepath.Dir(c.ProjectRoot), "audit-input.json")
		}
		if c.Version != 1 || !citationRetention(c) || !managedExternalRoot(c) || filepath.Base(filepath.Dir(c.ProjectRoot)) != entry.Name() {
			return nil, errors.New("external: чужая привязка временного входа")
		}
		if cfg.External != nil && !reflect.DeepEqual(externalScopeIdentity(c.External), externalScopeIdentity(cfg.External)) {
			return nil, errors.New("external: чужой project/corpus/scope")
		}
		// Original YAML is immutable provenance. Bind a copy to this restored
		// reports area without inspecting or depending on its original paths.
		c.ReportsDir = cfg.ReportsDir
		c.ProjectRoot = filepath.Join(filepath.Dir(path), "tree")
		c.External.Input = filepath.Join(filepath.Dir(path), "audit-input.json")
		c.External.CodeRoot = c.ProjectRoot
		configs = append(configs, c)
	}
	return configs, nil
}

// Reimport rehydrates only the two known temporary directories from a fully
// verified producer input. Retained metadata/results are never replaced.
func rehydrateExternal(cfg Config, x externalInput, codeData map[string][]byte) error {
	if _, _, err := savedExternalInput(cfg); err != nil {
		return err
	}
	parent := filepath.Dir(cfg.ProjectRoot)
	missing := []string{}
	for _, name := range []string{"tree", "corpus"} {
		_, err := os.Lstat(filepath.Join(parent, name))
		if os.IsNotExist(err) {
			missing = append(missing, name)
		} else if err != nil {
			return err
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// If a directory survived an interrupted release, verify all remaining
	// bytes and finish release first; a partial tree must never become current.
	if len(missing) != 2 {
		return errors.New("external import: освобождение прервано; завершите external release CONFIG")
	}
	stage, err := os.MkdirTemp(parent, ".rehydrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"tree", "corpus"} {
		if err := root.MkdirAll(name, 0700); err != nil {
			return err
		}
		files := map[string][]byte{}
		for path, data := range x.files {
			files[path] = data
		}
		if name == "tree" {
			for path, data := range codeData {
				files[path] = data
			}
		}
		for path, data := range files {
			if err := root.MkdirAll(filepath.Join(name, filepath.Dir(path)), 0700); err != nil {
				return err
			}
			if err := atomicWrite(root, filepath.Join(name, path), data, 0400); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"tree", "corpus"} {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(parent, name)); err != nil {
			return err
		}
	}
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer parentRoot.Close()
	if err := parentRoot.Remove("release.json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	slog.Debug("external: временные исходники восстановлены", "input", filepath.Base(parent))
	return nil
}

func releasedExternalInputs(cfg Config) error {
	if !citationRetention(cfg) {
		return nil
	}
	configs, err := retainedInputConfigs(cfg)
	if err != nil {
		return err
	}
	for _, c := range configs {
		if err := validateExternalMetadata(c); err != nil {
			return err
		}
		for _, name := range []string{"tree", "corpus"} {
			_, err := os.Lstat(filepath.Join(filepath.Dir(c.ProjectRoot), name))
			if !os.IsNotExist(err) {
				return errors.New("citations: временные исходники ещё доступны; выполните external release CONFIG перед bundle export")
			}
		}
		children, err := os.ReadDir(filepath.Dir(c.ProjectRoot))
		if err != nil {
			return err
		}
		for _, child := range children {
			if strings.HasPrefix(child.Name(), ".rehydrate-") {
				return errors.New("citations: прерванный import; выполните external release CONFIG")
			}
		}
	}
	return nil
}

// Verify the exact managed slice before deleting it. After an interrupted
// release the receipt permits missing files, but never altered or extra bytes.
func checkTemporarySources(cfg Config, x externalInput, partial bool) error {
	dir := filepath.Dir(cfg.ProjectRoot)
	for _, name := range []string{"tree", "corpus"} {
		expected := map[string]SourceFile{}
		for _, file := range x.manifest.Files {
			expected[file.Path] = SourceFile{Path: file.Path, SHA256: file.SHA256, Bytes: int(file.Size)}
		}
		if name == "tree" {
			for _, file := range x.code {
				expected[file.Path] = file
			}
		}
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) && partial {
			continue
		}
		guard, rel, err := openExact(path)
		if err != nil {
			return err
		}
		root, err := guard.OpenRoot(rel)
		guard.Close()
		if err != nil {
			return err
		}
		allowedDirs := map[string]bool{".": true}
		for p := range expected {
			for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
				allowedDirs[parent] = true
			}
		}
		err = fs.WalkDir(root.FS(), ".", func(p string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() && allowedDirs[p] {
				return nil
			}
			if !entry.Type().IsRegular() || entry.IsDir() {
				return fmt.Errorf("external release: неизвестный путь/симлинк %s/%s", name, p)
			}
			file, ok := expected[p]
			data, err := readRoot(root, p, maxFile)
			if err != nil {
				return err
			}
			if !ok || digest(data) != file.SHA256 || len(data) != file.Bytes {
				return fmt.Errorf("external release: неизвестный или изменённый %s/%s", name, p)
			}
			delete(expected, p)
			return nil
		})
		root.Close()
		if err != nil {
			return err
		}
		if len(expected) != 0 && !partial {
			return fmt.Errorf("external release: неполный %s", name)
		}
	}
	return nil
}

func releasableRun(reports *os.Root, runID string, m Manifest, cfg Config) error {
	run, err := reports.OpenRoot(runID)
	if err != nil {
		return err
	}
	defer run.Close()
	if _, err := readToolVersions(run); err != nil {
		return err
	}
	inv, err := readSourceInventory(reports, run, m)
	if err != nil {
		return err
	}
	if err := validateExternalRunMetadata(cfg, m, inv); err != nil {
		return err
	}
	raw, err := readRoot(run, "state.json", maxState)
	if err != nil {
		return err
	}
	var state State
	if err := strictJSON(raw, &state); err != nil {
		return err
	}
	expected := newState(m)
	if m.Incremental != nil && m.Incremental.Grouping == "" && !sameTaskIDs(state, expected) {
		expected = legacyIncrementalState(m)
	}
	if len(state.Entries) != len(expected.Entries) {
		return errors.New("повреждено число заданий")
	}
	for i, entry := range state.Entries {
		expected.Entries[i].Task.Attempt = entry.Task.Attempt
		if entry.Task.Attempt < 1 || !reflect.DeepEqual(entry.Task, expected.Entries[i].Task) || entry.Result == nil {
			return errors.New("требуется доставка всех заданий")
		}
		if err := validateResult(*entry.Result, entry.Task, m, nil); err != nil {
			return err
		}
		if !validDigest(entry.RawSHA256) {
			return errors.New("отсутствует hash raw")
		}
		data, err := readRoot(run, fmt.Sprintf("results/%s-attempt-%d.json", entry.Task.TaskID, entry.Task.Attempt), maxResult)
		var result Result
		if err != nil || digest(data) != entry.RawSHA256 || strictJSON(data, &result) != nil || !reflect.DeepEqual(result, *entry.Result) {
			return errors.New("повреждены raw/hash/state")
		}
	}
	if err := verifySDKRecords(run, state.SDK); err != nil {
		return err
	}
	reviews, err := readReviews(run, runID, m)
	if err != nil {
		return err
	}
	summary := summarizeReviews(runID, m, state, false, reviews)
	if !summary.Complete {
		return errors.New("требуется полное решение хоста")
	}
	report := makeReport(runID, m, state, false)
	report.HostReview = &summary
	check, err := checkForReport(run, report)
	if err != nil {
		return err
	}
	if check == nil || check.Conclusion != "confirmed" {
		return errors.New("требуется подтверждённая независимая review-check")
	}
	return nil
}

func externalRelease(config string) (any, error) {
	cfg, err := loadConfig(config, true)
	if err != nil {
		return nil, err
	}
	if !citationRetention(cfg) || !managedExternalRoot(cfg) {
		return nil, errors.New("external release: требуется импортированный CONFIG с source_retention: citations")
	}
	unlock, err := lockWorkspaces([]string{cfg.ReportsDir}, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	configs, err := retainedInputConfigs(cfg)
	if err != nil {
		return nil, err
	}
	reports, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	defer reports.Close()
	entries, err := fs.ReadDir(reports.FS(), ".")
	if err != nil {
		return nil, err
	}
	runs := 0
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		raw, err := readRoot(reports, entry.Name()+"/manifest.json", maxState)
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
		if !citationRetention(m.Config) || m.External == nil || m.Config.External == nil || !reflect.DeepEqual(externalScopeIdentity(m.Config.External), externalScopeIdentity(cfg.External)) {
			return nil, errors.New("external release: чужой run или режим хранения")
		}
		if err := releasableRun(reports, entry.Name(), m, cfg); err != nil {
			return nil, fmt.Errorf("external release: run %s: %w", entry.Name(), err)
		}
		runs++
	}
	if runs == 0 {
		return nil, errors.New("external release: нет завершённого run")
	}
	type pendingRelease struct {
		cfg     Config
		receipt externalReleaseReceipt
		stages  []string
	}
	pending := []pendingRelease{}
	for _, c := range configs {
		x, _, err := savedExternalInput(c)
		if err != nil {
			return nil, err
		}
		receipt := externalReleaseReceipt{1, digest(x.manifestRaw), codeDigest(x.code), false}
		raw, readErr := readExact(filepath.Join(filepath.Dir(c.ProjectRoot), "release.json"), maxConfig)
		partial := readErr == nil
		if partial {
			var prior externalReleaseReceipt
			if strictJSON(raw, &prior) != nil || prior.SchemaVersion != 1 || prior.ManifestHash != receipt.ManifestHash || prior.CodeHash != receipt.CodeHash {
				return nil, errors.New("external release: повреждено основание прерванного удаления")
			}
		} else if !os.IsNotExist(readErr) {
			return nil, readErr
		}
		if err := checkTemporarySources(c, x, partial); err != nil {
			return nil, err
		}
		stages := []string{}
		children, err := os.ReadDir(filepath.Dir(c.ProjectRoot))
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if !strings.HasPrefix(child.Name(), ".rehydrate-") {
				continue
			}
			stage := filepath.Join(filepath.Dir(c.ProjectRoot), child.Name())
			guard, rel, err := openExact(stage)
			if err != nil {
				return nil, err
			}
			stageRoot, err := guard.OpenRoot(rel)
			guard.Close()
			if err != nil {
				return nil, err
			}
			files, err := fs.ReadDir(stageRoot.FS(), ".")
			stageRoot.Close()
			if err != nil {
				return nil, err
			}
			for _, file := range files {
				if !oneOf(file.Name(), "tree", "corpus") || !file.IsDir() {
					return nil, errors.New("external release: неизвестный staging; удаление запрещено")
				}
			}
			staged := c
			staged.ProjectRoot = filepath.Join(stage, "tree")
			if err := checkTemporarySources(staged, x, true); err != nil {
				return nil, err
			}
			stages = append(stages, child.Name())
		}
		pending = append(pending, pendingRelease{c, receipt, stages})
	}
	// Every preflight finishes before the first removal.
	for _, p := range pending {
		root, err := os.OpenRoot(filepath.Dir(p.cfg.ProjectRoot))
		if err != nil {
			return nil, err
		}
		err = writeJSON(root, "release.json", p.receipt, 0400)
		if err == nil {
			err = root.RemoveAll("tree")
		}
		if err == nil {
			err = root.RemoveAll("corpus")
		}
		for _, stage := range p.stages {
			if err == nil {
				err = root.RemoveAll(stage)
			}
		}
		if err == nil {
			p.receipt.Complete = true
			err = writeJSON(root, "release.json", p.receipt, 0400)
		}
		root.Close()
		if err != nil {
			return nil, fmt.Errorf("external release: повторите для завершения: %w", err)
		}
	}
	slog.Debug("external: временные исходники освобождены", "inputs", len(pending), "runs", runs)
	return map[string]any{"schema_version": 2, "source_retention": "citations", "released": len(pending), "runs": runs, "complete": true}, nil
}

// Historical metadata remains an exact dependency even when source bytes were
// deliberately released. Remap physical paths without rewriting the saved run.
func validateExternalRunMetadata(restored Config, m Manifest, inv sourceInventory) error {
	if m.Config.External == nil {
		return errors.New("external metadata: внешний run без CONFIG external")
	}
	cfg := m.Config
	ext := *cfg.External
	// Input is intentionally omitted from persisted manifest JSON.
	ext.Input = filepath.Join(filepath.Dir(cfg.ProjectRoot), "audit-input.json")
	cfg.External = &ext
	if !managedExternalRoot(cfg) {
		return errors.New("external metadata: неверная привязка исторического external run")
	}
	key := filepath.Base(filepath.Dir(cfg.ProjectRoot))
	cfg.ReportsDir = restored.ReportsDir
	cfg.ProjectRoot = filepath.Join(cfg.ReportsDir, "._inputs", key, "tree")
	ext.Input = filepath.Join(filepath.Dir(cfg.ProjectRoot), "audit-input.json")
	ext.CodeRoot = cfg.ProjectRoot
	cfg.External = &ext
	x, expected, err := savedExternalInput(cfg)
	if err != nil {
		return err
	}
	actual := append([]SourceFile{}, m.Files...)
	sort.Slice(actual, func(i, j int) bool { return actual[i].Path < actual[j].Path })
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("external metadata: metadata не совпадают с файлами исторического run")
	}
	// RawMessage JSON gains indentation in the saved manifest; compare its
	// canonical encoding, while InputSHA256 still pins the original request bytes.
	actualBasis, err := json.Marshal(m.External)
	if err != nil {
		return err
	}
	expectedBasis, err := json.Marshal(externalBasis(x))
	if err != nil {
		return err
	}
	if !bytes.Equal(actualBasis, expectedBasis) {
		return errors.New("external metadata: metadata не совпадают с происхождением исторического run")
	}
	sources := map[string]SavedSource{}
	for _, source := range inv.Sources {
		sources[source.Path] = source
	}
	archives := 0
	for _, file := range x.manifest.Files {
		if file.Role != "archive" {
			continue
		}
		archives++
		source, ok := sources[file.Path]
		if !ok || source.Kind != "archive" || source.SHA256 != file.SHA256 || int64(source.Bytes) != file.Size {
			return errors.New("external metadata: опись archive не совпадает с исходным манифестом")
		}
	}
	names := []string{"source-manifest.json", "profile.json", "audit-input.json", "code-files.json", "original-config.yaml"}
	if len(inv.Sources) != len(m.Files)+archives+len(names) {
		return errors.New("external metadata: неполный или лишний состав описи")
	}
	for _, name := range names {
		source, ok := sources["_metadata/"+name]
		data, err := readExact(filepath.Join(filepath.Dir(cfg.ProjectRoot), name), maxFile)
		if err != nil {
			return err
		}
		if !ok || source.Kind != "metadata" || source.SHA256 != digest(data) || source.Bytes != len(data) {
			return fmt.Errorf("external metadata: исходные bytes %s изменились относительно описи run", name)
		}
	}
	return nil
}
