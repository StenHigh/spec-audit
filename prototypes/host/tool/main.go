//go:build darwin || linux

package main

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
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
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
	refRE  = regexp.MustCompile(`^(.+):([1-9][0-9]*)-([1-9][0-9]*)$`)
)

type Runtime struct {
	Kind    string `yaml:"kind" json:"kind"`
	Service string `yaml:"service" json:"service"`
}

type Scope struct {
	ID       string   `yaml:"id" json:"id"`
	Focus    string   `yaml:"focus" json:"focus"`
	SpecRefs []string `yaml:"spec_refs" json:"spec_refs"`
}

type Config struct {
	Version     int      `yaml:"version" json:"version"`
	ProjectRoot string   `yaml:"project_root" json:"project_root"`
	Specs       []string `yaml:"specs" json:"specs"`
	Code        []string `yaml:"code" json:"code"`
	Tests       []string `yaml:"tests" json:"tests"`
	ReportsDir  string   `yaml:"reports_dir" json:"reports_dir"`
	Runtime     Runtime  `yaml:"runtime" json:"runtime"`
	Scopes      []Scope  `yaml:"scopes" json:"scopes"`
}

type SourceFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type Manifest struct {
	Version    int          `json:"version"`
	Config     Config       `json:"config"`
	Files      []SourceFile `json:"files"`
	SnapshotID string       `json:"snapshot_id"`
}

type Task struct {
	TaskID     string   `json:"task_id"`
	Attempt    int      `json:"attempt"`
	SnapshotID string   `json:"snapshot_id"`
	Role       string   `json:"role"`
	Scope      string   `json:"scope"`
	Focus      string   `json:"focus"`
	SpecRefs   []string `json:"spec_refs"`
}

type Citation struct {
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Quote     string `json:"quote"`
}

type Observation struct {
	Key       string     `json:"key"`
	Verdict   string     `json:"verdict"`
	Statement string     `json:"statement"`
	Spec      []Citation `json:"spec"`
	Code      []Citation `json:"code"`
	Tests     []Citation `json:"tests"`
}

type Result struct {
	TaskID       string        `json:"task_id"`
	Attempt      int           `json:"attempt"`
	SnapshotID   string        `json:"snapshot_id"`
	Role         string        `json:"role"`
	Scope        string        `json:"scope"`
	Summary      string        `json:"summary"`
	Observations []Observation `json:"observations"`
	Limitations  []string      `json:"limitations"`
}

type Entry struct {
	Task   Task    `json:"task"`
	Result *Result `json:"result,omitempty"`
}

type State struct {
	Entries []Entry `json:"entries"`
}

type TaskBatch struct {
	RunID       string  `json:"run_id"`
	SnapshotID  string  `json:"snapshot_id"`
	ProjectRoot string  `json:"project_root"`
	Runtime     Runtime `json:"runtime"`
	Tasks       []Task  `json:"tasks"`
}

type Status struct {
	RunID      string         `json:"run_id"`
	SnapshotID string         `json:"snapshot_id"`
	Complete   bool           `json:"complete"`
	Expected   int            `json:"expected"`
	Submitted  int            `json:"submitted"`
	Pending    []Task         `json:"pending"`
	Counts     map[string]int `json:"counts"`
}

type ScopeReport struct {
	ID                         string         `json:"id"`
	Focus                      string         `json:"focus"`
	Complete                   bool           `json:"complete"`
	Counts                     map[string]int `json:"counts"`
	Conclusion                 string         `json:"conclusion"`
	Mapper                     *Result        `json:"mapper"`
	Redteam                    *Result        `json:"redteam"`
	HostReconciliationRequired bool           `json:"host_reconciliation_required"`
}

type Report struct {
	Status
	GeneratedAt                string        `json:"generated_at"`
	Conclusion                 string        `json:"conclusion"`
	Limitations                []string      `json:"limitations"`
	Scopes                     []ScopeReport `json:"scopes"`
	HostReconciliationRequired bool          `json:"host_reconciliation_required"`
}

func main() {
	value, err := execute(os.Args[1:])
	if err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"error": err.Error()})
		os.Exit(1)
	}
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
	f, err := os.Open(path)
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

func loadConfig(path string) (Config, error) {
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
		return cfg, fmt.Errorf("YAML: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return cfg, errors.New("разрешён ровно один YAML-документ")
	}
	if cfg.Version != 1 || cfg.Runtime.Kind != "docker" || cfg.Runtime.Service != "app" || len(cfg.Scopes) != 2 {
		return cfg, errors.New("пилот требует version: 1, runtime docker/app и ровно два scope")
	}
	cfg.ProjectRoot, err = canonicalPath(cfg.ProjectRoot)
	if err != nil {
		return cfg, fmt.Errorf("project_root: %w", err)
	}
	info, err := os.Stat(cfg.ProjectRoot)
	if err != nil || !info.IsDir() {
		return cfg, errors.New("project_root должен быть существующим каталогом")
	}
	cfg.ReportsDir, err = canonicalPath(cfg.ReportsDir)
	if err != nil {
		return cfg, fmt.Errorf("reports_dir: %w", err)
	}
	if within(cfg.ProjectRoot, cfg.ReportsDir) || within(cfg.ReportsDir, cfg.ProjectRoot) {
		return cfg, errors.New("каталоги reports_dir и project_root не должны содержать друг друга")
	}
	var directories []string
	for _, paths := range [][]string{cfg.Specs, cfg.Code, cfg.Tests} {
		if len(paths) == 0 {
			return cfg, errors.New("specs, code и tests должны быть непустыми списками")
		}
		sort.Strings(paths)
		for _, path := range paths {
			if !localPath(path) {
				return cfg, fmt.Errorf("недопустимый относительный каталог: %s", path)
			}
			for _, prior := range directories {
				if within(prior, path) || within(path, prior) {
					return cfg, fmt.Errorf("повторяющиеся или пересекающиеся каталоги: %s, %s", prior, path)
				}
			}
			directories = append(directories, path)
		}
	}
	ids := map[string]bool{}
	for i := range cfg.Scopes {
		scope := &cfg.Scopes[i]
		if !slugRE.MatchString(scope.ID) || ids[scope.ID] || strings.TrimSpace(scope.Focus) == "" || len(scope.SpecRefs) == 0 {
			return cfg, errors.New("scope требует уникальный id, focus и непустой spec_refs")
		}
		ids[scope.ID] = true
		sort.Strings(scope.SpecRefs)
		for j, ref := range scope.SpecRefs {
			if _, err := parseRef(ref); err != nil || (j > 0 && scope.SpecRefs[j-1] == ref) {
				return cfg, fmt.Errorf("недопустимый или повторный spec_ref: %s", ref)
			}
		}
	}
	sort.Slice(cfg.Scopes, func(i, j int) bool { return cfg.Scopes[i].ID < cfg.Scopes[j].ID })
	return cfg, nil
}

func parseRef(ref string) (Citation, error) {
	m := refRE.FindStringSubmatch(ref)
	if m == nil || !localPath(m[1]) {
		return Citation{}, errors.New("spec_ref должен иметь вид путь:начало-конец")
	}
	start, err1 := strconv.Atoi(m[2])
	end, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || end < start {
		return Citation{}, errors.New("недопустимый диапазон строк")
	}
	return Citation{Path: m[1], LineStart: start, LineEnd: end}, nil
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
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
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
	m := Manifest{Version: 1, Config: cfg, Files: []SourceFile{}}
	root, err := os.OpenRoot(cfg.ProjectRoot)
	if err != nil {
		return m, err
	}
	defer root.Close()
	total := 0
	for _, group := range []struct {
		kind  string
		paths []string
	}{{"spec", cfg.Specs}, {"code", cfg.Code}, {"tests", cfg.Tests}} {
		before := len(m.Files)
		for _, directory := range group.paths {
			if err := noSymlinks(root, directory); err != nil {
				return m, err
			}
			info, err := root.Stat(directory)
			if err != nil || !info.IsDir() {
				return m, fmt.Errorf("источник должен быть каталогом: %s", directory)
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
				data, err := readRoot(root, path, maxFile)
				if err != nil {
					return err
				}
				total += len(data)
				if total > 512<<20 || len(m.Files) >= 25000 {
					return errors.New("снимок превышает 512 MiB или 25000 файлов")
				}
				m.Files = append(m.Files, SourceFile{path, group.kind, digest(data), len(data)})
				return nil
			})
			if err != nil {
				return m, err
			}
		}
		if len(m.Files) == before {
			return m, fmt.Errorf("пустая группа источников: %s", group.kind)
		}
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	for _, scope := range cfg.Scopes {
		for _, ref := range scope.SpecRefs {
			citation, _ := parseRef(ref)
			file, ok := findSource(m, citation.Path)
			if !ok || file.Kind != "spec" {
				return m, fmt.Errorf("spec_ref отсутствует в specs: %s", ref)
			}
			data, err := readRoot(root, file.Path, maxFile)
			if err != nil {
				return m, err
			}
			if _, err = lineQuote(data, citation.LineStart, citation.LineEnd); err != nil {
				return m, fmt.Errorf("spec_ref %s: %w", ref, err)
			}
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	m.SnapshotID = digest(data)
	return m, nil
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
				return fmt.Errorf("повторный или недопустимый JSON-ключ: %v", token)
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
		return err
	}
	if _, err := keys.Token(); err != io.EOF {
		return errors.New("разрешён ровно один JSON-документ")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
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

func validateResult(result Result, task Task, m Manifest) error {
	if result.TaskID != task.TaskID || result.Scope != task.Scope || result.Role != task.Role {
		return errors.New("task_id, scope или role не совпадают с заданием")
	}
	if result.Attempt != task.Attempt {
		return errors.New("устаревшая или неверная attempt")
	}
	if result.SnapshotID != task.SnapshotID {
		return errors.New("неверный snapshot_id")
	}
	if strings.TrimSpace(result.Summary) == "" || len(result.Observations) > 256 || (result.Role == "mapper" && len(result.Observations) == 0) {
		return errors.New("результат требует summary и не более 256 observations; mapper требует хотя бы одно наблюдение")
	}
	if len(result.Observations) == 0 && len(result.Limitations) == 0 {
		return errors.New("пустой redteam требует явно указать limitation")
	}
	for _, limitation := range result.Limitations {
		if strings.TrimSpace(limitation) == "" {
			return errors.New("limitation не может быть пустой строкой")
		}
	}
	root, err := os.OpenRoot(m.Config.ProjectRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := map[string]bool{}
	for _, observation := range result.Observations {
		if !slugRE.MatchString(observation.Key) || seen[observation.Key] || strings.TrimSpace(observation.Statement) == "" {
			return errors.New("observation требует уникальный key и statement")
		}
		seen[observation.Key] = true
		switch observation.Verdict {
		case "supported", "gap":
			if len(observation.Spec) == 0 || len(observation.Code) == 0 {
				return errors.New("supported и gap требуют spec и code")
			}
		case "insufficient", "unspecified":
		default:
			return fmt.Errorf("неизвестный verdict: %s", observation.Verdict)
		}
		for _, group := range []struct {
			kind      string
			citations []Citation
		}{{"spec", observation.Spec}, {"code", observation.Code}, {"tests", observation.Tests}} {
			if len(group.citations) > 128 {
				return errors.New("слишком много цитат в observation")
			}
			for _, citation := range group.citations {
				file, ok := findSource(m, citation.Path)
				if !localPath(citation.Path) || !ok || file.Kind != group.kind {
					return fmt.Errorf("цитата вне группы %s: %s", group.kind, citation.Path)
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
	if err = root.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func writeJSON(root *os.Root, path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(root, path, append(data, '\n'), mode)
}

func saveState(root *os.Root, state State) error {
	// Reports are disposable projections; invalidate them before changing authoritative state.
	for _, path := range []string{"report.json", "report.html"} {
		if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return writeJSON(root, "state.json", state, 0600)
}

func newState(m Manifest) State {
	state := State{Entries: []Entry{}}
	for _, scope := range m.Config.Scopes {
		for _, role := range []string{"mapper", "redteam"} {
			state.Entries = append(state.Entries, Entry{Task: Task{
				TaskID: scope.ID + "-" + role, Attempt: 1, SnapshotID: m.SnapshotID,
				Role: role, Scope: scope.ID, Focus: scope.Focus, SpecRefs: scope.SpecRefs,
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

func emptyCounts() map[string]int {
	return map[string]int{"supported": 0, "gap": 0, "insufficient": 0, "unspecified": 0}
}

func makeStatus(runID string, m Manifest, state State) Status {
	status := Status{RunID: runID, SnapshotID: m.SnapshotID, Expected: len(state.Entries), Pending: pending(state), Counts: emptyCounts()}
	status.Submitted = status.Expected - len(status.Pending)
	status.Complete = len(status.Pending) == 0
	for _, entry := range state.Entries {
		if entry.Result != nil {
			for _, observation := range entry.Result.Observations {
				status.Counts[observation.Verdict]++
			}
		}
	}
	return status
}

func execute(args []string) (any, error) {
	if len(args) < 3 {
		return nil, errors.New("команды: prepare|tasks|status|report CONFIG RUN_ID; submit CONFIG RUN_ID TASK_ID RESULT_PATH; retry CONFIG RUN_ID TASK_ID")
	}
	command, runID := args[0], args[2]
	argc := map[string]int{"prepare": 3, "tasks": 3, "status": 3, "report": 3, "submit": 5, "retry": 4}
	if count, ok := argc[command]; !ok || len(args) != count || !slugRE.MatchString(runID) {
		return nil, errors.New("неизвестная команда, неверные аргументы или недопустимый RUN_ID")
	}
	cfg, err := loadConfig(args[1])
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
	m, err := snapshot(cfg)
	if err != nil {
		return nil, err
	}
	if command == "prepare" {
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
	state := newState(m)
	if command == "prepare" {
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
		var saved Manifest
		if err := strictJSON(data, &saved); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(m, saved) {
			return nil, errors.New("снимок источников или конфигурация изменились; нужен новый run")
		}
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
				if err := validateResult(*entry.Result, entry.Task, m); err != nil {
					return nil, fmt.Errorf("сохранённый результат повреждён: %w", err)
				}
			}
		}
		state = savedState
	}
	switch command {
	case "prepare", "tasks":
		return TaskBatch{runID, m.SnapshotID, cfg.ProjectRoot, cfg.Runtime, pending(state)}, nil
	case "status":
		return makeStatus(runID, m, state), nil
	case "report":
		report := makeReport(runID, m, state)
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
	if err := validateResult(result, entry.Task, m); err != nil {
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
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := writeJSON(run, resultPath, result, 0400); err != nil {
		return nil, err
	}
	entry.Result = &result
	if err := saveState(run, state); err != nil {
		return nil, err
	}
	return map[string]any{"accepted": true, "duplicate": false, "task_id": result.TaskID, "attempt": result.Attempt}, nil
}

func makeReport(runID string, m Manifest, state State) Report {
	report := Report{
		Status: makeStatus(runID, m, state), GeneratedAt: time.Now().UTC().Format(time.RFC3339), Scopes: []ScopeReport{},
		Conclusion:                 "Результаты ролей собраны; требуется согласование хост-сессией. Автоматический вывод о соответствии не делается.",
		HostReconciliationRequired: true,
		Limitations: []string{
			"complete означает только получение обеих ролей для каждого scope; это не PASS и не доказательство полноты аудита.",
			"Проверены структура JSON, принадлежность файлов снимку и точность цитат; смысл утверждений оценивает хост-сессия.",
			"Цитаты tests показывают текст тестов, а не факт их выполнения. CLI не запускает тесты проекта.",
			"Роли представлены отдельно. Вердикты отдельных утверждений не переносятся на весь scope; семантическое согласование и разрешение противоречий выполняет хост-сессия.",
			"Модельные API, стоимость, usage и время inference отсутствуют; агентов запускает хост-сессия.",
		},
	}
	if !report.Complete {
		report.Conclusion = "Аудит не завершён: отсутствуют результаты заданий. Вывод о соответствии невозможен."
	}
	for _, scope := range m.Config.Scopes {
		sr := ScopeReport{ID: scope.ID, Focus: scope.Focus, Counts: emptyCounts(), HostReconciliationRequired: true}
		for _, entry := range state.Entries {
			if entry.Task.Scope != scope.ID || entry.Result == nil {
				continue
			}
			if entry.Task.Role == "mapper" {
				sr.Mapper = entry.Result
			} else {
				sr.Redteam = entry.Result
			}
			for _, observation := range entry.Result.Observations {
				sr.Counts[observation.Verdict]++
			}
		}
		sr.Complete = sr.Mapper != nil && sr.Redteam != nil
		if !sr.Complete {
			sr.Conclusion = "Не завершено: нет результата одной или обеих ролей."
		} else {
			sr.Conclusion = "Обе роли получены; их утверждения требуют согласования хост-сессией. Соответствие scope автоматически не устанавливается."
		}
		report.Scopes = append(report.Scopes, sr)
	}
	return report
}

var reportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Пилот — {{.RunID}}</title><style>body{font:16px/1.55 system-ui,sans-serif;max-width:1080px;margin:2rem auto;padding:0 1rem;color:#182431;background:#f7f8fa}h1,h2,h3{line-height:1.25}section,article{background:white;border:1px solid #cdd5df;border-radius:8px;padding:1rem;margin:1rem 0}table{border-collapse:collapse}td,th{border:1px solid #cdd5df;padding:.4rem .7rem;text-align:left}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#eef1f5;padding:.8rem}code{overflow-wrap:anywhere}.missing{color:#9b2525}.meta{color:#506070;font-size:.9rem}</style></head>
<body><h1>Пилот проверки спецификации</h1><p>{{.Conclusion}}</p>
<p class="meta">Run: {{.RunID}} · {{.GeneratedAt}}<br>Snapshot: <code>{{.SnapshotID}}</code></p>
<p>Получено {{.Submitted}} из {{.Expected}} заданий. Completeness: {{.Complete}}.</p>
<table><thead><tr><th>Вердикт наблюдения</th><th>Количество</th></tr></thead><tbody>{{range $key,$value := .Counts}}<tr><td>{{$key}}</td><td>{{$value}}</td></tr>{{end}}</tbody></table>
{{if .Pending}}<h2>Ожидаются</h2><ul>{{range .Pending}}<li>{{.TaskID}}, attempt {{.Attempt}}</li>{{end}}</ul>{{end}}
<h2>Границы проверки</h2><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>
{{range .Scopes}}<section><h2>{{.ID}}</h2><p>{{.Focus}}</p><p>{{.Conclusion}}</p>
<p>host_reconciliation_required: {{.HostReconciliationRequired}}</p>
<h3>Mapper</h3>{{if .Mapper}}{{template "result" .Mapper}}{{else}}<p class="missing">Результат отсутствует.</p>{{end}}
<h3>Redteam</h3>{{if .Redteam}}{{template "result" .Redteam}}{{else}}<p class="missing">Результат отсутствует.</p>{{end}}</section>{{end}}
</body></html>
{{define "citations"}}{{range .}}<p class="meta">{{.Path}}:{{.LineStart}}–{{.LineEnd}}</p><pre>{{.Quote}}</pre>{{end}}{{end}}
{{define "result"}}<p>{{.Summary}}</p><p class="meta">{{.TaskID}}, attempt {{.Attempt}}</p>
{{range .Observations}}<article><h4>{{.Key}} — {{.Verdict}}</h4><p>{{.Statement}}</p>
{{if .Spec}}<h5>Спецификация</h5>{{template "citations" .Spec}}{{end}}
{{if .Code}}<h5>Код</h5>{{template "citations" .Code}}{{end}}
{{if .Tests}}<h5>Текст тестов; выполнение не подтверждено</h5>{{template "citations" .Tests}}{{end}}</article>{{end}}
{{if .Limitations}}<h4>Ограничения роли</h4><ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>{{end}}{{end}}`))
