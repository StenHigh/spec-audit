//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ExternalConfig pins the integration identity. Input is a local binding produced
// by external import; its physical location never participates in snapshot_id.
type ExternalConfig struct {
	Scope          string  `yaml:"scope" json:"scope"`
	Project        string  `yaml:"project" json:"project"`
	Corpus         string  `yaml:"corpus" json:"corpus"`
	Line           *string `yaml:"line" json:"line"`
	ProfileID      string  `yaml:"profile_id" json:"profile_id"`
	ProfileVersion string  `yaml:"profile_version" json:"profile_version"`
	Input          string  `yaml:"input,omitempty" json:"-"`
	CodeSHA256     string  `yaml:"code_sha256,omitempty" json:"code_sha256,omitempty"`
	CodeRoot       string  `yaml:"code_root,omitempty" json:"-"`
}

type ExternalProfile struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
type ExternalFile struct {
	Path      string          `json:"path"`
	Role      string          `json:"role"`
	Export    bool            `json:"export"`
	MediaType string          `json:"media_type"`
	Size      int64           `json:"size_bytes"`
	SHA256    string          `json:"sha256"`
	Origin    json.RawMessage `json:"origin"`
}
type ExternalManifest struct {
	SchemaVersion       int             `json:"schema_version"`
	SpecVersion         string          `json:"spec_version"`
	Project             string          `json:"project"`
	Corpus              string          `json:"corpus"`
	Line                *string         `json:"line"`
	PreviousSpecVersion *string         `json:"previous_spec_version"`
	Profile             ExternalProfile `json:"profile"`
	Files               []ExternalFile  `json:"files"`
	FileCount           int             `json:"file_count"`
}
type ExternalFreshness struct {
	Status         string  `json:"status"`
	CheckedAt      *string `json:"checked_at"`
	ObservationRef *string `json:"observation_ref"`
	Reason         *string `json:"reason"`
}
type AuditInput struct {
	SchemaVersion    int               `json:"schema_version"`
	CorpusDir        string            `json:"corpus_dir"`
	ManifestPath     string            `json:"manifest_path"`
	ProfilePath      string            `json:"profile_path"`
	ManifestSHA256   string            `json:"manifest_sha256"`
	CodeBasis        json.RawMessage   `json:"code_basis"`
	Freshness        ExternalFreshness `json:"freshness"`
	AcceptanceRef    *string           `json:"acceptance_ref"`
	ApplicabilityRef *string           `json:"applicability_ref"`
	PublicationBase  *string           `json:"publication_base"`
}
type ExternalBasis struct {
	Scope            string            `json:"scope"`
	SchemaVersion    int               `json:"schema_version"`
	Project          string            `json:"project"`
	Corpus           string            `json:"corpus"`
	Line             *string           `json:"line"`
	SpecVersion      string            `json:"spec_version"`
	ManifestSHA256   string            `json:"manifest_sha256"`
	Profile          ExternalProfile   `json:"profile"`
	InputSHA256      string            `json:"input_sha256"`
	CodeBasis        json.RawMessage   `json:"code_basis"`
	Freshness        ExternalFreshness `json:"freshness"`
	AcceptanceRef    *string           `json:"acceptance_ref"`
	ApplicabilityRef *string           `json:"applicability_ref"`
	PublicationBase  *string           `json:"publication_base"`
}

type externalInput struct {
	request                      AuditInput
	manifest                     ExternalManifest
	raw, manifestRaw, profileRaw []byte
	files                        map[string][]byte
	code                         []SourceFile
	scope                        string
}

func requiredKeys(data []byte, keys ...string) error {
	var fields map[string]json.RawMessage
	if err := strictJSON(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("нужен JSON object")
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("нет обязательного поля %s", key)
		}
	}
	return nil
}

func rawField(data []byte, key string) []byte {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	return fields[key]
}

func nonempty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func validateOrigin(raw []byte, code bool, attachment bool) error {
	var fields map[string]json.RawMessage
	if err := strictJSON(raw, &fields); err != nil || fields == nil {
		return errors.New("origin/code_basis: нужен object")
	}
	var kind string
	if err := json.Unmarshal(fields["kind"], &kind); err != nil {
		return errors.New("origin.kind: нужен тип происхождения")
	}
	required := []string{"kind"}
	nullable := map[string]bool{}
	switch kind {
	case "trucker":
		if code {
			return errors.New("code_basis: допустимы git/snapshot")
		}
		required = append(required, "project_id", "document_id", "revision_id", "file_version_id", "source_url")
		nullable["source_url"], nullable["file_version_id"] = true, !attachment
	case "git":
		required = append(required, "repository", "commit")
		if !code {
			required = append(required, "path")
		}
	case "snapshot":
		required = append(required, "snapshot_id")
		if code {
			required = append(required, "sha256")
		} else {
			required = append(required, "snapshot_sha256", "path")
		}
	default:
		return errors.New("неподдерживаемый origin.kind")
	}
	if len(fields) != len(required) {
		return errors.New("origin/code_basis: неполные или неизвестные поля")
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok {
			return fmt.Errorf("origin: нет %s", key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && nullable[key] {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil || !nonempty(text) {
			return fmt.Errorf("origin.%s: нужна непустая строка", key)
		}
		if strings.Contains(key, "sha256") && !validDigest(text) {
			return fmt.Errorf("origin.%s: нужен SHA-256", key)
		}
		if key == "path" && !localPath(text) {
			return errors.New("origin.path: небезопасный путь")
		}
		if key == "source_url" && !safeExternalURL(text) {
			return errors.New("origin.source_url: недопустимый URL")
		}
	}
	if kind == "trucker" && !attachment && !bytes.Equal(bytes.TrimSpace(fields["file_version_id"]), []byte("null")) {
		return errors.New("Markdown origin.file_version_id должен быть null")
	}
	return nil
}

func safeExternalURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && oneOf(u.Scheme, "http", "https") && u.Host != "" && u.User == nil && !strings.ContainsAny(value, "\x00\r\n")
}

func parseExternalManifest(data []byte) (ExternalManifest, error) {
	var m ExternalManifest
	if err := strictJSON(data, &m); err != nil {
		return m, err
	}
	if err := requiredKeys(data, "schema_version", "spec_version", "project", "corpus", "line", "previous_spec_version", "profile", "files", "file_count"); err != nil {
		return m, err
	}
	if m.SchemaVersion != 1 || !nonempty(m.SpecVersion, m.Project, m.Corpus, m.Profile.ID, m.Profile.Version) || !validDigest(m.Profile.SHA256) || m.FileCount != len(m.Files) || m.FileCount < 1 || m.FileCount > 25000 {
		return m, errors.New("source-manifest: версия, идентичность, profile или file_count не соответствуют r1")
	}
	if err := requiredKeys(rawField(data, "profile"), "id", "version", "sha256"); err != nil {
		return m, err
	}
	seen := map[string]bool{}
	var raws []json.RawMessage
	_ = json.Unmarshal(rawField(data, "files"), &raws)
	for i, file := range m.Files {
		if err := requiredKeys(raws[i], "path", "role", "export", "media_type", "size_bytes", "sha256", "origin"); err != nil {
			return m, err
		}
		if !localPath(file.Path) || seen[file.Path] || !oneOf(file.Role, "normative", "reference", "archive") || !nonempty(file.MediaType) || file.Size < 0 || file.Size > maxFile || !validDigest(file.SHA256) {
			return m, errors.New("source-manifest.files: путь, роль, размер или hash недопустимы")
		}
		if bytes.Equal(bytes.TrimSpace(rawField(raws[i], "export")), []byte("null")) || bytes.Equal(bytes.TrimSpace(rawField(raws[i], "size_bytes")), []byte("null")) {
			return m, errors.New("source-manifest: null не заменяет export/size_bytes")
		}
		seen[file.Path] = true
		if err := validateOrigin(file.Origin, false, !strings.EqualFold(strings.Split(file.MediaType, ";")[0], "text/markdown")); err != nil {
			return m, err
		}
	}
	return m, nil
}

func parseAuditInput(raw []byte) (AuditInput, error) {
	var in AuditInput
	if err := strictJSON(raw, &in); err != nil {
		return in, err
	}
	if err := requiredKeys(raw, "schema_version", "corpus_dir", "manifest_path", "profile_path", "manifest_sha256", "code_basis", "freshness", "acceptance_ref", "applicability_ref", "publication_base"); err != nil {
		return in, err
	}
	if in.SchemaVersion != 1 || !validDigest(in.ManifestSHA256) || !filepath.IsAbs(in.CorpusDir) || !filepath.IsAbs(in.ManifestPath) || !filepath.IsAbs(in.ProfilePath) {
		return in, errors.New("audit-input: версия, SHA-256 или абсолютные пути не соответствуют r1")
	}
	if err := validateOrigin(in.CodeBasis, true, false); err != nil {
		return in, err
	}
	if err := requiredKeys(rawField(raw, "freshness"), "status", "checked_at", "observation_ref", "reason"); err != nil {
		return in, err
	}
	if !oneOf(in.Freshness.Status, "unknown", "unchanged", "changed") {
		return in, errors.New("freshness.status: unknown/unchanged/changed")
	}
	if in.Freshness.CheckedAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *in.Freshness.CheckedAt); err != nil {
			return in, errors.New("freshness.checked_at: нужен ISO8601 с timezone")
		}
	}
	if in.Freshness.Status != "unknown" && in.Freshness.CheckedAt == nil {
		return in, errors.New("проверенная freshness требует checked_at")
	}
	if in.PublicationBase != nil && !safePublicationBase(*in.PublicationBase) {
		return in, errors.New("publication_base: нужен HTTP(S) URL без credentials")
	}
	return in, nil
}

// Darwin supplies /var, /tmp and /etc as aliases into /private. Recognize only
// these OS aliases with their exact targets; all supplied deeper links remain
// subject to noSymlinks. Do not canonicalize arbitrary caller-owned paths.
func exactSystemPath(abs string) string {
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp", "/etc"} {
			if abs == alias || strings.HasPrefix(abs, alias+"/") {
				target, err := os.Readlink(alias)
				if err == nil && filepath.Clean(filepath.Join("/", target)) == "/private"+alias {
					return "/private" + abs
				}
				break
			}
		}
	}
	return abs
}

// openExact rejects supplied symlinks in every path component, including the
// root itself, after recognizing the OS-owned Darwin aliases above.
func openExact(name string) (*os.Root, string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, "", err
	}
	rel := strings.TrimPrefix(filepath.ToSlash(exactSystemPath(abs)), "/")
	if err := noSymlinks(root, rel); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, rel, nil
}
func readExact(name string, limit int64) ([]byte, error) {
	root, rel, err := openExact(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return readRoot(root, rel, limit)
}

func readExternalInput(input string, pin *ExternalConfig) (externalInput, error) {
	var x externalInput
	if pin == nil || !nonempty(pin.Project, pin.Corpus, pin.Scope, pin.ProfileID, pin.ProfileVersion) {
		return x, errors.New("CONFIG.external: нужны project/corpus/line/profile_id/profile_version")
	}
	var err error
	x.raw, err = readExact(input, maxResult)
	if err != nil {
		return x, err
	}
	x.request, err = parseAuditInput(x.raw)
	if err != nil {
		return x, err
	}
	x.manifestRaw, err = readExact(x.request.ManifestPath, maxState)
	if err != nil {
		return x, err
	}
	if digest(x.manifestRaw) != x.request.ManifestSHA256 {
		return x, errors.New("manifest_sha256 не совпадает с исходными bytes")
	}
	x.manifest, err = parseExternalManifest(x.manifestRaw)
	if err != nil {
		return x, err
	}
	if x.manifest.Project != pin.Project || x.manifest.Corpus != pin.Corpus || !reflect.DeepEqual(x.manifest.Line, pin.Line) || x.manifest.Profile.ID != pin.ProfileID || x.manifest.Profile.Version != pin.ProfileVersion {
		return x, errors.New("чужие project/corpus/line или неподдерживаемый profile")
	}
	x.profileRaw, err = readExact(x.request.ProfilePath, maxConfig)
	if err != nil {
		return x, err
	}
	var profile map[string]json.RawMessage
	if digest(x.profileRaw) != x.manifest.Profile.SHA256 || strictJSON(x.profileRaw, &profile) != nil || profile == nil {
		return x, errors.New("profile bytes/hash или JSON object недопустимы")
	}
	x.files, err = readCorpus(x.request.CorpusDir, x.manifest)
	return x, err
}

func readCorpus(dir string, m ExternalManifest) (map[string][]byte, error) {
	guard, _, err := openExact(dir)
	if err != nil {
		return nil, err
	}
	guard.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := map[string][]byte{}
	expected := map[string]ExternalFile{}
	for _, f := range m.Files {
		expected[f.Path] = f
	}
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("corpus: symlink запрещён")
		}
		if entry.IsDir() {
			return nil
		}
		f, ok := expected[path]
		if !ok || !entry.Type().IsRegular() {
			return fmt.Errorf("corpus: лишний/необычный файл %s", path)
		}
		data, err := readRoot(root, path, maxFile)
		if err != nil {
			return err
		}
		if int64(len(data)) != f.Size || digest(data) != f.SHA256 {
			return fmt.Errorf("corpus: размер/hash %s не совпадает", path)
		}
		total += int64(len(data))
		if total > 512<<20 {
			return errors.New("corpus превышает 512 MiB")
		}
		files[path] = data
		return nil
	})
	if err == nil && len(files) != len(expected) {
		err = errors.New("corpus: отсутствует файл манифеста")
	}
	return files, err
}

// selectedCode reads only code/tests declared in CONFIG; no runtime executes.
func selectedCode(cfg Config) ([]SourceFile, map[string][]byte, error) {
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	files := []SourceFile{}
	data := map[string][]byte{}
	kinds := map[string]string{}
	totalBytes := 0
	for _, g := range []struct {
		kind    string
		sources Sources
	}{{"code", cfg.Code}, {"tests", cfg.Tests}} {
		for _, dir := range g.sources.Paths {
			if err := noSymlinks(root, dir); err != nil {
				return nil, nil, err
			}
			err := fs.WalkDir(root.FS(), dir, func(path string, e fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if e.Type()&os.ModeSymlink != 0 {
					return errors.New("code/tests: symlink запрещён")
				}
				if e.IsDir() {
					return nil
				}
				if !e.Type().IsRegular() {
					return errors.New("code/tests: нужен обычный файл")
				}
				if !selected(g.sources, filepath.Base(path)) {
					return nil
				}
				if _, ok := data[path]; ok {
					if kinds[path] == g.kind {
						return nil
					}
					return errors.New("code/tests: файл попал в несколько категорий")
				}
				b, err := readRoot(root, path, maxFile)
				if err != nil {
					return err
				}
				totalBytes += len(b)
				if totalBytes > 512<<20 || len(files) >= 25000 {
					return errors.New("code/tests превышают 512 MiB или 25000 файлов")
				}
				data[path] = b
				kinds[path] = g.kind
				files = append(files, SourceFile{Path: path, Kind: g.kind, SHA256: digest(b), Bytes: len(b)})
				return nil
			})
			if err != nil {
				return nil, nil, err
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) > 25000 {
		return nil, nil, errors.New("слишком много code/tests")
	}
	var total int
	for _, f := range files {
		total += f.Bytes
	}
	if total > 512<<20 {
		return nil, nil, errors.New("code/tests превышают 512 MiB")
	}
	return files, data, nil
}

func codeDigest(files []SourceFile) string { data, _ := json.Marshal(files); return digest(data) }
func gitRead(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(processContext, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var output cappedBuffer
	output.limit = maxFile
	cmd.Stdout = &output
	err := cmd.Run()
	if err != nil {
		return nil, errors.New("code_basis: Git-основание недоступно")
	}
	return output.Bytes(), nil
}
func verifyCodeBasis(cfg Config, raw []byte, files []SourceFile, data map[string][]byte) error {
	var basis map[string]string
	if err := json.Unmarshal(raw, &basis); err != nil {
		return err
	}
	if basis["kind"] == "snapshot" {
		if basis["sha256"] != codeDigest(files) {
			return errors.New("code_basis.sha256 не совпадает с выбранным code/tests snapshot")
		}
		return nil
	}
	commit := basis["commit"]
	if (len(commit) != 40 && len(commit) != 64) || strings.Trim(commit, "0123456789abcdef") != "" {
		return errors.New("code_basis.commit: нужен полный Git object ID")
	}
	head, err := gitRead(cfg.ProjectRoot, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) != commit {
		return errors.New("code_basis.commit не равен HEAD")
	}
	top, err := gitRead(cfg.ProjectRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	repository := strings.TrimSpace(string(top))
	requestedRepository := basis["repository"]
	if filepath.IsAbs(requestedRepository) {
		requestedRepository = exactSystemPath(filepath.Clean(requestedRepository))
	}
	remote, remoteErr := gitRead(cfg.ProjectRoot, "remote", "get-url", "origin")
	if requestedRepository != repository && (remoteErr != nil || basis["repository"] != strings.TrimSpace(string(remote))) {
		return errors.New("code_basis.repository не соответствует checkout")
	}
	for _, f := range files {
		rel, err := filepath.Rel(repository, filepath.Join(cfg.ProjectRoot, f.Path))
		if err != nil || !filepath.IsLocal(rel) {
			return errors.New("code_basis: файл вне Git checkout")
		}
		b, err := gitRead(cfg.ProjectRoot, "show", commit+":"+filepath.ToSlash(rel))
		if err != nil || !bytes.Equal(b, data[f.Path]) {
			return fmt.Errorf("code_basis: dirty/untracked %s", f.Path)
		}
	}
	return nil
}

func externalBasis(x externalInput) *ExternalBasis {
	m, in := x.manifest, x.request
	return &ExternalBasis{x.scope, 1, m.Project, m.Corpus, m.Line, m.SpecVersion, in.ManifestSHA256, m.Profile, digest(x.raw), in.CodeBasis, in.Freshness, in.AcceptanceRef, in.ApplicabilityRef, in.PublicationBase}
}

func externalCommand(args []string) (any, error) {
	if len(args) == 2 && args[0] == "release" {
		return externalRelease(args[1])
	}
	if len(args) == 3 && args[0] == "result" {
		return externalResult(args[1], args[2])
	}
	if len(args) != 3 || !oneOf(args[0], "check", "import") {
		return nil, errors.New("external check|import CONFIG AUDIT_INPUT")
	}
	cfg, err := loadConfig(args[1])
	if err != nil {
		return nil, err
	}
	x, err := readExternalInput(args[2], cfg.External)
	if err != nil {
		return nil, err
	}
	code, codeData, err := selectedCode(cfg)
	if err != nil {
		return nil, err
	}
	if err := verifyCodeBasis(cfg, x.request.CodeBasis, code, codeData); err != nil {
		return nil, err
	}
	x.code = code
	x.scope = cfg.External.Scope
	if !slugRE.MatchString(cfg.External.Scope) {
		return nil, errors.New("external.scope: нужен стабильный slug")
	}
	// Every normative/reference file must be selected in its declared category.
	for _, f := range x.manifest.Files {
		if _, ok := codeData[f.Path]; ok {
			return nil, errors.New("corpus пересекается с code/tests")
		}
		inSpecs := sourceSelected(cfg.Specs, f.Path)
		inRefs := cfg.References != nil && sourceSelected(*cfg.References, f.Path)
		if (f.Role == "normative") != inSpecs || (f.Role == "reference") != inRefs {
			return nil, fmt.Errorf("CONFIG specs/references не соответствуют роли %s", f.Path)
		}
	}
	if args[0] == "check" {
		return map[string]any{"schema_version": 1, "basis": externalBasis(x), "files": len(x.files), "code_sha256": codeDigest(code)}, nil
	}
	if err := os.MkdirAll(cfg.ReportsDir, 0700); err != nil {
		return nil, err
	}
	unlock, err := lockWorkspaces([]string{cfg.ReportsDir}, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return installExternal(args[1], cfg, x, codeData)
}

func sourceSelected(group Sources, path string) bool {
	if !selected(group, filepath.Base(path)) {
		return false
	}
	for _, dir := range group.Paths {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

func installExternal(configPath string, cfg Config, x externalInput, codeData map[string][]byte) (any, error) {
	if err := os.MkdirAll(cfg.ReportsDir, 0700); err != nil {
		return nil, err
	}
	if err := ensureRetention(cfg); err != nil {
		return nil, err
	}
	reports, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	defer reports.Close()
	pinRaw, _ := json.Marshal(externalScopeIdentity(cfg.External))
	prior, pinErr := readRoot(reports, "._external-identity.json", maxConfig)
	if pinErr == nil && !bytes.Equal(prior, pinRaw) {
		return nil, errors.New("reports_dir уже принадлежит другому project/corpus/scope")
	}
	if pinErr != nil && !os.IsNotExist(pinErr) {
		return nil, pinErr
	}
	if err := reports.MkdirAll("._inputs", 0700); err != nil {
		return nil, err
	}
	if err := noSymlinks(reports, "._inputs"); err != nil {
		return nil, err
	}
	identityCfg := cfg
	identityCfg.ProjectRoot = ""
	identityCfg.ReportsDir = ""
	pinned := *cfg.External
	pinned.Input = ""
	identityCfg.External = &pinned
	identityRaw, _ := json.Marshal(identityCfg)
	key := digest(append(append(append([]byte(x.request.ManifestSHA256), []byte(codeDigest(x.code))...), []byte(digest(x.raw))...), identityRaw...))
	target := filepath.Join(cfg.ReportsDir, "._inputs", key)
	if _, err := os.Lstat(target); err == nil {
		if err := noSymlinks(reports, "._inputs/"+key); err != nil {
			return nil, err
		}
		existing, err := loadConfig(filepath.Join(target, "config.yaml"), true)
		if err != nil {
			return nil, err
		}
		if citationRetention(cfg) {
			ext := *existing.External
			ext.CodeRoot = runtimeSourceRoot(cfg)
			existing.External = &ext
			if err := rehydrateExternal(existing, x, codeData); err != nil {
				return nil, err
			}
		}
		if _, err := snapshot(existing); err != nil && cfg.IndexMode != "accepted" {
			return nil, err
		}
		if _, err := boundExternal(existing, append(externalSourceFiles(x.manifest), x.code...)); err != nil {
			return nil, err
		}
		if citationRetention(cfg) {
			// This YAML is the mutable physical binding. original-config.yaml,
			// request bytes, manifests and run history remain immutable provenance.
			data, err := yaml.Marshal(existing)
			if err != nil {
				return nil, err
			}
			if err := atomicWrite(reports, "._inputs/"+key+"/config.yaml", data, 0400); err != nil {
				return nil, err
			}
		}
		return map[string]any{"config": filepath.Join(target, "config.yaml"), "basis": externalBasis(x), "installed": false}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Join(cfg.ReportsDir, "._inputs"), ".staging-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	root, err := os.OpenRoot(stage)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	put := func(path string, data []byte) error {
		if err := root.MkdirAll(filepath.ToSlash(filepath.Dir(path)), 0700); err != nil {
			return err
		}
		return atomicWrite(root, path, data, 0400)
	}
	for path, data := range x.files {
		if err := put("corpus/"+path, data); err != nil {
			return nil, err
		}
		if err := put("tree/"+path, data); err != nil {
			return nil, err
		}
	}
	for path, data := range codeData {
		if err := put("tree/"+path, data); err != nil {
			return nil, err
		}
	}
	for path, data := range map[string][]byte{"source-manifest.json": x.manifestRaw, "profile.json": x.profileRaw, "audit-input.json": x.raw} {
		if err := put(path, data); err != nil {
			return nil, err
		}
	}
	if err := writeJSON(root, "code-files.json", x.code, 0400); err != nil {
		return nil, err
	}
	original, err := readExact(configPath, maxConfig)
	if err != nil {
		return nil, err
	}
	if err := put("original-config.yaml", original); err != nil {
		return nil, err
	}
	runtimeRoot := runtimeSourceRoot(cfg)
	cfg.ProjectRoot = filepath.Join(target, "tree")
	ext := *cfg.External
	ext.Input = "audit-input.json"
	ext.CodeSHA256 = codeDigest(x.code)
	ext.CodeRoot = runtimeRoot
	cfg.External = &ext
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := put("config.yaml", data); err != nil {
		return nil, err
	}
	// Validate the complete staged tree before the single publication point.
	probe := cfg
	probe.ProjectRoot = filepath.Join(stage, "tree")
	probe.External = &ext
	probe.External.Input = filepath.Join(stage, "audit-input.json")
	if _, err := scanSnapshot(probe); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, target); err != nil {
		return nil, err
	}
	if err := atomicWrite(reports, "._external-identity.json", pinRaw, 0400); err != nil {
		return nil, err
	}
	return map[string]any{"config": filepath.Join(target, "config.yaml"), "basis": externalBasis(x), "installed": true}, nil
}

func externalScopeIdentity(ext *ExternalConfig) any {
	return struct {
		Project string `json:"project"`
		Corpus  string `json:"corpus"`
		Scope   string `json:"scope"`
	}{ext.Project, ext.Corpus, ext.Scope}
}

func externalSourceFiles(m ExternalManifest) []SourceFile {
	files := []SourceFile{}
	for _, f := range m.Files {
		if f.Role != "archive" {
			files = append(files, SourceFile{Path: f.Path, Kind: "spec", SHA256: f.SHA256, Bytes: int(f.Size), Reference: f.Role == "reference"})
		}
	}
	return files
}

func boundExternal(cfg Config, files []SourceFile) (*ExternalBasis, error) {
	if cfg.External == nil || cfg.External.Input == "" {
		return nil, errors.New("external: сначала выполните external import")
	}
	dir := filepath.Dir(cfg.External.Input)
	raw, err := readExact(cfg.External.Input, maxResult)
	if err != nil {
		return nil, err
	}
	in, err := parseAuditInput(raw)
	if err != nil {
		return nil, err
	}
	// Paths in the original request are provenance, never live dependencies after import.
	manifestRaw, err := readExact(filepath.Join(dir, "source-manifest.json"), maxState)
	if err != nil {
		return nil, err
	}
	m, err := parseExternalManifest(manifestRaw)
	if err != nil {
		return nil, err
	}
	profile, err := readExact(filepath.Join(dir, "profile.json"), maxConfig)
	if err != nil {
		return nil, err
	}
	if digest(manifestRaw) != in.ManifestSHA256 || digest(profile) != m.Profile.SHA256 || m.Project != cfg.External.Project || m.Corpus != cfg.External.Corpus || !reflect.DeepEqual(m.Line, cfg.External.Line) || m.Profile.ID != cfg.External.ProfileID || m.Profile.Version != cfg.External.ProfileVersion {
		return nil, errors.New("external binding: hash/идентичность/profile изменились")
	}
	corpus, err := readCorpus(filepath.Join(dir, "corpus"), m)
	if err != nil {
		return nil, err
	}
	b, err := readExact(filepath.Join(dir, "code-files.json"), maxState)
	if err != nil {
		return nil, err
	}
	var code []SourceFile
	if err := strictJSON(b, &code); err != nil {
		return nil, err
	}
	if !validDigest(cfg.External.CodeSHA256) || cfg.External.CodeSHA256 != codeDigest(code) {
		return nil, errors.New("external binding: code/tests не совпадает с закреплённой описью")
	}
	expected := append(externalSourceFiles(m), code...)
	sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
	actual := append([]SourceFile{}, files...)
	sort.Slice(actual, func(i, j int) bool { return actual[i].Path < actual[j].Path })
	if !reflect.DeepEqual(expected, actual) {
		return nil, errors.New("external binding: CONFIG/snapshot не совпадает с переданными corpus/code/tests")
	}
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, f := range m.Files {
		data, err := readRoot(root, f.Path, maxFile)
		if err != nil || !bytes.Equal(data, corpus[f.Path]) {
			return nil, fmt.Errorf("external binding: tree %s изменился", f.Path)
		}
	}
	basis := externalBasis(externalInput{raw: raw, request: in, manifest: m})
	basis.Scope = cfg.External.Scope
	return basis, nil
}

// The sole exception to the ordinary source/report separation is the immutable
// tree published by external import, with metadata outside that tree (§78).
func managedExternalRoot(cfg Config) bool {
	if cfg.External == nil || cfg.External.Input == "" {
		return false
	}
	rel, err := filepath.Rel(cfg.ReportsDir, cfg.ProjectRoot)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) == 3 && parts[0] == "._inputs" && validDigest(parts[1]) && parts[2] == "tree" && cfg.External.Input == filepath.Join(filepath.Dir(cfg.ProjectRoot), "audit-input.json")
}

func safePublicationBase(value string) bool {
	u, err := url.Parse(value)
	return err == nil && safeExternalURL(value) && u.RawQuery == "" && u.Fragment == ""
}
func externalScopePath(ext *ExternalConfig) string {
	return "external/" + digest([]byte(ext.Project + "\x00" + ext.Corpus))[:32] + "/" + ext.Scope
}

func runtimeSourceRoot(cfg Config) string {
	if cfg.External != nil && cfg.External.CodeRoot != "" {
		return cfg.External.CodeRoot
	}
	return cfg.ProjectRoot
}
func runtimeSourcesCurrent(cfg Config) error {
	if cfg.External == nil {
		return nil
	}
	runtimeCfg := cfg
	runtimeCfg.ProjectRoot = runtimeSourceRoot(cfg)
	guard, _, err := openExact(runtimeCfg.ProjectRoot)
	if err != nil {
		return err
	}
	guard.Close()
	files, _, err := selectedCode(runtimeCfg)
	if err != nil {
		return err
	}
	if codeDigest(files) != cfg.External.CodeSHA256 {
		return errors.New("runtime code/tests изменились после внешнего импорта; нужен новый вход и run")
	}
	return nil
}

// Check all existing ancestors before creating a destination, so a supplied
// symlink cannot cause even staging directories outside the declared boundary.
func mkdirExact(name string, mode os.FileMode) error {
	abs, err := filepath.Abs(name)
	if err != nil {
		return err
	}
	ancestor := abs
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			root, _, err := openExact(ancestor)
			if err != nil {
				return err
			}
			root.Close()
			if !info.IsDir() {
				return errors.New("каталог назначения не является обычным каталогом")
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return err
		}
		ancestor = parent
	}
	return os.MkdirAll(abs, mode)
}
