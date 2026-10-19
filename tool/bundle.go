//go:build darwin || linux

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

const maxBundleBytes = int64(1 << 30)

type BundleRequest struct {
	SchemaVersion int      `json:"schema_version"`
	Configs       []string `json:"configs"`
	Sites         []string `json:"sites"`
}
type BundleArea struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	OriginalRoot string `json:"original_root"`
}
type BundleFile struct {
	Area   string `json:"area"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Mode   uint32 `json:"mode"`
}
type BundleConfig struct {
	ID           string   `json:"id"`
	OriginalPath string   `json:"original_path"`
	Config       Config   `json:"config"`
	Input        string   `json:"external_input,omitempty"`
	Related      []string `json:"related"`
	BaselineRun  string   `json:"baseline_run,omitempty"`
}
type BundleIndex struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	ToolVersion   string         `json:"tool_version"`
	Areas         []BundleArea   `json:"areas"`
	Files         []BundleFile   `json:"files"`
	Configs       []BundleConfig `json:"configs"`
	Limitations   []string       `json:"limitations"`
}
type evidenceBundle struct {
	Index   BundleIndex
	Objects map[string][]byte
	SHA256  string
}

func bundleID(index BundleIndex) string {
	index.ID = ""
	data, _ := json.Marshal(index)
	return digest(data)
}
func bundleCommand(args []string) (any, error) {
	if len(args) == 3 && args[0] == "export" {
		return exportBundle(args[1], args[2])
	}
	if len(args) == 2 && args[0] == "verify" {
		bundle, err := readBundle(args[1])
		if err != nil {
			return nil, err
		}
		if err := verifyBundleState(bundle); err != nil {
			return nil, err
		}
		return bundleSummary(bundle), nil
	}
	if len(args) == 3 && args[0] == "restore" {
		bundle, err := readBundle(args[1])
		if err != nil {
			return nil, err
		}
		return restoreBundle(bundle, args[2])
	}
	return nil, errors.New("bundle export REQUEST OUT.tar.gz | verify ARCHIVE | restore ARCHIVE NEW_DIR")
}
func bundleSummary(b evidenceBundle) map[string]any {
	return map[string]any{"schema_version": b.Index.SchemaVersion, "bundle_id": b.Index.ID, "archive_sha256": b.SHA256, "files": len(b.Index.Files), "objects": len(b.Objects), "limitations": b.Index.Limitations}
}

func readBundleRequest(path string) (BundleRequest, error) {
	var r BundleRequest
	data, err := readExact(path, maxConfig)
	if err != nil {
		return r, err
	}
	if err := strictJSON(data, &r); err != nil {
		return r, err
	}
	if err := requiredKeys(data, "schema_version", "configs", "sites"); err != nil {
		return r, err
	}
	if r.SchemaVersion != 1 || len(r.Configs) == 0 || len(r.Configs) > 64 || len(r.Sites) > 64 {
		return r, errors.New("bundle request: версия/число configs/sites недопустимы")
	}
	seen := map[string]bool{}
	for _, p := range append(append([]string{}, r.Configs...), r.Sites...) {
		if !filepath.IsAbs(p) || seen[p] {
			return r, errors.New("bundle request: нужны уникальные абсолютные пути")
		}
		seen[p] = true
	}
	return r, nil
}

func exportBundle(request, out string) (any, error) {
	r, err := readBundleRequest(request)
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	cfgs := []Config{}
	for _, path := range r.Configs {
		cfg, err := loadConfig(path, true)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, cfg.ReportsDir)
		cfgs = append(cfgs, cfg)
	}
	unlock, err := lockWorkspaces(dirs, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	absOut, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	for _, cfg := range cfgs {
		if within(cfg.ReportsDir, absOut) || within(cfg.ProjectRoot, absOut) {
			return nil, errors.New("bundle: OUT должен быть вне исходников и reports_dir")
		}
	}
	for _, site := range r.Sites {
		if within(site, absOut) {
			return nil, errors.New("bundle: OUT должен быть вне sites")
		}
	}
	b, err := collectBundle(r, cfgs)
	if err != nil {
		return nil, err
	}
	if err := verifyBundleState(b); err != nil {
		return nil, err
	}
	if err := writeBundle(out, b); err != nil {
		return nil, err
	}
	verified, err := readBundle(out)
	if err != nil {
		return nil, err
	}
	result := bundleSummary(verified)
	result["archive"] = out
	return result, nil
}

func collectBundle(request BundleRequest, cfgs []Config) (evidenceBundle, error) {
	b := evidenceBundle{Index: BundleIndex{SchemaVersion: 1, ToolVersion: version, Areas: []BundleArea{}, Files: []BundleFile{}, Configs: []BundleConfig{}, Limitations: []string{}}, Objects: map[string][]byte{}}
	var total int64
	logicalFiles := map[string]BundleFile{}
	add := func(area, path string, data []byte, mode uint32) error {
		hash := digest(data)
		key := area + "/" + path
		entry := BundleFile{area, path, hash, int64(len(data)), mode}
		if prior, ok := logicalFiles[key]; ok {
			if prior != entry {
				return errors.New("bundle: общий файл изменился при чтении")
			}
			return nil
		}
		logicalFiles[key] = entry
		if _, ok := b.Objects[hash]; !ok {
			total += int64(len(data))
			if total > maxBundleBytes {
				return errors.New("bundle превышает 1 GiB уникальных bytes")
			}
			b.Objects[hash] = data
		}
		if len(b.Index.Files) >= 200000 {
			return errors.New("bundle превышает 200000 файлов")
		}
		b.Index.Files = append(b.Index.Files, BundleFile{area, path, hash, int64(len(data)), mode})
		return nil
	}
	roots := map[string]string{}
	addArea := func(kind, dir string) (string, error) {
		canonical, err := canonicalPath(dir)
		if err != nil {
			return "", err
		}
		if id, ok := roots[kind+":"+canonical]; ok {
			return id, nil
		}
		guard, _, err := openExact(canonical)
		if err != nil {
			return "", err
		}
		guard.Close()
		id := kind + "-" + digest([]byte(canonical))[:16]
		roots[kind+":"+canonical] = id
		b.Index.Areas = append(b.Index.Areas, BundleArea{id, kind, canonical})
		return id, nil
	}
	configsKnown := map[string]bool{}
	for _, p := range request.Configs {
		configsKnown[filepath.Clean(p)] = true
	}
	b.Index.Areas = append(b.Index.Areas, BundleArea{"configs", "config", ""})
	for i, cfg := range cfgs {
		if err := validateRetention(cfg); err != nil {
			return b, err
		}
		if citationRetention(cfg) {
			b.Index.SchemaVersion = 2
			if err := releasedExternalInputs(cfg); err != nil {
				return b, err
			}
			if cfg.External != nil {
				if err := validateExternalMetadata(cfg); err != nil {
					return b, err
				}
			}
		}
		original := request.Configs[i]
		id := "scope-" + digest([]byte(original))[:16]
		for _, related := range cfg.Related {
			if !configsKnown[related] {
				return b, fmt.Errorf("bundle: related CONFIG не включён: %s", related)
			}
		}
		for _, area := range b.Index.Areas {
			if area.Kind == "reports" && area.OriginalRoot == cfg.ReportsDir {
				return b, errors.New("bundle: разные CONFIG одной reports_dir не поддерживаются; выберите её владельца")
			}
		}
		reportsID, err := addArea("reports", cfg.ReportsDir)
		if err != nil {
			return b, err
		}
		if err := walkBundleArea(cfg.ReportsDir, func(path string, data []byte, mode uint32) error { return add(reportsID, path, data, mode) }); err != nil {
			return b, err
		}
		if !citationRetention(cfg) {
			workspaceID, err := addArea("workspace", cfg.ProjectRoot)
			if err != nil {
				if !os.IsNotExist(err) || cfg.External != nil {
					return b, err
				}
				b.Index.Limitations = append(b.Index.Limitations, id+": текущее дерево исходников отсутствует; доступна только сохранённая история")
				workspaceID = ""
			}
			if workspaceID != "" && cfg.External != nil {
				if _, err := scanSnapshot(cfg); err != nil {
					return b, err
				}
				if err := walkBundleArea(cfg.ProjectRoot, func(path string, data []byte, mode uint32) error { return add(workspaceID, path, data, mode) }); err != nil {
					return b, err
				}
			} else if workspaceID != "" {
				current, err := scanSnapshotWithoutAcceptance(cfg)
				if err != nil {
					return b, err
				}
				for _, file := range current.Files {
					data, err := readExact(filepath.Join(cfg.ProjectRoot, file.Path), maxFile)
					if err != nil {
						return b, err
					}
					info, err := os.Stat(filepath.Join(cfg.ProjectRoot, file.Path))
					if err != nil {
						return b, err
					}
					if err := add(workspaceID, file.Path, data, uint32(info.Mode().Perm())); err != nil {
						return b, err
					}
				}
			}
		}
		raw, err := readExact(original, maxConfig)
		if err != nil {
			return b, err
		}
		if err := add("configs", id+".yaml", raw, 0600); err != nil {
			return b, err
		}
		input := ""
		if cfg.External != nil {
			input = cfg.External.Input
		}
		b.Index.Configs = append(b.Index.Configs, BundleConfig{id, original, cfg, input, cfg.Related, cfg.BaselineRun})
		root, err := os.OpenRoot(cfg.ReportsDir)
		if err != nil {
			return b, err
		}
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil {
			root.Close()
			return b, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			run, err := root.OpenRoot(entry.Name())
			if err != nil {
				root.Close()
				return b, err
			}
			raw, readErr := readRoot(run, "manifest.json", maxState)
			if os.IsNotExist(readErr) {
				run.Close()
				continue
			}
			var m Manifest
			if readErr != nil {
				run.Close()
				root.Close()
				return b, readErr
			}
			if err := strictJSON(raw, &m); err != nil {
				run.Close()
				root.Close()
				return b, err
			}
			inv, err := readSourceInventory(root, run, m)
			run.Close()
			if err != nil {
				root.Close()
				return b, err
			}
			for _, source := range inv.Sources {
				if !source.Full {
					b.Index.Limitations = append(b.Index.Limitations, id+"/"+entry.Name()+": "+source.Path+": "+source.Limitation)
				}
			}
		}
		root.Close()
	}
	// Saved publication events survive removal or relocation of the hosting directory.
	published := map[string]bool{}
	retained := append([]BundleFile{}, b.Index.Files...)
	for _, file := range retained {
		if !strings.HasPrefix(file.Path, "._publications/") {
			continue
		}
		var publication PublicationSnapshot
		if err := strictJSON(b.Objects[file.SHA256], &publication); err != nil {
			return b, err
		}
		if err := validatePublication(publication); err != nil {
			return b, err
		}
		if published[publication.ID] {
			continue
		}
		published[publication.ID] = true
		areaID := "publication-" + publication.ID
		b.Index.Areas = append(b.Index.Areas, BundleArea{areaID, "site", ""})
		for _, source := range publication.Files {
			var object []byte
			found := false
			for _, candidate := range retained {
				if candidate.Area == file.Area && candidate.Path == "._objects/"+source.SHA256 {
					object = b.Objects[candidate.SHA256]
					found = true
					break
				}
			}
			if !found || len(object) != source.Bytes || digest(object) != source.SHA256 {
				return b, errors.New("bundle: потеряна зависимость сохранённой публикации")
			}
			if err := add(areaID, source.Path, object, 0644); err != nil {
				return b, err
			}
		}
	}
	for _, site := range request.Sites {
		for _, area := range b.Index.Areas {
			if area.OriginalRoot != "" && (within(area.OriginalRoot, site) || within(site, area.OriginalRoot)) {
				return b, errors.New("bundle: site пересекается с рабочей областью")
			}
		}
		area, err := addArea("site", site)
		if err != nil {
			return b, err
		}
		if err := walkBundleArea(site, func(path string, data []byte, mode uint32) error { return add(area, path, data, mode) }); err != nil {
			return b, err
		}
	}
	sort.Slice(b.Index.Files, func(i, j int) bool {
		a, c := b.Index.Files[i], b.Index.Files[j]
		return a.Area+"/"+a.Path < c.Area+"/"+c.Path
	})
	sort.Slice(b.Index.Areas, func(i, j int) bool { return b.Index.Areas[i].ID < b.Index.Areas[j].ID })
	b.Index.ID = bundleID(b.Index)
	return b, nil
}

func scanSnapshotWithoutAcceptance(cfg Config) (Manifest, error) { return scanSnapshot(cfg) }
func transientBundleFile(path string) bool {
	name := filepath.Base(path)
	return name == ".workspace.lock" || strings.HasSuffix(name, ".lock") && strings.HasPrefix(name, ".")
}
func walkBundleArea(dir string, add func(string, []byte, uint32) error) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle: symlink %s", path)
		}
		if e.IsDir() {
			return nil
		}
		if !e.Type().IsRegular() {
			return errors.New("bundle: необычный файл")
		}
		if transientBundleFile(path) {
			return nil
		}
		data, err := readRoot(root, path, maxFile)
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		return add(path, data, uint32(info.Mode().Perm()))
	})
}

func writeBundle(out string, b evidenceBundle) error {
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	parent := filepath.Dir(abs)
	guard, _, err := openExact(parent)
	if err != nil {
		return err
	}
	guard.Close()
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return errors.New("bundle: OUT должен отсутствовать")
	}
	f, err := os.CreateTemp(parent, ".bundle-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	index, err := json.MarshalIndent(b.Index, "", "  ")
	if err != nil {
		f.Close()
		return err
	}
	if len(index) > maxState {
		f.Close()
		return errors.New("bundle index превышает 32 MiB")
	}
	if err := write("index.json", append(index, '\n')); err != nil {
		f.Close()
		return err
	}
	hashes := []string{}
	for hash := range b.Objects {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	for _, hash := range hashes {
		if err := processContext.Err(); err != nil {
			f.Close()
			return err
		}
		if err := write("objects/"+hash, b.Objects[hash]); err != nil {
			f.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), abs); err != nil {
		return err
	}
	return nil
}

func readBundle(path string) (evidenceBundle, error) {
	var b evidenceBundle
	raw, err := readExact(path, maxBundleBytes)
	if err != nil {
		return b, err
	}
	b.SHA256 = digest(raw)
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return b, err
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxBundleBytes+maxState+1))
	b.Objects = map[string][]byte{}
	seen := map[string]bool{}
	var total int64
	for {
		if err := processContext.Err(); err != nil {
			return b, err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return b, err
		}
		if header.Typeflag != tar.TypeReg || seen[header.Name] || header.Size < 0 || header.Size > maxFile {
			return b, errors.New("bundle: недопустимый/повторный объект архива")
		}
		seen[header.Name] = true
		data, err := readLimited(tr, maxFile)
		if err != nil {
			return b, err
		}
		total += int64(len(data))
		if total > maxBundleBytes {
			return b, errors.New("bundle: превышен лимит bytes")
		}
		if header.Name == "index.json" {
			if err := strictJSON(data, &b.Index); err != nil {
				return b, err
			}
			continue
		}
		hash := strings.TrimPrefix(header.Name, "objects/")
		if header.Name != "objects/"+hash || !validDigest(hash) || digest(data) != hash {
			return b, errors.New("bundle: повреждён общий объект или неизвестный путь")
		}
		b.Objects[hash] = data
	}
	// Consume the compressed stream fully: a truncated checksum must not pass.
	tail, err := readLimited(gz, 1<<20)
	if err != nil {
		return b, err
	}
	if len(bytes.Trim(tail, "\x00")) != 0 {
		return b, errors.New("bundle: данные после конца tar")
	}
	if err := validateBundle(b); err != nil {
		return b, err
	}
	return b, nil
}

func validateBundle(b evidenceBundle) error {
	ix := b.Index
	if (ix.SchemaVersion != 1 && ix.SchemaVersion != 2) || !validDigest(ix.ID) || ix.ID != bundleID(ix) || len(ix.Configs) == 0 || len(ix.Files) > 200000 {
		return errors.New("bundle: неподдерживаемая схема или неверная идентичность")
	}
	areas := map[string]BundleArea{}
	for _, a := range ix.Areas {
		if !slugRE.MatchString(a.ID) || !oneOf(a.Kind, "reports", "workspace", "site", "config") {
			return errors.New("bundle: недопустимая область")
		}
		if _, ok := areas[a.ID]; ok {
			return errors.New("bundle: повторная область")
		}
		areas[a.ID] = a
	}
	paths := map[string]bool{}
	objects := map[string]bool{}
	for _, f := range ix.Files {
		if _, ok := areas[f.Area]; !ok || !localPath(f.Path) || !validDigest(f.SHA256) || f.Bytes < 0 || f.Bytes > maxFile || f.Mode > 0777 {
			return errors.New("bundle: неверный логический путь/размер/mode")
		}
		key := f.Area + "/" + f.Path
		if paths[key] {
			return errors.New("bundle: повторный логический файл")
		}
		paths[key] = true
		data, ok := b.Objects[f.SHA256]
		if !ok || int64(len(data)) != f.Bytes || digest(data) != f.SHA256 {
			return errors.New("bundle: отсутствует/повреждена зависимость")
		}
		objects[f.SHA256] = true
	}
	if len(objects) != len(b.Objects) {
		return errors.New("bundle: лишние общие объекты")
	}
	// A file cannot be an ancestor of another path.
	for key := range paths {
		for parent := filepath.ToSlash(filepath.Dir(key)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if paths[parent] {
				return errors.New("bundle: пересечение файла и каталога")
			}
		}
	}
	ids := map[string]bool{}
	reportModes := map[string]bool{}
	hasCitations := false
	for _, cfg := range ix.Configs {
		if !slugRE.MatchString(cfg.ID) || ids[cfg.ID] || !paths["configs/"+cfg.ID+".yaml"] {
			return errors.New("bundle: недопустимый CONFIG")
		}
		ids[cfg.ID] = true
		citations := citationRetention(cfg.Config)
		hasCitations = hasCitations || citations
		if !oneOf(cfg.Config.SourceRetention, "", "archive", "citations") || ix.SchemaVersion == 1 && citations {
			return errors.New("bundle: режим CONFIG несовместим со схемой пакета")
		}
		found := false
		for _, area := range ix.Areas {
			if area.Kind == "reports" && area.OriginalRoot == cfg.Config.ReportsDir {
				if _, duplicate := reportModes[area.ID]; duplicate {
					return errors.New("bundle: повторный владелец reports")
				}
				reportModes[area.ID], found = citations, true
			}
		}
		if !found {
			return errors.New("bundle: CONFIG без области reports")
		}
	}
	if ix.SchemaVersion == 2 && !hasCitations {
		return errors.New("bundle: схема 2 требует режим citations")
	}
	if ix.SchemaVersion == 2 {
		for _, area := range ix.Areas {
			if area.Kind != "workspace" {
				continue
			}
			owned := false
			for _, cfg := range ix.Configs {
				owned = owned || !citationRetention(cfg.Config) && cfg.Config.ProjectRoot == area.OriginalRoot
			}
			if !owned {
				return errors.New("bundle: область исходников без архивного CONFIG")
			}
		}
	}
	for _, f := range ix.Files {
		if areas[f.Area].Kind != "reports" {
			continue
		}
		citations, owned := reportModes[f.Area]
		if !owned {
			return errors.New("bundle: область reports без CONFIG")
		}
		parts := strings.Split(f.Path, "/")
		if citations && len(parts) >= 3 && parts[1] == "sources" && slugRE.MatchString(parts[0]) {
			return errors.New("bundle: полные страницы источников в режиме citations")
		}
		if len(parts) != 2 || parts[1] != "manifest.json" || !slugRE.MatchString(parts[0]) {
			continue
		}
		var m Manifest
		if err := strictJSON(b.Objects[f.SHA256], &m); err != nil {
			return err
		}
		if !oneOf(m.Config.SourceRetention, "", "archive", "citations") || citationRetention(m.Config) != citations {
			return errors.New("bundle: режим run не совпадает с CONFIG")
		}
		if (m.External != nil || citations) && !paths[f.Area+"/"+parts[0]+"/sources.json"] {
			return errors.New("bundle: внешний run без обязательных полных источников")
		}
	}
	// Validate each retained source inventory against its manifest and common objects.
	files := map[string]BundleFile{}
	for _, f := range ix.Files {
		files[f.Area+"/"+f.Path] = f
	}
	for _, f := range ix.Files {
		if areas[f.Area].Kind != "reports" || filepath.Base(f.Path) != "sources.json" {
			continue
		}
		var inv sourceInventory
		if err := strictJSON(b.Objects[f.SHA256], &inv); err != nil {
			return err
		}
		mf, ok := files[f.Area+"/"+filepath.ToSlash(filepath.Join(filepath.Dir(f.Path), "manifest.json"))]
		if !ok {
			return errors.New("bundle: sources без manifest")
		}
		var m Manifest
		if err := strictJSON(b.Objects[mf.SHA256], &m); err != nil {
			return err
		}
		citations := citationRetention(m.Config)
		if (inv.SchemaVersion != 1 && !citations || inv.SchemaVersion != 2 && citations) || inv.SnapshotID != m.SnapshotID {
			return errors.New("bundle: неверное основание sources")
		}
		sources := map[string]SavedSource{}
		for _, s := range inv.Sources {
			if !localPath(s.Path) || !validDigest(s.SHA256) || s.Bytes < 0 || s.Bytes > maxFile {
				return errors.New("bundle: неверная запись источника")
			}
			if citations {
				if s.Full || s.Limitation == "" || s.URL != "" || s.RawURL != "" {
					return errors.New("bundle: полный источник в режиме citations")
				}
				if paths[f.Area+"/._objects/"+s.SHA256] {
					return errors.New("bundle: полные bytes источника в режиме citations")
				}
			} else {
				object, ok := files[f.Area+"/._objects/"+s.SHA256]
				if !ok || object.SHA256 != s.SHA256 || object.Bytes != int64(s.Bytes) || !s.Full {
					return errors.New("bundle: неполный общий источник")
				}
			}
			if _, ok := sources[s.Path]; ok {
				return errors.New("bundle: повторный источник")
			}
			sources[s.Path] = s
		}
		for _, source := range m.Files {
			s, ok := sources[source.Path]
			if !ok || s.SHA256 != source.SHA256 || s.Bytes != source.Bytes || s.Kind != source.Kind {
				return errors.New("bundle: потеряна зависимость snapshot")
			}
		}
	}
	return nil
}

func restoreBundle(b evidenceBundle, destination string) (any, error) {
	abs, err := filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return nil, errors.New("restore: NEW_DIR должен отсутствовать")
	}
	guard, _, err := openExact(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	guard.Close()
	stage, err := os.MkdirTemp(filepath.Dir(abs), ".restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	if err := materializeBundle(b, stage, stage); err != nil {
		return nil, err
	}
	if err := validateRestoredState(b, stage); err != nil {
		return nil, err
	}
	if err := materializeBundle(b, stage, abs); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, abs); err != nil {
		return nil, err
	}
	configs := []string{}
	for _, cfg := range b.Index.Configs {
		configs = append(configs, filepath.Join(abs, "configs", cfg.ID+".yaml"))
	}
	result := bundleSummary(b)
	result["directory"] = abs
	result["configs"] = configs
	sites := []string{}
	for _, a := range b.Index.Areas {
		if a.Kind == "site" {
			sites = append(sites, filepath.Join(abs, "areas", a.ID))
		}
	}
	result["sites"] = sites
	return result, nil
}

func mappedBundlePath(index BundleIndex, base, original string) (string, error) {
	best := ""
	var area BundleArea
	for _, a := range index.Areas {
		if a.OriginalRoot != "" && within(a.OriginalRoot, original) && len(a.OriginalRoot) > len(best) {
			best = a.OriginalRoot
			area = a
		}
	}
	if best == "" {
		return "", errors.New("bundle: путь CONFIG не покрыт сохранёнными областями")
	}
	rel, err := filepath.Rel(best, original)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "areas", area.ID, rel), nil
}

func materializeBundle(b evidenceBundle, physical, logical string) error {
	root, err := os.OpenRoot(physical)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, f := range b.Index.Files {
		if err := processContext.Err(); err != nil {
			return err
		}
		path := "areas/" + f.Area + "/" + f.Path
		if err := root.MkdirAll(filepath.ToSlash(filepath.Dir(path)), 0700); err != nil {
			return err
		}
		if err := atomicWrite(root, path, b.Objects[f.SHA256], os.FileMode(f.Mode)); err != nil {
			return err
		}
	}
	if err := root.MkdirAll("configs", 0700); err != nil {
		return err
	}
	paths := map[string]string{}
	for _, c := range b.Index.Configs {
		paths[c.OriginalPath] = filepath.Join(logical, "configs", c.ID+".yaml")
	}
	for _, c := range b.Index.Configs {
		cfg := c.Config
		cfg.ReportsDir, err = mappedBundlePath(b.Index, logical, cfg.ReportsDir)
		if err != nil {
			return err
		}
		if citationRetention(cfg) && cfg.External == nil {
			// A sibling archive may cover the same checkout. Its bytes must not
			// silently rebind a citations result to an available source workspace.
			cfg.ProjectRoot = filepath.Join(logical, "unavailable", c.ID)
		} else {
			cfg.ProjectRoot, err = mappedBundlePath(b.Index, logical, cfg.ProjectRoot)
			if err != nil {
				if cfg.External != nil || !missingWorkspace(b.Index, c.ID) {
					return err
				}
				cfg.ProjectRoot = filepath.Join(logical, "unavailable", c.ID)
			}
		}
		cfg.BaselineRun = c.BaselineRun
		cfg.Related = []string{}
		for _, related := range c.Related {
			path, ok := paths[related]
			if !ok {
				return errors.New("restore: отсутствует related CONFIG")
			}
			cfg.Related = append(cfg.Related, path)
		}
		if cfg.External != nil {
			ext := *cfg.External
			ext.CodeRoot = cfg.ProjectRoot
			ext.Input, err = mappedBundlePath(b.Index, logical, c.Input)
			if err != nil {
				return err
			}
			cfg.External = &ext
		}
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		if err := atomicWrite(root, "configs/"+c.ID+".yaml", data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func missingWorkspace(index BundleIndex, id string) bool {
	for _, text := range index.Limitations {
		if strings.HasPrefix(text, id+": текущее дерево исходников отсутствует;") {
			return true
		}
	}
	return false
}
func verifyBundleState(b evidenceBundle) error {
	if err := validateBundle(b); err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "spec-audit-bundle-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err := materializeBundle(b, temp, temp); err != nil {
		return err
	}
	return validateRestoredState(b, temp)
}
func validateRestoredState(b evidenceBundle, base string) error {
	for _, c := range b.Index.Configs {
		config := filepath.Join(base, "configs", c.ID+".yaml")
		cfg, err := loadConfig(config, true)
		if err != nil {
			return err
		}
		if cfg.IndexMode == "accepted" {
			if _, _, err := readAccepted(cfg.ReportsDir); err != nil {
				return err
			}
		}
		if citationRetention(cfg) {
			if err := validateRetention(cfg); err != nil {
				return err
			}
			if err := releasedExternalInputs(cfg); err != nil {
				return err
			}
			if cfg.External != nil {
				if err := validateExternalMetadata(cfg); err != nil {
					return err
				}
			}
		} else {
			if _, err := scanSnapshot(cfg); err != nil && !missingWorkspace(b.Index, c.ID) {
				return err
			}
		}
		reports, err := os.OpenRoot(cfg.ReportsDir)
		if err != nil {
			return err
		}
		entries, err := fs.ReadDir(reports.FS(), ".")
		if err != nil {
			reports.Close()
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if _, err := reports.Lstat(entry.Name() + "/manifest.json"); os.IsNotExist(err) {
				continue
			} else if err != nil {
				reports.Close()
				return err
			}
			if _, err := execute([]string{"status", config, entry.Name()}); err != nil {
				reports.Close()
				return fmt.Errorf("bundle: run %s не восстанавливается: %w", entry.Name(), err)
			}
			if citationRetention(cfg) {
				run, err := reports.OpenRoot(entry.Name())
				if err != nil {
					reports.Close()
					return err
				}
				data, err := readRoot(run, "manifest.json", maxState)
				var m Manifest
				var inv sourceInventory
				if err == nil {
					err = strictJSON(data, &m)
				}
				if err == nil {
					inv, err = readSourceInventory(reports, run, m)
				}
				if err == nil && m.External != nil {
					err = validateExternalRunMetadata(cfg, m, inv)
				}
				run.Close()
				if err != nil {
					reports.Close()
					return fmt.Errorf("bundle: sources run %s: %w", entry.Name(), err)
				}
			}
		}
		reports.Close()
	}
	return nil
}
