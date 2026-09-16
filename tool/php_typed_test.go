package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSDKConfig(t *testing.T) {
	config, base := fixture(t)
	original := readFixture(t, config)
	cfg, err := loadConfig(config)
	if err != nil || cfg.SDK != nil {
		t.Fatal("конфигурация без блока sdk должна загружаться с nil SDK", err)
	}
	before, err := snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	withDocker := bytes.Replace(original, []byte("runtime: {kind: none}"), []byte("runtime: {kind: docker-php}"), 1)
	cases := map[string]struct {
		config []byte
		ok     bool
	}{
		"sdk_without_docker":   {append(bytes.Clone(original), []byte("sdk: {profile: php}\n")...), false},
		"profile_unknown":      {append(bytes.Clone(withDocker), []byte("sdk: {profile: symfony}\n")...), false},
		"profile_missing":      {append(bytes.Clone(withDocker), []byte("sdk: {timeout_seconds: 10}\n")...), false},
		"timeout_too_large":    {append(bytes.Clone(withDocker), []byte("sdk: {profile: laravel, timeout_seconds: 901}\n")...), false},
		"timeout_negative":     {append(bytes.Clone(withDocker), []byte("sdk: {profile: laravel, timeout_seconds: -1}\n")...), false},
		"unknown_key_memory":   {append(bytes.Clone(withDocker), []byte("sdk: {profile: php, memory_limit: 2G}\n")...), false},
		"unknown_key_env":      {append(bytes.Clone(withDocker), []byte("sdk: {profile: php, env: {A: b}}\n")...), false},
		"unknown_key_config":   {append(bytes.Clone(withDocker), []byte("sdk: {profile: laravel, config: phpstan.neon}\n")...), false},
		"php_defaults":         {append(bytes.Clone(withDocker), []byte("sdk: {profile: php}\n")...), true},
		"laravel_explicit":     {append(bytes.Clone(withDocker), []byte("sdk: {profile: laravel, timeout_seconds: 900}\n")...), true},
		"docker_without_sdk":   {withDocker, true},
		"original_without_sdk": {original, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(base, name+".yaml")
			writeFixture(t, path, tc.config)
			cfg, err := loadConfig(path)
			if tc.ok != (err == nil) {
				t.Fatalf("ожидалось ok=%v, получено %v", tc.ok, err)
			}
			if !tc.ok {
				return
			}
			switch {
			case strings.HasPrefix(name, "php_defaults"):
				if cfg.SDK == nil || cfg.SDK.Profile != "php" || cfg.SDK.TimeoutSeconds != sdkDefaultTimeout {
					t.Fatalf("defaults не применены: %+v", cfg.SDK)
				}
			case strings.HasPrefix(name, "laravel_explicit"):
				if cfg.SDK == nil || cfg.SDK.Profile != "laravel" || cfg.SDK.TimeoutSeconds != sdkMaxTimeout {
					t.Fatalf("явные значения потеряны: %+v", cfg.SDK)
				}
			default:
				if cfg.SDK != nil {
					t.Fatal("блок sdk появился из ничего")
				}
			}
		})
	}
	// Отсутствие блока сохраняет идентичность snapshot; его появление меняет её.
	after, err := snapshot(cfg)
	if err != nil || after.SnapshotID != before.SnapshotID {
		t.Fatal("snapshot_id без блока sdk должен совпадать", err)
	}
	sdkCfg, err := loadConfig(filepath.Join(base, "php_defaults.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	withSDK, err := snapshot(sdkCfg)
	if err != nil || withSDK.SnapshotID == before.SnapshotID {
		t.Fatal("блок sdk входит в идентичность snapshot", err)
	}
	if !bytes.Contains(mustMarshal(t, withSDK), []byte(`"sdk":{"profile":"php","timeout_seconds":300}`)) || bytes.Contains(mustMarshal(t, before), []byte(`"sdk"`)) {
		t.Fatal("сериализация блока sdk в manifest некорректна")
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRenderNeon(t *testing.T) {
	php := string(renderNeon("/tmp/spec-audit-x", "php", false))
	laravel := string(renderNeon("/tmp/spec-audit-x", "laravel", false))
	installer := string(renderNeon("/tmp/spec-audit-x", "laravel", true))
	for name, neon := range map[string]string{"php": php, "laravel": laravel, "installer": installer} {
		for _, want := range []string{"tmpDir: '/tmp/spec-audit-x/tmp'", "maximumNumberOfProcesses: 1", "reportUnmatchedIgnoredErrors: false", "level: 0", "sdkSha256: '" + typedSHA256 + "'", "parametersSchema:"} {
			if !strings.Contains(neon, want) {
				t.Fatalf("%s: нет %q", name, want)
			}
		}
		if strings.Contains(neon, "bootstrapFiles:\n") || strings.Contains(neon, "{{") {
			t.Fatalf("%s: экспортёр идёт через --autoload-file, плейсхолдеры должны быть подставлены", name)
		}
	}
	if strings.Contains(php, "larastan") || !strings.Contains(php, "profile: 'php'") || !strings.Contains(php, "migrationPaths: []") {
		t.Fatal("профиль php не должен подключать Larastan")
	}
	if !strings.HasPrefix(laravel, "includes:\n    - "+larastanNeon+"\n") || !strings.Contains(laravel, "migrationPaths: %databaseMigrationsPath%") || !strings.Contains(laravel, "schemaPaths: %squashedMigrationsPath%") {
		t.Fatal("профиль laravel требует абсолютный include Larastan и пути миграций")
	}
	if strings.Contains(installer, "includes:") || !strings.Contains(installer, "profile: 'laravel'") {
		t.Fatal("при extension-installer секция includes должна отсутствовать")
	}
	if len(typedSHA256) != 64 || !validDigest(typedSHA256) || typedSHA256 != digest(append(append([]byte{}, sdkTyped...), typedNeon...)) {
		t.Fatal("typedSHA256 должен связывать экспортёр и шаблон")
	}
}

// typedFixture готовит проект с PHP-исходником, composer.lock и bootstrap для профиля.
// PHP в тестах никогда не исполняется: контейнер имитируется мок-docker.
type typedFixture struct {
	config, base, source, phpstanOut string
	cfg                              Config
	manifest                         Manifest
	envelope                         TypedEnvelope
	php                              string
}

const typedSubject = "<?php\nthrow new RuntimeException('must not execute');\nclass Demo { function answer() { return 42; } }\n$demo = new Demo();\n$demo->answer();\n"

const typedLock = `{"packages":[{"name":"laravel/framework","version":"v13.0.0"}],"packages-dev":[{"name":"phpstan/phpstan","version":"2.2.1"},{"name":"larastan/larastan","version":"v3.10.0"}]}` + "\n"

func newTypedFixture(t *testing.T, profile string) *typedFixture {
	t.Helper()
	config, base := fixture(t)
	f := &typedFixture{config: config, base: base, source: filepath.Join(base, "source"), php: typedSubject}
	writeFixture(t, filepath.Join(f.source, "Subject.php"), []byte(f.php))
	writeFixture(t, filepath.Join(f.source, "composer.lock"), []byte(typedLock))
	writeFixture(t, filepath.Join(f.source, "bootstrap/app.php"), []byte("<?php return null;\n"))
	writeFixture(t, filepath.Join(f.source, "bootstrap/cache/packages.php"), []byte("<?php return [];\n"))
	writeFixture(t, filepath.Join(f.source, "config/app.php"), []byte("<?php return ['name' => 'fixture'];\n"))
	raw := string(readFixture(t, config))
	raw = strings.Replace(raw, "[source.go, go.mod]", "[source.go, go.mod, Subject.php, composer.lock, bootstrap, config]", 1)
	raw = strings.Replace(raw, "runtime: {kind: none}", "runtime: {kind: docker-php}\nsdk: {profile: "+profile+", timeout_seconds: 1}", 1)
	writeFixture(t, config, []byte(raw))
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	f.manifest, err = snapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.envelope = TypedEnvelope{Version: typedVersion, EvidenceKind: typedEvidence, Profile: profile,
		Runtime:        TypedRuntime{PHP: "8.4.23", OS: "Linux", Arch: "aarch64", ComposerLockSHA256: digest([]byte(typedLock)), SDKSHA256: typedSHA256},
		Files:          []SDKFile{{"Subject.php", digest([]byte(f.php)), len(f.php)}},
		BootstrapFiles: []string{},
		Basis:          []SDKFile{},
		Facts: []TypedFact{
			{Citation: Citation{"Subject.php", 3, 3, "class Demo { function answer() { return 42; } }"}, Syntax: "Stmt_ClassMethod", Name: "answer", Origin: "phpstan", Resolution: "declared", ReceiverType: "", Targets: []TypedTarget{}},
			{Citation: Citation{"Subject.php", 5, 5, "$demo->answer();"}, Syntax: "Expr_MethodCall", Name: "answer", Origin: "phpstan", Resolution: "resolved", ReceiverType: "Demo", Targets: []TypedTarget{{Class: "Demo", Method: "answer", File: "Subject.php", Line: 3}}},
		}}
	if profile == "laravel" {
		app, _ := findSource(f.manifest, "config/app.php")
		f.envelope.BootstrapFiles = []string{larastanBoot}
		f.envelope.Basis = []SDKFile{{"config/app.php", app.SHA256, app.Bytes}, {"vendor/larastan/larastan/stubs/common/Model.stub", digest([]byte("stub")), 4}}
	}
	return f
}

func (f *typedFixture) body(t *testing.T, mutate func(*TypedEnvelope)) []byte {
	t.Helper()
	var e TypedEnvelope
	if err := json.Unmarshal(mustMarshal(t, f.envelope), &e); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(&e)
	}
	return mustMarshal(t, e)
}

// phpstanJSON оборачивает envelope в вывод `phpstan analyse --error-format=json`, как делает экспортёр.
func phpstanJSON(t *testing.T, envelope []byte, extraMessages int, errorsList []string) []byte {
	t.Helper()
	messages := []map[string]any{{"message": string(envelope), "line": 0, "ignorable": false, "identifier": typedIdentifier}}
	for i := 0; i < extraMessages; i++ {
		messages = append(messages, map[string]any{"message": "Diagnostic", "line": 1, "ignorable": true, "identifier": "fixture.diag"})
	}
	if errorsList == nil {
		errorsList = []string{}
	}
	return mustMarshal(t, map[string]any{"totals": map[string]int{"errors": len(errorsList), "file_errors": len(messages)}, "files": map[string]any{"N/A": map[string]any{"errors": len(messages), "messages": messages}}, "errors": errorsList})
}

func (f *typedFixture) mockDocker(t *testing.T, phpstanOut []byte) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(f.base, "bin/docker")
	writeFixture(t, fake, readFixture(t, exe))
	if err := os.Chmod(fake, 0700); err != nil {
		t.Fatal(err)
	}
	f.phpstanOut = filepath.Join(f.base, "phpstan.json")
	writeFixture(t, f.phpstanOut, phpstanOut)
	log := filepath.Join(f.base, "docker-calls.log")
	t.Setenv("PATH", filepath.Join(f.base, "bin"))
	t.Setenv("SPEC_AUDIT_MOCK_DOCKER", "1")
	t.Setenv("SPEC_AUDIT_MOCK_ROOT", f.source)
	t.Setenv("SPEC_AUDIT_MOCK_PHPSTAN", f.phpstanOut)
	t.Setenv("SPEC_AUDIT_MOCK_LOG", log)
	t.Setenv("SPEC_AUDIT_DOCKER_MODE", "")
	t.Setenv("SPEC_AUDIT_PHPSTAN_EXIT", "")
	return log
}

func callLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func resetLog(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTypedBoundary(t *testing.T) {
	for _, profile := range []string{"php", "laravel"} {
		f := newTypedFixture(t, profile)
		lock := []byte(typedLock)
		if _, err := verifyTyped(f.body(t, nil), f.manifest, f.cfg, []string{"Subject.php"}, lock); err != nil {
			t.Fatal(profile, err)
		}
		accepted := map[string]func(*TypedEnvelope){
			"empty_facts_ok": func(e *TypedEnvelope) { e.Facts = []TypedFact{} },
			"basis_vendor_ok": func(e *TypedEnvelope) {
				e.Basis = append(e.Basis, SDKFile{"vendor/x/y.php", digest([]byte("z")), 1})
			},
			"virtual_ok": func(e *TypedEnvelope) {
				e.Facts[1].Resolution, e.Facts[1].Origin = "virtual", "larastan"
				e.Facts[1].Targets = []TypedTarget{{Class: "Demo", Method: "answer"}}
			},
			"native_ok": func(e *TypedEnvelope) {
				e.Facts[1].Targets = []TypedTarget{{Class: "DateTimeImmutable", Method: "format", Native: true}}
			},
			"outside_mount_ok": func(e *TypedEnvelope) {
				e.Facts[1].Targets = []TypedTarget{{Class: "Other", Method: "answer", File: "/opt/lib/Other.php", Line: 7}}
			},
			"ambiguous_ok": func(e *TypedEnvelope) {
				e.Facts[1].Resolution = "ambiguous"
				e.Facts[1].Targets = append(e.Facts[1].Targets, TypedTarget{Class: "Other", Method: "answer", File: "Subject.php", Line: 3})
			},
			"dynamic_ok": func(e *TypedEnvelope) {
				e.Facts[1].Resolution, e.Facts[1].Name, e.Facts[1].Targets = "dynamic", "{dynamic}", []TypedTarget{}
			},
		}
		for name, mutate := range accepted {
			if _, err := verifyTyped(f.body(t, mutate), f.manifest, f.cfg, []string{"Subject.php"}, lock); err != nil {
				t.Fatal(profile, name, err)
			}
		}
		rejected := map[string]func(*TypedEnvelope){
			"quote":                  func(e *TypedEnvelope) { e.Facts[0].Citation.Quote = "class Demo { function answer() { return 43; } }" },
			"line_end_overflow":      func(e *TypedEnvelope) { e.Facts[0].Citation.LineEnd = e.Facts[0].Citation.LineStart + maxFactLines },
			"line_out_of_source":     func(e *TypedEnvelope) { e.Facts[0].Citation.LineStart, e.Facts[0].Citation.LineEnd = 40, 40 },
			"path_traversal":         func(e *TypedEnvelope) { e.Facts[0].Citation.Path = "../Subject.php" },
			"path_not_analysed":      func(e *TypedEnvelope) { e.Facts[0].Citation.Path = "source.go" },
			"lock":                   func(e *TypedEnvelope) { e.Runtime.ComposerLockSHA256 = digest([]byte("other")) },
			"sdk_hash":               func(e *TypedEnvelope) { e.Runtime.SDKSHA256 = digest(sdk) },
			"analysed_missing":       func(e *TypedEnvelope) { e.Files = []SDKFile{} },
			"files_extra":            func(e *TypedEnvelope) { e.Files = append(e.Files, SDKFile{"source.go", "x", 1}) },
			"foreign_snapshot":       func(e *TypedEnvelope) { e.Files[0].SHA256 = digest([]byte("other revision")) },
			"basis_outside_snapshot": func(e *TypedEnvelope) { e.Basis = append(e.Basis, SDKFile{"config/extra.php", digest([]byte("x")), 1}) },
			"basis_sha_mismatch":     func(e *TypedEnvelope) { e.Basis = append(e.Basis, SDKFile{"config/app.php", digest([]byte("x")), 1}) },
			"basis_traversal":        func(e *TypedEnvelope) { e.Basis = append(e.Basis, SDKFile{"../etc/passwd", digest([]byte("x")), 1}) },
			"basis_absolute":         func(e *TypedEnvelope) { e.Basis = append(e.Basis, SDKFile{"/etc/passwd", digest([]byte("x")), 1}) },
			"virtual_with_file": func(e *TypedEnvelope) {
				e.Facts[1].Resolution = "virtual"
			},
			"resolved_two_targets": func(e *TypedEnvelope) {
				e.Facts[1].Targets = append(e.Facts[1].Targets, TypedTarget{Class: "Other", Method: "answer", File: "Subject.php", Line: 3})
			},
			"resolved_without_location": func(e *TypedEnvelope) { e.Facts[1].Targets = []TypedTarget{{Class: "Demo", Method: "answer"}} },
			"dynamic_with_targets": func(e *TypedEnvelope) {
				e.Facts[1].Resolution, e.Facts[1].Name = "dynamic", "{dynamic}"
			},
			"dynamic_wrong_name":       func(e *TypedEnvelope) { e.Facts[1].Resolution, e.Facts[1].Targets = "dynamic", []TypedTarget{} },
			"unresolved_with_targets":  func(e *TypedEnvelope) { e.Facts[1].Resolution = "unresolved" },
			"ambiguous_single":         func(e *TypedEnvelope) { e.Facts[1].Resolution = "ambiguous" },
			"declared_two_targets":     func(e *TypedEnvelope) { e.Facts[0].Targets = append(e.Facts[1].Targets, e.Facts[1].Targets[0]) },
			"line_without_file":        func(e *TypedEnvelope) { e.Facts[1].Targets[0].File = "" },
			"file_without_line":        func(e *TypedEnvelope) { e.Facts[1].Targets[0].Line = 0 },
			"target_traversal":         func(e *TypedEnvelope) { e.Facts[1].Targets[0].File = "../Subject.php" },
			"target_mount_absolute":    func(e *TypedEnvelope) { e.Facts[1].Targets[0].File = containerMount + "/Subject.php" },
			"syntax_unknown":           func(e *TypedEnvelope) { e.Facts[1].Syntax = "Expr_FuncCall" },
			"origin_unknown":           func(e *TypedEnvelope) { e.Facts[1].Origin = "psalm" },
			"resolution_unknown":       func(e *TypedEnvelope) { e.Facts[1].Resolution = "guessed" },
			"name_empty":               func(e *TypedEnvelope) { e.Facts[1].Name = "" },
			"receiver_too_long":        func(e *TypedEnvelope) { e.Facts[1].ReceiverType = strings.Repeat("x", maxReceiverType+1) },
			"profile_mismatch":         func(e *TypedEnvelope) { e.Profile = map[string]string{"php": "laravel", "laravel": "php"}[profile] },
			"version":                  func(e *TypedEnvelope) { e.Version = "sdk/2" },
			"evidence_kind":            func(e *TypedEnvelope) { e.EvidenceKind = "syntax_only" },
			"php_too_old":              func(e *TypedEnvelope) { e.Runtime.PHP = "8.1.30" },
			"os":                       func(e *TypedEnvelope) { e.Runtime.OS = "Darwin" },
			"bootstrap_outside_vendor": func(e *TypedEnvelope) { e.BootstrapFiles = append(e.BootstrapFiles, "bootstrap/custom.php") },
		}
		if profile == "php" {
			rejected["bootstrap_files_for_php_profile"] = func(e *TypedEnvelope) { e.BootstrapFiles = []string{larastanBoot} }
		} else {
			rejected["bootstrap_files_missing_larastan"] = func(e *TypedEnvelope) { e.BootstrapFiles = []string{} }
			rejected["bootstrap_files_other_vendor_only"] = func(e *TypedEnvelope) { e.BootstrapFiles = []string{"vendor/nesbot/carbon/lazy.php"} }
		}
		for name, mutate := range rejected {
			body := f.body(t, mutate)
			if _, err := verifyTyped(body, f.manifest, f.cfg, []string{"Subject.php"}, lock); err == nil {
				t.Fatal(profile, "SDK принял", name)
			} else if strings.Contains(err.Error(), "Subject") || strings.Contains(err.Error(), "must not") {
				t.Fatal(profile, name, "ошибка раскрывает данные envelope:", err)
			}
		}
		for name, body := range map[string][]byte{
			"extra_field":       []byte(strings.Replace(string(f.body(t, nil)), `"version":`, `"notes":"x","version":`, 1)),
			"missing_field":     []byte(strings.Replace(string(f.body(t, nil)), `"bootstrap_files":[],`, ``, 1)),
			"quote_utf8":        []byte(strings.Replace(string(f.body(t, nil)), "return 42;", "return \xff;", 1)),
			"sdk2_body_as_sdk3": mustMarshal(t, SDKEnvelope{Version: "sdk/2", EvidenceKind: "syntax_only"}),
			"secret":            []byte(`{"secret":"must not leak"}`),
			"null":              []byte(`null`),
		} {
			if profile == "laravel" && name == "missing_field" {
				body = []byte(strings.Replace(string(f.body(t, nil)), `"bootstrap_files":["`+larastanBoot+`"],`, ``, 1))
			}
			_, err := verifyTyped(body, f.manifest, f.cfg, []string{"Subject.php"}, lock)
			if err == nil {
				t.Fatal(profile, "SDK принял", name)
			}
			if strings.Contains(err.Error(), "must not leak") {
				t.Fatal("ошибка раскрывает envelope")
			}
		}
	}
	// Версии анализаторов берутся из composer.lock; иные major, dev-версии и отсутствие пакета отклоняются.
	for name, lock := range map[string]string{
		"phpstan_1x":              strings.Replace(typedLock, `"version":"2.2.1"`, `"version":"1.12.0"`, 1),
		"phpstan_missing_in_lock": strings.Replace(typedLock, `{"name":"phpstan/phpstan","version":"2.2.1"},`, ``, 1),
		"phpstan_dev":             strings.Replace(typedLock, `"version":"2.2.1"`, `"version":"dev-main"`, 1),
		"not_json":                "{",
	} {
		if _, _, _, err := analyzerVersions([]byte(lock), "php"); err == nil {
			t.Fatal("принята версия", name)
		}
	}
	if _, _, _, err := analyzerVersions([]byte(strings.Replace(typedLock, `"v3.10.0"`, `"2.9.0"`, 1)), "laravel"); err == nil {
		t.Fatal("Larastan 2.x принят для laravel")
	}
	phpstan, larastan, installer, err := analyzerVersions([]byte(strings.Replace(typedLock, `"v3.10.0"`, `"2.9.0"`, 1)), "php")
	if err != nil || phpstan != "2.2.1" || larastan != "" || installer {
		t.Fatal("профиль php не требует Larastan", err)
	}
	if _, _, installer, _ := analyzerVersions([]byte(strings.Replace(typedLock, `"packages-dev":[`, `"packages-dev":[{"name":"phpstan/extension-installer","version":"1.4.3"},`, 1)), "laravel"); !installer {
		t.Fatal("extension-installer не обнаружен")
	}
	for _, version := range []string{"8.2.0", "8.4.23-dev", "9.0.0RC1"} {
		if !phpAtLeast(version, 8, 2) {
			t.Fatal("версия PHP должна приниматься:", version)
		}
	}
	for _, version := range []string{"8.1.99", "7.4", "8", ""} {
		if phpAtLeast(version, 8, 2) {
			t.Fatal("версия PHP должна отклоняться:", version)
		}
	}
}

func TestTypedProcess(t *testing.T) {
	for _, profile := range []string{"php", "laravel"} {
		t.Run(profile, func(t *testing.T) {
			f := newTypedFixture(t, profile)
			log := f.mockDocker(t, phpstanJSON(t, f.body(t, nil), 2, nil))
			result, err := phpTyped(f.cfg, f.manifest, []string{"Subject.php"})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result.raw, f.body(t, nil)) || len(result.envelope.Facts) != 2 || result.diagnostics != 2 || result.phpstan != "2.2.1" || (profile == "laravel") != (result.larastan == "v3.10.0") {
				t.Fatalf("результат php-typed искажён: %+v", result)
			}
			calls := callLog(t, log)
			var order []string
			for _, call := range calls {
				switch {
				case strings.Contains(call, "spec-audit:preflight"):
					order = append(order, "preflight")
				case strings.Contains(call, "spec-audit:setup"):
					order = append(order, "setup")
				case strings.Contains(call, "spec-audit:cleanup"):
					order = append(order, "cleanup")
				case strings.Contains(call, "vendor/bin/phpstan analyse"):
					order = append(order, "analyse")
				}
			}
			if strings.Join(order, ",") != "preflight,setup,setup,analyse,cleanup" {
				t.Fatal("порядок шагов нарушен:", order)
			}
			joined := strings.Join(calls, "\n")
			if strings.Contains(joined, "composer") || !strings.Contains(joined, "-e XDEBUG_MODE=off") || !strings.Contains(joined, "--autoload-file /tmp/spec-audit-") || !strings.Contains(joined, "--memory-limit="+sdkMemoryLimit) || !strings.Contains(joined, "timeout -s TERM -k 1 1 php") {
				t.Fatal("команда анализа не соответствует контракту")
			}
			for _, pair := range laravelIsolationEnv {
				if strings.Contains(joined, "-e "+pair) != (profile == "laravel") {
					t.Fatal("изоляция bootstrap применяется только для laravel:", pair)
				}
			}
			modes := map[string]string{"no_phpstan": "PHPStan не установлен", "config_cached": "закешированный config", "setup_fails": "рабочую область", "internal_error": "экспорт SDK не завершён", "empty_stdout": "экспорт SDK не завершён", "slow": "таймаут SDK", "exit_2": "экспорт SDK не завершён", "huge_output": "превышает лимит", "bootstrap_writes": "снимок изменился"}
			if profile == "laravel" {
				modes["no_larastan"] = "Larastan не установлен"
			}
			for mode, want := range modes {
				resetLog(t, log)
				t.Setenv("SPEC_AUDIT_DOCKER_MODE", mode)
				_, err := phpTyped(f.cfg, f.manifest, []string{"Subject.php"})
				_ = os.Remove(filepath.Join(f.source, "bootstrap/cache/packages.php"))
				writeFixture(t, filepath.Join(f.source, "bootstrap/cache/packages.php"), []byte("<?php return [];\n"))
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("%s: ожидалось %q, получено %v", mode, want, err)
				}
				after := strings.Join(callLog(t, log), "\n")
				if strings.Contains(after, "vendor/bin/phpstan analyse") && !strings.Contains(after, "spec-audit:cleanup") {
					t.Fatal(mode, "рабочая область не очищена после отказа")
				}
				if strings.Contains(after, "composer") {
					t.Fatal(mode, "Composer не должен вызываться")
				}
			}
			t.Setenv("SPEC_AUDIT_DOCKER_MODE", "")
			if profile == "php" {
				t.Setenv("SPEC_AUDIT_DOCKER_MODE", "no_larastan")
				if _, err := phpTyped(f.cfg, f.manifest, []string{"Subject.php"}); err != nil {
					t.Fatal("профиль php не требует Larastan:", err)
				}
				t.Setenv("SPEC_AUDIT_DOCKER_MODE", "")
			}
			// Sink отсутствует / errors непустой / exit 0 — отказ без успешного пустого графа.
			for name, out := range map[string][]byte{
				"sink_missing":    phpstanJSON(t, []byte(`{"version":"sdk/3"}`), 1, nil),
				"errors_nonempty": phpstanJSON(t, f.body(t, nil), 0, []string{"Internal error"}),
			} {
				if name == "sink_missing" {
					out = bytes.Replace(out, []byte(typedIdentifier), []byte("other.identifier"), 1)
				}
				writeFixture(t, f.phpstanOut, out)
				if _, err := phpTyped(f.cfg, f.manifest, []string{"Subject.php"}); err == nil {
					t.Fatal(name, "принят")
				}
			}
			writeFixture(t, f.phpstanOut, phpstanJSON(t, f.body(t, nil), 0, nil))
			t.Setenv("SPEC_AUDIT_PHPSTAN_EXIT", "0")
			if _, err := phpTyped(f.cfg, f.manifest, []string{"Subject.php"}); err == nil {
				t.Fatal("exit 0 без экспортёра принят")
			}
			t.Setenv("SPEC_AUDIT_PHPSTAN_EXIT", "")
			resetLog(t, log)
			for _, bad := range [][]string{{}, {"source.go"}, {"Subject.php", "Subject.php"}, {"composer.lock"}} {
				if _, err := phpTyped(f.cfg, f.manifest, bad); err == nil {
					t.Fatal("принят выбор файлов", bad)
				}
			}
			if len(callLog(t, log)) != 0 {
				t.Fatal("docker вызван до проверки аргументов")
			}
		})
	}
	t.Run("preconditions", func(t *testing.T) {
		f := newTypedFixture(t, "laravel")
		log := f.mockDocker(t, phpstanJSON(t, f.body(t, nil), 0, nil))
		raw := strings.Replace(string(readFixture(t, f.config)), ", composer.lock, bootstrap, config]", ", bootstrap, config]", 1)
		noLock := filepath.Join(f.base, "nolock.yaml")
		writeFixture(t, noLock, []byte(raw))
		cfg, err := loadConfig(noLock)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := snapshot(cfg)
		if _, err := phpTyped(cfg, m, []string{"Subject.php"}); err == nil || !strings.Contains(err.Error(), "composer.lock") {
			t.Fatal("composer.lock вне snapshot должен отклоняться:", err)
		}
		raw = strings.Replace(string(readFixture(t, f.config)), ", bootstrap, config]", ", config]", 1)
		noBoot := filepath.Join(f.base, "noboot.yaml")
		writeFixture(t, noBoot, []byte(raw))
		cfg, _ = loadConfig(noBoot)
		m, _ = snapshot(cfg)
		if _, err := phpTyped(cfg, m, []string{"Subject.php"}); err == nil || !strings.Contains(err.Error(), "bootstrap/app.php") {
			t.Fatal("bootstrap/app.php вне snapshot должен отклоняться:", err)
		}
		if len(callLog(t, log)) != 0 {
			t.Fatal("docker вызван до проверки предусловий")
		}
		cfgNoSDK := f.cfg
		cfgNoSDK.SDK = nil
		if _, err := phpTyped(cfgNoSDK, f.manifest, []string{"Subject.php"}); err == nil {
			t.Fatal("без блока sdk должен быть отказ")
		}
	})
}

func TestTypedCLI(t *testing.T) {
	f := newTypedFixture(t, "laravel")
	log := f.mockDocker(t, phpstanJSON(t, f.body(t, nil), 1, nil))
	const run = "typed"
	batch := runOK(t, "prepare", f.config, run).(TaskBatch)
	if batch.SDK != nil {
		t.Fatal("новый run не должен содержать записей SDK")
	}
	plainBatch := mustMarshal(t, batch)
	if bytes.Contains(plainBatch, []byte(`"sdk"`)) {
		t.Fatal("TaskBatch без SDK должен сериализоваться как прежде")
	}
	// Роли и согласование хоста до импорта фактов.
	for _, task := range batch.Tasks {
		path := filepath.Join(f.base, task.TaskID+".json")
		writeFixture(t, path, legacyMarshal(t, sampleResult(t, task, f.source)))
		runOK(t, "submit", f.config, run, task.TaskID, path)
	}
	view := runOK(t, "review", f.config, run).(ReviewContext)
	decision := ReviewDecision{1, "decision-1", run, view.SnapshotID, view.BasisSHA256, "host-session", "Обоснованное согласование", append([]Assessment{}, view.Entries[0].Result.Assessments...), []string{"Суждение хоста; не сертификат"}}
	decisionPath := filepath.Join(f.base, "host-decision.json")
	writeFixture(t, decisionPath, legacyMarshal(t, decision))
	runOK(t, "review", f.config, run, decisionPath)
	runOK(t, "report", f.config, run)
	if got := runOK(t, "status", f.config, run).(Status); got.HostReviewState != "current" {
		t.Fatal("review должен быть current до импорта")
	}
	stateBefore := readFixture(t, filepath.Join(f.base, "runs", run, "state.json"))
	if bytes.Contains(stateBefore, []byte(`"sdk"`)) {
		t.Fatal("state без SDK не должен содержать ключ sdk")
	}
	// Отказы до docker: без блока sdk и при stale snapshot.
	noSDK := filepath.Join(f.base, "nosdk.yaml")
	writeFixture(t, noSDK, bytes.Replace(readFixture(t, f.config), []byte("sdk: {profile: laravel, timeout_seconds: 1}"), []byte(""), 1))
	runFail(t, "php-typed", noSDK, run, "Subject.php")
	if len(callLog(t, log)) != 0 {
		t.Fatal("docker вызван без блока sdk")
	}
	writeFixture(t, filepath.Join(f.source, "Subject.php"), []byte(f.php+"// stale\n"))
	runFail(t, "php-typed", f.config, run, "Subject.php")
	writeFixture(t, filepath.Join(f.source, "Subject.php"), []byte(f.php))
	if len(callLog(t, log)) != 0 {
		t.Fatal("docker вызван при stale snapshot")
	}
	// Успешный импорт.
	response := runOK(t, "php-typed", f.config, run, "Subject.php").(map[string]any)
	if response["evidence_kind"] != typedEvidence || response["facts"] != 2 || response["diagnostics"] != 1 || response["snapshot_id"] != batch.SnapshotID {
		t.Fatalf("ответ php-typed: %v", response)
	}
	artifact := response["artifact"].(string)
	info, err := os.Stat(artifact)
	if err != nil || info.Mode().Perm() != 0400 || !bytes.Equal(readFixture(t, artifact), f.body(t, nil)) {
		t.Fatal("артефакт должен быть сырыми байтами экспортёра с режимом 0400", err)
	}
	if _, err := os.Stat(filepath.Join(f.base, "runs", run, "report.json")); !os.IsNotExist(err) {
		t.Fatal("импорт должен инвалидировать прежний отчёт")
	}
	var state State
	if err := json.Unmarshal(readFixture(t, filepath.Join(f.base, "runs", run, "state.json")), &state); err != nil || len(state.SDK) != 1 {
		t.Fatal("state должен содержать одну запись SDK", err)
	}
	record := state.SDK[0]
	if record.Artifact != filepath.Base(artifact) || record.SHA256 != digest(f.body(t, nil)) || record.PHPStanVersion != "2.2.1" || record.LarastanVersion != "v3.10.0" || !strings.HasSuffix(record.RecordedAt, "Z") {
		t.Fatalf("запись SDK: %+v", record)
	}
	if got := runOK(t, "status", f.config, run).(Status); got.HostReviewState != "outdated" {
		t.Fatal("импорт фактов должен делать review outdated")
	}
	// Новое решение с прежним basis отклоняется (CAS); байт-идентичный повтор остаётся идемпотентным.
	stale := decision
	stale.ReviewID = "decision-2"
	stalePath := filepath.Join(f.base, "host-decision-2.json")
	writeFixture(t, stalePath, legacyMarshal(t, stale))
	runFail(t, "review", f.config, run, stalePath)
	tasks := runOK(t, "tasks", f.config, run).(TaskBatch)
	if len(tasks.SDK) != 1 || tasks.SDK[0].Artifact != artifact || tasks.SDK[0].SHA256 != record.SHA256 {
		t.Fatal("TaskBatch должен отдавать запись с абсолютным путём артефакта")
	}
	// Повторный импорт добавляет запись; прежняя остаётся историей.
	runOK(t, "php-typed", f.config, run, "Subject.php")
	if err := json.Unmarshal(readFixture(t, filepath.Join(f.base, "runs", run, "state.json")), &state); err != nil || len(state.SDK) != 2 || state.SDK[0] != record {
		t.Fatal("повторный импорт должен дописывать запись", err)
	}
	// Подмена байта артефакта блокирует чтение run любой командой.
	data := readFixture(t, artifact)
	if err := os.Chmod(artifact, 0600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, artifact, append(data[:len(data)-1], '}', ' '))
	runFail(t, "status", f.config, run)
	runFail(t, "tasks", f.config, run)
	writeFixture(t, artifact, data)
	runOK(t, "status", f.config, run)
	// Отказ экспортёра не меняет state и не создаёт артефакт.
	before := readFixture(t, filepath.Join(f.base, "runs", run, "state.json"))
	entries, _ := os.ReadDir(filepath.Join(f.base, "runs", run))
	t.Setenv("SPEC_AUDIT_DOCKER_MODE", "internal_error")
	runFail(t, "php-typed", f.config, run, "Subject.php")
	t.Setenv("SPEC_AUDIT_DOCKER_MODE", "")
	after, _ := os.ReadDir(filepath.Join(f.base, "runs", run))
	if !bytes.Equal(before, readFixture(t, filepath.Join(f.base, "runs", run, "state.json"))) || len(after) != len(entries) {
		t.Fatal("отказ php-typed изменил state или оставил артефакт")
	}
	// Старый run без SDK читается байт-в-байт как прежде.
	plain := runOK(t, "prepare", f.config, "plain").(TaskBatch)
	if bytes.Contains(mustMarshal(t, plain), []byte(`"sdk"`)) || bytes.Contains(readFixture(t, filepath.Join(f.base, "runs/plain/state.json")), []byte(`"sdk"`)) {
		t.Fatal("run без импорта не должен получать ключ sdk")
	}
}

func TestPHPSDKControlBaseline(t *testing.T) {
	// Frozen semantic expectations for SA-031…034; the pin changes only with an explicit contract revision.
	data := readFixture(t, "../acceptance/php-sdk-control.json")
	if digest(data) != "c584ffac8533636c70a6fb8896ffbe1b82648f6113dbff65dc5667a833500ecb" {
		t.Fatal("php-sdk-control.json изменён без пересмотра пина")
	}
	var control struct {
		Version int `json:"version"`
		Cases   []struct {
			ID          string `json:"id"`
			Requirement string `json:"requirement"`
			Kind        string `json:"kind"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &control); err != nil || control.Version != 1 || len(control.Cases) < 30 {
		t.Fatal("контроль повреждён", err)
	}
	seen := map[string]bool{}
	for _, c := range control.Cases {
		if seen[c.ID] || !strings.HasPrefix(c.Requirement, "REQ-SA-03") || !oneOf(c.Kind, "positive", "report", "mutation", "history", "negative") {
			t.Fatal("некорректный case контроля", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestTypedNoLeak(t *testing.T) {
	f := newTypedFixture(t, "laravel")
	f.mockDocker(t, phpstanJSON(t, f.body(t, nil), 0, nil))
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	if _, err := phpTyped(f.cfg, f.manifest, []string{"Subject.php"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPEC_AUDIT_DOCKER_MODE", "internal_error")
	_, _ = phpTyped(f.cfg, f.manifest, []string{"Subject.php"})
	logs := buffer.String()
	for _, secret := range []string{"must not execute", "spec_audit_disabled", `"facts"`, "Internal error", "class Demo"} {
		if strings.Contains(logs, secret) {
			t.Fatal("журнал раскрывает данные:", secret)
		}
	}
	if !strings.Contains(logs, "php-typed: анализ завершён") || !strings.Contains(logs, "php-typed: workspace") {
		t.Fatal("ожидаемые события журнала отсутствуют")
	}
}
