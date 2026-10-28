//go:build darwin || linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type CleanupPublication struct {
	Area     string              `json:"area"`
	Snapshot PublicationSnapshot `json:"snapshot"`
	Manifest string              `json:"manifest_bytes"`
}
type PublicationCleanupBasis struct {
	IDs          []string             `json:"ids"`
	Files        []BundleFile         `json:"live_files"`
	Publications []CleanupPublication `json:"publications"`
}

func capturePublicationCleanup(b evidenceBundle, ids []string) (*PublicationCleanupBasis, error) {
	basis := &PublicationCleanupBasis{IDs: append([]string{}, ids...), Files: []BundleFile{}, Publications: []CleanupPublication{}}
	sort.Strings(basis.IDs)
	seen := map[string]bool{}
	for _, id := range basis.IDs {
		if !validDigest(id) || seen[id] {
			return nil, errors.New("cleanup: нужны уникальные полные publication IDs")
		}
		seen[id] = true
	}
	for _, area := range b.Index.Areas {
		if area.Kind != "reports" {
			continue
		}
		if err := walkBundleAreaExcluding(area.OriginalRoot, auditReportRoots(b.Index), func(path string, data []byte, mode uint32) error {
			basis.Files = append(basis.Files, BundleFile{area.ID, path, digest(data), int64(len(data)), mode})
			if strings.HasPrefix(path, "._publications/") {
				var p PublicationSnapshot
				if err := strictJSON(data, &p); err != nil {
					return err
				}
				if err := validatePublication(p); err != nil {
					return err
				}
				if path != "._publications/"+p.ID+".json" {
					return errors.New("cleanup: имя manifest не соответствует publication ID")
				}
				basis.Publications = append(basis.Publications, CleanupPublication{area.ID, p, string(data)})
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Slice(basis.Files, func(i, j int) bool {
		return basis.Files[i].Area+"/"+basis.Files[i].Path < basis.Files[j].Area+"/"+basis.Files[j].Path
	})
	return basis, nil
}

func makePublicationCleanupPlan(b evidenceBundle, basis *PublicationCleanupBasis) (CleanupPlan, error) {
	p := CleanupPlan{SchemaVersion: 2, BundleID: b.Index.ID, ArchiveSHA256: b.SHA256, ToolVersion: version, Candidates: []CleanupCandidate{}, PublicationBasis: basis}
	if basis == nil || len(basis.IDs) == 0 {
		return p, errors.New("cleanup: нет выбранных публикаций")
	}
	selected := map[string]bool{}
	for _, id := range basis.IDs {
		if !validDigest(id) || selected[id] {
			return p, errors.New("cleanup: неверный/повторный ID")
		}
		selected[id] = true
	}
	areas := map[string]BundleArea{}
	for _, a := range b.Index.Areas {
		areas[a.ID] = a
	}
	files := map[string]BundleFile{}
	for _, f := range basis.Files {
		if areas[f.Area].Kind != "reports" || !localPath(f.Path) || !validDigest(f.SHA256) || f.Bytes < 0 || f.Bytes > maxFile || f.Mode > 0777 {
			return p, errors.New("cleanup: неверная live-опись")
		}
		key := f.Area + "/" + f.Path
		if _, ok := files[key]; ok {
			return p, errors.New("cleanup: повтор live-файла")
		}
		files[key] = f
		p.BytesBefore += f.Bytes
	}
	protected := map[string]bool{}
	// All archived source inventories protect their full objects, across all runs.
	for _, f := range b.Index.Files {
		if areas[f.Area].Kind != "reports" || !strings.HasSuffix(f.Path, "/sources.json") {
			continue
		}
		var inv sourceInventory
		if err := strictJSON(b.Objects[f.SHA256], &inv); err != nil {
			return p, err
		}
		for _, s := range inv.Sources {
			if s.Full {
				protected[f.Area+"/._objects/"+s.SHA256] = true
			}
		}
	}
	backed := map[string]BundleFile{}
	derived := map[string]bool{}
	for _, f := range b.Index.Files {
		if areas[f.Area].Kind != "reports" {
			continue
		}
		key := f.Area + "/" + f.Path
		backed[key] = f
		if live, ok := files[key]; !ok || live.SHA256 != f.SHA256 || live.Bytes != f.Bytes || live.Mode != f.Mode {
			return p, errors.New("cleanup: audit backup не соответствует live-основанию")
		}
		if strings.HasSuffix(f.Path, "/report.json") {
			derived[f.Area+"/"+strings.TrimSuffix(f.Path, "report.json")+"report.html"] = true
		}
		if strings.HasSuffix(f.Path, "/sources.json") {
			var inv sourceInventory
			if err := strictJSON(b.Objects[f.SHA256], &inv); err != nil {
				return p, err
			}
			for _, src := range inv.Sources {
				if src.Full {
					key := f.Area + "/" + filepath.ToSlash(filepath.Dir(f.Path)) + "/sources/" + sourcePageKey(src)
					derived[key+".html"], derived[key+".bin"] = true, true
				}
			}
		}
	}
	for _, entry := range basis.Publications {
		derived[entry.Area+"/._publications/"+entry.Snapshot.ID+".json"] = true
		for _, f := range entry.Snapshot.Files {
			if !protected[entry.Area+"/._objects/"+f.SHA256] {
				derived[entry.Area+"/._objects/"+f.SHA256] = true
			}
		}
	}
	for key := range files {
		if _, ok := backed[key]; !ok && !derived[key] {
			return p, errors.New("cleanup: live-данные не покрыты audit backup")
		}
	}
	known := map[string]bool{}
	pubFiles := map[string]bool{}
	wantedObjects := map[string]bool{}
	for _, entry := range basis.Publications {
		a, s := entry.Area, entry.Snapshot
		if areas[a].Kind != "reports" {
			return p, errors.New("cleanup: публикация вне reports")
		}
		if err := validatePublication(s); err != nil {
			return p, err
		}
		path := "._publications/" + s.ID + ".json"
		key := a + "/" + path
		f, ok := files[key]
		if !ok || pubFiles[key] {
			return p, errors.New("cleanup: нет исходного manifest или он повторён")
		}
		pubFiles[key] = true
		// writeJSON appends LF. Preserve the exact original file metadata rather
		// than assuming its indentation; the snapshot identity validates its body.
		if f.Bytes <= 0 {
			return p, errors.New("cleanup: пустой manifest")
		}
		var original PublicationSnapshot
		if digest([]byte(entry.Manifest)) != f.SHA256 || int64(len(entry.Manifest)) != f.Bytes || strictJSON([]byte(entry.Manifest), &original) != nil || !reflect.DeepEqual(original, s) {
			return p, errors.New("cleanup: publication metadata не соответствует исходным bytes")
		}
		if selected[s.ID] {
			known[s.ID] = true
			p.Candidates = append(p.Candidates, CleanupCandidate{a, path, f.SHA256, f.Bytes, "publication_manifest", "Производный снимок сайта; аудит сохранён и проверен отдельно", []string{}, "audit bundle " + b.Index.ID})
		}
		for _, v := range s.Files {
			k := a + "/._objects/" + v.SHA256
			object, ok := files[k]
			if !ok || object.SHA256 != v.SHA256 || object.Bytes != int64(v.Bytes) {
				return p, errors.New("cleanup: потеряна зависимость публикации")
			}
			if selected[s.ID] {
				wantedObjects[k] = true
			} else {
				protected[k] = true
			}
		}
	}
	for id := range selected {
		if !known[id] {
			return p, fmt.Errorf("cleanup: публикация %s отсутствует", id)
		}
	}
	for _, f := range basis.Files {
		if strings.HasPrefix(f.Path, "._publications/") && !pubFiles[f.Area+"/"+f.Path] {
			return p, errors.New("cleanup: неизвестный publication manifest")
		}
	}
	// A protected object is never a candidate, even when bytes match a site asset.
	for k := range wantedObjects {
		if protected[k] {
			continue
		}
		f := files[k]
		p.Candidates = append(p.Candidates, CleanupCandidate{f.Area, f.Path, f.SHA256, f.Bytes, "publication_object", "Объект выбранной публикации без сохраняемых потребителей", []string{}, "saved report + source inventories"})
	}
	var err error
	p.Candidates, err = protectCleanupPhysicalFiles(b, basis.Publications, p.Candidates)
	if err != nil {
		return CleanupPlan{}, err
	}
	sort.Slice(p.Candidates, func(i, j int) bool {
		a, c := p.Candidates[i], p.Candidates[j]
		if a.Class != c.Class {
			return a.Class == "publication_manifest"
		}
		return a.Area+"/"+a.Path < c.Area+"/"+c.Path
	})
	for _, c := range p.Candidates {
		p.FreedBytes += c.Bytes
	}
	p.BytesAfter = p.BytesBefore - p.FreedBytes
	p.ID = cleanupPlanID(p)
	if err := checkCleanupPlanSize(p, maxCleanupPlan); err != nil {
		return CleanupPlan{}, err
	}
	return p, nil
}

func comparePublicationLive(b evidenceBundle, p CleanupPlan) error {
	allowed := map[string]bool{}
	for _, c := range p.Candidates {
		allowed[c.Area+"/"+c.Path] = true
	}
	expected := map[string]BundleFile{}
	for _, f := range p.PublicationBasis.Files {
		expected[f.Area+"/"+f.Path] = f
	}
	present := map[string]bool{}
	for _, area := range b.Index.Areas {
		if area.Kind != "reports" {
			continue
		}
		if err := walkBundleAreaExcluding(area.OriginalRoot, auditReportRoots(b.Index), func(path string, data []byte, mode uint32) error {
			key := area.ID + "/" + path
			was, ok := expected[key]
			if !ok || was.SHA256 != digest(data) || was.Bytes != int64(len(data)) || was.Mode != mode {
				return errors.New("cleanup: live-основание изменилось; нужен новый план")
			}
			present[key] = true
			return nil
		}); err != nil {
			return err
		}
	}
	for key := range expected {
		if !present[key] && !allowed[key] {
			return errors.New("cleanup: исчезла сохраняемая зависимость")
		}
	}
	// CONFIGs outside reports also belong to the exact backup basis.
	cfgs := []Config{}
	for _, c := range b.Index.Configs {
		raw, err := readExact(c.OriginalPath, maxConfig)
		if err != nil {
			return err
		}
		var want []byte
		for _, f := range b.Index.Files {
			if f.Area == "configs" && f.Path == c.ID+".yaml" {
				want = b.Objects[f.SHA256]
			}
		}
		if !bytes.Equal(raw, want) {
			return errors.New("cleanup: CONFIG изменился; нужен новый пакет")
		}
		cfg, err := loadConfig(c.OriginalPath, true)
		if err != nil {
			return err
		}
		cfgs = append(cfgs, cfg)
	}
	return comparePublicationWorkspaces(b, cfgs)
}

func provePublicationCleanup(b evidenceBundle, p CleanupPlan) (CleanupProof, error) {
	proof, err := proveBundleRestore(b, p)
	if err != nil {
		return proof, err
	}
	proof.SchemaVersion = 2
	temp, err := os.MkdirTemp("", "spec-audit-publication-proof-")
	if err != nil {
		return proof, err
	}
	defer os.RemoveAll(temp)
	if err := materializeBundle(b, temp, temp); err != nil {
		return proof, err
	}
	areas := map[string]BundleArea{}
	for _, a := range b.Index.Areas {
		areas[a.ID] = a
	}
	for _, c := range p.Candidates {
		path := filepath.Join(temp, "areas", c.Area, filepath.FromSlash(c.Path))
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return proof, err
		}
	}
	if err := validateRestoredState(b, temp); err != nil {
		return proof, err
	}
	for i, c := range b.Index.Configs {
		path := filepath.Join(temp, "configs", c.ID+".yaml")
		cfg, err := loadConfig(path, true)
		if err != nil {
			return proof, err
		}
		store := publishStore{path, cfg}
		_, selected, err := publicationOverview([]publishStore{store}, "", nil)
		if err != nil {
			return proof, err
		}
		if len(selected) > 0 && selected[0] != nil {
			if _, err := publishCommand(filepath.Join(temp, fmt.Sprintf("site-%d", i)), []string{path}); err != nil {
				return proof, err
			}
			if _, err := publishCommand(filepath.Join(temp, fmt.Sprintf("history-%d", i)), []string{"--run", selected[0].ID, path}); err != nil {
				return proof, err
			}
		}
	}
	return proof, nil
}

func publicationCleanupCommand(args []string, b evidenceBundle) (any, error) {
	if args[0] == "plan" {
		if len(args) < 4 || args[2] != "--publications" {
			return nil, errors.New("cleanup plan ARCHIVE --publications ID...")
		}
		// Before taking a disposable live snapshot, verify the retained audit basis.
		if err := compareCleanupBasis(b.Index, nil); err != nil {
			return nil, err
		}
		basis, err := capturePublicationCleanup(b, args[3:])
		if err != nil {
			return nil, err
		}
		p, err := makePublicationCleanupPlan(b, basis)
		if err != nil {
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
	expected, err := makePublicationCleanupPlan(b, p.PublicationBasis)
	if err != nil {
		return nil, err
	}
	if p.SchemaVersion != 2 || p.ID != cleanupPlanID(p) || !reflect.DeepEqual(p, expected) {
		return nil, errors.New("cleanup: изменённый/чужой план или версия инструмента")
	}
	if err := comparePublicationLive(b, p); err != nil {
		return nil, err
	}
	proof, err := provePublicationCleanup(b, p)
	if err != nil {
		return nil, err
	}
	if args[0] == "verify" && len(args) == 3 {
		return proof, nil
	}
	if args[0] != "apply" || len(args) != 4 {
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
	if !reflect.DeepEqual(proof, supplied) {
		return nil, errors.New("cleanup: нет подтверждённого восстановления точного плана")
	}
	if err := comparePublicationLive(b, p); err != nil {
		return nil, err
	}
	removed, skipped := []string{}, []string{}
	var freed int64
	areas := map[string]BundleArea{}
	for _, a := range b.Index.Areas {
		areas[a.ID] = a
	}
	finish := func(err error) (any, error) {
		problems := []string{}
		if err != nil {
			problems = append(problems, err.Error())
		}
		return map[string]any{"schema_version": 2, "plan_id": p.ID, "bundle_id": b.Index.ID, "removed": removed, "skipped": skipped, "freed_bytes": freed, "complete": err == nil, "errors": problems}, err
	}
	for _, c := range p.Candidates {
		if err := processContext.Err(); err != nil {
			return finish(err)
		}
		root, err := os.OpenRoot(areas[c.Area].OriginalRoot)
		if err != nil {
			return finish(err)
		}
		raw, err := readRoot(root, c.Path, maxFile)
		if os.IsNotExist(err) {
			root.Close()
			skipped = append(skipped, c.Area+"/"+c.Path)
			continue
		}
		if err != nil {
			root.Close()
			return finish(err)
		}
		if digest(raw) != c.SHA256 || int64(len(raw)) != c.Bytes {
			root.Close()
			return finish(errors.New("cleanup: кандидат изменился"))
		}
		err = root.Remove(c.Path)
		root.Close()
		if err != nil {
			return finish(err)
		}
		removed = append(removed, c.Area+"/"+c.Path)
		freed += c.Bytes
	}
	return finish(nil)
}

// Restoration splits areas into separate trees. Protect shared live paths before
// using that proof; two logical files can otherwise hide one physical consumer.
func protectCleanupPhysicalFiles(b evidenceBundle, publications []CleanupPublication, candidates []CleanupCandidate) ([]CleanupCandidate, error) {
	areas := map[string]BundleArea{}
	for _, area := range b.Index.Areas {
		areas[area.ID] = area
	}
	physical := func(area, path string) string {
		return exactSystemPath(filepath.Join(areas[area].OriginalRoot, filepath.FromSlash(path)))
	}
	disposable := map[string]bool{}
	for _, c := range candidates {
		disposable[c.Area+"/"+c.Path] = true
	}
	retained := map[string]bool{}
	for _, f := range b.Index.Files {
		if areas[f.Area].OriginalRoot != "" && !disposable[f.Area+"/"+f.Path] {
			retained[physical(f.Area, f.Path)] = true
		}
	}
	boundaries := []string{}
	for _, c := range b.Index.Configs {
		retained[exactSystemPath(filepath.Clean(c.OriginalPath))] = true
		// Local citations have no workspace area. Their selected live source
		// paths still own physical files and must remain safe to audit again.
		if c.Config.External == nil {
			groups := []Sources{c.Config.Specs, c.Config.Code, c.Config.Tests}
			if c.Config.References != nil {
				groups = append(groups, *c.Config.References)
			}
			for _, group := range groups {
				for _, path := range group.Paths {
					boundaries = append(boundaries, exactSystemPath(filepath.Join(c.Config.ProjectRoot, filepath.FromSlash(path))))
				}
			}
		}
		// The normalized Config in index JSON omits external runtime roots.
		// Recover that deny boundary from the exact archived YAML, without opening it.
		for _, f := range b.Index.Files {
			if f.Area != "configs" || f.Path != c.ID+".yaml" {
				continue
			}
			var raw Config
			if err := yaml.Unmarshal(b.Objects[f.SHA256], &raw); err != nil {
				return nil, err
			}
			if raw.External != nil && raw.External.CodeRoot != "" {
				if !filepath.IsAbs(raw.External.CodeRoot) {
					return nil, errors.New("cleanup: external.code_root должен быть абсолютным для проверки физических потребителей")
				}
				for _, group := range []Sources{c.Config.Code, c.Config.Tests} {
					for _, path := range group.Paths {
						boundaries = append(boundaries, exactSystemPath(filepath.Join(raw.External.CodeRoot, filepath.FromSlash(path))))
					}
				}
			}
		}
	}
	protected := func(path string) bool {
		if retained[path] {
			return true
		}
		for _, boundary := range boundaries {
			if within(boundary, path) || within(path, boundary) {
				return true
			}
		}
		return false
	}
	// A publication manifest retained for another physical consumer still owns
	// every referenced object, even when its ID was explicitly selected.
	for _, entry := range publications {
		if protected(physical(entry.Area, "._publications/"+entry.Snapshot.ID+".json")) {
			for _, file := range entry.Snapshot.Files {
				retained[physical(entry.Area, "._objects/"+file.SHA256)] = true
			}
		}
	}
	result := []CleanupCandidate{}
	for _, c := range candidates {
		path := physical(c.Area, c.Path)
		if protected(path) {
			slog.Debug("[FIX:cleanup-physical] сохраняемый физический потребитель защищён", "path", path, "class", c.Class)
			continue
		}
		result = append(result, c)
	}
	return result, nil
}

// Recollect only workspace areas. Recollecting reports after partial deletion
// would mistake leftover publication-only objects for new retained audit files.
func comparePublicationWorkspaces(b evidenceBundle, cfgs []Config) error {
	roots := map[string]string{}
	expected := map[string]BundleFile{}
	for _, area := range b.Index.Areas {
		if area.Kind == "workspace" {
			roots[area.OriginalRoot] = area.ID
		}
	}
	for _, f := range b.Index.Files {
		for _, id := range roots {
			if f.Area == id {
				expected[f.Area+"/"+f.Path] = f
				break
			}
		}
	}
	present := map[string]bool{}
	for _, cfg := range cfgs {
		if citationRetention(cfg) {
			continue
		}
		area, ok := roots[cfg.ProjectRoot]
		if !ok {
			if _, err := os.Stat(cfg.ProjectRoot); !os.IsNotExist(err) {
				return errors.New("cleanup: доступность дерева исходников изменилась; нужен новый пакет")
			}
			continue
		}
		if err := walkBundleWorkspace(cfg, func(path string, data []byte, mode uint32) error {
			key := area + "/" + path
			want, ok := expected[key]
			if !ok || want.SHA256 != digest(data) || want.Bytes != int64(len(data)) || want.Mode != mode {
				slog.Debug("[FIX:cleanup-physical] дерево исходников изменилось", "root", cfg.ProjectRoot, "path", path)
				return errors.New("cleanup: дерево исходников изменилось; нужен новый пакет и план")
			}
			present[key] = true
			return nil
		}); err != nil {
			return err
		}
	}
	if len(present) != len(expected) {
		return errors.New("cleanup: исчез сохраняемый файл дерева исходников")
	}
	return nil
}
