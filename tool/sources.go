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
		if err := putObject(reports, data); err != nil {
			return err
		}
		source := SavedSource{Path: file.Path, Kind: file.Kind, SHA256: file.SHA256, Bytes: file.Bytes, Origin: origins[file.Path], Full: true}
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
	return run.MkdirAll("sources", 0700)
}

func readSourceInventory(reports, run *os.Root, m Manifest) (sourceInventory, error) {
	data, err := readRoot(run, "sources.json", maxState)
	if os.IsNotExist(err) {
		if m.External != nil {
			return sourceInventory{}, errors.New("у внешнего run отсутствует обязательная опись полных источников")
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
	if inv.SchemaVersion != 1 || inv.SnapshotID != m.SnapshotID {
		return inv, errors.New("sources: несовместимое основание")
	}
	seen := map[string]SavedSource{}
	for _, source := range inv.Sources {
		if !localPath(source.Path) || !validDigest(source.SHA256) || !source.Full || source.Bytes < 0 || source.Bytes > maxFile {
			return inv, errors.New("sources: неверная запись")
		}
		if _, ok := seen[source.Path]; ok {
			return inv, errors.New("sources: повторный путь")
		}
		seen[source.Path] = source
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
	byPath := map[string]SavedSource{}
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
	return nil
}

// Latest-only local publication still needs the resources linked by that report.
func copyReportResources(reportsDir, runID, target string) error {
	reports, err := os.OpenRoot(reportsDir)
	if err != nil {
		return err
	}
	defer reports.Close()
	put := func(name string, data []byte) error {
		parent := filepath.Join(target, filepath.Dir(name))
		if err := mkdirExact(parent, 0755); err != nil {
			return err
		}
		root, err := os.OpenRoot(parent)
		if err != nil {
			return err
		}
		defer root.Close()
		return atomicWrite(root, filepath.Base(name), data, 0644)
	}
	data, err := readRoot(reports, runID+"/report.json", maxState)
	if err == nil {
		if err := put("report.json", data); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	sources := filepath.Join(reportsDir, runID, "sources")
	if _, err := os.Lstat(sources); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	guard, _, err := openExact(sources)
	if err != nil {
		return err
	}
	guard.Close()
	return walkBundleArea(sources, func(name string, data []byte, _ uint32) error { return put("sources/"+name, data) })
}
