//go:build darwin || linux

package main

// Promoted from the host pilot; source guards and the per-run protocol are reused.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	maxConfig = 1 << 20
	maxResult = 4 << 20
	maxFile   = 32 << 20
	maxState  = 32 << 20
)

var (
	slugRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,95}$`)
)

type Runtime struct {
	Kind           string   `yaml:"kind" json:"kind"`
	Service        string   `yaml:"service" json:"service"`
	Paths          []string `yaml:"paths" json:"paths"`
	Tests          []string `yaml:"tests" json:"tests"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type Scope struct {
	ID           string   `yaml:"id" json:"id"`
	Focus        string   `yaml:"focus" json:"focus"`
	Requirements []string `yaml:"requirements" json:"requirements"`
}

type Sources struct {
	Paths   []string `yaml:"paths" json:"paths"`
	Include []string `yaml:"include" json:"include"`
	Exclude []string `yaml:"exclude" json:"exclude"`
}

type Config struct {
	IndexMode   string  `yaml:"index_mode,omitempty" json:"index_mode,omitempty"`
	Version     int     `yaml:"version" json:"version"`
	ProjectRoot string  `yaml:"project_root" json:"project_root"`
	Specs       Sources `yaml:"specs" json:"specs"`
	Code        Sources `yaml:"code" json:"code"`
	Tests       Sources `yaml:"tests" json:"tests"`
	ReportsDir  string  `yaml:"reports_dir" json:"reports_dir"`
	Runtime     Runtime `yaml:"runtime" json:"runtime"`
	Scopes      []Scope `yaml:"scopes" json:"scopes"`
}

type SourceFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type Manifest struct {
	Version      int              `json:"version"`
	Config       Config           `json:"config"`
	Files        []SourceFile     `json:"files"`
	Profile      string           `json:"profile"`
	Requirements []Requirement    `json:"requirements"`
	SnapshotID   string           `json:"snapshot_id"`
	Accepted     *AcceptedSummary `json:"accepted,omitempty"`
}

type Task struct {
	TaskID       string        `json:"task_id"`
	Attempt      int           `json:"attempt"`
	SnapshotID   string        `json:"snapshot_id"`
	Role         string        `json:"role"`
	Scope        string        `json:"scope"`
	Focus        string        `json:"focus"`
	Requirements []Requirement `json:"requirements"`
}

type Citation struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Quote     string `json:"quote"`
}

type TestCitation struct {
	TestID   string   `json:"test_id"`
	Citation Citation `json:"citation"`
}

type Assessment struct {
	RequirementID  string         `json:"requirement_id"`
	Specification  string         `json:"specification"`
	Implementation string         `json:"implementation"`
	Assertion      string         `json:"assertion"`
	Statement      string         `json:"statement"`
	Spec           []Citation     `json:"spec"`
	Code           []Citation     `json:"code"`
	Tests          []TestCitation `json:"tests"`
	Limitations    []string       `json:"limitations"`
}

type Result struct {
	TaskID      string       `json:"task_id"`
	Attempt     int          `json:"attempt"`
	SnapshotID  string       `json:"snapshot_id"`
	Role        string       `json:"role"`
	Scope       string       `json:"scope"`
	Summary     string       `json:"summary"`
	Assessments []Assessment `json:"assessments"`
	Limitations []string     `json:"limitations"`
}

type Entry struct {
	Task      Task    `json:"task"`
	Result    *Result `json:"result,omitempty"`
	RawSHA256 string  `json:"raw_sha256,omitempty"`
}

type State struct {
	Entries    []Entry   `json:"entries"`
	Executions []Receipt `json:"executions"`
}

type TaskBatch struct {
	RunID       string       `json:"run_id"`
	SnapshotID  string       `json:"snapshot_id"`
	ProjectRoot string       `json:"project_root"`
	Runtime     Runtime      `json:"runtime"`
	Tasks       []Task       `json:"tasks"`
	Files       []SourceFile `json:"files"`
}

type Status struct {
	RunID             string `json:"run_id"`
	SnapshotID        string `json:"snapshot_id"`
	DeliveryComplete  bool   `json:"delivery_complete"`
	Freshness         string `json:"freshness"`
	RequirementsTotal int    `json:"requirements_total"`
	Expected          int    `json:"expected"`
	Submitted         int    `json:"submitted"`
	Pending           []Task `json:"pending"`
	HostReviewState   string `json:"host_review_state,omitempty"`
}

type TestExecution struct {
	TestID    string `json:"test_id"`
	ReceiptID string `json:"receipt_id"`
	State     string `json:"state"`
}

type RoleReport struct {
	Role       string          `json:"role"`
	Attempt    int             `json:"attempt"`
	Assessment *Assessment     `json:"assessment"`
	Executions []TestExecution `json:"executions"`
}

type ResultSummary struct {
	TaskID      string   `json:"task_id"`
	Summary     string   `json:"summary"`
	Limitations []string `json:"limitations"`
}

type RequirementReport struct {
	Requirement Requirement  `json:"requirement"`
	Scope       string       `json:"scope"`
	Roles       []RoleReport `json:"roles"`
}

type Report struct {
	Status
	GeneratedAt                string              `json:"generated_at"`
	Conclusion                 string              `json:"conclusion"`
	Limitations                []string            `json:"limitations"`
	Requirements               []RequirementReport `json:"requirements"`
	Executions                 []Receipt           `json:"executions"`
	Summaries                  []ResultSummary     `json:"summaries"`
	HostReconciliationRequired bool                `json:"host_reconciliation_required"`
	Accepted                   *AcceptedSummary    `json:"accepted,omitempty"`
	CompletenessBasis          string              `json:"completeness_basis"`
	SemanticCompletenessProven bool                `json:"semantic_completeness_proven"`
	HostReview                 *ReviewSummary      `json:"host_review,omitempty"`
	RawProvenance              []RawProvenance     `json:"raw_provenance"`
}

func main() {
	level := new(slog.LevelVar)
	if value := os.Getenv("LOG_LEVEL"); value != "" {
		if err := level.UnmarshalText([]byte(strings.ToUpper(value))); err != nil {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": "LOG_LEVEL: debug/info/warn/error"})
			os.Exit(1)
		}
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	slog.Debug("начало операции")
	value, err := execute(os.Args[1:])
	if err != nil {
		// Input data must not reach logs through decoder diagnostics.
		slog.Error("операция отклонена", "error", err.Error())
		os.Exit(1)
	}
	slog.Info("операция завершена")
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("превышен лимит %d байт", limit)
	}
	return data, err
}

func readPath(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("нужен обычный файл: %s", path)
	}
	return readLimited(f, limit)
}

func localPath(path string) bool {
	return path != "." && fs.ValidPath(path) && !strings.ContainsAny(path, "\\\x00")
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// Resolve existing ancestors before deciding whether a new report directory is outside the source tree.
func canonicalPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("ожидается абсолютный путь")
	}
	path = filepath.Clean(path)
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(path) == path {
			return "", err
		}
		tail = append(tail, filepath.Base(path))
		path = filepath.Dir(path)
	}
}

func loadConfig(path string, history ...bool) (Config, error) {
	var cfg Config
	data, err := readPath(path, maxConfig)
	if err != nil {
		return cfg, err
	}
	if !utf8.Valid(data) {
		return cfg, errors.New("конфигурация должна быть UTF-8")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, errors.New("недопустимый YAML: проверьте типы, неизвестные и повторные поля")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return cfg, errors.New("разрешён ровно один YAML-документ")
	}
	if cfg.Version != 1 || cfg.ProjectRoot == "" || cfg.ReportsDir == "" || len(cfg.Scopes) > 16 {
		return cfg, errors.New("нужны version: 1, project_root, reports_dir; не более 16 scope")
	}
	if !oneOf(cfg.IndexMode, "", "accepted") {
		return cfg, errors.New("index_mode: допустимо только accepted или отсутствие поля")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return cfg, err
	}
	if !filepath.IsAbs(cfg.ProjectRoot) {
		cfg.ProjectRoot = filepath.Join(base, cfg.ProjectRoot)
	}
	if !filepath.IsAbs(cfg.ReportsDir) {
		cfg.ReportsDir = filepath.Join(base, cfg.ReportsDir)
	}
	cfg.ProjectRoot, err = canonicalPath(cfg.ProjectRoot)
	if err != nil {
		return cfg, fmt.Errorf("project_root: %w", err)
	}
	info, err := os.Stat(cfg.ProjectRoot)
	if (err != nil || !info.IsDir()) && !(len(history) > 0 && history[0]) {
		return cfg, errors.New("project_root должен быть существующим каталогом")
	}
	cfg.ReportsDir, err = canonicalPath(cfg.ReportsDir)
	if err != nil {
		return cfg, fmt.Errorf("reports_dir: %w", err)
	}
	if within(cfg.ReportsDir, cfg.ProjectRoot) {
		return cfg, errors.New("reports_dir не может содержать project_root")
	}
	if len(cfg.Specs.Paths) == 0 || len(cfg.Code.Paths) == 0 {
		return cfg, errors.New("specs/code.paths должны быть непустыми")
	}
	for _, group := range []*Sources{&cfg.Specs, &cfg.Code, &cfg.Tests} {
		sort.Strings(group.Paths)
		sort.Strings(group.Include)
		sort.Strings(group.Exclude)
		for _, pattern := range append(append([]string{}, group.Include...), group.Exclude...) {
			if strings.ContainsAny(pattern, "/\\") {
				return cfg, errors.New("include/exclude сопоставляет только имена файлов")
			}
			if _, err := filepath.Match(pattern, ""); err != nil {
				return cfg, errors.New("недопустимый glob")
			}
		}
		for i, path := range group.Paths {
			if !localPath(path) {
				return cfg, errors.New("недопустимый относительный источник")
			}
			if i > 0 && path == group.Paths[i-1] {
				return cfg, errors.New("повторный путь источника")
			}
			input := filepath.Join(cfg.ProjectRoot, path)
			if within(input, cfg.ReportsDir) || within(cfg.ReportsDir, input) {
				return cfg, errors.New("reports_dir пересекается с входным деревом")
			}
		}
	}
	if err := validateRuntime(&cfg.Runtime); err != nil {
		return cfg, err
	}
	ids := map[string]bool{}
	for i := range cfg.Scopes {
		scope := &cfg.Scopes[i]
		if !slugRE.MatchString(scope.ID) || len(scope.ID) > 80 || ids[scope.ID] || strings.TrimSpace(scope.Focus) == "" || len(scope.Requirements) == 0 || len(scope.Requirements) > 64 {
			return cfg, errors.New("scope требует уникальный id, focus и 1–64 requirements")
		}
		ids[scope.ID] = true
		sort.Strings(scope.Requirements)
		for j, id := range scope.Requirements {
			if !requirementID.MatchString(id) || (j > 0 && scope.Requirements[j-1] == id) {
				return cfg, errors.New("недопустимый или повторный requirement ID")
			}
		}
	}
	sort.Slice(cfg.Scopes, func(i, j int) bool { return cfg.Scopes[i].ID < cfg.Scopes[j].ID })
	return cfg, nil
}

func noSymlinks(root *os.Root, path string) error {
	if !localPath(path) {
		return fmt.Errorf("недопустимый относительный путь: %s", path)
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("символическая ссылка запрещена: %s", path)
		}
	}
	return nil
}

func readRoot(root *os.Root, path string, limit int64) ([]byte, error) {
	if err := noSymlinks(root, path); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("нужен обычный файл: %s", path)
	}
	return readLimited(f, limit)
}

func lineQuote(data []byte, start, end int) (string, error) {
	if !utf8.Valid(data) {
		return "", errors.New("цитируемый источник должен быть UTF-8")
	}
	if len(data) == 0 {
		return "", errors.New("пустой источник не содержит строк")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if start < 1 || end < start || end > len(lines) {
		return "", errors.New("диапазон цитаты вне источника")
	}
	return strings.TrimSuffix(strings.Join(lines[start-1:end], "\n"), "\r"), nil
}

func snapshot(cfg Config) (Manifest, error) {
	m, err := scanSnapshot(cfg)
	if err != nil {
		return m, err
	}
	if cfg.IndexMode == "accepted" {
		_, state, err := readAccepted(cfg.ReportsDir)
		if err != nil {
			return m, err
		}
		if !acceptedFresh(state, m.Files) {
			return m, errors.New("принятый индекс отсутствует или stale; нужен reconcile")
		}
		m.Profile, m.Accepted = "accepted-index/1;protocol/1", &state.AcceptedSummary
		for _, record := range state.Records {
			if record.Status == "active" {
				m.Requirements = append(m.Requirements, record.Requirement)
			}
		}
	}
	if err := assignRequirements(&m); err != nil {
		return m, err
	}
	identity := m
	identity.Config.ProjectRoot, identity.Config.ReportsDir = "", ""
	data, err := json.Marshal(identity)
	if err != nil {
		return m, err
	}
	m.SnapshotID = digest(data)
	return m, nil
}

func scanSnapshot(cfg Config) (Manifest, error) {
	m := Manifest{Version: 1, Config: cfg, Files: []SourceFile{}, Profile: markdownProfile, Requirements: []Requirement{}}
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return m, err
	}
	defer root.Close()
	total := 0
	seen := map[string]string{}
	for _, group := range []struct {
		kind    string
		sources Sources
	}{{"spec", cfg.Specs}, {"code", cfg.Code}, {"tests", cfg.Tests}} {
		before := len(m.Files)
		for _, directory := range group.sources.Paths {
			if err := noSymlinks(root, directory); err != nil {
				return m, err
			}
			info, err := root.Stat(directory)
			if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
				return m, fmt.Errorf("источник должен быть каталогом или обычным файлом: %s", directory)
			}
			err = fs.WalkDir(root.FS(), directory, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("символическая ссылка запрещена: %s", path)
				}
				if entry.IsDir() {
					return nil
				}
				if !entry.Type().IsRegular() {
					return fmt.Errorf("необычный файл в источнике: %s", path)
				}
				if !selected(group.sources, filepath.Base(path)) {
					return nil
				}
				if prior, ok := seen[path]; ok {
					if prior == group.kind {
						return nil
					}
					return errors.New("файл попал в несколько категорий")
				}
				seen[path] = group.kind
				data, err := readRoot(root, path, maxFile)
				if err != nil {
					return err
				}
				total += len(data)
				if total > 512<<20 || len(m.Files) >= 25000 {
					return errors.New("снимок превышает 512 MiB или 25000 файлов")
				}
				m.Files = append(m.Files, SourceFile{path, group.kind, digest(data), len(data)})
				if group.kind == "spec" && cfg.IndexMode == "" {
					reqs, err := parseRequirements(path, data)
					if err != nil {
						return err
					}
					m.Requirements = append(m.Requirements, reqs...)
					if len(m.Requirements) > 512 {
						return errors.New("не более 512 требований")
					}
				}
				return nil
			})
			if err != nil {
				return m, err
			}
		}
		if len(m.Files) == before && group.kind != "tests" {
			return m, fmt.Errorf("пустая группа источников: %s", group.kind)
		}
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}

func selected(group Sources, name string) bool {
	match := func(patterns []string) bool {
		for _, pattern := range patterns {
			if yes, _ := filepath.Match(pattern, name); yes {
				return true
			}
		}
		return false
	}
	return (len(group.Include) == 0 || match(group.Include)) && !match(group.Exclude)
}

func findSource(m Manifest, path string) (SourceFile, bool) {
	i := sort.Search(len(m.Files), func(i int) bool { return m.Files[i].Path >= path })
	if i < len(m.Files) && m.Files[i].Path == path {
		return m.Files[i], true
	}
	return SourceFile{}, false
}

// encoding/json accepts duplicate keys; reject them before decoding a typed value.
func checkJSONKeys(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON вложен глубже 64 уровней")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("повторный или недопустимый JSON-ключ")
			}
			seen[key] = true
			if err := checkJSONKeys(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := checkJSONKeys(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("неожиданный JSON-разделитель")
	}
	_, err = dec.Token()
	return err
}

func strictJSON(data []byte, out any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON должен быть UTF-8")
	}
	keys := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONKeys(keys, 0); err != nil {
		return errors.New("недопустимый JSON или повторные ключи")
	}
	if _, err := keys.Token(); err != io.EOF {
		return errors.New("разрешён ровно один JSON-документ")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errors.New("JSON не соответствует типам/полям контракта")
	}
	return nil
}

// Require the exact spelling of every result field, including empty arrays and empty quoted lines.
func requiredJSON(data json.RawMessage, typ reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null не заменяет обязательное поле")
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		if len(fields) != typ.NumField() {
			return errors.New("неполный или неизвестный набор полей результата")
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			key := field.Tag.Get("json")
			value, ok := fields[key]
			if !ok {
				return fmt.Errorf("отсутствует поле %s", key)
			}
			if err := requiredJSON(value, field.Type); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := requiredJSON(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateResult(result Result, task Task, m Manifest, checkSources bool) error {
	if result.TaskID != task.TaskID || result.Scope != task.Scope || result.Role != task.Role {
		return errors.New("task_id, scope или role не совпадают с заданием")
	}
	if result.Attempt != task.Attempt {
		return errors.New("устаревшая или неверная attempt")
	}
	if result.SnapshotID != task.SnapshotID {
		return errors.New("неверный snapshot_id")
	}
	if strings.TrimSpace(result.Summary) == "" || len(result.Assessments) != len(task.Requirements) {
		return errors.New("нужны summary и assessment для каждого назначенного требования")
	}
	for _, limitation := range result.Limitations {
		if strings.TrimSpace(limitation) == "" {
			return errors.New("limitation не может быть пустой строкой")
		}
	}
	var root *os.Root
	if checkSources {
		var err error
		root, err = os.OpenRoot(m.Config.ProjectRoot)
		if err != nil {
			return err
		}
		defer root.Close()
	}
	assigned := map[string]Requirement{}
	for _, req := range task.Requirements {
		assigned[req.ID] = req
	}
	seen := map[string]bool{}
	for _, assessment := range result.Assessments {
		req, ok := assigned[assessment.RequirementID]
		if !ok || seen[req.ID] || strings.TrimSpace(assessment.Statement) == "" {
			return errors.New("assessment требует назначенный уникальный requirement_id и statement")
		}
		seen[req.ID] = true
		if !oneOf(assessment.Specification, "clear", "ambiguous") || !oneOf(assessment.Implementation, "supported", "contradicted", "unknown") || !oneOf(assessment.Assertion, "relevant", "weak", "contradicts", "missing", "unknown") {
			return errors.New("недопустимое состояние specification/implementation/assertion")
		}
		if req.Accepted != nil && req.Accepted.Clarity == "ambiguous" && assessment.Specification != "ambiguous" {
			return errors.New("принятая неоднозначность требует новой редакции, не оценки clear")
		}
		if len(assessment.Spec) == 0 || (assessment.Implementation != "unknown" && len(assessment.Code) == 0) {
			return errors.New("нужна spec; supported/contradicted требуют code")
		}
		if oneOf(assessment.Assertion, "relevant", "weak", "contradicts") && len(assessment.Tests) == 0 {
			return errors.New("оценка assertion требует тестовый источник")
		}
		for _, limitation := range assessment.Limitations {
			if strings.TrimSpace(limitation) == "" {
				return errors.New("пустое limitation")
			}
		}
		tests := []Citation{}
		for _, test := range assessment.Tests {
			if strings.TrimSpace(test.TestID) == "" || len(test.TestID) > 1024 {
				return errors.New("нужен непустой test_id до 1024 байт")
			}
			tests = append(tests, test.Citation)
		}
		for _, group := range []struct {
			kind      string
			citations []Citation
		}{{"spec", assessment.Spec}, {"code", assessment.Code}, {"tests", tests}} {
			if len(group.citations) > 128 {
				return errors.New("слишком много цитат в assessment")
			}
			for _, citation := range group.citations {
				file, ok := findSource(m, citation.Path)
				if !localPath(citation.Path) || !ok || file.Kind != group.kind {
					return fmt.Errorf("цитата вне группы %s", group.kind)
				}
				if citation.LineStart < 1 || citation.LineEnd < citation.LineStart {
					return errors.New("неверный диапазон цитаты")
				}
				if group.kind == "spec" && !req.containsSource(citation) {
					return errors.New("цитата вне блока назначенного требования")
				}
				if !checkSources {
					continue
				}
				data, err := readRoot(root, citation.Path, maxFile)
				if err != nil {
					return err
				}
				if digest(data) != file.SHA256 {
					return errors.New("снимок источников изменился")
				}
				quote, err := lineQuote(data, citation.LineStart, citation.LineEnd)
				if err != nil || quote != citation.Quote {
					return fmt.Errorf("цитата не совпадает с полными строками %s:%d-%d", citation.Path, citation.LineStart, citation.LineEnd)
				}
			}
		}
	}
	return nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func atomicWrite(root *os.Root, path string, data []byte, mode os.FileMode) error {
	tmp := ".tmp-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return publishPreparedFile(root, tmp, path, dir)
}

func publishPreparedFile(root *os.Root, tmp, path string, dir *os.File) error {
	if err := root.Rename(tmp, path); err != nil {
		return err
	}
	// Rename is the commit point. A later durability failure cannot mean refusal.
	if err := dir.Sync(); err != nil {
		slog.Warn("запись опубликована; сохранность после сбоя питания не подтверждена")
	}
	return nil
}

func writeJSON(root *os.Root, path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if (path == "state.json" || path == "manifest.json") && len(data)+1 > maxState {
		return errors.New("JSON превышает лимит state 32 MiB")
	}
	return atomicWrite(root, path, append(data, '\n'), mode)
}

func saveState(root *os.Root, state State) error {
	if err := checkStateSize(state); err != nil {
		return err
	}
	if err := invalidateReports(root); err != nil {
		return err
	}
	return writeJSON(root, "state.json", state, 0600)
}

func invalidateReports(root *os.Root) error {
	// Reports are disposable projections; invalidate them before changing authoritative state.
	for _, path := range []string{"report.json", "report.html"} {
		if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func checkStateSize(state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxState {
		return errors.New("state превышает 32 MiB; результат не опубликован")
	}
	return nil
}

func newState(m Manifest) State {
	state := State{Entries: []Entry{}, Executions: []Receipt{}}
	for _, scope := range m.Config.Scopes {
		reqs := []Requirement{}
		for _, req := range m.Requirements {
			for _, id := range scope.Requirements {
				if req.ID == id {
					reqs = append(reqs, req)
				}
			}
		}
		for _, role := range []string{"mapper", "redteam"} {
			state.Entries = append(state.Entries, Entry{Task: Task{
				TaskID: scope.ID + "-" + role, Attempt: 1, SnapshotID: m.SnapshotID,
				Role: role, Scope: scope.ID, Focus: scope.Focus, Requirements: reqs,
			}})
		}
	}
	return state
}

func pending(state State) []Task {
	tasks := []Task{}
	for _, entry := range state.Entries {
		if entry.Result == nil {
			tasks = append(tasks, entry.Task)
		}
	}
	return tasks
}

func makeStatus(runID string, m Manifest, state State, fresh bool) Status {
	status := Status{RunID: runID, SnapshotID: m.SnapshotID, Expected: len(state.Entries), Pending: pending(state), RequirementsTotal: len(m.Requirements), Freshness: "fresh"}
	if !fresh {
		status.Freshness = "stale"
	}
	status.Submitted = status.Expected - len(status.Pending)
	status.DeliveryComplete = len(status.Pending) == 0
	return status
}

func execute(args []string) (any, error) {
	if len(args) > 0 && args[0] == "reconcile" {
		if len(args) != 2 && len(args) != 4 {
			return nil, errors.New("reconcile CONFIG [RAW DECISION]")
		}
		cfg, err := loadConfig(args[1], len(args) == 2)
		if err != nil {
			return nil, err
		}
		return reconcile(cfg, args[2:])
	}
	if len(args) == 2 && args[0] == "init" {
		if err := initConfig(args[1]); err != nil {
			return nil, err
		}
		return map[string]bool{"created": true}, nil
	}
	if len(args) == 2 && args[0] == "index" {
		cfg, err := loadConfig(args[1])
		if err != nil {
			return nil, err
		}
		return indexConfig(cfg)
	}
	if len(args) < 3 {
		return nil, errors.New("команды: init/index CONFIG; reconcile CONFIG [RAW DECISION]; prepare/tasks/status/report CONFIG RUN_ID; review CONFIG RUN_ID [DECISION]; submit/retry/test/php-facts CONFIG RUN_ID ...")
	}
	command, runID := args[0], args[2]
	argc := map[string]int{"prepare": 3, "tasks": 3, "status": 3, "report": 3, "review": -2, "submit": 5, "retry": 4, "test": 4, "php-facts": -1}
	if count, ok := argc[command]; !ok || (count >= 0 && len(args) != count) || (count == -1 && len(args) < 4) || (count == -2 && len(args) != 3 && len(args) != 4) || !slugRE.MatchString(runID) {
		return nil, errors.New("неизвестная команда, неверные аргументы или недопустимый RUN_ID")
	}
	readOnly := oneOf(command, "status", "report") || (command == "review" && len(args) == 3)
	cfg, err := loadConfig(args[1], readOnly)
	if err != nil {
		return nil, err
	}
	if command == "prepare" {
		if err := os.MkdirAll(cfg.ReportsDir, 0700); err != nil {
			return nil, err
		}
	}
	reports, err := os.OpenRoot(cfg.ReportsDir)
	if err != nil {
		return nil, err
	}
	defer reports.Close()
	lock, err := reports.OpenFile("."+runID+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	// ponytail: one lock per run; only consider per-task locks if submission contention is measured.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if command == "prepare" {
		if _, err := reports.Lstat(runID); err == nil || !os.IsNotExist(err) {
			return nil, errors.New("run уже существует; prepare не перезаписывает его")
		}
	}
	var m Manifest
	fresh := true
	if command == "prepare" {
		m, err = snapshot(cfg)
		if err != nil {
			return nil, err
		}
		if len(m.Requirements) == 0 {
			return nil, errors.New("нет объявленных требований; это не доказательство отсутствия обязанностей")
		}
		// Check serialized bounds before publishing a new run directory.
		body, err := json.MarshalIndent(m, "", "  ")
		if err != nil || len(body)+1 > maxState {
			return nil, errors.New("manifest превышает 32 MiB")
		}
		if err := checkStateSize(newState(m)); err != nil {
			return nil, err
		}
		if err := reports.Mkdir(runID, 0700); err != nil {
			return nil, err
		}
	}
	if err := noSymlinks(reports, runID); err != nil {
		return nil, err
	}
	run, err := reports.OpenRoot(runID)
	if err != nil {
		return nil, err
	}
	defer run.Close()
	var state State
	if command == "prepare" {
		state = newState(m)
		if err := run.Mkdir("results", 0700); err != nil {
			return nil, err
		}
		if err := writeJSON(run, "manifest.json", m, 0400); err != nil {
			return nil, err
		}
		if err := saveState(run, state); err != nil {
			return nil, err
		}
	} else {
		data, err := readRoot(run, "manifest.json", maxState)
		if err != nil {
			return nil, err
		}
		if err := strictJSON(data, &m); err != nil {
			return nil, err
		}
		current, snapshotErr := snapshot(cfg)
		fresh = snapshotErr == nil && current.SnapshotID == m.SnapshotID
		if !fresh && !readOnly {
			return nil, errors.New("снимок stale; нужен новый run")
		}
		m.Config.ProjectRoot, m.Config.ReportsDir = cfg.ProjectRoot, cfg.ReportsDir
		state = newState(m)
		data, err = readRoot(run, "state.json", maxState)
		if err != nil {
			return nil, err
		}
		var savedState State
		if err := strictJSON(data, &savedState); err != nil {
			return nil, err
		}
		if len(savedState.Entries) != len(state.Entries) {
			return nil, errors.New("повреждено число заданий в state")
		}
		for i, entry := range savedState.Entries {
			state.Entries[i].Task.Attempt = entry.Task.Attempt
			if entry.Task.Attempt < 1 || !reflect.DeepEqual(entry.Task, state.Entries[i].Task) {
				return nil, errors.New("повреждено описание задания в state")
			}
			if entry.Result != nil {
				if err := validateResult(*entry.Result, entry.Task, m, fresh); err != nil {
					return nil, fmt.Errorf("сохранённый результат повреждён: %w", err)
				}
			}
			if entry.RawSHA256 != "" {
				if entry.Result == nil || !validDigest(entry.RawSHA256) {
					return nil, errors.New("неверное происхождение raw")
				}
				raw, err := readRoot(run, fmt.Sprintf("results/%s-attempt-%d.json", entry.Task.TaskID, entry.Task.Attempt), maxResult)
				var recorded Result
				if err != nil || digest(raw) != entry.RawSHA256 || strictJSON(raw, &recorded) != nil || !reflect.DeepEqual(recorded, *entry.Result) {
					return nil, errors.New("сохранённый raw не совпадает с hash/state")
				}
			}
		}
		state = savedState
	}
	switch command {
	case "prepare", "tasks":
		return TaskBatch{runID, m.SnapshotID, cfg.ProjectRoot, cfg.Runtime, pending(state), m.Files}, nil
	case "status":
		view, err := reviewContext(run, runID, m, state, fresh)
		if err != nil {
			return nil, err
		}
		status := makeStatus(runID, m, state, fresh)
		status.HostReviewState = view.State
		return status, nil
	case "review":
		if len(args) == 4 {
			return submitReview(run, runID, m, state, args[3])
		}
		return reviewContext(run, runID, m, state, fresh)
	case "report":
		report := makeReport(runID, m, state, fresh)
		view, err := reviewContext(run, runID, m, state, fresh)
		if err != nil {
			return nil, err
		}
		report.HostReview = &view.ReviewSummary
		report.HostReviewState = view.State
		report.HostReconciliationRequired = view.State != "current"
		if view.State == "current" {
			report.Conclusion = "Хост согласовал результаты по текущим свидетельствам. Это его обоснованная оценка, не автоматическое соответствие или доказательство полноты ТЗ."
		}
		var html bytes.Buffer
		if err := reportTemplate.Execute(&html, report); err != nil {
			return nil, err
		}
		if err := writeJSON(run, "report.json", report, 0600); err != nil {
			return nil, err
		}
		if err := atomicWrite(run, "report.html", html.Bytes(), 0600); err != nil {
			return nil, err
		}
		return map[string]any{"status": report.Status, "report_json": filepath.Join(cfg.ReportsDir, runID, "report.json"), "report_html": filepath.Join(cfg.ReportsDir, runID, "report.html")}, nil
	case "test":
		if !slugRE.MatchString(args[3]) {
			return nil, errors.New("недопустимый EXECUTION_ID")
		}
		for _, receipt := range state.Executions {
			if receipt.ID == args[3] {
				return nil, errors.New("EXECUTION_ID уже записан")
			}
		}
		if cfg.Runtime.Kind == "docker-php" {
			unlock, err := lockPHPTests(reports)
			if err != nil {
				return nil, err
			}
			defer unlock()
			current, err := snapshot(cfg)
			if err != nil || current.SnapshotID != m.SnapshotID {
				return nil, errors.New("snapshot изменился во время ожидания runtime")
			}
		}
		receipt, err := executeTests(cfg, m, args[3])
		if err != nil {
			return nil, err
		}
		state.Executions = append(state.Executions, receipt)
		if err := saveState(run, state); err != nil {
			return nil, err
		}
		return receipt, nil
	case "php-facts":
		envelope, err := phpFacts(cfg, m, args[3:])
		if err != nil {
			return nil, err
		}
		artifact := "php-facts-" + rand.Text() + ".json"
		if err := writeJSON(run, artifact, envelope, 0400); err != nil {
			return nil, err
		}
		return map[string]any{"snapshot_id": m.SnapshotID, "artifact": filepath.Join(cfg.ReportsDir, runID, artifact), "evidence_kind": "syntax_only"}, nil
	}
	index := -1
	for i := range state.Entries {
		if state.Entries[i].Task.TaskID == args[3] {
			index = i
		}
	}
	if index < 0 {
		return nil, errors.New("неизвестный TASK_ID")
	}
	entry := &state.Entries[index]
	if command == "retry" {
		entry.Task.Attempt++
		entry.Result = nil
		entry.RawSHA256 = ""
		if err := saveState(run, state); err != nil {
			return nil, err
		}
		return entry.Task, nil
	}
	data, err := readPath(args[4], maxResult)
	if err != nil {
		return nil, err
	}
	var result Result
	if err := strictJSON(data, &result); err != nil {
		return nil, fmt.Errorf("JSON результата: %w", err)
	}
	if err := requiredJSON(data, reflect.TypeOf(result)); err != nil {
		return nil, err
	}
	if err := validateResult(result, entry.Task, m, true); err != nil {
		return nil, err
	}
	canonical, err := json.MarshalIndent(result, "", "  ")
	if err != nil || len(canonical)+1 > maxResult {
		return nil, errors.New("нормализованный результат превышает лимит 4 MiB")
	}
	if entry.Result != nil {
		if !reflect.DeepEqual(*entry.Result, result) {
			return nil, errors.New("конфликт повторной отправки: результат этой attempt уже принят")
		}
		return map[string]any{"accepted": true, "duplicate": true, "task_id": result.TaskID, "attempt": result.Attempt}, nil
	}
	if err := noSymlinks(run, "results"); err != nil {
		return nil, err
	}
	resultPath := fmt.Sprintf("results/%s-attempt-%d.json", result.TaskID, result.Attempt)
	if prior, err := readRoot(run, resultPath, maxResult); err == nil {
		var priorResult Result
		if err := strictJSON(prior, &priorResult); err != nil || !reflect.DeepEqual(priorResult, result) {
			return nil, errors.New("конфликт с ранее записанным результатом этой attempt")
		}
		data = prior // Recover the first accepted bytes after publication interrupted before state.
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	entry.Result = &result
	entry.RawSHA256 = digest(data)
	if err := checkStateSize(state); err != nil {
		return nil, err
	}
	current, err := snapshot(cfg)
	if err != nil || current.SnapshotID != m.SnapshotID {
		return nil, errors.New("источники изменились во время submit")
	}
	if err := atomicWrite(run, resultPath, data, 0400); err != nil {
		return nil, err
	}
	if err := saveState(run, state); err != nil {
		return nil, err
	}
	return map[string]any{"accepted": true, "duplicate": false, "task_id": result.TaskID, "attempt": result.Attempt}, nil
}

func makeReport(runID string, m Manifest, state State, fresh bool) Report {
	report := Report{
		Status: makeStatus(runID, m, state, fresh), GeneratedAt: time.Now().UTC().Format(time.RFC3339), Requirements: []RequirementReport{}, Executions: append([]Receipt{}, state.Executions...),
		Conclusion:                 "Результаты ролей собраны; требуется согласование хост-сессией. Автоматический вывод о соответствии не делается.",
		HostReconciliationRequired: true,
		Accepted:                   m.Accepted,
		CompletenessBasis:          "declared_requirements_only",
		Summaries:                  []ResultSummary{},
		RawProvenance:              []RawProvenance{},
		Limitations: []string{
			"delivery_complete означает получение двух оценок каждой объявленной нормы. Полнота естественно-языкового ТЗ не доказана.",
			"Проверены структура JSON, принадлежность файлов снимку и точность цитат; смысл утверждений оценивает хост-сессия.",
			"Цитаты tests и execution receipts раздельны. passed подтверждает выполнение теста, но не силу assertion и не соответствие продукта.",
			"Роли представлены отдельно. Вердикты отдельных утверждений не переносятся на весь scope; семантическое согласование и разрешение противоречий выполняет хост-сессия.",
			"Модельные API, стоимость, usage и время inference отсутствуют; агентов запускает хост-сессия.",
		},
	}
	if m.Accepted != nil {
		report.CompletenessBasis = "accepted_requirements_only"
		report.Limitations = append(report.Limitations, "База полноты: accepted_requirements_only. История содержит также reject/defer/retire; принятие не доказывает полноту Markdown и правильность решений хоста.")
	}
	if !report.DeliveryComplete {
		report.Conclusion = "Аудит не завершён: отсутствуют результаты заданий. Вывод о соответствии невозможен."
	}
	if !fresh {
		report.Conclusion = "STALE: источники изменились. Ниже только исторические свидетельства; нужен новый run."
	}
	for i := range report.Executions {
		if !fresh || report.Executions[i].SnapshotID != m.SnapshotID {
			report.Executions[i].State = "stale"
		}
	}
	for _, entry := range state.Entries {
		if entry.Result != nil {
			report.Summaries = append(report.Summaries, ResultSummary{entry.Task.TaskID, entry.Result.Summary, entry.Result.Limitations})
			kind := "legacy_canonical"
			if entry.RawSHA256 != "" {
				kind = "exact_raw"
			}
			report.RawProvenance = append(report.RawProvenance, RawProvenance{entry.Task.TaskID, entry.Task.Attempt, kind, entry.RawSHA256})
		}
	}
	for _, req := range m.Requirements {
		row := RequirementReport{Requirement: req, Roles: []RoleReport{}}
		for _, entry := range state.Entries {
			for _, assigned := range entry.Task.Requirements {
				if assigned.ID != req.ID {
					continue
				}
				row.Scope = entry.Task.Scope
				role := RoleReport{Role: entry.Task.Role, Attempt: entry.Task.Attempt, Executions: []TestExecution{}}
				if entry.Result != nil {
					for _, assessment := range entry.Result.Assessments {
						if assessment.RequirementID != req.ID {
							continue
						}
						role.Assessment = &assessment
						for _, test := range assessment.Tests {
							latest := TestExecution{TestID: test.TestID, State: "not_recorded"}
							for _, receipt := range report.Executions {
								for _, executed := range receipt.Tests {
									if executed.ID != test.TestID {
										continue
									}
									status := executed.State
									if receipt.State == "stale" {
										status = "stale"
									}
									latest = TestExecution{test.TestID, receipt.ID, status}
								}
							}
							role.Executions = append(role.Executions, latest)
						}
					}
				}
				row.Roles = append(row.Roles, role)
			}
		}
		report.Requirements = append(report.Requirements, row)
	}
	return report
}

var reportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>spec-audit — {{.RunID}}</title><style>body{font:16px/1.55 system-ui,sans-serif;max-width:1080px;margin:2rem auto;padding:0 1rem;color:#182431;background:#f7f8fa}h1,h2,h3{line-height:1.25}section,article{background:white;border:1px solid #cdd5df;border-radius:8px;padding:1rem;margin:1rem 0}table{border-collapse:collapse}td,th{border:1px solid #cdd5df;padding:.4rem .7rem;text-align:left}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#eef1f5;padding:.8rem}code{overflow-wrap:anywhere}.missing{color:#9b2525}.meta{color:#506070;font-size:.9rem}</style></head>
<body><h1>Проверка спецификации</h1><p>{{.Conclusion}}</p>
<p class="meta">Run: {{.RunID}} · {{.GeneratedAt}}<br>Snapshot: <code>{{.SnapshotID}}</code></p>
<p>Freshness: {{.Freshness}}. Получено {{.Submitted}} из {{.Expected}} заданий. Delivery complete: {{.DeliveryComplete}}. Объявленных норм: {{.RequirementsTotal}}.</p>
<p>host_reconciliation_required: {{.HostReconciliationRequired}}. Перед решением обновите status/report: статическая страница не обнаруживает последующие изменения файлов.</p>
{{if .Pending}}<h2>Ожидаются</h2><ul>{{range .Pending}}<li>{{.TaskID}}, attempt {{.Attempt}}</li>{{end}}</ul>{{end}}
<h2>Границы проверки</h2><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>
{{if .Accepted}}<h2>История принятия индекса</h2><p>accepted_requirements_only · semantic_completeness_proven: false · {{.Accepted.Head}}</p>{{range .Accepted.History}}<h3>{{.Decision.DecisionID}}</h3><p>Raw: {{.Decision.RawSHA256}}</p>{{range .Decision.Operations}}<p>{{.Action}} · previous: {{.Previous}} · {{.Reason}}</p><ul>{{range .Targets}}<li>{{.Candidate}} · {{.Title}}</li>{{end}}</ul>{{end}}<ul>{{range .Assignments}}<li>{{.Candidate}} → {{.RequirementID}}</li>{{end}}</ul><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>{{end}}{{end}}
<h2>Итоги ролей</h2>{{range .Summaries}}<h3>{{.TaskID}}</h3><p>{{.Summary}}</p><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>{{end}}
<h2>Происхождение ответов</h2>{{range .RawProvenance}}<p>{{.TaskID}} · attempt {{.Attempt}} · {{.Kind}} · <code>{{.SHA256}}</code></p>{{end}}
{{if .HostReview}}<section><h2>Согласование хоста — {{.HostReview.State}}</h2><p>Это суждение хоста, не автоматический сертификат. При outdated ниже только история; требуется новое решение.</p>{{range .HostReview.History}}<p>{{.ReviewID}} · {{.Reviewer}} · <code>{{.RawSHA256}}</code> · {{.Summary}}</p>{{end}}{{with .HostReview.Latest}}<h3>{{.ReviewID}} — {{.Reviewer}}</h3><p>{{.Summary}}</p><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>{{range .Assessments}}<article><h3>{{.RequirementID}} — решение хоста</h3>{{template "assessment" .}}</article>{{end}}{{end}}</section>{{end}}
<h2>Фактические запуски</h2>{{range .Executions}}<p>{{.ID}} — {{.State}} · {{.Runtime}} · exit {{.ExitCode}}</p><ul>{{range .Tests}}<li>{{.ID}} — {{.State}}</li>{{end}}</ul>{{else}}<p>Нет записанного исполнения.</p>{{end}}
{{range .Requirements}}<section><h2>{{.Requirement.ID}} — {{.Requirement.Title}}</h2><p>Scope: {{.Scope}}</p>{{if .Requirement.Accepted}}<p>Редакция: {{.Requirement.Accepted.Revision}} · {{.Requirement.Accepted.Clarity}} · parents: {{.Requirement.Accepted.Parents}}</p><p>Условие: {{.Requirement.Condition}}</p><p>Требование: {{.Requirement.Statement}}</p><p>Способ проверки (хост): {{.Requirement.Verification}}</p><ul>{{range .Requirement.Accepted.Exceptions}}<li>Исключение: {{.}}</li>{{end}}{{range .Requirement.Accepted.Unresolved}}<li>Вопрос: {{.}}</li>{{end}}</ul>{{template "citations" .Requirement.Accepted.Citations}}{{else}}<pre>{{.Requirement.Source.Quote}}</pre>{{end}}
{{range .Roles}}<article><h3>{{.Role}} · attempt {{.Attempt}}</h3>{{if .Assessment}}{{template "assessment" .Assessment}}{{else}}<p class="missing">pending: результат отсутствует.</p>{{end}}
{{range .Executions}}<p>Последняя запись для {{.TestID}} — {{.State}} · receipt {{.ReceiptID}}. Полная история запусков выше.</p>{{end}}</article>{{end}}</section>{{end}}
</body></html>
{{define "citations"}}{{range .}}<p class="meta">{{.Path}}:{{.LineStart}}–{{.LineEnd}}</p><pre>{{.Quote}}</pre>{{end}}{{end}}
{{define "assessment"}}<p>Норма: {{.Specification}} · Код: {{.Implementation}} · Assertion: {{.Assertion}}</p><p>{{.Statement}}</p>
{{if .Spec}}<h5>Спецификация</h5>{{template "citations" .Spec}}{{end}}
{{if .Code}}<h5>Код</h5>{{template "citations" .Code}}{{end}}
{{if .Tests}}<h5>Текст тестов — не факт исполнения</h5>{{range .Tests}}<p>{{.TestID}} · {{.Citation.Path}}:{{.Citation.LineStart}}–{{.Citation.LineEnd}}</p><pre>{{.Citation.Quote}}</pre>{{end}}{{end}}
{{if .Limitations}}<h4>Ограничения оценки</h4><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>{{end}}{{end}}`))
