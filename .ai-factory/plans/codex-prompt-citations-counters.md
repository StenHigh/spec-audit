# Implementation Plan: prompt.md от бинарника, индекс цитат по файлу, счётчики доставки в `tasks` (§30)

Branch: codex/prompt-citations-counters
Created: 2026-09-18

## Original Request
Уроки седьмого прогона 2026-09-18, пункты 5 + 3 + 6 (решение владельца 2026-09-18: после 2+4+1 — мелочами одним планом; владелец отсутствует, решения по форме принимает ведущая сессия): (5) генерация `prompt.md` бинарником из `task.json` + `protocol.txt` в каталоге задания — снимает дрейф протокол↔промпт (хост в run 03–18-02 копировал prompt.md прежнего run скриптом и вручную правил фразы протокола); хост правит только SDK_HINTS; совпадает с пунктом 5 шестого прогона; (3) подсказка «этот path:line цитируется ролью X под нормой Y» — для решения о собственных цитатах хоста под соседней нормой; (6) `tasks` после полной доставки — явные счётчики вместо пустого списка. Ограничения: frozen §1–10 (поля ответов добавляются, не меняются); новый §30 tool-spec (редакция 1.10); формы решения и зеркало `host-decision.md` не меняются; никаких путей/имён пилота (`TestSkillFiles`).

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки седьмого прогона 2026-09-18 (решение владельца по каждому пункту)"
Rationale: решение владельца 2026-09-18 — порядок 2 + 4 + 1 (сделано, v0.1.16), затем 5 + 3 + 6 мелочами; пункт 5 шестого прогона закрывается тем же.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 (ответ `prepare`/`tasks` — TaskBatch, поля добавляются) > §24.2/§26.3 (dispatch: `task.json`, общий `files.json`; «протокол и промпт кладёт launcher») > §19.2/§28.2 (протокол роли — `references/protocol.txt`, встроен в бинарник через `dist.Files`) > §27.2/§29.1 (`only_here`, чтение по норме) > [RULES.md](../RULES.md) > [rules/base.md](../rules/base.md) > ROADMAP > отчёты `sa-clean` run 18-01/18-02.

Разведка по коду: `writeDispatch(run, task)` (`tool/main.go:876`) пишет только `task.json`; вызывается из `prepare` (`:1200`) и `retry` (`:1387`). Бинарник уже несёт `skills/spec-audit/references/protocol.txt` в `dist.Files` (`tool/skill.go:39`) — тот же байтовый набор, что устанавливает `skill install`. Абсолютные пути: `cfg.ProjectRoot`/`cfg.ReportsDir` канонические (`:337–351`), путь CONFIG — `args[1]` (нужен `filepath.Abs`), бинарник — `os.Executable()`. `TaskBatch` (`:158`) — `run_id, snapshot_id, project_root, runtime, tasks, files, sdk`; `Status` (`:170`) уже считает `expected/submitted/delivery_complete` (`makeStatus`, `:1034`). `review` — `argc["review"] = -2` (3 или 4 аргумента), `readOnly` по `requirementIDRE` (`:1108`), диспетчер `case "review"` (`:1267`); `requirementView` (`tool/review.go:934`) собирает роли по норме из `state.Entries`; `Latest.Assessments` — вердикт хоста с объединёнными цитатами. Прецедент ключевого слова в позиции аргумента — `validate CONFIG RUN_ID host DECISION` (§27). Промпт пилота (run 18-02) — образец содержания: роль, SOURCE_ROOT, TASK, FILES, PROTOCOL, OUTPUT_PATH, рабочий каталог, VALIDATE, правила контекста, порядок работы, SDK_HINTS.

### Решения
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| §30 (редакция 1.10): 30.1 `prepare` пишет `dispatch/protocol.txt` (байты встроенного протокола) один раз и `dispatch/<task_id>/prompt.md` — промпт роли из задания: роль и её задача, SOURCE_ROOT, абсолютные пути TASK/FILES/PROTOCOL/OUTPUT_PATH (`result.json`) и рабочего каталога, команда VALIDATE с абсолютными путями бинарника и CONFIG, правила контекста и порядок работы, раздел SDK_HINTS: «если хост положил `sdk_hints.json` в этот каталог — прочитай; иначе подсказки не передаются»; `retry` перезаписывает `task.json` и `prompt.md`, прочие файлы каталога не трогает; хост промпт не правит — подсказки SDK кладёт отдельным файлом. 30.2 `review CONFIG RUN_ID citations [PATH]` — read-only индекс цитат run: по каждой цитате ролей и последнего решения хоста `{path, line_start, line_end, kind: spec|code|tests, test_id, role: mapper|redteam|host, task_id, requirement_id}` без quote, отсортировано по path/line_start/line_end/role; PATH — точный относительный путь, сужает список; неизвестный путь даёт `[]`; провенанс не пишется. 30.3 `prepare`/`tasks` дополняют TaskBatch полями `expected`, `submitted`, `delivery_complete` (как в `status`) | Пункты 5, 3, 6; owner отсутствует — форма по прецедентам §21/§24/§27 | `TestPrepareDispatch`, новый `TestReviewCitations`, `TestTypedCLI`/`TestPrepareDispatch` для счётчиков |
| Код: `writeDispatch(run, task, prompt dispatchPrompt)` — `dispatchPrompt{Binary, Config, ReportsDir, ProjectRoot, RunID}`; `prompt.md` собирается `fmt`-шаблоном (`rolePrompt(task, prompt) []byte`), mapper/redteam-абзац — из первых слов протокола (текст в коде, без имён пилота); `prepare` пишет `dispatch/protocol.txt` из `dist.Files` (`skillSourceDir + "/references/protocol.txt"`); `TaskBatch` + `Expected int json:"expected"`, `Submitted int json:"submitted"`, `DeliveryComplete bool json:"delivery_complete"`; `argc["review"] = -3` → 3, 4 или 5 аргументов при `args[3] == "citations"`; `readOnly` включает `args[3] == "citations"`; `citationIndex(state, latest, path) []CitationRef` в `tool/review.go` | Минимальный контракт; ponytail | `go test ./tool` |
| Docs: SKILL.md шаг 3 — «`prepare` writes `dispatch/protocol.txt` and each task's `task.json` + `prompt.md`; give the role only its `prompt.md` path; never rewrite prompt.md — put optional `sdk_hints.json` in the task directory»; шаг 6 — `review CONFIG RUN_ID citations PATH` перед собственными цитатами; шаг 1/4 — счётчики `tasks`. README, ROADMAP (седьмой 5, 3, 6 `[x]`; шестой 5 `[x]`), adoption, AGENTS, ARCHITECTURE; релиз `v0.1.17` после завершения текущего прогона пилота (skill в пилоте не обновлять, пока run на новом разделе идёт) | §18 | `gh release view v0.1.17` |

## Tasks

### Phase 1: Контракт и код
- [x] Task 1: §30 tool-spec; AGENTS/ARCHITECTURE
  - Deliverable: шапка «1.10 добавляет §30 — мелочи седьмого прогона (prompt.md и protocol.txt от `prepare`, индекс цитат `review … citations`, счётчики доставки в `tasks`)»; §30 перед «См. также» с 30.1–30.3 (требование, проверка, источник). AGENTS — строка §30, «§13–30»; ARCHITECTURE — main.go (prompt.md/protocol.txt), review.go (citationIndex).
  - Files: `docs/tool-spec.md`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
  - Verify: `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; `shasum -a 256 -c acceptance/*.sha256 acceptance/legacy/baseline.sha256`.
  - Logging: DEBUG «dispatch: промпт роли» {task_id}, «review: индекс цитат» {run_id, path, citations}.
  - REQ: §24.2, §26.3, §27.2, §29.1

- [x] Task 2: код и тесты
  - Deliverable: `tool/main.go`: `dispatchPrompt`, `rolePrompt`, `writeDispatch` с промптом, `dispatch/protocol.txt` в `prepare`, счётчики в TaskBatch, `argc`/`readOnly`/диспетчер `citations`; `tool/review.go`: `CitationRow`, `citationIndex`. Тесты: `TestPrepareDispatch` — `prompt.md` содержит роль, абсолютные пути task.json/files.json/protocol.txt/result.json, команду validate с CONFIG и бинарником, «sdk_hints.json»; `dispatch/protocol.txt` == `skills/spec-audit/references/protocol.txt`; `retry` перезаписывает prompt.md, чужой файл (`notes.txt`) не трогает; `tasks` после полной доставки → `tasks: []`, `expected == submitted`, `delivery_complete: true`. Новый `TestReviewCitations` (`tool/review_test.go`, на `reviewV3Input`): без PATH — все цитаты обеих ролей и хоста, отсортированы; с PATH — только этот файл, роли/нормы верны; неизвестный путь → `[]`; провенанс не пишется; `review … citations` до доставки тоже отвечает (индекс по тому, что есть).
  - Files: `tool/main.go`, `tool/review.go`, `tool/main_test.go`, `tool/review_test.go`
  - Depends: 1
  - Verify: `go build ./... && go vet ./tool && gofmt -l tool && go test ./tool -count=1`.
  - Logging: как в Task 1.
  - REQ: 30.1–30.3
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Документация и поставка
- [ ] Task 3: SKILL.md, README, ROADMAP, adoption
  - Deliverable: SKILL.md шаги 1, 3, 4, 6; README «Первый запуск» и «Приёмка и ограничения» (строка §30); ROADMAP — седьмой прогон 5, 3, 6 `[x]`, шестой 5 `[x]`, строка таблицы; adoption — сноска об 0.1.17.
  - Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`, `docs/smsplace-adoption.md`
  - Depends: 2
  - Verify: `go test ./tool -run 'TestSkillFiles' -count=1`; полный `go test ./tool`.
  - REQ: REQ-SA-037

- [ ] Task 4: релиз `v0.1.17`, update хоста/синтетики; пилот — после завершения run на новом разделе
  - Deliverable: merge, тег, approve; `update`; `skill update` в синтетике (коммит) и в пилоте только после отчёта `sa-clean` (коммит); read-only на пилоте: `tasks CONFIG <run>` → счётчики; `review CONFIG <run> citations <path>`; README — строка релиза.
  - Files: `README.md`
  - Depends: 3
  - Verify: `gh release view v0.1.17` — 5 ассетов; `spec-audit version` → 0.1.17.
  - REQ: REQ-SA-035, REQ-SA-037

## Открытые вопросы (не блокируют)
1. Промпт — на русском (как протокол); SKILL.md остаётся на английском.
2. `os.Executable()` в промпте — путь установленного бинарника; при запуске из `go run` это временный файл, что для тестов безразлично.

## Риски
- Ответ `prepare`/`tasks` получает три новых поля — потребители читают JSON по ключам, ломаться нечему.
- Промпт бинарника фиксирует порядок работы роли; если хост хочет иной, он пишет свой файл рядом, не правя `prompt.md` (после `retry` он был бы перезаписан).
