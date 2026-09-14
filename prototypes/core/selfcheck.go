package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type checkResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

func fixtureProcess() {
	if len(os.Args) < 3 {
		os.Exit(2)
	}
	switch os.Args[2] {
	case "sleep":
		time.Sleep(5 * time.Second)
	case "failure":
		fmt.Fprintln(os.Stdout, `{"status":"ok"}`)
		os.Exit(7)
	case "large":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), maxOutput+1))
	case "echo":
		fmt.Fprintln(os.Stderr, "fixture diagnostic")
		if err := json.NewEncoder(os.Stdout).Encode(os.Args[3:]); err != nil {
			os.Exit(1)
		}
	default:
		os.Exit(2)
	}
}

func selfcheck() (any, error) {
	started := time.Now()
	checks := []checkResult{}
	record := func(name string, passed bool, detail string) {
		checks = append(checks, checkResult{name, passed, detail})
	}
	fixtures := []struct {
		Name, Text string
		Need       []string
	}{
		{"unicode_and_gfm", "# Требования 😀\r\n\r\n> Система ДОЛЖНА сохранить `0012`.\r\n\r\n- [x] Проверить\r\n  - Вложенное условие\r\n\r\n| Значение | Результат |\r\n|---|---|\r\n| 0012 | отдельно от 12 |\r\n\r\n```json\r\n{\"status\":\"SUCCESS\"}\r\n```\r\n\r\n[ссылка](https://example.invalid/spec) и **норма**.\r\n", []string{"Heading", "Blockquote", "List", "Table", "FencedCodeBlock", "Link", "Emphasis", "TaskCheckBox"}},
		{"eof_code", "```php\n    $x = 'текст😀';", []string{"FencedCodeBlock"}},
		{"tabs_and_lists", "- Условие\n\n\tВложенная строка\n\n\t\tкод\n", []string{"List"}},
		{"references_and_html", "[ref]: https://example.invalid \"Заголовок\"\n\n[ссылка][ref]\n\n<!-- комментарий -->\n\n<span>Текст &amp; 😀</span>\n", []string{"Link", "HTMLBlock", "RawHTML"}},
	}
	for _, fixture := range fixtures {
		doc, err := parseMarkdown(fixture.Name, []byte(fixture.Text))
		record("markdown_"+fixture.Name, err == nil, fmt.Sprint(err))
		if err != nil {
			continue
		}
		for _, kind := range fixture.Need {
			record(fixture.Name+"_"+kind, doc.Nodes[kind] > 0, "")
		}
		for _, segment := range doc.Spans {
			if fixture.Text[segment.Start:segment.Stop] != segment.Quote {
				record("raw_quote_exact", false, fixture.Name)
			}
		}
	}
	doc, err := parseMarkdown("citation", []byte("# 😀\r\n\r\nСистема ДОЛЖНА хранить 0012.\r\n"))
	if err != nil {
		return nil, err
	}
	found := false
	for _, segment := range doc.Spans {
		if segment.Kind == "Paragraph" {
			found = segment.Start == len("# 😀\r\n\r\n") && segment.Quote == "Система ДОЛЖНА хранить 0012."
		}
	}
	record("utf8_byte_offset_after_emoji_and_crlf", found, "exact paragraph slice; inline Text nodes may split the same sentence")
	citationSource := []byte("😀\r\nСистема ДОЛЖНА хранить 0012.")
	citationStart := len("😀\r\n")
	record("citation_exact_accepted", verifyQuote(citationSource, citationStart, len(citationSource), "Система ДОЛЖНА хранить 0012.") == nil, "")
	record("citation_altered_text_rejected", verifyQuote(citationSource, citationStart, len(citationSource), "Система ДОЛЖНА хранить 12.") != nil, "")
	record("citation_shifted_offset_rejected", verifyQuote(citationSource, citationStart+1, len(citationSource), "Система ДОЛЖНА хранить 0012.") != nil, "")
	record("citation_out_of_bounds_rejected", verifyQuote(citationSource, citationStart, len(citationSource)+1, "") != nil, "")
	record("citation_split_utf8_rejected", verifyQuote(citationSource, 1, 4, string(citationSource[1:4])) != nil, "")
	_, err = parseMarkdown("invalid", []byte{0xff, 0xfe})
	record("invalid_utf8_rejected", err != nil, "")

	valid := map[string]any{"version": "sdk/1", "evidence_kind": "syntax_only", "runtime": map[string]any{"php": "8.4", "os": "Linux", "arch": "aarch64"}, "files": []any{map[string]any{"path": "x.php", "sha256": strings.Repeat("a", 64), "bytes": 5}}, "facts": []any{}}
	body, err := json.Marshal(valid)
	if err != nil {
		return nil, err
	}
	record("schema_valid", validateEnvelope(body) == nil, "")
	for _, name := range []string{"version", "missing", "type", "extra", "range"} {
		var changed map[string]any
		if err = json.Unmarshal(body, &changed); err != nil {
			return nil, err
		}
		switch name {
		case "version":
			changed["version"] = "sdk/2"
		case "missing":
			delete(changed, "facts")
		case "type":
			changed["files"] = "not an array"
		case "extra":
			changed["new_key"] = true
		case "range":
			changed["files"].([]any)[0].(map[string]any)["bytes"] = -1
		}
		invalid, _ := json.Marshal(changed)
		record("schema_reject_"+name, validateEnvelope(invalid) != nil, "")
	}
	record("schema_reject_trailing_json", validateEnvelope(append(append([]byte{}, body...), []byte(" {}")...)) != nil, "")

	temp, err := os.MkdirTemp("", "spec-binary-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	snapshot := filepath.Join(temp, "snapshot")
	if err = os.Mkdir(snapshot, 0700); err != nil {
		return nil, err
	}
	initial := map[string]string{"requirement.txt": "при x MUST y", "implementation.php": "return 1;", "test.php": "assertSame(1, run());", "composer.lock": "version-a", "config.json": "{}"}
	for path, content := range initial {
		if err = os.WriteFile(filepath.Join(snapshot, path), []byte(content), 0600); err != nil {
			return nil, err
		}
	}
	profile := map[string]string{"model": "fixture-model", "prompt": "fixture-v1", "adapter": digest(sdk), "parser": "goldmark-1.8.6"}
	key, err := snapshotKey(snapshot, profile)
	if err != nil {
		return nil, err
	}
	produced := 0
	produce := func() ([]byte, error) { produced++; return []byte(fmt.Sprintf(`{"generation":%d}`, produced)), nil }
	cache := filepath.Join(temp, "cache.json")
	_, hit, err := cacheValue(cache, key, produce)
	record("cache_cold", err == nil && !hit && produced == 1, fmt.Sprint(err))
	_, hit, err = cacheValue(cache, key, produce)
	record("cache_warm", err == nil && hit && produced == 1, fmt.Sprint(err))
	for path, content := range initial {
		if err = os.WriteFile(filepath.Join(snapshot, path), []byte(content+" changed"), 0600); err != nil {
			return nil, err
		}
		changed, err := snapshotKey(snapshot, profile)
		if err != nil {
			return nil, err
		}
		_, hit, err = cacheValue(cache, changed, produce)
		record("disk_change_invalidates_"+path, err == nil && !hit && changed != key, fmt.Sprint(err))
		if err = os.WriteFile(filepath.Join(snapshot, path), []byte(content), 0600); err != nil {
			return nil, err
		}
	}
	for _, field := range []string{"model", "prompt", "adapter", "parser"} {
		before := profile[field]
		profile[field] += "changed"
		changed, err := snapshotKey(snapshot, profile)
		record("profile_change_"+field, err == nil && changed != key, fmt.Sprint(err))
		profile[field] = before
	}
	if err = os.WriteFile(filepath.Join(snapshot, "new.php"), []byte("new dependency"), 0600); err != nil {
		return nil, err
	}
	changed, err := snapshotKey(snapshot, profile)
	record("new_file_invalidates", err == nil && changed != key, fmt.Sprint(err))
	if err = os.Remove(filepath.Join(snapshot, "new.php")); err != nil {
		return nil, err
	}
	if err = os.Remove(filepath.Join(snapshot, "implementation.php")); err != nil {
		return nil, err
	}
	changed, err = snapshotKey(snapshot, profile)
	record("deleted_file_invalidates", err == nil && changed != key, fmt.Sprint(err))
	if err = os.WriteFile(filepath.Join(snapshot, "implementation.php"), []byte(initial["implementation.php"]), 0600); err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(snapshot, "requirement.txt"), []byte("\n\n"+initial["requirement.txt"]), 0600); err != nil {
		return nil, err
	}
	changed, err = snapshotKey(snapshot, profile)
	record("line_shift_invalidates_stale_citation", err == nil && changed != key, "conservative full invalidation; not semantic-only reuse")
	if err = os.WriteFile(cache, []byte(`{"key":"x","hash":"bad","payload":{}}`), 0600); err != nil {
		return nil, err
	}
	_, _, err = cacheValue(cache, key, produce)
	record("corrupted_cache_rejected", err != nil, "")

	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, _, err = runProcess(ctx, "", nil, self, "fixture-process", "sleep")
	cancel()
	record("process_timeout", errors.Is(err, context.DeadlineExceeded), fmt.Sprint(err))
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = runProcess(ctx, "", nil, self, "fixture-process", "failure")
	cancel()
	record("nonzero_with_success_json_rejected", err != nil, "")
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = runProcess(ctx, "", nil, self, "fixture-process", "large")
	cancel()
	record("oversized_stdout_rejected", err != nil, "")
	argument := "$(not-executed); текст with spaces"
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	stdout, stderr, err := runProcess(ctx, "", nil, self, "fixture-process", "echo", argument)
	cancel()
	var arguments []string
	decodeErr := json.Unmarshal(stdout, &arguments)
	record("arguments_not_shell_and_stderr_separate", err == nil && decodeErr == nil && len(arguments) == 1 && arguments[0] == argument && strings.Contains(stderr, "diagnostic"), "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad":
			fmt.Fprint(w, "{broken")
		case "/refused":
			fmt.Fprint(w, `{"status":"refused"}`)
		case "/rate":
			w.WriteHeader(429)
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		case "/large":
			fmt.Fprint(w, strings.Repeat("x", maxOutput+1))
		default:
			fmt.Fprint(w, `{"status":"ok","usage":{"input_tokens":3,"output_tokens":5},"fixture":true}`)
		}
	}))
	defer server.Close()
	response, err := fetchJSON(context.Background(), server.Client(), server.URL+"/ok")
	record("http_fixture_usage_preserved", err == nil && response["usage"].(map[string]any)["input_tokens"] == float64(3), "synthetic receipt, not paid provider usage")
	for _, path := range []string{"bad", "refused", "rate", "large"} {
		_, err = fetchJSON(context.Background(), server.Client(), server.URL+"/"+path)
		record("http_reject_"+path, err != nil, "")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = fetchJSON(ctx, server.Client(), server.URL+"/slow")
	cancel()
	record("http_timeout", errors.Is(err, context.DeadlineExceeded), fmt.Sprint(err))
	passed := true
	for _, check := range checks {
		passed = passed && check.Passed
	}
	return map[string]any{"passed": passed, "platform": runtime.GOOS + "/" + runtime.GOARCH, "checks": checks, "count": len(checks), "seconds": time.Since(started).Seconds(), "fixture_directory_removed_on_exit": temp}, nil
}
