//go:build darwin || linux

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

type countingTransport struct {
	calls atomic.Int32
	next  http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return c.next.RoundTrip(r)
}

// A signed release directory: manifest, signature and one asset for the current platform.
type testRelease struct {
	dir    string
	pubHex string
	priv   ed25519.PrivateKey
}

func newTestRelease(t *testing.T, version string, bin []byte, mutate func(m *ReleaseManifest)) testRelease {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := testRelease{dir: t.TempDir(), pubHex: hex.EncodeToString(pub), priv: priv}
	m := ReleaseManifest{SchemaVersion: releaseSchema, Version: version, Assets: []ReleaseAsset{
		{OS: runtime.GOOS, Arch: runtime.GOARCH, File: binaryName + "-" + runtime.GOOS + "-" + runtime.GOARCH, SHA256: digest(bin)},
		{OS: "linux", Arch: "s390x", File: "spec-audit-linux-s390x", SHA256: digest([]byte("other"))},
	}}
	if mutate != nil {
		mutate(&m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	r.write(t, releaseManifestName, raw)
	r.write(t, releaseSigName, ed25519.Sign(priv, raw))
	r.write(t, m.Assets[0].File, bin)
	return r
}

func (r testRelease) manifest(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.dir, releaseManifestName))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (r testRelease) write(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestVersion(t *testing.T) {
	got := runVersion()
	if got["version"] != "dev" || got["release"] != false || got["os"] != runtime.GOOS || got["arch"] != runtime.GOARCH {
		t.Fatalf("%v", got)
	}
	key := strings.Repeat("ab", ed25519.PublicKeySize)
	if releaseBuild("1.2.3", "") || releaseBuild("dev", key) || releaseBuild("1.2.3", "ab") || releaseBuild("1.2.3", strings.Repeat("zz", ed25519.PublicKeySize)) || !releaseBuild("1.2.3", key) {
		t.Fatal("releaseBuild")
	}
}

func TestNewerVersion(t *testing.T) {
	for _, tc := range []struct {
		cur, cand string
		newer     bool
		fail      bool
	}{
		{"0.1.0", "0.1.1", true, false}, {"0.1.0", "0.1.0", false, false}, {"0.2.0", "0.1.9", false, false},
		{"0.9.9", "0.10.0", true, false}, {"1.0.0", "0.99.99", false, false}, {"dev", "0.1.0", false, true}, {"0.1.0", "v0.1.1", false, true},
	} {
		newer, err := newerVersion(tc.cur, tc.cand)
		if (err != nil) != tc.fail || newer != tc.newer {
			t.Fatalf("%s → %s: newer=%v err=%v", tc.cur, tc.cand, newer, err)
		}
	}
}

func TestUpdate(t *testing.T) {
	old, next := []byte("old-binary"), []byte("new-binary-bytes")
	big := bytes.Repeat([]byte("x"), maxFile+1)
	cases := []struct {
		name       string
		current    string
		exeName    string
		noKey      bool
		umask      int
		viaSymlink bool
		release    func(t *testing.T) testRelease
		mutate     func(t *testing.T, r testRelease)
		fetches    int32
		updated    bool
		fail       bool
		wantBody   []byte
	}{
		{name: "dev_build", current: "dev", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, fail: true},
		{name: "no_key", current: "0.1.0", noKey: true, release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, fail: true},
		{name: "renamed_binary", current: "0.1.0", exeName: "spec-audit-copy", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, fail: true},
		{name: "bad_signature", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			r.write(t, releaseSigName, bytes.Repeat([]byte{1}, ed25519.SignatureSize))
		}, fetches: 2, fail: true},
		{name: "short_signature", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			r.write(t, releaseSigName, []byte("short"))
		}, fetches: 2, fail: true},
		{name: "bad_manifest_version", current: "0.1.0", release: func(t *testing.T) testRelease {
			return newTestRelease(t, "v0.1.1", next, nil)
		}, fetches: 2, fail: true},
		{name: "bad_manifest_schema", current: "0.1.0", release: func(t *testing.T) testRelease {
			return newTestRelease(t, "0.1.1", next, func(m *ReleaseManifest) { m.SchemaVersion = "spec-audit-release/2" })
		}, fetches: 2, fail: true},
		{name: "bad_manifest_file", current: "0.1.0", release: func(t *testing.T) testRelease {
			return newTestRelease(t, "0.1.1", next, func(m *ReleaseManifest) { m.Assets[0].File = "spec-audit" })
		}, fetches: 2, fail: true},
		{name: "bad_manifest_duplicate_key", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			// Otherwise valid: only the repeated key can cause the refusal.
			raw := bytes.Replace(r.manifest(t), []byte(`"version":"0.1.1"`), []byte(`"version":"0.1.1","version":"0.1.1"`), 1)
			r.write(t, releaseManifestName, raw)
			r.write(t, releaseSigName, ed25519.Sign(r.priv, raw))
		}, fetches: 2, fail: true},
		{name: "bad_manifest_extra_field", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			raw := bytes.Replace(r.manifest(t), []byte(`"version":"0.1.1"`), []byte(`"version":"0.1.1","notes":"x"`), 1)
			r.write(t, releaseManifestName, raw)
			r.write(t, releaseSigName, ed25519.Sign(r.priv, raw))
		}, fetches: 2, fail: true},
		{name: "bad_manifest_duplicate_platform", current: "0.1.0", release: func(t *testing.T) testRelease {
			return newTestRelease(t, "0.1.1", next, func(m *ReleaseManifest) { m.Assets = append(m.Assets, m.Assets[0]) })
		}, fetches: 2, fail: true},
		{name: "long_signature", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			sig, _ := os.ReadFile(filepath.Join(r.dir, releaseSigName))
			r.write(t, releaseSigName, append(sig, 0))
		}, fetches: 2, fail: true},
		{name: "already_latest", current: "0.1.1", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, fetches: 2},
		{name: "downgrade", current: "0.2.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.9", next, nil) }, fetches: 2},
		{name: "no_asset", current: "0.1.0", release: func(t *testing.T) testRelease {
			return newTestRelease(t, "0.1.1", next, func(m *ReleaseManifest) {
				m.Assets[0].OS, m.Assets[0].File = "plan9", "spec-audit-plan9-"+runtime.GOARCH
			})
		}, fetches: 2, fail: true},
		{name: "sha", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			r.write(t, binaryName+"-"+runtime.GOOS+"-"+runtime.GOARCH, []byte("tampered"))
		}, fetches: 3, fail: true},
		{name: "limit_manifest", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			r.write(t, releaseManifestName, bytes.Repeat([]byte("{"), maxConfig+1))
		}, fetches: 1, fail: true},
		{name: "limit_binary", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", big, nil) }, fetches: 3, fail: true},
		{name: "http_error", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, mutate: func(t *testing.T, r testRelease) {
			if err := os.Remove(filepath.Join(r.dir, binaryName+"-"+runtime.GOOS+"-"+runtime.GOARCH)); err != nil {
				t.Fatal(err)
			}
		}, fetches: 3, fail: true},
		{name: "ok", current: "0.1.0", release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, fetches: 3, updated: true, wantBody: next},
		{name: "ok_umask", current: "0.1.0", umask: 0077, release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, fetches: 3, updated: true, wantBody: next},
		{name: "ok_symlinked_exe", current: "0.1.0", viaSymlink: true, release: func(t *testing.T) testRelease { return newTestRelease(t, "0.1.1", next, nil) }, fetches: 3, updated: true, wantBody: next},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.release(t)
			if tc.mutate != nil {
				tc.mutate(t, r)
			}
			srv := httptest.NewServer(http.FileServer(http.Dir(r.dir)))
			defer srv.Close()
			transport := &countingTransport{next: srv.Client().Transport}
			client := &http.Client{Transport: transport, Timeout: releaseTimeout}
			exeDir := t.TempDir()
			exeName := binaryName
			if tc.exeName != "" {
				exeName = tc.exeName
			}
			exe := filepath.Join(exeDir, exeName)
			if err := os.WriteFile(exe, old, 0755); err != nil {
				t.Fatal(err)
			}
			target := exe
			if tc.viaSymlink {
				target = filepath.Join(t.TempDir(), "spec-audit-link")
				if err := os.Symlink(exe, target); err != nil {
					t.Fatal(err)
				}
			}
			if tc.umask != 0 {
				previous := syscall.Umask(tc.umask)
				defer syscall.Umask(previous)
			}
			pubHex := r.pubHex
			if tc.noKey {
				pubHex = ""
			}
			got, err := runUpdate(client, srv.URL, target, tc.current, pubHex)
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v got=%v", err, got)
			}
			if err != nil && (strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "tampered") || strings.Contains(err.Error(), releaseSchema) || strings.Contains(err.Error(), "0.1.1")) {
				t.Fatalf("ошибка раскрывает URL или байты ответа: %v", err)
			}
			if transport.calls.Load() != tc.fetches {
				t.Fatalf("сетевых вызовов %d, ожидалось %d", transport.calls.Load(), tc.fetches)
			}
			want := old
			if tc.wantBody != nil {
				want = tc.wantBody
			}
			body, err := os.ReadFile(exe)
			if err != nil || !bytes.Equal(body, want) {
				t.Fatalf("байты бинарника: %v %q", err, body)
			}
			info, err := os.Stat(exe)
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("права: %v %v", err, info.Mode())
			}
			if tc.fail {
				return
			}
			if got["updated"] != tc.updated || got["from"] != tc.current {
				t.Fatalf("%v", got)
			}
			if entries, _ := os.ReadDir(exeDir); len(entries) != 1 {
				t.Fatalf("временные файлы остались: %v", entries)
			}
		})
	}
}

func TestOpenSSLSignatureCompatible(t *testing.T) {
	out, err := exec.Command("openssl", "version").Output()
	if err != nil || !strings.HasPrefix(string(out), "OpenSSL 3") {
		t.Skipf("нужен OpenSSL 3: %q %v", out, err)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "key.pem")
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.Command("openssl", args...).Output()
		if err != nil {
			t.Fatalf("openssl %v: %v", args, err)
		}
		return out
	}
	run("genpkey", "-algorithm", "ed25519", "-out", key)
	der := run("pkey", "-in", key, "-pubout", "-outform", "DER")
	if len(der) < ed25519.PublicKeySize {
		t.Fatal("короткий DER")
	}
	pubHex := hex.EncodeToString(der[len(der)-ed25519.PublicKeySize:])
	raw, _ := json.Marshal(ReleaseManifest{SchemaVersion: releaseSchema, Version: "0.1.0", Assets: []ReleaseAsset{
		{OS: "darwin", Arch: "arm64", File: "spec-audit-darwin-arm64", SHA256: digest([]byte("a"))},
		{OS: "linux", Arch: "amd64", File: "spec-audit-linux-amd64", SHA256: digest([]byte("b"))},
	}})
	manifest := filepath.Join(dir, releaseManifestName)
	if err := os.WriteFile(manifest, raw, 0644); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, releaseSigName)
	run("pkeyutl", "-sign", "-rawin", "-inkey", key, "-in", manifest, "-out", sigPath)
	sig, err := os.ReadFile(sigPath)
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("подпись: %v %d", err, len(sig))
	}
	m, err := parseManifest(raw, sig, pubHex)
	if err != nil || m.Version != "0.1.0" || len(m.Assets) != 2 {
		t.Fatalf("подпись OpenSSL не принята Go: %v", err)
	}
	if _, err := parseManifest(append(raw, '\n'), sig, pubHex); err == nil {
		t.Fatal("изменённый манифест принят")
	}
}

// install.sh против локального релиза через file://: платформа, sha256 из манифеста, staged-публикация.
func TestInstallScript(t *testing.T) {
	for _, tool := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("нет %s", tool)
		}
	}
	if _, err := exec.LookPath("shasum"); err != nil {
		if _, err := exec.LookPath("sha256sum"); err != nil {
			t.Skip("нет shasum/sha256sum")
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!/bin/sh\necho installed-ok\n")
	run := func(t *testing.T, release testRelease, path string) (string, string, error) {
		t.Helper()
		home := t.TempDir()
		dest := filepath.Join(home, "bin")
		cmd := exec.Command("sh", script)
		cmd.Env = []string{"PATH=" + path, "HOME=" + home, "SPEC_AUDIT_RELEASE_BASE=file://" + release.dir, "SPEC_AUDIT_INSTALL_DIR=" + dest}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if entries, _ := filepath.Glob(filepath.Join(dest, ".spec-audit.staged.*")); len(entries) != 0 {
			t.Fatalf("staged-файл остался: %v", entries)
		}
		return dest, stderr.String(), err
	}
	t.Run("ok", func(t *testing.T) {
		release := newTestRelease(t, "0.1.0", bin, nil)
		dest, stderr, err := run(t, release, os.Getenv("PATH"))
		if err != nil {
			t.Fatalf("%v: %s", err, stderr)
		}
		installed := filepath.Join(dest, binaryName)
		data, err := os.ReadFile(installed)
		if err != nil || !bytes.Equal(data, bin) {
			t.Fatalf("установленный файл: %v", err)
		}
		if info, _ := os.Stat(installed); info.Mode().Perm() != 0755 {
			t.Fatalf("права %v", info.Mode())
		}
		if out, err := exec.Command(installed).Output(); err != nil || strings.TrimSpace(string(out)) != "installed-ok" {
			t.Fatalf("исполнение: %v %q", err, out)
		}
	})
	t.Run("sha_mismatch", func(t *testing.T) {
		release := newTestRelease(t, "0.1.0", bin, nil)
		release.write(t, binaryName+"-"+runtime.GOOS+"-"+runtime.GOARCH, []byte("tampered"))
		dest, stderr, err := run(t, release, os.Getenv("PATH"))
		if err == nil || !strings.Contains(stderr, "spec-audit install: sha256") {
			t.Fatalf("ожидался отказ по sha256: %v %s", err, stderr)
		}
		if _, err := os.Stat(filepath.Join(dest, binaryName)); !os.IsNotExist(err) {
			t.Fatal("бинарник установлен при несовпадении sha256")
		}
	})
	t.Run("missing_asset_sha", func(t *testing.T) {
		release := newTestRelease(t, "0.1.0", bin, func(m *ReleaseManifest) { m.Assets = m.Assets[1:] })
		dest, stderr, err := run(t, release, os.Getenv("PATH"))
		if err == nil || !strings.Contains(stderr, "нет sha256") {
			t.Fatalf("%v %s", err, stderr)
		}
		if _, err := os.Stat(filepath.Join(dest, binaryName)); !os.IsNotExist(err) {
			t.Fatal("бинарник установлен без sha256 в манифесте")
		}
	})
	t.Run("dest_is_dir", func(t *testing.T) {
		release := newTestRelease(t, "0.1.0", bin, nil)
		home := t.TempDir()
		dest := filepath.Join(home, "bin")
		if err := os.MkdirAll(filepath.Join(dest, binaryName), 0755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "SPEC_AUDIT_RELEASE_BASE=file://" + release.dir, "SPEC_AUDIT_INSTALL_DIR=" + dest}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil || !strings.Contains(stderr.String(), "каталог") {
			t.Fatalf("ожидался отказ: %v %s", err, stderr.String())
		}
		if entries, _ := os.ReadDir(filepath.Join(dest, binaryName)); len(entries) != 0 {
			t.Fatalf("файл перемещён внутрь каталога: %v", entries)
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		wrappers := t.TempDir()
		uname := "#!/bin/sh\ncase \"$1\" in -s) echo FreeBSD ;; -m) echo riscv64 ;; esac\n"
		if err := os.WriteFile(filepath.Join(wrappers, "uname"), []byte(uname), 0755); err != nil {
			t.Fatal(err)
		}
		release := newTestRelease(t, "0.1.0", bin, nil)
		if err := os.Remove(filepath.Join(release.dir, releaseManifestName)); err != nil {
			t.Fatal(err)
		}
		dest, stderr, err := run(t, release, wrappers+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err == nil || !strings.Contains(stderr, "не поддерживается") {
			t.Fatalf("%v %s", err, stderr)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal("каталог установки создан до проверки платформы")
		}
	})
}

// Сквозная поставка реальными процессами без сети и Docker: install.sh (file://) → version → skill install →
// init/index → update до следующей версии через локальный сервер релиза → skill update → повторный update.
func TestNativeDistribution(t *testing.T) {
	if testing.Short() {
		t.Skip("нативная сборка и процессы")
	}
	for _, tool := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("нет %s", tool)
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)
	next := t.TempDir()
	srv := httptest.NewServer(http.FileServer(http.Dir(next)))
	defer srv.Close()
	asset := binaryName + "-" + runtime.GOOS + "-" + runtime.GOARCH
	build := func(dir, version string) []byte {
		t.Helper()
		out := filepath.Join(dir, asset)
		flags := "-X main.version=" + version + " -X main.releasePublicKeyHex=" + pubHex + " -X main.releaseBaseURL=" + srv.URL
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", flags, "-o", out, ".")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("сборка %s: %v: %s", version, err, output)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	publish := func(dir, version string, bin []byte) {
		t.Helper()
		raw, _ := json.Marshal(ReleaseManifest{SchemaVersion: releaseSchema, Version: version, Assets: []ReleaseAsset{
			{OS: runtime.GOOS, Arch: runtime.GOARCH, File: asset, SHA256: digest(bin)},
		}})
		for name, data := range map[string][]byte{releaseManifestName: raw, releaseSigName: ed25519.Sign(priv, raw)} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	first := t.TempDir()
	binA := build(first, "0.1.0")
	publish(first, "0.1.0", binA)
	binB := build(next, "0.1.1")
	publish(next, "0.1.1", binB)

	home := t.TempDir()
	dest := filepath.Join(home, "bin")
	installer, _ := filepath.Abs(filepath.Join("..", "scripts", "install.sh"))
	sh := exec.Command("sh", installer)
	sh.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "SPEC_AUDIT_RELEASE_BASE=file://" + first, "SPEC_AUDIT_INSTALL_DIR=" + dest}
	if output, err := sh.CombinedOutput(); err != nil {
		t.Fatalf("install.sh: %v: %s", err, output)
	}
	installed := filepath.Join(dest, binaryName)
	run := func(level string, success bool, args ...string) map[string]any {
		t.Helper()
		cmd := exec.Command(installed, args...)
		cmd.Env = []string{"PATH=", "HOME=" + home, "LOG_LEVEL=" + level}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if (err == nil) != success {
			t.Fatalf("%v: %v: %s", args, err, &stderr)
		}
		if level == "error" && success && stderr.Len() != 0 {
			t.Fatalf("%v: успех не молчит: %s", args, &stderr)
		}
		if !success {
			if !json.Valid(stderr.Bytes()) || strings.Contains(stderr.String(), srv.URL) || strings.Contains(stderr.String(), "127.0.0.1") || strings.Contains(stderr.String(), "Usage") || strings.Contains(stderr.String(), "flag provided") {
				t.Fatalf("%v: диагностика не JSON, содержит usage или раскрывает URL: %s", args, &stderr)
			}
			return nil
		}
		var value map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
			t.Fatalf("%v: stdout не JSON-объект: %s", args, &stdout)
		}
		return value
	}
	if v := run("error", true, "version"); v["version"] != "0.1.0" || v["release"] != true {
		t.Fatalf("version после установки: %v", v)
	}
	proj := t.TempDir()
	run("error", false, "skill", "install", "--dir", proj)
	run("error", false, "skill", "install", "--dir", proj, "--host", "both", "--bogus")
	if got := run("error", true, "skill", "install", "--dir", proj, "--host", "both"); got["updated"] != true || got["version"] != "0.1.0" {
		t.Fatalf("skill install: %v", got)
	}
	if target, err := os.Readlink(filepath.Join(proj, ".claude", "skills", "spec-audit")); err != nil || target != skillLinkTarget {
		t.Fatalf("host-ссылка: %q %v", target, err)
	}
	if got := run("error", true, "init", filepath.Join(proj, "audit.yaml")); got["created"] != true {
		t.Fatalf("init: %v", got)
	}
	config, _ := fixture(t)
	if got := run("error", true, "index", config); got == nil {
		t.Fatal("index")
	}
	if got := run("error", true, "update"); got["updated"] != true || got["from"] != "0.1.0" || got["to"] != "0.1.1" {
		t.Fatalf("update: %v", got)
	}
	if data, err := os.ReadFile(installed); err != nil || !bytes.Equal(data, binB) {
		t.Fatal("установленный бинарник не равен ассету релиза")
	}
	if v := run("error", true, "version"); v["version"] != "0.1.1" || v["release"] != true {
		t.Fatalf("version после update: %v", v)
	}
	if got := run("error", true, "skill", "update", "--dir", proj, "--host", "both"); got["updated"] != false || got["version"] != "0.1.1" {
		t.Fatalf("skill update: %v", got)
	}
	if got := run("error", true, "update"); got["updated"] != false || got["from"] != "0.1.1" {
		t.Fatalf("повторный update: %v", got)
	}
	if err := os.Remove(filepath.Join(next, releaseSigName)); err != nil {
		t.Fatal(err)
	}
	run("error", false, "update")
	if data, err := os.ReadFile(installed); err != nil || !bytes.Equal(data, binB) {
		t.Fatal("отказ update изменил бинарник")
	}
}
