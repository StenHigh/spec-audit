package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMarkdownProfile(t *testing.T) {
	valid := "### REQ-X-001 — Юникод\n\nУсловие: А.\nТребование: Б.\nПроверка: В.\n"
	for name, input := range map[string]string{
		"plain":    valid,
		"crlf":     strings.ReplaceAll(valid, "\n", "\r\n"),
		"examples": "```md\n" + valid + "```\n\n> ### REQ-Y-001 — Пример\n\n" + valid,
		"indented": "    ### REQ-Y-001 — Пример\n\n" + valid,
	} {
		reqs, err := parseRequirements("rules.md", []byte(input))
		if err != nil || len(reqs) != 1 || reqs[0].ID != "REQ-X-001" {
			t.Fatal(name, reqs, err)
		}
		start := map[string]int{"plain": 1, "crlf": 1, "examples": 11, "indented": 3}[name]
		quote := strings.TrimSuffix(valid, "\n")
		if name == "crlf" {
			quote = strings.ReplaceAll(quote, "\n", "\r\n")
		}
		want := Requirement{ID: "REQ-X-001", Title: "Юникод", Condition: "А.", Statement: "Б.", Verification: "В.",
			ContentHash: digest([]byte(`["REQ-X-001","Юникод","А.","Б.","В."]`)), Source: Citation{"rules.md", start, start + 4, quote}}
		if reqs[0] != want {
			t.Fatalf("%s: извлечённые поля/диапазон/hash: %+v; ожидалось %+v", name, reqs[0], want)
		}
	}
	for name, input := range map[string]string{
		"missing":        strings.Replace(valid, "Проверка: В.\n", "", 1),
		"duplicate":      valid + "Требование: Г.\n",
		"wrapped":        strings.Replace(valid, "Требование: Б.", "Требование: Б.\nПродолжение нормы", 1),
		"wrong_level":    strings.Replace(valid, "###", "##", 1),
		"utf8":           string([]byte{255}),
		"empty":          strings.Replace(valid, "Проверка: В.", "Проверка:", 1),
		"field_in_fence": strings.Replace(valid, "Проверка: В.", "\n```\nПроверка: В.\n```", 1),
	} {
		if _, err := parseRequirements("rules.md", []byte(input)); err == nil {
			t.Fatal("принято", name)
		}
	}
	if reqs, err := parseRequirements("legacy.md", []byte("# Legacy\nОператор должен проверить лимит.\n")); err != nil || len(reqs) != 0 {
		t.Fatal("выдуман semantic index")
	}
	config, base := fixture(t)
	cfg, _ := loadConfig(config)
	data := readFixture(t, filepath.Join(base, "source/rules.md"))
	writeFixture(t, filepath.Join(base, "source/rules.md"), append(data, data...))
	if _, err := snapshot(cfg); err == nil {
		t.Fatal("дубликаты норм приняты")
	}
}

func TestActualRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("реальные процессы")
	}
	config, base := fixture(t)
	raw := strings.Replace(string(readFixture(t, config)), "runtime: {kind: none}", "runtime: {kind: go, paths: ['.'], timeout_seconds: 120}", 1)
	writeFixture(t, config, []byte(raw))
	// A trusted test observes the actual child environment, not just the configured strings.
	controlledTemp := "package fixture\nimport (\"os\"; \"path/filepath\"; \"testing\")\nfunc TestRuntimeTemp(t *testing.T) { if os.Getenv(\"GOTMPDIR\") == \"\" || os.TempDir() != os.Getenv(\"GOTMPDIR\") { t.Fatal(\"uncontrolled temp\") }; path, err := os.CreateTemp(\"\", \"observed-\"); if err != nil { t.Fatal(err) }; name := path.Name(); path.Close(); defer os.Remove(name); if filepath.Dir(name) != os.TempDir() { t.Fatal(\"escaped temp\") } }\n"
	tempTest := filepath.Join(base, "source", "temporary_test.go")
	writeFixture(t, tempTest, []byte(controlledTemp))
	raw = strings.Replace(raw, "paths: [source_test.go]", "paths: [source_test.go, temporary_test.go]", 1)
	writeFixture(t, config, []byte(raw))
	runOK(t, "prepare", config, "green")
	receipt := runOK(t, "test", config, "green", "first").(Receipt)
	if receipt.State != "passed" || len(receipt.Tests) != 5 {
		t.Fatalf("реальные тесты: %+v", receipt)
	}
	runFail(t, "test", config, "green", "first")
	batch := runOK(t, "tasks", config, "green").(TaskBatch)
	for _, task := range batch.Tasks {
		result := sampleResult(t, task, filepath.Join(base, "source"))
		body, _ := json.Marshal(result)
		path := filepath.Join(base, task.TaskID+".json")
		writeFixture(t, path, body)
		runOK(t, "submit", config, "green", task.TaskID, path)
	}
	runOK(t, "report", config, "green")
	var report Report
	if err := json.Unmarshal(readFixture(t, filepath.Join(base, "runs/green/report.json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.Requirements[1].Roles[0].Assessment.Assertion != "weak" || report.Requirements[1].Roles[0].Executions[0].State != "passed" || report.Requirements[2].Roles[0].Assessment.Implementation != "contradicted" {
		t.Fatal("зелёный процесс подменил смысл")
	}
	writeFixture(t, config, []byte(strings.Replace(raw, "paths: ['.']", "paths: ['.'], tests: [TestNotFound]", 1)))
	runOK(t, "prepare", config, "none")
	if receipt := runOK(t, "test", config, "none", "empty").(Receipt); receipt.State != "no_tests" {
		t.Fatal("пустой запуск стал passed", receipt)
	}
	writeFixture(t, config, []byte(raw))
	testPath := filepath.Join(base, "source/source_test.go")
	prior := readFixture(t, testPath)
	writeFixture(t, testPath, []byte(strings.Replace(string(prior), "got <= 0", "got != 15", 1)))
	runOK(t, "prepare", config, "red")
	if receipt := runOK(t, "test", config, "red", "failed").(Receipt); receipt.State != "failed" {
		t.Fatal("провал скрыт", receipt)
	}
	writeFixture(t, testPath, prior)
	// Actual process is still running when its selected source is edited.
	waiting := strings.Replace(string(prior), `import "testing"`, "import (\"testing\"; \"time\")", 1) + "\nfunc TestWait(t *testing.T) { time.Sleep(3*time.Second) }\n"
	writeFixture(t, testPath, []byte(waiting))
	runOK(t, "prepare", config, "changing")
	changed := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		path := filepath.Join(base, "source/source.go")
		body, err := os.ReadFile(path)
		if err == nil {
			err = os.WriteFile(path, append(body, []byte("\n// edited during run\n")...), 0600)
		}
		changed <- err
	}()
	if receipt := runOK(t, "test", config, "changing", "changed").(Receipt); receipt.State != "stale" {
		t.Fatal("не замечено изменение источников", receipt)
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	writeFixture(t, config, []byte(strings.Replace(raw, "timeout_seconds: 120", "timeout_seconds: 1", 1)))
	runOK(t, "prepare", config, "timeout")
	if receipt := runOK(t, "test", config, "timeout", "interrupted").(Receipt); receipt.State != "incomplete" {
		t.Fatal("прерванный запуск не incomplete", receipt)
	}
}

func TestRuntimeParsers(t *testing.T) {
	for _, data := range []string{"", "not json", `{"Action":"pass","Package":"x","Test":"TestPhantom"}`, `{"Action":"start","Package":"x"}`} {
		if _, valid := goTestResults([]byte(data)); valid {
			t.Fatal("принят неполный Go report", data)
		}
	}
	tests, valid := junitResults([]byte(`<testsuites><testsuite><testcase classname="C" name="a"/><testcase classname="C" name="b"><failure/></testcase><testcase classname="C" name="c"><skipped/></testcase></testsuite></testsuites>`))
	if !valid || len(tests) != 3 || tests[1].State != "failed" || tests[2].State != "skipped" {
		t.Fatal("JUnit", tests)
	}
	for _, invalid := range []string{`<bad/>`, `<testsuite><testcase name="x"/></testsuite>`, `<testsuite>`, `<testsuite><testcase classname="C" name="x"/><testcase classname="C" name="x"/></testsuite>`} {
		if _, valid := junitResults([]byte(invalid)); valid {
			t.Fatal("неверный JUnit принят")
		}
	}
}

// This child mocks Docker's external protocol. It never runs PHP or a business project.
func init() {
	if os.Getenv("SPEC_AUDIT_MOCK_DOCKER") != "1" || filepath.Base(os.Args[0]) != "docker" {
		return
	}
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(2)
	}
	mode := os.Getenv("SPEC_AUDIT_DOCKER_MODE")
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if log := os.Getenv("SPEC_AUDIT_MOCK_LOG"); log != "" {
		if f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			_, _ = f.WriteString(strings.Join(args, " ") + "\n")
			_ = f.Close()
		}
	}
	switch args[0] {
	case "compose":
		if strings.Join(args, " ") != "compose ps --status running -q app" {
			os.Exit(3)
		}
		if mode != "missing" {
			fmt.Println(id)
		}
		if mode == "multiple" {
			fmt.Println(id)
		}
	case "inspect":
		root := os.Getenv("SPEC_AUDIT_MOCK_ROOT")
		if mode == "wrong_mount" {
			root = "/some/other/project"
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"State": map[string]bool{"Running": mode != "stopped"}, "Mounts": []map[string]string{{"Source": root, "Destination": "/var/www/html"}}})
	case "exec":
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, id) || !strings.Contains(joined, "timeout") || !strings.Contains(joined, "--workdir /var/www/html") {
			os.Exit(4)
		}
		if strings.Contains(joined, "spec-audit:preflight") {
			switch {
			case mode == "no_phpstan":
				os.Exit(10)
			case mode == "no_larastan" && strings.HasSuffix(joined, "-- laravel"):
				os.Exit(11)
			case mode == "config_cached":
				os.Exit(13)
			}
			fmt.Print("ok")
		} else if strings.Contains(joined, "spec-audit:setup") {
			_, _ = io.Copy(io.Discard, os.Stdin)
			if mode == "setup_fails" {
				os.Exit(20)
			}
		} else if strings.Contains(joined, "spec-audit:cleanup") {
			if mode == "cleanup_fails" {
				os.Exit(1)
			}
		} else if strings.Contains(joined, "vendor/bin/phpstan") {
			// The typed exporter never runs here: stdout comes from a recorded fixture.
			switch mode {
			case "internal_error":
				fmt.Print(`{"totals":{"errors":1,"file_errors":0},"files":{},"errors":["Internal error: fixture"]}`)
				os.Exit(1)
			case "empty_stdout":
				os.Exit(1)
			case "slow":
				os.Exit(124)
			case "killed":
				os.Exit(137)
			case "huge_stderr":
				chunk := strings.Repeat("e", 1<<16)
				for i := 0; i < 80; i++ {
					fmt.Fprint(os.Stderr, chunk)
				}
				os.Exit(1)
			case "exit_2":
				fmt.Print("Invalid configuration")
				os.Exit(2)
			case "huge_output":
				chunk := strings.Repeat("x", 1<<16)
				for i := 0; i < 80; i++ {
					fmt.Print(chunk)
				}
				os.Exit(1)
			case "bootstrap_writes":
				_ = os.MkdirAll(filepath.Join(os.Getenv("SPEC_AUDIT_MOCK_ROOT"), "bootstrap/cache"), 0700)
				_ = os.WriteFile(filepath.Join(os.Getenv("SPEC_AUDIT_MOCK_ROOT"), "bootstrap/cache/packages.php"), []byte("<?php return ['rewritten' => true];\n"), 0600)
			}
			data, _ := os.ReadFile(os.Getenv("SPEC_AUDIT_MOCK_PHPSTAN"))
			fmt.Print(string(data))
			if code := os.Getenv("SPEC_AUDIT_PHPSTAN_EXIT"); code != "" {
				n, _ := strconv.Atoi(code)
				os.Exit(n)
			}
			os.Exit(1)
		} else if strings.Contains(joined, "eval(substr") {
			_, _ = io.Copy(io.Discard, os.Stdin)
			data, _ := os.ReadFile(os.Getenv("SPEC_AUDIT_MOCK_ENVELOPE"))
			fmt.Print(string(data))
		} else if strings.Contains(joined, "PHP_VERSION") {
			fmt.Print("8.5.0 Linux x86_64")
		} else if strings.Contains(joined, "file_get_contents") {
			fmt.Print(`<testsuites><testsuite><testcase classname="DemoTest" name="testExample"/></testsuite></testsuites>`)
		} else if strings.Contains(joined, "vendor/bin/phpunit") {
			if marker := os.Getenv("SPEC_AUDIT_PHP_MARKER"); marker != "" {
				file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					os.Exit(31)
				} // Simulated shared database cannot run two tests at once.
				_ = file.Close()
				time.Sleep(600 * time.Millisecond)
				_ = os.Remove(marker)
			}
			fmt.Println("fixture output")
		} else {
			os.Exit(5)
		}
	default:
		os.Exit(6)
	}
	os.Exit(0)
}

func TestPHPBoundary(t *testing.T) {
	config, base := fixture(t)
	source := filepath.Join(base, "source")
	php := "<?php\nthrow new RuntimeException('must not execute');\nclass Demo { function answer() { return 42; } }\n"
	writeFixture(t, filepath.Join(source, "Subject.php"), []byte(php))
	writeFixture(t, filepath.Join(source, "composer.lock"), []byte("{}\n"))
	raw := string(readFixture(t, config))
	raw = strings.Replace(raw, "[source.go, go.mod]", "[source.go, go.mod, Subject.php, composer.lock]", 1)
	raw = strings.Replace(raw, "kind: none", "kind: docker-php, paths: [source_test.go]", 1)
	writeFixture(t, config, []byte(raw))
	cfg, _ := loadConfig(config)
	m, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	envelope := SDKEnvelope{Version: "sdk/2", EvidenceKind: "syntax_only", Files: []SDKFile{{"Subject.php", digest([]byte(php)), len(php)}}, Facts: []SDKFact{}}
	envelope.Runtime.PHP, envelope.Runtime.OS, envelope.Runtime.Arch = "8.5.0", "Linux", "x86_64"
	envelope.Runtime.ParserVersion, envelope.Runtime.ParserReference = "v5.6.2", "fixture-reference"
	envelope.Runtime.ComposerLockSHA256, envelope.Runtime.SDKSHA256 = digest([]byte("{}\n")), digest(sdk)
	start := strings.Index(php, "function answer")
	quote := "function answer() { return 42; }"
	envelope.Facts = []SDKFact{{File: "Subject.php", Kind: "Stmt_ClassMethod", Name: "answer", Start: start, Stop: start + len(quote), Line: 3, Quote: quote, SourceSHA256: digest([]byte(php))}}
	valid, _ := json.Marshal(envelope)
	if _, err := verifySDK(valid, m, []string{"Subject.php"}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SDKEnvelope){
		"quote":          func(e *SDKEnvelope) { e.Facts[0].Quote = "invented" },
		"line":           func(e *SDKEnvelope) { e.Facts[0].Line++ },
		"hash":           func(e *SDKEnvelope) { e.Files[0].SHA256 = "old" },
		"traversal":      func(e *SDKEnvelope) { e.Files[0].Path = "../Subject.php" },
		"missing":        func(e *SDKEnvelope) { e.Files = []SDKFile{} },
		"extra":          func(e *SDKEnvelope) { e.Files = append(e.Files, e.Files[0]) },
		"sdk_version":    func(e *SDKEnvelope) { e.Version = "sdk/1" },
		"parser_missing": func(e *SDKEnvelope) { e.Runtime.ParserVersion = "" },
		"sdk_hash":       func(e *SDKEnvelope) { e.Runtime.SDKSHA256 = "other" },
		"lock":           func(e *SDKEnvelope) { e.Runtime.ComposerLockSHA256 = "other" },
	} {
		var e SDKEnvelope
		_ = json.Unmarshal(valid, &e)
		mutate(&e)
		body, _ := json.Marshal(e)
		if _, err := verifySDK(body, m, []string{"Subject.php"}); err == nil {
			t.Fatal("SDK принял", name)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(base, "bin/docker")
	writeFixture(t, fake, readFixture(t, exe))
	if err := os.Chmod(fake, 0700); err != nil {
		t.Fatal(err)
	}
	answer := filepath.Join(base, "envelope.json")
	writeFixture(t, answer, valid)
	t.Setenv("PATH", filepath.Join(base, "bin"))
	t.Setenv("SPEC_AUDIT_MOCK_DOCKER", "1")
	t.Setenv("SPEC_AUDIT_MOCK_ROOT", source)
	t.Setenv("SPEC_AUDIT_MOCK_ENVELOPE", answer)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, mode := range []string{"missing", "multiple", "wrong_mount", "stopped"} {
		t.Setenv("SPEC_AUDIT_DOCKER_MODE", mode)
		if _, err := verifiedContainer(ctx, cfg); err == nil {
			t.Fatal("принят контейнер", mode)
		}
	}
	t.Setenv("SPEC_AUDIT_DOCKER_MODE", "")
	if _, err := phpFacts(cfg, m, []string{"Subject.php"}); err != nil {
		t.Fatal(err)
	}
	process, _, command, tests, validReport, err := phpTests(ctx, cfg)
	if err != nil || process.exitCode != 0 || !validReport || len(tests) != 1 || strings.Contains(strings.Join(command, " "), "compose exec") {
		t.Fatal("PHPUnit bridge", command, tests, err)
	}
	t.Run("different_runs_serialize_phpunit", func(t *testing.T) {
		marker := filepath.Join(base, "php-active")
		t.Setenv("SPEC_AUDIT_PHP_MARKER", marker)
		var commands []*exec.Cmd
		var outputs []*bytes.Buffer
		for _, run := range []string{"phpunit", "php-second"} {
			runOK(t, "prepare", config, run)
			cmd := exec.Command(exe, "-test.run=^TestContract$", "--", "--audit-child", "test", config, run, "observed")
			output := new(bytes.Buffer)
			cmd.Stdout, cmd.Stderr = output, output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			commands, outputs = append(commands, cmd), append(outputs, output)
			if run == "phpunit" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(marker); err == nil {
						break
					}
					if time.Now().After(deadline) {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
						t.Fatal("первый PHPUnit не стартовал")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
		for i, cmd := range commands {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("test: %v: %s", err, outputs[i])
			}
			var receipt Receipt
			if err := json.Unmarshal(outputs[i].Bytes(), &receipt); err != nil || receipt.State != "passed" {
				t.Fatalf("конкурентные RUN_ID повредили runtime: %s", outputs[i])
			}
		}
	})
	writeFixture(t, answer, []byte(`{"secret":"must not leak"}`))
	if _, err := phpFacts(cfg, m, []string{"Subject.php"}); err == nil || strings.Contains(err.Error(), "must not leak") {
		t.Fatal("неверный envelope или утечка диагностики", err)
	}
}
