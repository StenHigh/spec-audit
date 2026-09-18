# Implementation Plan: `cite` — точная цитата от бинарника, журнал `validate` роли в промпте, политика постороннего симлинка (§31)

Branch: codex/cite-symlink-validate-log
Created: 2026-09-18

## Original Request
Уроки четвёртого прогона 2026-09-17, пункты 8 и 11 (решение владельца 2026-09-17: «4/5/7/8/11 — отдельно после следующего прогона»; прогоны состоялись, владелец 2026-09-18 поручил вести очередь автономно), и пункт 6 шестого прогона: (8) `spec-audit cite CONFIG PATH A B` — Citation JSON с точной quote (сборка цитат sed+jq — главная причина отказов `validate` у ролей); (11) SKILL.md: политика постороннего симлинка внутри `.spec-audit/`, не участвующего в CONFIG/reports_dir (хост продолжил, трактуя «symlink escape» узко); (6 шестого) журнал `validate` в каталоге задания — где роли бьются с формой: `validate` по REQ-SA-039 ничего не пишет, поэтому журнал ведёт роль по промпту (`validate.log` в рабочем каталоге), бинарник не меняется. Ограничения: frozen §1–10; REQ-SA-039 не меняется; новый §31 (редакция 1.11); никаких путей/имён пилота.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки четвёртого прогона 2026-09-17 (решение владельца по каждому пункту)"
Rationale: пункты 8 и 11 отложены владельцем «после следующего прогона»; пункт 6 шестого прогона закрывается без изменения контракта `validate`.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 > REQ-SA-039 (`validate` без записи — журнал ведёт не бинарник) > §7 (Citation: точные полные строки, `lineQuote` — `tool/main.go:459`) > §30.1 (prompt.md от `prepare` — место для CITE и журнала) > SKILL.md строка 12 (граница workspace, «stop on a symlink escape») > [RULES.md](../RULES.md) > [rules/base.md](../rules/base.md) > ROADMAP.

Разведка: `readRoot(root, path, maxFile)` (`tool/main.go:443`) отказывает на симлинке и не-файле; `loadConfig(path, true)` не требует существующего reports_dir (`:354`); `os.OpenRoot(cfg.ProjectRoot)` — как в `checkCitations`. Двухаргументные команды разбираются до общего `argc` (`execute`, `:1050`). `rolePrompt` (`:~900`) — строки VALIDATE и «Как работать».

### Решения
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| §31 (редакция 1.11): 31.1 `cite CONFIG PATH A B` — read-only: PATH относительный под `project_root` (симлинк, каталог, выход за корень — отказ), A ≤ B 1-based; ответ — Citation `{path, line_start, line_end, quote}` с quote по правилам §7 (`lineQuote`); файл не обязан входить в snapshot — принадлежность FILES проверяет `validate`/`submit`; ничего не пишется. 31.2 промпт роли (§30.1) получает строку CITE с абсолютными путями и просьбу дописывать вывод каждого VALIDATE в `validate.log` рабочего каталога (журнал роли, не бинарника; REQ-SA-039 не меняется). 31.3 политика симлинка (SKILL.md): стоп — только когда CONFIG, reports_dir или путь к ним проходят через симлинк наружу либо reports_dir/источники пересекаются; посторонний симлинк внутри `.spec-audit/`, не участвующий в CONFIG/reports_dir/skill, не читается, не удаляется и называется в описании границы перед extraction | Пункты 8, 11, 6 (шестого) | `TestCite`, `TestPrepareDispatch` (CITE и validate.log в промпте), `TestSkillFiles` |
| Код: `cite` в `execute` до `argc` (`len(args) == 5 && args[0] == "cite"`): `loadConfig(args[1], true)`, `strconv.Atoi` A/B, `os.OpenRoot(cfg.ProjectRoot)`, `readRoot`, `lineQuote` → `Citation`; DEBUG «cite: цитата» {path, lines}. `rolePrompt`: строка CITE после VALIDATE; шаг 4 — «quote бери из CITE»; шаг 5 — `validate.log` | Минимальный контракт | `go test ./tool` |
| Docs: SKILL.md строка 12 (симлинк), шаг 3 (CITE/validate.log — читать `validate.log` роли при отказах); README, ROADMAP (четвёртый 8, 11 `[x]`; шестой 6 `[x]`), AGENTS, ARCHITECTURE; `usage`; релиз `v0.1.18` | §18 | `gh release view v0.1.18` |

## Tasks

### Phase 1: Контракт и код
- [x] Task 1: §31 tool-spec; AGENTS/ARCHITECTURE
  - Deliverable: шапка «1.11 добавляет §31 …»; §31 перед «См. также» с 31.1–31.3. AGENTS — строка §31, «§13–31»; ARCHITECTURE — main.go (`cite`).
  - Files: `docs/tool-spec.md`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
  - Verify: `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; хэши acceptance.
  - REQ: §7, REQ-SA-039, §30.1

- [x] Task 2: код и тесты — `cite`, промпт (CITE, validate.log), usage
  - Deliverable: `tool/main.go`: команда `cite`; `rolePrompt` — CITE и validate.log; `usage`. Тесты: `TestCite` (`tool/main_test.go`): диапазон внутри файла → Citation, равная `lineQuote` и проходящая `validate` в результате роли; A > B, B за концом, каталог, путь вне корня (`../x`), симлинк → отказы; провенанс/каталог reports не создаются; `TestPrepareDispatch` — промпт содержит ` cite ` с CONFIG и `validate.log`.
  - Files: `tool/main.go`, `tool/main_test.go`
  - Depends: 1
  - Verify: `go build ./... && go vet ./tool && gofmt -l tool && go test ./tool -count=1`.
  - Logging: DEBUG «cite: цитата» {path, line_start, line_end}.
  - REQ: 31.1–31.2
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Документация и поставка
- [x] Task 3: SKILL.md (симлинк, CITE/validate.log), README, ROADMAP
  - Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`
  - Depends: 2
  - Verify: `go test ./tool -run TestSkillFiles -count=1`; полный `go test ./tool`.
  - REQ: REQ-SA-037

- [x] Task 4: релиз `v0.1.18`, update хоста/синтетики; пилот — после завершения run на новом разделе
  - Files: `README.md`
  - Depends: 3
  - Verify: `gh release view v0.1.18` — 5 ассетов; `spec-audit version` → 0.1.18.
  - REQ: REQ-SA-035, REQ-SA-037

## Открытые вопросы (не блокируют)
1. `cite` не проверяет принадлежность файла snapshot — иначе каждая цитата стоила бы полного хэширования; `validate` проверяет это по-прежнему.

## Риски
- Роли получают ещё одну команду бинарника (read-only); протокол по-прежнему запрещает им всё остальное.
