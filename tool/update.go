//go:build darwin || linux

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Set at release build time through -ldflags -X; a source build stays "dev" without a key and refuses update offline.
var (
	version             = "dev"
	releasePublicKeyHex string
	releaseBaseURL      = "https://github.com/StenHigh/spec-audit/releases/latest/download"
)

const (
	releaseTimeout      = 120 * time.Second
	releaseSchema       = "spec-audit-release/1"
	releaseManifestName = "release-manifest.json"
	releaseSigName      = "release-manifest.sig"
	binaryName          = "spec-audit"
)

var (
	releaseVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	platformTokenRE  = regexp.MustCompile(`^[a-z0-9]+$`)
)

type ReleaseAsset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type ReleaseManifest struct {
	SchemaVersion string         `json:"schema_version"`
	Version       string         `json:"version"`
	Assets        []ReleaseAsset `json:"assets"`
}

func releaseBuild(current, pubHex string) bool {
	return releaseVersionRE.MatchString(current) && pubHex != ""
}

func runVersion() map[string]any {
	return map[string]any{"version": version, "release": releaseBuild(version, releasePublicKeyHex), "os": runtime.GOOS, "arch": runtime.GOARCH}
}

// Verify before decoding: unsigned bytes never reach the JSON parser.
func parseManifest(raw, sig []byte, pubHex string) (ReleaseManifest, error) {
	var m ReleaseManifest
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return m, errors.New("встроенный публичный ключ релиза недопустим")
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(pub), raw, sig) {
		return m, errors.New("подпись манифеста релиза не подтверждена")
	}
	if err := strictJSON(raw, &m); err != nil {
		return m, fmt.Errorf("манифест релиза: %w", err)
	}
	if m.SchemaVersion != releaseSchema || !releaseVersionRE.MatchString(m.Version) || len(m.Assets) == 0 {
		return m, errors.New("манифест релиза не соответствует контракту")
	}
	seen := map[string]bool{}
	for _, asset := range m.Assets {
		key := asset.OS + "/" + asset.Arch
		if !platformTokenRE.MatchString(asset.OS) || !platformTokenRE.MatchString(asset.Arch) || seen[key] || asset.File != binaryName+"-"+asset.OS+"-"+asset.Arch || !validDigest(asset.SHA256) {
			return m, errors.New("ассет манифеста релиза не соответствует контракту")
		}
		seen[key] = true
	}
	return m, nil
}

func versionParts(value string) ([3]int, error) {
	var out [3]int
	if !releaseVersionRE.MatchString(value) {
		return out, errors.New("версия не в формате X.Y.Z")
	}
	for i, part := range strings.SplitN(value, ".", 3) {
		n, err := strconv.Atoi(part)
		if err != nil {
			return out, err
		}
		out[i] = n
	}
	return out, nil
}

func newerVersion(current, candidate string) (bool, error) {
	cur, err := versionParts(current)
	if err != nil {
		return false, err
	}
	next, err := versionParts(candidate)
	if err != nil {
		return false, err
	}
	for i := range cur {
		if next[i] != cur[i] {
			return next[i] > cur[i], nil
		}
	}
	return false, nil
}

func assetFor(m ReleaseManifest, goos, goarch string) (ReleaseAsset, bool) {
	for _, asset := range m.Assets {
		if asset.OS == goos && asset.Arch == goarch {
			return asset, true
		}
	}
	return ReleaseAsset{}, false
}

// Errors name the failing step only: transport errors embed the URL and bodies may be attacker-controlled.
func fetch(client *http.Client, url string, limit int64) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, errors.New("сервер релиза недоступен")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("сервер релиза ответил статусом %d", resp.StatusCode)
	}
	data, err := readLimited(resp.Body, limit)
	if err != nil {
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("ответ релиза: %w", err)
		}
		return nil, errors.New("чтение ответа релиза прервано")
	}
	return data, nil
}

func runUpdate(client *http.Client, baseURL, exe, current, pubHex string) (map[string]any, error) {
	if !releaseBuild(current, pubHex) {
		return nil, errors.New("обновление недоступно в сборке из исходников; установите релиз через install.sh")
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return nil, err
	}
	if filepath.Base(resolved) != binaryName {
		return nil, errors.New("обновляется только исполняемый файл с именем spec-audit")
	}
	raw, err := fetch(client, baseURL+"/"+releaseManifestName, maxConfig)
	if err != nil {
		return nil, err
	}
	sig, err := fetch(client, baseURL+"/"+releaseSigName, ed25519.SignatureSize)
	if err != nil {
		return nil, err
	}
	m, err := parseManifest(raw, sig, pubHex)
	if err != nil {
		return nil, err
	}
	slog.Info("update: манифест проверен", "version", m.Version, "current", current)
	newer, err := newerVersion(current, m.Version)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"updated": false, "from": current, "to": current, "latest": m.Version}
	if !newer {
		slog.Info("update: обновление не требуется", "version", current)
		return result, nil
	}
	asset, ok := assetFor(m, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return nil, errors.New("в релизе нет ассета для текущей платформы")
	}
	slog.Debug("update: ассет выбран", "os", asset.OS, "arch", asset.Arch, "file", asset.File)
	bin, err := fetch(client, baseURL+"/"+asset.File, maxFile)
	if err != nil {
		return nil, err
	}
	if digest(bin) != asset.SHA256 {
		return nil, errors.New("sha256 скачанного бинарника не совпадает с манифестом")
	}
	dir, err := os.OpenRoot(filepath.Dir(resolved))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err := atomicWrite(dir, binaryName, bin, 0755); err != nil {
		return nil, err
	}
	slog.Info("update: бинарник заменён", "from", current, "to", m.Version, "bytes", len(bin))
	result["updated"], result["to"] = true, m.Version
	return result, nil
}
