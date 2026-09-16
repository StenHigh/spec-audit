# Implementation Plan: Поставка spec-audit — установка и обновление бинарника и skill

Branch: codex/distribution-install-update
Created: 2026-09-16

## Original Request
Поставка spec-audit: установка и обновление бинарника и skill по модели Pri-Fly (install.sh + release-manifest с sha256, команда update с Ed25519-подписью через GitHub Actions secret, матрица darwin/arm64 + linux/amd64, встроенный skill с командами skill install/update и skill.lock, замена симлинков в пилоте разрешена)

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Безопасная установка audit-skill при init" + "Воспроизводимый выпуск macOS/Linux"
Rationale: оба пункта «Ближайших этапов» [ROADMAP](../ROADMAP.md) закрываются одним этапом «Поставка»: подписанный релиз с `install.sh`/`update` и встроенный skill с `skill install|update`; на docs-checkpoint пункты объединяются.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) §12.3, REQ-SA-001 (frozen §5), REQ-SA-014, REQ-SA-015 > [RULES.md](../RULES.md), [rules/base.md](../rules/base.md), [ARCHITECTURE.md](../ARCHITECTURE.md) > [docs/smsplace-adoption.md](../../docs/smsplace-adoption.md) (только пилотная задача) > ROADMAP (объём). Эталон дизайна — Pri-Fly (`internal/release/release.go`, `scripts/install.sh`, `.github/workflows/release.yml`, `openspec/specs/release-distribution/spec.md`) как образец, не норма.

### Решения владельца 2026-09-16
| Решение | Следствие |
| --- | --- |
| Матрица релиза только `darwin/arm64` + `linux/amd64` | Иные платформы — отказ до скачивания; «поддержка» заявляется только после реального исполнения на платформе |
| Публикация через GitHub Actions по тегу `v*`, приватный Ed25519-ключ — секрет окружения `release`, публичный — `-ldflags` | Dev-сборка без ключа отказывает в `update` без сети |
| Skill встроен в бинарник | Версия skill = версия бинарника; `skill update` обновляет копию до встроенной; сеть и checkout не нужны |
| Замена symlink `.spec-audit/skill` в пилоте разрешена | Только явным `skill install --replace`; чужие каталоги не заменяются |
| `init CONFIG` не меняется (frozen REQ-SA-001, §12.3) | Установка skill — отдельная явная команда, не флаг `init`; ROADMAP-формулировка «при init» уточняется на checkpoint |
| REQ-SA-015 остаётся нормой аудита | `update` пишет только свой исполняемый файл, `skill install|update` — только `DIR/.spec-audit/skill` и две host-ссылки; это отдельные явно вызванные операции владельца, фиксируется в §18 |

### Ключевые проектные решения (подтверждены разведкой и рефутерами)
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| Контракт — новый §18 tool-spec (REQ-SA-035…038, редакция 0.7); отдельный `docs/distribution.md` не создаётся; §1–10 и §6 не редактируются | Прецедент §14/§17: новые команды вводятся расширением; `acceptance/*.sha256` не покрывают tool-spec.md | `shasum -c` всех списков; `git diff` tool-spec затрагивает только новый раздел и «См. также» |
| Ассеты — **сырые бинарники** `spec-audit-<os>-<arch>` (без tar.gz); `release-manifest.json` = `{"schema_version":"spec-audit-release/1","version":"X.Y.Z","assets":[{"os","arch","file","sha256"}]}`; `release-manifest.sig` — 64 сырых байта Ed25519 над точными байтами файла манифеста | sha256 в подписанном манифесте покрывает байты бинарника так же, как архив; без tar исчезают `extractBinary`, семь негативов и tar-шаги. Совместимость `openssl pkeyutl -sign -rawin` ↔ `crypto/ed25519.Verify` проверена рефутером на OpenSSL 3 | `TestManifest*`, `TestOpenSSLSignatureCompatible` (skip без OpenSSL ≥ 3) |
| Порядок `update`: fetch manifest (≤ `maxConfig`) → fetch sig (ровно 64 байта) → `ed25519.Verify` → `strictJSON` → версия `^\d+\.\d+\.\d+$` строго больше текущей → ассет точного `runtime.GOOS/GOARCH` → fetch файла (≤ `maxFile`) → `digest == sha256` → `atomicWrite(os.OpenRoot(dir), "spec-audit", bin, 0755)` | Нет новых механизмов замены: `atomicWrite` (temp + rename + fsync dir) уже есть; равная/меньшая версия — успешный no-op `updated:false` | `TestUpdate` таблица: dev-сборка, подпись, downgrade, чужая платформа, sha, лимиты, HTTP ≠ 2xx; инвариант — байты бинарника неизменны при любом отказе |
| Без receipt управляемой установки | Receipt из двух констант не даёт безопасности (кто пишет receipt — пишет и бинарник) и запрещает `update` текущей ручной копии; достаточно: release-сборка (`version != "dev" && releasePublicKeyHex != ""`) и `filepath.Base(EvalSymlinks(os.Executable())) == "spec-audit"` | `TestUpdate/dev_build`, `/renamed_binary` |
| `version` → `{"version","release","os","arch"}`; переменные `main.version = "dev"`, `main.releasePublicKeyHex`, `main.releaseBaseURL` (GitHub `releases/latest/download`) через `-ldflags -X`; функции принимают параметры, глобали — только defaults | Тесты не мутируют package-переменные; e2e подменяет базу URL через `-X` при сборке тест-бинарника | `TestNativeDistribution` |
| Embed из корня репо: `embed.go` (`package dist`, `//go:embed skills/spec-audit docs/accepted-index.md docs/legacy-extraction.md docs/php-sdk-contract.md`), данные без логики; `tool/skill.go` импортирует его | `go:embed` не выходит из каталога пакета `tool/`; проверено рефутером: dot-файлы исключаются, импорт из `./tool` работает | `TestSkillFiles`: набор == WalkDir(skills/spec-audit) + 3 docs |
| Контракт согласования хоста §14 — зеркало `skills/spec-audit/references/host-decision.md` (тест равенства с §14 tool-spec); `tool-spec.md` целиком не встраивается | tool-spec содержит имена пилота (§12.4, ссылки на спецификацию применения) — общий launcher их не несёт (§12.1); §14 — 14 строк | `TestHostDecisionMirror` |
| Ссылки SKILL.md: `../../docs/<x>.md` → при материализации `references/docs/<x>.md`; `../../README.md` убирается (фраза «бинарник ставится/обновляется через релиз»); `tool-spec.md#14-…` → `references/host-decision.md`; `protocol.txt` байт-в-байт | В checkout ссылки продолжают работать; установленная копия самодостаточна. Известное решение владельца: `accepted-index.md`/`legacy-extraction.md` содержат в «См. также» ссылку на документ применения пилота — это внешний указатель, не бизнес-норма (файлы frozen хэшами, не редактируются) | Тест: в копии нет `](../`; каждая относительная ссылка без якоря существует; упоминания пилота только в строках «См. также»; нет абсолютных путей и ID задач |
| Раскладка skill: реальный каталог `DIR/.spec-audit/skill/` (`SKILL.md`, `references/protocol.txt`, `references/host-decision.md`, `references/docs/*.md`, `.spec-audit-skill.json`), относительные host-ссылки `DIR/.agents/skills/spec-audit` и/или `DIR/.claude/skills/spec-audit` → `../../.spec-audit/skill`; `--host codex|claude|both` **обязателен** (без default) | Совпадает с одобренной раскладкой пилота; создание каталогов конфигурации агента — только по явному выбору | `TestSkill/host_required`, `/codex_only`, `/claude_only` |
| Путь-безопасность через `os.OpenRoot(DIR)` и `dirRoot.OpenRoot(".spec-audit/skill")`: Lstat/Readlink/Mkdir/Symlink/Remove/atomicWrite внутри Root; выход symlink-ом за DIR отвергается Root | Не дублирует `canonicalPath/within`; temp-файлы и fsync — в каталоге skill | `TestSkill/escape_symlink` |
| Receipt skill `.spec-audit-skill.json` = `{"schema_version":"spec-audit-skill/1","version":"X.Y.Z|dev","files":{"<rel>":"<sha256>"}}` (ключи отсортированы, сам receipt не входит). `skill update`: receipt обязателен; drift любого управляемого файла → отказ со списком путей; лишние файлы не учитываются и не удаляются; все digest равны встроенным → `updated:false` без записи (версия в receipt — только информация) | Модель Pri-Fly workflows update (digest дерева, отказ при локальных правках, чужое не трогать) без её `extend.yaml` | `TestSkill/update_noop`, `/update_drift`, `/foreign_file_untouched` |
| Автомат состояний до первой записи: `.spec-audit/skill` ∈ {absent, symlink, ours, foreign(без receipt), drift}; host-ссылка ∈ {absent, ours, foreign-link, real-path}. `install`: absent → создать; ours → как update; symlink → отказ, с `--replace` снять ссылку (цель не трогать) и создать каталог; foreign → отказ всегда; host foreign-link → отказ, с `--replace` перелинковать; host real-path → отказ всегда. `update`: ours без drift → no-op; drift → отказ, с `--replace` переписать только управляемые файлы; symlink/foreign/absent → отказ (нужен `install`) | Любой отказ оставляет дерево DIR байт-в-байт | `TestSkill` таблица 12 состояний + walk DIR до/после |
| Ответ JSON `skill`: `{"dir","skill","version","updated","links":{"<rel>":"created|kept|relinked"}}`; `.gitignore` не читается и не меняется — рекомендация только в README | Инструмент не трогает Git и конфигурацию проекта | `TestSkill` |
| Флаги через `flag.NewFlagSet(..., flag.ContinueOnError)` с `SetOutput(io.Discard)`; ошибка разбора → обычная JSON-ошибка в stderr | §6: stderr — JSON, без usage-текста | `TestSkill/bad_flag` |
| `install.sh` (POSIX sh, образец Pri-Fly): `SPEC_AUDIT_RELEASE_BASE` (default GitHub latest/download), `SPEC_AUDIT_INSTALL_DIR` (default `$HOME/.local/bin`), `uname` → две платформы, `mktemp`+`trap`, `curl -fsSL` манифеста и файла, sha из манифеста без `jq`, `shasum -a 256`/`sha256sum`, `chmod 755`, staged `mv -f`, PATH-hint, без sudo и правки shell-профилей. Подпись не проверяется — граница доверия первой установки — HTTPS до GitHub (в §18 явно); дальше — `spec-audit update` с подписью | Как в Pri-Fly README | `TestInstallScript` через `SPEC_AUDIT_RELEASE_BASE=file://…` (skip без sh/curl/tar-less набора) |
| Релиз shell-шагами без второго Go-бинарника: `release.yml` на тег `v[0-9]+.[0-9]+.[0-9]+`, `permissions: contents: read`; job `build-linux-amd64` (ubuntu-latest) и `build-darwin-arm64` (`macos-latest`, `test "$(uname -m)" = arm64`): `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-X main.version=${GITHUB_REF_NAME#v} -X main.releasePublicKeyHex=$PUB" -o out/spec-audit-<os>-<arch> ./tool`, нативная квалификация (`version` → release:true; `index acceptance/config.yaml`; `skill install --dir $tmp --host both` затем `skill update` → updated:false; `if out/… update; then exit 1; fi` — отказ без сети через `env -i`); job `release` (`environment: release`, `contents: write`): `shasum -a 256`, `jq -c -n` манифест, `openssl pkeyutl -sign -rawin -inkey <(secret)`, `gh release create --verify-tag` с `install.sh`, манифестом, `.sig` и двумя бинарниками. Публичный ключ — repository variable `SPEC_AUDIT_RELEASE_PUBLIC_KEY` (не environment-scoped: читается build-job без окружения) | Без `cmd/release-build`; уникальные имена артефактов исключают схлопывание при download | Прогон workflow на теге `v0.1.0`; ассеты и подпись проверяются `update` с обеих платформ |
| Ключи: владелец генерирует пару один раз OpenSSL 3 (`brew install openssl@3`; системный LibreSSL macOS не умеет Ed25519 `-rawin`): `openssl genpkey -algorithm ed25519 -out key.pem`; публичный hex — `openssl pkey -in key.pem -pubout -outform DER \| tail -c 32 \| xxd -p -c 64`; приватный — secret `SPEC_AUDIT_RELEASE_SIGNING_KEY` окружения `release` с ревьюером | Как Pri-Fly: credentials разделены, публикует только один job | Документируется в README и §18 |
| Лимиты/таймауты: `http.Client{Timeout: releaseTimeout}` (одна константа), манифест ≤ `maxConfig`, бинарник ≤ `maxFile`, статус ≠ 2xx — отказ; тексты ошибок без URL и байтов ответа | Существующие константы; нет новых | `TestUpdate/limits`, `TestUpdateNoLeak` |
| Размещение кода: `tool/update.go` + `tool/update_test.go`, `tool/skill.go` + `tool/skill_test.go`, `embed.go` (корень), `scripts/install.sh`, `.github/workflows/release.yml`; диспетчер `version`/`update`/`skill` в `execute` до проверки `len(args) < 3`; usage-строка дополняется | Один `package main` в `tool/` + data-only embed-пакет; ARCHITECTURE/rules/base уточняются на checkpoint | `go vet ./...`, `go test ./tool` |

### Поддерживаемые комбинации
| Комбинация | Вход / валидация | Состояние | Результат | Проверка |
| --- | --- | --- | --- | --- |
| `version` в dev-сборке | — | — | `{"version":"dev","release":false,…}` | `TestVersion` |
| `update` в dev-сборке | `releasePublicKeyHex == ""` | — | отказ «обновление недоступно в сборке из исходников» без сети | `TestUpdate/dev_build` (httptest не вызван) |
| `update`, бинарник переименован/скопирован под другим именем | basename ≠ `spec-audit` | — | отказ, сети нет | `TestUpdate/renamed_binary` |
| `update`, подпись неверна / манифест битый / версия не `X.Y.Z` | verify/strictJSON | — | отказ до скачивания бинарника | `TestUpdate/bad_signature`, `/bad_manifest` |
| `update`, manifest.version ≤ current | сравнение трёх int | — | `{"updated":false,…}` exit 0 | `TestUpdate/already_latest`, `/downgrade` |
| `update`, нет ассета для GOOS/GOARCH | точное совпадение | — | отказ | `TestUpdate/no_asset` |
| `update`, sha бинарника ≠ манифесту / файл > `maxFile` / HTTP 404 | digest, readLimited, статус | бинарник не тронут | отказ | `TestUpdate/sha`, `/limit`, `/http_error` |
| `update` успешный | всё выше OK | `atomicWrite` в каталоге бинарника | `{"updated":true,"from","to"}`; следующий `version` = новая | `TestUpdate/ok`, e2e |
| `skill install --host <h>` в пустом DIR | DIR существует, host валиден | каталог + receipt + ссылки | `updated:true`, links created | `TestSkill/fresh_*` |
| `skill install` без `--host` / неверный host / DIR не каталог | flag | — | JSON-отказ, дерево неизменно | `TestSkill/host_required`, `/bad_dir` |
| `skill install`, на месте `.spec-audit/skill` symlink (пилот) | Lstat | без `--replace` отказ; с `--replace` ссылка снята, цель не тронута, каталог создан | ссылки host перелинкованы при `--replace` | `TestSkill/symlink_replace`, T9 на пилоте |
| `skill install`, чужой каталог без receipt | Lstat/receipt | — | отказ всегда | `TestSkill/foreign_dir` |
| `skill install`, host-ссылка чужая / реальный каталог | Readlink | — | отказ; с `--replace` перелинковка только symlink | `TestSkill/foreign_link`, `/real_path` |
| `skill update`, digest равны | receipt ours | — | `updated:false`, ничего не записано | `TestSkill/update_noop` |
| `skill update`, drift | digest ≠ | — | отказ со списком путей; с `--replace` переписаны только управляемые файлы, чужие сохранены | `TestSkill/update_drift`, `/foreign_file_untouched` |
| `skill update` без receipt / над symlink | — | — | отказ «сначала skill install» | `TestSkill/update_needs_install` |
| `install.sh` на неподдерживаемой платформе | uname | — | отказ до скачивания | `TestInstallScript/unsupported` |
| `install.sh`, sha ≠ манифесту | shasum | staged-файл удалён | отказ, бинарник не установлен | `TestInstallScript/sha_mismatch` |
| e2e: `install.sh` (file://) → `version` → `skill install` → `init`/`index` → `update` до B → `version` = B → `skill update` | реальный процесс | — | всё зелёное без сети | `TestNativeDistribution` (не в `-short`) |

Представительные реальные артефакты: `acceptance/config.yaml` (квалификация `index` в CI), пилотная установка `.spec-audit/` (T9), первый релиз `v0.1.0` (T8).

## Commit Plan
- **Commit 1** (after tasks 1-2): "docs(dist): зафиксировать контракт поставки §18 и встроить skill в бинарник"
- **Commit 2** (after tasks 3-5): "feat(dist): команды version/update/skill и install.sh с подписанным манифестом"
- **Commit 3** (after tasks 6-7): "ci(dist): подписанный релиз по тегу и e2e поставки без сети"
- **Commit 4** (after tasks 8-10): "docs(dist): первый релиз, установка на пилоте и документация поставки"

## Tasks

### Phase 1: Контракт до кода и встраивание skill
- [x] Task 1: §18 tool-spec «Поставка: версия, обновление и установка skill» (REQ-SA-035…038)
  - Deliverable: новый раздел перед «См. также» (редакция 0.7 с причиной), по профилю §4: REQ-SA-035 «Подписанный релиз и явное обновление» (release-manifest/sig форматы, порядок проверок, только `X.Y.Z` вверх, точная платформа, атомарная замена, отказ dev-сборки без сети, лимиты `maxConfig`/`maxFile`, таймаут); REQ-SA-036 «Явная непривилегированная первичная установка» (`install.sh`: без sudo, без правки профилей, sha из манифеста того же релиза, граница доверия — HTTPS GitHub, нет подмены сборкой из исходников); REQ-SA-037 «Установка skill только по явной команде» (раскладка, `--host` обязателен, receipt `.spec-audit-skill.json`, автомат состояний в компактной таблице «состояние × команда → исход», `--replace` только для symlink/host-ссылки/drift, чужое никогда не удаляется, `.gitignore`/Git/CONFIG не трогаются); REQ-SA-038 «Матрица платформ и квалификация» (только `darwin/arm64` + `linux/amd64`, ассет заявлен лишь после нативного исполнения, macOS — native runner). Оговорки: отношение к REQ-SA-015 (отдельные явные операции владельца), `init` не меняется (REQ-SA-001, §12.3), встроенные документы launcher — собственные контракты инструмента, известное решение владельца о ссылках «См. также». §1–10 и §6 не редактируются; §12.3 правится только на checkpoint (T10).
  - Files: `docs/tool-spec.md`
  - Verify: `git diff codex/bootstrap -- docs/tool-spec.md` затрагивает только шапку (редакция), новый §18 и нумерацию «См. также»; `mdq tree docs/tool-spec.md` показывает §18 с четырьмя REQ; `shasum -a 256 -c` всех шести списков `acceptance/` OK; `rg -n 'smsplace|SMSPlace' docs/tool-spec.md` — без новых вхождений в §18; `go test ./tool` зелёный.
  - Logging: документ; правило «ошибки без URL, байтов ответа и содержимого файлов» ссылается на практику `main.go:233`.
  - REQ: REQ-SA-001, REQ-SA-014, REQ-SA-015, §12.3, §12.1

- [x] Task 2: Embed-пакет, зеркало §14 и материализация skill с тестами инвариантов
  - Deliverable: `embed.go` в корне (`package dist`, `//go:embed skills/spec-audit docs/accepted-index.md docs/legacy-extraction.md docs/php-sdk-contract.md`, `var Files embed.FS`); `skills/spec-audit/references/host-decision.md` — копия §14 с шапкой-ссылкой; SKILL.md: ссылка `../../README.md` → фраза о поставке через релиз (`spec-audit update`), `../../docs/tool-spec.md#14-…` → `references/host-decision.md`; `tool/skill.go` (часть 1): `skillFiles(version string) (map[string][]byte, error)` — материализация набора для копии: `SKILL.md` с `strings.ReplaceAll("](../../docs/", "](references/docs/")`, `references/protocol.txt` и `references/host-decision.md` байт-в-байт, `references/docs/<name>.md` из embed; `skillReceipt(files, version) []byte` (sorted, sha256). Тесты `tool/skill_test.go`: `TestSkillFiles` (набор == WalkDir(`skills/spec-audit`) + 3 docs; в копии нет `](../`; каждая относительная ссылка без `#` существует в наборе; упоминания `smsplace` только в строках «См. также»; нет абсолютных путей `/Users`, ID задач `#\d+`), `TestHostDecisionMirror` (файл равен §14 tool-spec между заголовками `## 14.` и `## 15.`).
  - Files: `embed.go`, `skills/spec-audit/SKILL.md`, `skills/spec-audit/references/host-decision.md`, `tool/skill.go`, `tool/skill_test.go`
  - Depends: 1
  - Verify: `go vet ./... && go test ./tool -run 'TestSkillFiles|TestHostDecisionMirror' -count=1`; `go build ./...` компилирует корневой пакет; `rg -n '\]\(\.\./\.\./README' skills/spec-audit/SKILL.md` пусто; `TestPHPSDKControlBaseline` и прочие пины не тронуты.
  - Logging: DEBUG «skill: набор файлов подготовлен» {files, version}.
  - REQ: REQ-SA-037, §12.1
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Команды бинарника и установщик
- [x] Task 3: `tool/update.go` — `version`, проверка манифеста и подписи, `update` с атомарной заменой
  - Deliverable: `var version = "dev"`, `var releasePublicKeyHex string`, `var releaseBaseURL = "https://github.com/StenHigh/spec-audit/releases/latest/download"`, `const releaseTimeout = 120 * time.Second`; типы `ReleaseManifest{SchemaVersion, Version, Assets []ReleaseAsset{OS, Arch, File, SHA256}}`; чистые функции `parseManifest(raw, sig []byte, pubHex string) (ReleaseManifest, error)` (verify → strictJSON → schema → `^\d+\.\d+\.\d+$` → `fileRE ^spec-audit-[a-z0-9]+-[a-z0-9]+$` → `validDigest`), `newerVersion(current, candidate string) (bool, error)`, `assetFor(m, os, arch)`, `fetch(client, url, limit) ([]byte, error)` (статус 2xx, `readLimited`), `runUpdate(client *http.Client, baseURL, exe, current, pubHex string) (map[string]any, error)`: release-проверка → basename → manifest/sig → newer → asset → bin → digest → `atomicWrite(os.OpenRoot(dir), "spec-audit", bin, 0755)`; `runVersion()`; диспетчер: `version` (1 арг), `update` (1 арг) в `execute` до `len(args) < 3`; usage. Тесты `tool/update_test.go`: `TestVersion`; `TestUpdate` таблица через `httptest.NewServer(http.FileServer)` с ключом `ed25519.GenerateKey`, exe в `t.TempDir()`: dev_build (сервер не вызван), renamed_binary, bad_signature, bad_manifest, already_latest, downgrade, no_asset, sha, limit, http_error, ok (байты заменены, права 0755, старые байты сохранены при отказах); `TestUpdateNoLeak` (ошибки/stderr без URL и байтов); `TestOpenSSLSignatureCompatible` (skip, если `openssl version` не OpenSSL ≥ 3: подпись `pkeyutl -sign -rawin` проверяется Go).
  - Files: `tool/update.go`, `tool/update_test.go`, `tool/main.go`
  - Depends: 1
  - Verify: `go test ./tool -run 'TestVersion|TestUpdate|TestOpenSSL' -count=1`; `go vet ./tool`; `TestNativeCLI` (существующий) зелёный после правки usage; `./bin/spec-audit version` → `release:false`; `./bin/spec-audit update` → JSON-отказ без сети (проверить `tcpdump`-free: тест с сервером, который падает при обращении).
  - Logging: INFO «update: манифест проверен» {version, current}; INFO «update: бинарник заменён» {from, to, bytes}; INFO «update: обновление не требуется» {version}; DEBUG «update: ассет выбран» {os, arch, file}; ошибки без URL/байтов ответа.
  - REQ: REQ-SA-035, REQ-SA-038, REQ-SA-014

- [x] Task 4: `tool/skill.go` — `skill install|update` через `os.Root`, receipt, автомат состояний, `--replace`
  - Deliverable: разбор `flag.NewFlagSet("skill", flag.ContinueOnError)` с `SetOutput(io.Discard)`: `--dir` (default cwd → `filepath.Abs`), `--host` (обязателен: codex|claude|both), `--replace`; `runSkill(sub, dir, host string, replace bool, version string) (map[string]any, error)`: `dirRoot := os.OpenRoot(dir)`; чтение состояния (`Lstat(".spec-audit/skill")`, receipt `strictJSON`, digest управляемых файлов; `Lstat/Readlink` host-ссылок) — все отказы до первой записи; переходы по таблице контракта; запись: `dirRoot.MkdirAll(".spec-audit/skill")` → `skillRoot := dirRoot.OpenRoot(".spec-audit/skill")` → per-file `atomicWrite(skillRoot, rel, data, 0644)` (подкаталоги `references/docs`) → receipt последним → host-ссылки `dirRoot.Symlink("../../.spec-audit/skill", ".agents/skills/spec-audit")` и/или `.claude/…` (создание `.agents/skills`/`.claude/skills` при отсутствии); `--replace`: `dirRoot.Remove` для symlink на месте skill и чужой host-ссылки (цель не трогается), переписывание управляемых файлов при drift; ответ `{"dir","skill","version","updated","links"}`. Диспетчер `skill` в `execute`. Тесты `tool/skill_test.go`: `TestSkill` таблица состояний (fresh_both/codex/claude, host_required, bad_host, bad_dir, symlink_no_replace, symlink_replace (цель symlink нетронута), foreign_dir, foreign_link, foreign_link_replace, real_path, update_noop (mtime/байты без изменений), update_drift, update_drift_replace (чужой файл `.DS_Store`/`notes.md` сохранён), update_needs_install, escape_symlink (`.spec-audit` → вне DIR), bad_flag (stderr JSON, без usage)); инвариант — WalkDir DIR до/после идентичен при отказе.
  - Files: `tool/skill.go`, `tool/skill_test.go`, `tool/main.go`
  - Depends: 2
  - Verify: `go test ./tool -run 'TestSkill' -count=1 -v` — все subtests; `go vet ./tool`; ручной прогон в `t := $(mktemp -d)`: `./bin/spec-audit skill install --dir $t --host both` → ссылки, `readlink $t/.claude/skills/spec-audit` = `../../.spec-audit/skill`; повторный `skill update --dir $t --host both` → `updated:false`.
  - Logging: INFO «skill: установлен» / «skill: обновлён» / «skill: обновление не требуется» {dir?: нет — только version, files, links}; DEBUG «skill: состояние» {skill_state, links}; WARN отсутствует; отказы без содержимого файлов, drift — только относительные пути.
  - REQ: REQ-SA-037, REQ-SA-015

- [x] Task 5: `scripts/install.sh` и его проверка через `file://`
  - Deliverable: POSIX `sh`, `set -eu`: `SPEC_AUDIT_RELEASE_BASE` (default GitHub latest/download), `SPEC_AUDIT_INSTALL_DIR` (default `$HOME/.local/bin`); `uname -s/-m` → `darwin/arm64` | `linux/amd64`, иначе отказ до сети; `mktemp -d` + `trap cleanup 0 HUP INT TERM`; `curl --fail --location --silent --show-error` манифеста и `spec-audit-<os>-<arch>`; sha из манифеста без `jq` (`tr`/`grep`/`sed`, проверка 64 hex); `shasum -a 256` или `sha256sum`, иначе отказ; `chmod 755`; staged `mv -f` в `$dest/spec-audit`; PATH-hint без правки профилей; подсказка «далее: `spec-audit skill install --dir <проект> --host codex|claude|both`». Тест `TestInstallScript` (`tool/update_test.go`): фикстура релиза в `t.TempDir()` (манифест + бинарник = копия тестового исполняемого), `SPEC_AUDIT_RELEASE_BASE=file://…`, `SPEC_AUDIT_INSTALL_DIR=$tmp/bin`; subtests ok, sha_mismatch (файл не установлен), unsupported (подмена `uname` через PATH-обёртку); skip без `sh`/`curl`/`shasum|sha256sum`.
  - Files: `scripts/install.sh`, `tool/update_test.go`
  - Depends: 3
  - Verify: `sh -n scripts/install.sh`; `shellcheck scripts/install.sh` при наличии; `go test ./tool -run TestInstallScript -count=1 -v`; вручную: `SPEC_AUDIT_RELEASE_BASE=file://$PWD/.local/release-fixture sh scripts/install.sh` ставит бинарник в `$SPEC_AUDIT_INSTALL_DIR`.
  - Logging: stderr скрипта — одна строка с префиксом `spec-audit install:` при отказе; stdout — итоговый путь и подсказки.
  - REQ: REQ-SA-036, REQ-SA-038
<!-- Commit checkpoint: tasks 3-5 -->

### Phase 3: Релиз и сквозная проверка
- [x] Task 6: `.github/workflows/release.yml` — нативные сборки с квалификацией и подписанный релиз shell-шагами
  - Deliverable: триггер `push.tags: ['v[0-9]+.[0-9]+.[0-9]+']`, `permissions: contents: read`, `env: GOTOOLCHAIN=local`; jobs `build-linux-amd64` (ubuntu-latest) и `build-darwin-arm64` (`macos-latest`, шаг `test "$(uname -m)" = arm64`): `test -n "${{ vars.SPEC_AUDIT_RELEASE_PUBLIC_KEY }}"`, `go test ./tool -short`, `go vet ./...`, `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-X main.version=${GITHUB_REF_NAME#v} -X main.releasePublicKeyHex=$PUB" -o out/spec-audit-<os>-<arch> ./tool`; квалификация: `version` → `release:true`; `index "$PWD/acceptance/config.yaml"`; `skill install --dir $tmp --host both` затем `skill update …` → `updated:false`; `if env -i PATH=/usr/bin:/bin out/spec-audit-… update; then exit 1; fi`; `upload-artifact` с уникальным именем. Job `release` (`needs`, `environment: release`, `permissions: contents: write`): `download-artifact` в `dist/`, `test -n "$SIGNING_KEY"`, `shasum -a 256`, `jq -c -n` → `release-manifest.json` (`schema_version`, `version=${GITHUB_REF_NAME#v}`, два ассета), `openssl pkeyutl -sign -rawin -inkey key.pem -in release-manifest.json -out release-manifest.sig` (ключ из секрета во временный файл 0600, удаляется), `cp scripts/install.sh dist/`, `gh release create "$GITHUB_REF_NAME" --verify-tag --title … --notes … dist/*`. README-раздел «Релиз»: генерация ключей OpenSSL 3 (Homebrew), настройка variable/secret/окружения `release` с ревьюером, порядок выпуска (тег на прошедшем `go test`/`vet` коммите).
  - Files: `.github/workflows/release.yml`, `README.md`
  - Depends: 3, 4, 5
  - Verify: `actionlint .github/workflows/release.yml` при наличии; локальная имитация release-шагов: `PUB=$(…)`, `go build -ldflags …`, `jq -c -n …`, `openssl pkeyutl -sign -rawin` (OpenSSL 3), `./bin/spec-audit-dev … ` — `parseManifest` принимает подпись (через `TestOpenSSLSignatureCompatible`); первый реальный прогон — T8.
  - Logging: шаги workflow печатают версии инструментов и sha; секрет не выводится (`::add-mask::` не требуется — ключ только в файле).
  - REQ: REQ-SA-035, REQ-SA-038

- [x] Task 7: E2E без сети реальным процессом: `install.sh` → `skill install` → `init`/`index` → `update` → `skill update`
  - Deliverable: `TestNativeDistribution` (skip в `-short`): собрать бинарники A (`-X main.version=0.1.0 -X main.releasePublicKeyHex=<test pub> -X main.releaseBaseURL=file:///…`) — нет, `update` использует `http.Client`; поэтому `releaseBaseURL` = `httptest.NewServer` (FileServer над каталогом релиза) — и B (`0.1.1`); релизный каталог: `spec-audit-<os>-<arch>` = B, манифест, `.sig` (`ed25519.Sign` тестовым ключом); `install.sh` с `SPEC_AUDIT_RELEASE_BASE=file://` ставит A в `$tmp/bin`; `A version` → `0.1.0`; `A skill install --dir $proj --host both`; `A init $proj/audit.yaml`; `A index …` (fixture-проект); `A update` → `updated:true`; `bin/spec-audit version` → `0.1.1`; `B skill update --dir $proj --host both` → `updated:false` (набор идентичен) или `updated:true` при изменённом наборе; повторный `update` → `updated:false`. Всё в `t.TempDir()`, без сети/Docker.
  - Files: `tool/update_test.go`
  - Depends: 5, 6
  - Verify: `go test ./tool -run TestNativeDistribution -count=1 -v` на macOS (arm64); `go test -short ./tool` пропускает; полный `go test ./tool` зелёный.
  - Logging: тест собирает stderr процессов и проверяет: при `LOG_LEVEL=error` успех молчит; отказы — валидный JSON без URL.
  - REQ: REQ-SA-035, REQ-SA-036, REQ-SA-037
<!-- Commit checkpoint: tasks 6-7 -->

### Phase 4: Первый релиз, пилот и документация
- [ ] Task 8: Первый релиз `v0.1.0` и managed-установка на хосте (действия владельца, сеть)
  - Deliverable: владелец создаёт ключи (OpenSSL 3) и настраивает `SPEC_AUDIT_RELEASE_PUBLIC_KEY` (repository variable), `SPEC_AUDIT_RELEASE_SIGNING_KEY` (secret окружения `release`), окружение `release` с ревьюером; тег `v0.1.0` на коммите `codex/bootstrap` с зелёными `go test`/`vet`; прогон `release.yml` — обе платформы исполнены нативно (квалификация в job); установка на хосте одной командой из README; `~/.local/bin/spec-audit version` → `0.1.0`, `release:true`; `spec-audit update` → `updated:false`. Свидетельства (лог workflow, вывод `version`) — в README «Приёмка и ограничения» одной строкой; прежняя ручная копия заменяется установленной.
  - Files: `README.md`
  - Depends: 7
  - Verify: страница Release содержит `install.sh`, `release-manifest.json`, `release-manifest.sig`, `spec-audit-darwin-arm64`, `spec-audit-linux-amd64`; `curl -fsSL …/release-manifest.sig | wc -c` = 64; `spec-audit version` на macOS хоста; Linux — квалификация в job (нативного Linux-хоста у владельца нет — так и фиксируется).
  - Logging: нет нового.
  - REQ: REQ-SA-035, REQ-SA-036, REQ-SA-038

- [ ] Task 9: Пилот — замена symlink встроенной копией и smoke из чистой сессии (разрешение владельца 2026-09-16)
  - Deliverable: в checkout пилота: `spec-audit skill install --dir <pilot> --host both --replace` релизным бинарником → `.spec-audit/skill/` реальный каталог с receipt, `.agents/skills/spec-audit` и `.claude/skills/spec-audit` → `../../.spec-audit/skill`; `git -C <pilot> status --porcelain` пуст (пути уже в `.gitignore` пилота); повторный `skill update` → `updated:false`; smoke: новая сессия Codex/Claude в корне пилота видит `$spec-audit`/`/spec-audit`, `spec-audit reconcile .spec-audit/config.yaml` read-only работает; бизнес-код, ТЗ, CI не меняются. Итог — строка в `docs/smsplace-adoption.md` «Самостоятельное использование из SMSPlace» (T10).
  - Files: (пилот, вне репозитория инструмента); `docs/smsplace-adoption.md` — в T10
  - Depends: 8
  - Verify: `readlink <pilot>/.spec-audit/skill` → не ссылка (`test ! -L`); `test -f <pilot>/.spec-audit/skill/.spec-audit-skill.json`; `git -C <pilot> status --porcelain | wc -l` = 0; ссылки резолвятся: `test -f <pilot>/.claude/skills/spec-audit/SKILL.md`; `rg -n '\]\(\.\./' <pilot>/.spec-audit/skill/SKILL.md` пусто.
  - Logging: вывод `skill install` сохраняется в `.local/daily-smsplace/skill-install-<date>.json`.
  - REQ: REQ-SA-037, REQ-SA-015, REQ-SMS-001, smsplace-adoption «Самостоятельное использование из SMSPlace»

- [ ] Task 10: Docs checkpoint — README, tool-spec §12.3, smsplace-adoption, ROADMAP, ARCHITECTURE, AGENTS, rules/base
  - Deliverable: README: «Быстрый старт» с одной командой установки (`install_file=$(mktemp) … sh "$install_file"`), `spec-audit update`, `spec-audit skill install --dir … --host …`, рекомендация `.gitignore` (`/.spec-audit/`, `/.agents/skills/spec-audit`, `/.claude/skills/spec-audit`), матрица платформ и граница доверия первой установки, раздел «Релиз» (ключи, окружение, тег), строка приёмки T8/T9; tool-spec §12.3: «автоматическая установка skill ещё не реализована» → ссылка на §18 и статус; `docs/smsplace-adoption.md` «Самостоятельное использование из SMSPlace»: локальная установка через релизный бинарник и `skill install`, зависимость от checkout снята; ROADMAP: два пункта объединены в выполненный этап «Поставка» с датой и основанием, таблица завершённых дополнена; ARCHITECTURE: строки `tool/update.go`, `tool/skill.go`, `embed.go` (data-only пакет), поставка как граница; AGENTS.md: карта (README «Быстрый старт», §18); `.ai-factory/rules/base.md`: строка «один Go package main в tool/» уточняется «+ data-only embed-пакет в корне» (единственная фактическая правка правил; владелец подтверждает).
  - Files: `README.md`, `docs/tool-spec.md`, `docs/smsplace-adoption.md`, `.ai-factory/ROADMAP.md`, `.ai-factory/ARCHITECTURE.md`, `AGENTS.md`, `.ai-factory/rules/base.md`
  - Depends: 9
  - Verify: `git diff --stat docs/tool-spec.md` — §12.3 одна фраза + §18 из T1; `shasum -a 256 -c` всех списков OK; `rg -n 'ещё не реализована' docs/tool-spec.md docs/smsplace-adoption.md README.md` пусто; `go test ./tool` зелёный; `mdq tree README.md` содержит «Быстрый старт» и «Релиз».
  - Logging: нет (документы).
  - REQ: REQ-SA-035…038, §12.3, RULES «Честный статус»
<!-- Commit checkpoint: tasks 8-10 -->

## Открытые вопросы (не блокируют T1–T7)
1. До T6: лейбл macOS-runner — `macos-latest` (сейчас arm64) с проверкой `uname -m`; при смене лейбла GitHub workflow нужно обновить.
2. До T8: имя релиза/тега `v0.1.0` — первый публичный релиз spec-audit; предыдущие сборки считаются dev.
3. До T9: ставить skill на пилот dev-сборкой до релиза или только релизной — план выбирает релизную (T8 → T9).
4. После T10: нужен ли `spec-audit skill uninstall` — не вводится до потребности (удаление вручную описано в README).

## Риски
- `openssl` на macOS — LibreSSL без Ed25519 `-rawin`: генерация ключей и локальная проверка требуют Homebrew OpenSSL 3; CI на ubuntu использует OpenSSL 3.
- Замена запущенного бинарника: rename безопасен на darwin/linux (процесс держит старые байты); Windows не поддерживается.
- `macos-latest` меняет версию образа; квалификация `uname -m = arm64` ловит смену архитектуры, но не удаление лейбла.
- Встроенные `accepted-index.md`/`legacy-extraction.md` содержат ссылку «См. также» на документ применения пилота — зафиксировано как решение владельца; тест гарантирует отсутствие других упоминаний, абсолютных путей и ID задач.
- `skill update` не удаляет чужие файлы в `.spec-audit/skill/` — устаревшие файлы прежних версий launcher (если появятся) остаются; при переименовании файлов в будущих версиях потребуется явная миграция.
- Первая установка доверяет HTTPS GitHub без подписи — как у Pri-Fly; фиксируется в §18 и README, не скрывается.
- Нативного Linux-хоста у владельца нет: Linux-квалификация — только job GitHub Actions; это честно указывается в README.
