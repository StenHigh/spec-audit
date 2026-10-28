//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type SavedSource struct {
	Path       string          `json:"path"`
	Kind       string          `json:"kind"`
	SHA256     string          `json:"sha256"`
	Bytes      int             `json:"bytes"`
	Origin     json.RawMessage `json:"origin,omitempty"`
	Full       bool            `json:"full"`
	URL        string          `json:"url,omitempty"`
	RawURL     string          `json:"raw_url,omitempty"`
	LiveURL    string          `json:"current_url,omitempty"`
	Limitation string          `json:"limitation,omitempty"`
}
type sourceInventory struct {
	SchemaVersion int           `json:"schema_version"`
	SnapshotID    string        `json:"snapshot_id"`
	Sources       []SavedSource `json:"sources"`
}

func putObject(reports *os.Root, data []byte) error {
	hash := digest(data)
	name := "._objects/" + hash
	if err := reports.MkdirAll("._objects", 0700); err != nil {
		return err
	}
	old, err := readRoot(reports, name, maxFile)
	if err == nil {
		if !bytes.Equal(old, data) {
			return errors.New("повреждён существующий общий объект")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return atomicWrite(reports, name, data, 0400)
}

func captureSources(reports, run *os.Root, m Manifest) error {
	root, err := os.OpenRoot(m.Config.ProjectRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	inventory := sourceInventory{1, m.SnapshotID, []SavedSource{}}
	citations := citationRetention(m.Config)
	if citations {
		inventory.SchemaVersion = 2
	}
	origins := map[string]json.RawMessage{}
	if m.External != nil {
		raw, err := readExact(filepath.Join(filepath.Dir(m.Config.External.Input), "source-manifest.json"), maxState)
		if err != nil {
			return err
		}
		manifest, err := parseExternalManifest(raw)
		if err != nil {
			return err
		}
		for _, file := range manifest.Files {
			origins[file.Path] = file.Origin
		}
	}
	add := func(file SourceFile, data []byte) error {
		if digest(data) != file.SHA256 || len(data) != file.Bytes {
			return fmt.Errorf("capture: изменился %s", file.Path)
		}
		if !citations {
			if err := putObject(reports, data); err != nil {
				return err
			}
		}
		source := SavedSource{Path: file.Path, Kind: file.Kind, SHA256: file.SHA256, Bytes: file.Bytes, Origin: origins[file.Path], Full: !citations}
		if citations {
			source.Limitation = "Сохранены только цитаты; полный исторический исходник не сохраняется (source_retention: citations). Текущий файл не подставляется."
		}
		if m.External != nil && (file.Kind == "code" || file.Kind == "tests") {
			source.Origin = m.External.CodeBasis
		}
		if len(source.Origin) == 0 {
			source.Origin, _ = json.Marshal(map[string]string{"kind": "snapshot", "snapshot_id": m.SnapshotID, "snapshot_sha256": m.SnapshotID, "path": file.Path})
		}
		inventory.Sources = append(inventory.Sources, source)
		return nil
	}
	selected := map[string]bool{}
	for _, file := range m.Files {
		data, err := readRoot(root, file.Path, maxFile)
		if err != nil {
			return err
		}
		if err := add(file, data); err != nil {
			return err
		}
		selected[file.Path] = true
	}
	if m.External != nil {
		dir := filepath.Dir(m.Config.External.Input)
		raw, err := readExact(filepath.Join(dir, "source-manifest.json"), maxState)
		if err != nil {
			return err
		}
		manifest, err := parseExternalManifest(raw)
		if err != nil {
			return err
		}
		for _, file := range manifest.Files {
			if !selected[file.Path] {
				data, err := readRoot(root, file.Path, maxFile)
				if err != nil {
					return err
				}
				if err := add(SourceFile{Path: file.Path, Kind: "archive", SHA256: file.SHA256, Bytes: int(file.Size)}, data); err != nil {
					return err
				}
			}
		}
		for _, name := range []string{"source-manifest.json", "profile.json", "audit-input.json", "code-files.json", "original-config.yaml"} {
			data, err := readExact(filepath.Join(dir, name), maxFile)
			if err != nil {
				return err
			}
			if err := add(SourceFile{Path: "_metadata/" + name, Kind: "metadata", SHA256: digest(data), Bytes: len(data)}, data); err != nil {
				return err
			}
		}
	}
	sort.Slice(inventory.Sources, func(i, j int) bool { return inventory.Sources[i].Path < inventory.Sources[j].Path })
	if err := writeJSON(run, "sources.json", inventory, 0400); err != nil {
		return err
	}
	if citations {
		return nil
	}
	return run.MkdirAll("sources", 0700)
}

func readSourceInventory(reports, run *os.Root, m Manifest) (sourceInventory, error) {
	data, err := readRoot(run, "sources.json", maxState)
	if os.IsNotExist(err) {
		if m.External != nil || citationRetention(m.Config) {
			return sourceInventory{}, errors.New("у run отсутствует обязательная опись источников")
		}
		inv := sourceInventory{1, m.SnapshotID, []SavedSource{}}
		for _, file := range m.Files {
			inv.Sources = append(inv.Sources, SavedSource{Path: file.Path, Kind: file.Kind, SHA256: file.SHA256, Bytes: file.Bytes, Full: false, Limitation: "Полный исторический исходник недоступен; сохранены только цитаты. Текущий файл не подставляется."})
		}
		return inv, nil
	}
	var inv sourceInventory
	if err != nil {
		return inv, err
	}
	if err := strictJSON(data, &inv); err != nil {
		return inv, err
	}
	citations := citationRetention(m.Config)
	wantSchema := 1
	if citations {
		wantSchema = 2
	}
	if inv.SchemaVersion != wantSchema || inv.SnapshotID != m.SnapshotID {
		return inv, errors.New("sources: несовместимое основание")
	}
	seen := map[string]SavedSource{}
	for _, source := range inv.Sources {
		if !localPath(source.Path) || !validDigest(source.SHA256) || source.Full == citations || source.Bytes < 0 || source.Bytes > maxFile || !oneOf(source.Kind, "spec", "code", "tests", "archive", "metadata") {
			return inv, errors.New("sources: неверная запись")
		}
		if citations && (source.URL != "" || source.RawURL != "" || source.LiveURL != "" || strings.TrimSpace(source.Limitation) == "") {
			return inv, errors.New("sources: citations требует ограничение и не допускает ссылки на полный исходник")
		}
		if _, ok := seen[source.Path]; ok {
			return inv, errors.New("sources: повторный путь")
		}
		seen[source.Path] = source
		if citations {
			continue
		}
		data, err := readRoot(reports, "._objects/"+source.SHA256, maxFile)
		if err != nil {
			return inv, err
		}
		if digest(data) != source.SHA256 || len(data) != source.Bytes {
			return inv, errors.New("sources: повреждён общий объект")
		}
	}
	for _, file := range m.Files {
		s, ok := seen[file.Path]
		if !ok || s.SHA256 != file.SHA256 || s.Bytes != file.Bytes || s.Kind != file.Kind {
			return inv, errors.New("sources: отсутствует зависимость manifest")
		}
	}
	return inv, nil
}

type sourceLine struct {
	Number int
	Text   string
}
type sourcePage struct {
	Path, SHA256, Origin, Raw string
	Lines                     []sourceLine
}

var sourceTemplate = template.Must(template.New("source").Funcs(template.FuncMap{"uiStyles": uiStyles}).Parse(`<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>{{.Path}} · сохранённый исходник</title><style>{{uiStyles}}pre{white-space:pre;overflow:auto}.line{display:block;min-height:1.5em}.line:target,.line.selected{background:#715e14}.number{display:inline-block;min-width:5ch;margin-right:1em;color:#929baa;text-decoration:none;user-select:none}</style></head><body><main><h1>{{.Path}}</h1><p>Полный сохранённый исходник · SHA-256 <code>{{.SHA256}}</code></p><p>Основание: <code>{{.Origin}}</code></p><p><a href="{{.Raw}}" download>Скачать точные bytes (BOM/CRLF сохранены)</a> · <a href="../report.html">Отчёт</a></p><pre>{{range .Lines}}<span class="line" id="L{{.Number}}"><a class="number" href="#L{{.Number}}">{{.Number}}</a>{{.Text}}</span>{{end}}</pre></main><script>const q=new URLSearchParams(location.search),a=Number(q.get('start')),b=Number(q.get('end'));if(Number.isInteger(a)&&Number.isInteger(b)&&a>=1&&b>=a){for(const e of document.querySelectorAll('.line')){const n=Number(e.id.slice(1));e.classList.toggle('selected',n>=a&&n<=b)}};</script></body></html>`))

func renderSource(data []byte, source SavedSource) ([]byte, error) {
	page := sourcePage{Path: source.Path, SHA256: source.SHA256, Origin: string(source.Origin), Raw: sourcePageKey(source) + ".bin"}
	if utf8.Valid(data) {
		for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			page.Lines = append(page.Lines, sourceLine{i + 1, strings.TrimSuffix(line, "\r")})
		}
	} else {
		page.Lines = []sourceLine{{1, "Бинарный файл: точные bytes доступны по ссылке скачивания."}}
	}
	var b cappedBuffer
	b.limit = maxFile
	err := sourceTemplate.Execute(&b, page)
	return b.Bytes(), err
}

func sourcePageKey(s SavedSource) string {
	return digest([]byte(s.Path + "\x00" + s.SHA256 + "\x00" + string(s.Origin)))
}

func sourceRangeURL(hash string, start, end int) string {
	return fmt.Sprintf("sources/%s.html?start=%d&end=%d#L%d", hash, start, end, start)
}

func attachSources(reports, run *os.Root, report *Report, m Manifest) error {
	inv, err := readSourceInventory(reports, run, m)
	if err != nil {
		return err
	}
	report.External = m.External
	report.Sources = inv.Sources
	for i, source := range report.Sources {
		if source.Full {
			if err := run.MkdirAll("sources", 0700); err != nil {
				return err
			}
			if err := noSymlinks(run, "sources"); err != nil {
				return err
			}
			data, err := readRoot(reports, "._objects/"+source.SHA256, maxFile)
			if err != nil {
				return err
			}
			page, err := renderSource(data, source)
			if err != nil {
				return err
			}
			if err := atomicWrite(run, "sources/"+sourcePageKey(source)+".html", page, 0600); err != nil {
				return err
			}
			if err := atomicWrite(run, "sources/"+sourcePageKey(source)+".bin", data, 0600); err != nil {
				return err
			}
			source.URL = sourceRangeURL(sourcePageKey(source), 1, 1)
			source.RawURL = "sources/" + sourcePageKey(source) + ".bin"
			var origin map[string]json.RawMessage
			_ = json.Unmarshal(source.Origin, &origin)
			var live string
			_ = json.Unmarshal(origin["source_url"], &live)
			if safeExternalURL(live) {
				source.LiveURL = live
			}
			report.Sources[i] = source
		}
	}
	linkReportSources(report)
	return nil
}

func linkReportSources(report *Report) {
	byPath := map[string]SavedSource{}
	for _, source := range report.Sources {
		byPath[source.Path] = source
	}
	for i := range report.Navigation.Files {
		file := &report.Navigation.Files[i]
		s, ok := byPath[file.Path]
		if !ok {
			continue
		}
		file.SourceURL = s.URL
		file.SourceLimitation = s.Limitation
		file.LiveURL = s.LiveURL
		for j := range file.Evidence {
			c := file.Evidence[j].Citation
			if s.Full {
				file.Evidence[j].SourceURL = sourceRangeURL(sourcePageKey(s), c.LineStart, c.LineEnd)
			}
		}
	}
}
