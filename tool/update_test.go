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
	if releaseBuild("1.2.3", "") || releaseBuild("dev", "ab") || !releaseBuild("1.2.3", "ab") {
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
		name     string
		current  string
		exeName  string
		noKey    bool
		release  func(t *testing.T) testRelease
		mutate   func(t *testing.T, r testRelease)
		fetches  int32
		updated  bool
		fail     bool
		wantBody []byte
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
			raw := []byte(`{"schema_version":"spec-audit-release/1","version":"0.1.1","version":"0.1.2","assets":[]}`)
			r.write(t, releaseManifestName, raw)
			r.write(t, releaseSigName, ed25519.Sign(r.priv, raw))
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
			pubHex := r.pubHex
			if tc.noKey {
				pubHex = ""
			}
			got, err := runUpdate(client, srv.URL, exe, tc.current, pubHex)
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v got=%v", err, got)
			}
			if err != nil && (strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "tampered")) {
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
