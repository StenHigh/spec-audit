package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

//go:embed sdk.php
var sdk []byte

//go:embed envelope.schema.json
var schemaBytes []byte

const maxOutput = 4 << 20

func digest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func verifyQuote(source []byte, start, stop int, quote string) error {
	if start < 0 || stop < start || stop > len(source) || !utf8.Valid(source[start:stop]) || string(source[start:stop]) != quote {
		return errors.New("citation does not match the exact source byte range")
	}
	return nil
}

type span struct {
	Kind        string
	Start, Stop int
	Quote       string
}
type document struct {
	Path           string         `json:"path"`
	SHA            string         `json:"sha256"`
	Bytes          int            `json:"bytes"`
	Nodes          map[string]int `json:"nodes"`
	Segments       int            `json:"segments"`
	GeneratedNodes map[string]int `json:"nodes_without_own_position"`
	PaddedSegments int            `json:"padded_or_synthetic_newline_segments"`
	Spans          []span         `json:"-"`
}

func parseMarkdown(path string, source []byte) (document, error) {
	result := document{Path: path, SHA: digest(source), Bytes: len(source), Nodes: map[string]int{}, GeneratedNodes: map[string]int{}}
	if !utf8.Valid(source) {
		return result, errors.New("Markdown is not valid UTF-8")
	}
	tree := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	add := func(kind string, segment text.Segment) error {
		if segment.Start < 0 || segment.Stop < segment.Start || segment.Stop > len(source) {
			return fmt.Errorf("invalid %s byte range %d:%d", kind, segment.Start, segment.Stop)
		}
		// Cite raw bytes, never Segment.Value(): it may add padding or a synthetic final newline.
		quote := source[segment.Start:segment.Stop]
		if !utf8.Valid(quote) {
			return fmt.Errorf("range splits UTF-8 in %s", kind)
		}
		result.Spans = append(result.Spans, span{kind, segment.Start, segment.Stop, string(quote)})
		result.Segments++
		if segment.Padding != 0 || segment.ForceNewline {
			result.PaddedSegments++
		}
		return nil
	}
	err := ast.Walk(tree, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		kind := node.Kind().String()
		result.Nodes[kind]++
		if node.Pos() < 0 {
			result.GeneratedNodes[kind]++
		} else if node.Pos() > len(source) {
			return ast.WalkStop, fmt.Errorf("invalid node position %s", kind)
		}
		if node.Type() == ast.TypeBlock {
			for i := 0; i < node.Lines().Len(); i++ {
				if err := add(kind, node.Lines().At(i)); err != nil {
					return ast.WalkStop, err
				}
			}
		}
		switch n := node.(type) {
		case *ast.Text:
			if err := add(kind, n.Segment); err != nil {
				return ast.WalkStop, err
			}
		case *ast.RawHTML:
			for i := 0; i < n.Segments.Len(); i++ {
				if err := add(kind, n.Segments.At(i)); err != nil {
					return ast.WalkStop, err
				}
			}
		}
		return ast.WalkContinue, nil
	})
	return result, err
}

func scan(root string) (any, error) {
	started := time.Now()
	docs := []document{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink outside scan contract: %s", path)
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		doc, err := parseMarkdown(filepath.ToSlash(relative), source)
		if err != nil {
			return fmt.Errorf("%s: %w", relative, err)
		}
		docs = append(docs, doc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, errors.New("empty Markdown corpus")
	}
	return map[string]any{"platform": runtime.GOOS + "/" + runtime.GOARCH, "files": len(docs), "seconds": time.Since(started).Seconds(), "documents": docs, "limit": "structural parse and raw ranges, not semantic requirement extraction"}, nil
}

func envelopeSchema() (*jsonschema.Schema, error) {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("urn:pilot:sdk", value); err != nil {
		return nil, err
	}
	return compiler.Compile("urn:pilot:sdk")
}

func validateEnvelope(body []byte) error {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	schema, err := envelopeSchema()
	if err != nil {
		return err
	}
	return schema.Validate(value)
}

func verifyEnvelopeSources(body []byte, root string) (any, error) {
	if err := validateEnvelope(body); err != nil {
		return nil, err
	}
	var envelope struct {
		Files []struct {
			Path, SHA256 string
			Bytes        int
		}
		Facts []struct {
			File, Quote       string
			Start, Stop, Line int
			SHA               string `json:"source_sha256"`
		}
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	sources := map[string][]byte{}
	for _, file := range envelope.Files {
		if !filepath.IsLocal(file.Path) {
			return nil, errors.New("non-local source path")
		}
		path, err := filepath.EvalSymlinks(filepath.Join(root, file.Path))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, errors.New("source outside root")
		}
		if _, exists := sources[file.Path]; exists {
			return nil, errors.New("duplicate source")
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if digest(source) != file.SHA256 || len(source) != file.Bytes {
			return nil, errors.New("source snapshot changed")
		}
		sources[file.Path] = source
	}
	for _, fact := range envelope.Facts {
		source, found := sources[fact.File]
		if !found || digest(source) != fact.SHA {
			return nil, errors.New("fact has no matching source")
		}
		if err := verifyQuote(source, fact.Start, fact.Stop, fact.Quote); err != nil {
			return nil, err
		}
		if fact.Stop <= fact.Start || bytes.Count(source[:fact.Start], []byte("\n"))+1 != fact.Line {
			return nil, errors.New("invalid fact line or range")
		}
	}
	return map[string]any{"passed": true, "files": len(sources), "facts": len(envelope.Facts)}, nil
}

type boundedBuffer struct{ buffer bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > maxOutput {
		return 0, errors.New("process output exceeds limit")
	}
	return b.buffer.Write(p)
}

func runProcess(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdin = bytes.NewReader(stdin)
	command.WaitDelay = time.Second
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return nil, stderr.buffer.String(), ctx.Err()
	}
	if err != nil {
		return nil, stderr.buffer.String(), err
	}
	return stdout.buffer.Bytes(), stderr.buffer.String(), nil
}

func bridge(project, root string, files []string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	ids, _, err := runProcess(ctx, project, nil, "docker", "compose", "ps", "-q", "app")
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(string(ids))
	if id == "" || strings.Contains(id, "\n") {
		return nil, errors.New("expected exactly one running app container")
	}
	mountJSON, _, err := runProcess(ctx, project, nil, "docker", "inspect", id, "--format", "{{json .Mounts}}")
	if err != nil {
		return nil, err
	}
	var mounts []struct{ Source, Destination string }
	if err = json.Unmarshal(mountJSON, &mounts); err != nil {
		return nil, err
	}
	wanted, err := filepath.EvalSymlinks(project)
	if err != nil {
		return nil, err
	}
	found := false
	for _, mount := range mounts {
		actual, _ := filepath.EvalSymlinks(mount.Source)
		if actual == wanted && mount.Destination == "/var/www/html" {
			found = true
		}
	}
	if !found {
		return nil, errors.New("app container does not mount this checkout")
	}
	request, err := json.Marshal(map[string]any{"root": root, "files": files})
	if err != nil {
		return nil, err
	}
	// Container-side timeout is essential: killing Docker CLI alone need not stop remote PHP.
	body, stderr, err := runProcess(ctx, project, sdk, "docker", "compose", "exec", "-T", "app", "timeout", "-s", "TERM", "-k", "1", "30", "php", "-r", "eval(substr(stream_get_contents(STDIN), 5));", "--", string(request))
	if err != nil {
		return nil, fmt.Errorf("container SDK failed: %w (stderr bytes=%d)", err, len(stderr))
	}
	if err = validateEnvelope(body); err != nil {
		return nil, fmt.Errorf("invalid SDK envelope: %w", err)
	}
	var result any
	err = json.Unmarshal(body, &result)
	return result, err
}

// ponytail: hash every file in the selected snapshot; no speculative dependency graph or narrow invalidation.
func snapshotKey(root string, profile map[string]string) (string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in snapshot")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = digest(body)
		return nil
	})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal([]any{files, profile})
	return digest(data), err
}

type cacheRecord struct {
	Key     string          `json:"key"`
	Hash    string          `json:"hash"`
	Payload json.RawMessage `json:"payload"`
}

func cacheValue(path, key string, produce func() ([]byte, error)) ([]byte, bool, error) {
	if stored, err := os.ReadFile(path); err == nil {
		var record cacheRecord
		if err = json.Unmarshal(stored, &record); err != nil {
			return nil, false, err
		}
		if digest(record.Payload) != record.Hash {
			return nil, false, errors.New("cache payload corrupted")
		}
		if record.Key == key {
			return record.Payload, true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	body, err := produce()
	if err != nil {
		return nil, false, err
	}
	if !json.Valid(body) {
		return nil, false, errors.New("producer emitted invalid JSON")
	}
	var compact bytes.Buffer
	if err = json.Compact(&compact, body); err != nil {
		return nil, false, err
	}
	body = compact.Bytes()
	encoded, err := json.Marshal(cacheRecord{key, digest(body), body})
	if err != nil {
		return nil, false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "cache-*.tmp")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(encoded); err != nil {
		temp.Close()
		return nil, false, err
	}
	if err = temp.Close(); err != nil {
		return nil, false, err
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return nil, false, err
	}
	return body, false, nil
}

func fetchJSON(ctx context.Context, client *http.Client, url string) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxOutput+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if len(body) > maxOutput {
		return nil, errors.New("HTTP output exceeds limit")
	}
	var result map[string]any
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if result["status"] != "ok" {
		return nil, errors.New("model response is not successful")
	}
	return result, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pilot {selfcheck|scan|verify-sdk|bridge|fixture-process}")
		os.Exit(2)
	}
	var result any
	var err error
	switch os.Args[1] {
	case "selfcheck":
		result, err = selfcheck()
	case "scan":
		if len(os.Args) != 3 {
			err = errors.New("scan requires a corpus directory")
		} else {
			result, err = scan(os.Args[2])
		}
	case "bridge":
		flags := flag.NewFlagSet("bridge", flag.ContinueOnError)
		project := flags.String("project", "", "host checkout")
		root := flags.String("root", "/var/www/html", "container source root")
		if err = flags.Parse(os.Args[2:]); err == nil {
			if *project == "" || len(flags.Args()) == 0 {
				err = errors.New("project and files required")
			} else {
				result, err = bridge(*project, *root, flags.Args())
			}
		}
	case "verify-sdk":
		if len(os.Args) != 4 {
			err = errors.New("verify-sdk requires envelope and host source root")
		} else {
			var body []byte
			body, err = os.ReadFile(os.Args[2])
			if err == nil {
				result, err = verifyEnvelopeSources(body, os.Args[3])
			}
		}
	case "fixture-process":
		fixtureProcess()
		return
	default:
		err = errors.New("unknown command")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if checks, ok := result.(map[string]any); ok && checks["passed"] == false {
		os.Exit(1)
	}
}
