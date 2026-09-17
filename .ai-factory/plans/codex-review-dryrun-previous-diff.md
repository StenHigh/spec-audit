# Implementation Plan: Проверка решения хоста без записи, statement прошлого решения и разность цитат ролей в `outcomes[]` (§27)

Branch: codex/review-dryrun-previous-diff
Created: 2026-09-18

## Original Request
Уроки шестого прогона 2026-09-18, пункты 1 + 2 + 3 (решение владельца 2026-09-18: сначала эти три — шаг 6 согласования): (1) dry-run решения хоста — проверка DECISION без записи в append-only журнал: та же валидация, что у `review CONFIG RUN_ID DECISION` (версия/идентичность, форма, counts, собственные цитаты хоста по snapshot, полнота §7 на объединении для v3, basis/CAS, дубликат/конфликт review_id, полнота доставки), но без публикации и без записи провенанса; ответ — `{valid, version, review_id, review_state_after?, duplicate?, requirements, own_citations[]}` или отказ с тем же текстом, что дал бы `review`; форма команды — по контракту CLI §1–10 (позиционный): предпочтительно новая команда `verify CONFIG RUN_ID DECISION`? — нет, имя `validate` занято ролями; выбрать имя в плане по прецеденту `check` (§21) и `draft` (§24), например `check-review` недопустимо из-за дефиса? — решить в плане; хост в run 06 писал в журнал «вслепую», в run 03 ошибка счётчика стоила второго review_id; (2) `previous_host` в `outcomes[]` дополнить `statement` и `limitations` прошлого решения по норме (из последней записи журнала соседнего run: assessments[i].statement/limitations для v1, verdicts[i] для v2/v3) — хост восстанавливал «почему weak» из памяти сессии; только чтение, поле по-прежнему подсказка; (3) разность цитат ролей по норме в `outcomes[]` — для каждой роли количество цитат и список цитат (path, line_start, line_end, без quote), которые есть только у этой роли (`only_here`), чтобы хост решал, нужны ли собственные цитаты, не собирая это jq из entries; цитаты spec/code/tests раздельно; entries не меняются. Ограничения: frozen §1–10 не меняются; §14/§23/§25 расширяются новым разделом tool-spec (редакция 1.7) по прецеденту; если раздел меняет контракт решения — в зеркало host-decision.md; никаких путей/имён пилота (TestSkillFiles). См. ROADMAP «Уроки шестого прогона 2026-09-18», docs/smsplace-adoption.md «пятый цикл», tool-spec §14, §21 (check), §23–§25, tool/review.go (submitReview, validateReview, previousHost, outcomes, draftDecision), tool/main.go (argc/readOnly/draft class).

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки шестого прогона 2026-09-18 (решение владельца по каждому пункту)"
Rationale: решение владельца 2026-09-18 — пункты 1 + 2 + 3 первыми: снимают слепую запись в append-only журнал и ручной jq по `entries` на шаге 6; затем 4 + 7 + пункт 5 четвёртого прогона; 8 — отдельно; 9 — только с новой версией формы. Имя команды dry-run — решение владельца 2026-09-18: `validate CONFIG RUN_ID host DECISION`.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 (`validate CONFIG RUN_ID TASK_ID RESULT_PATH` — §19 REQ-SA-038-подобно «проверка ответа роли без записи»; позиционный CLI; `host` как псевдо-TASK_ID — расширение, не изменение существующего пути: у реального задания ID вида `<scope>-<role>` и `slugRE`, слово `host` не совпадает с ним, если scope не назван `host` без роли — невозможно) > §14 (журнал append-only, дубликат/конфликт review_id, основание, «запись только при полной доставке», лимит 64, повторная сверка snapshot) > §23/§25 (формы v2/v3, собственные цитаты хоста, `own_citations`) > §24.1/§25.1–25.2 (`outcomes[]`, `previous_host` — подсказка, не перенос) > [RULES.md](../RULES.md) («stale-свидетельства не становятся текущими автоматически»; спецификация прежде реализации; universality) > [rules/base.md](../rules/base.md) (ранние проверки, ошибки с контекстом, отсутствие скрытых мутаций) > ROADMAP (объём) > отчёт `sa-clean` run 18-01.

Разведка по коду: `submitReview(run, runID, m, state, path, versions)` (`tool/review.go`, ~`:520-580`): `readReviews` → `readPath` → `validateReview(data, …, nil, true)` (форма) → `summarizeReviews` → цикл дубликата (`duplicate:true` при тех же байтах, конфликт при других) → `pending`/basis → при `Version >= 2` повторная валидация со `state` (цитаты хоста, объединение) → лимит 64 → `json.MarshalIndent(journal)`/`maxState` → `snapshot(m.Config)` повторная сверка → `invalidateReports` → запись → `publishToolVersion` → ответ. Всё до `invalidateReports` — чистые проверки; их можно вынести в `stageReview(run, runID, m, state, path) (reviewRecord, ReviewSummary, reviewJournal, []byte, bool /*duplicate*/, error)`. CLI: `validate` — `argc 5`, класс без провенанса и без readOnly (`tool/main.go:1101,1170`), ветка `index := -1 … "неизвестный TASK_ID"` (`:1361-1368`) — перед ней можно перехватить `args[3] == "host"`. `previousHostRun` — лёгкая структура `row{RequirementID, Specification, Implementation, Assertion}` (`tool/review.go:343-348`) по `assessments`/`verdicts` последней записи; `PreviousHost{RunID, ReviewID, Specification, Implementation, Assertion}` (`:191-197`). `RoleOutcome{TaskID, Attempt, Specification, Implementation, Assertion, Limitations}` (`:180-187`); `outcomes(m, state, previous)` обходит `state.Entries` и `entry.Result.Assessments` — цитаты `Assessment.Spec/Code []Citation`, `Tests []TestCitation` (`tool/main.go:112-122`) доступны там же. Пилот run 18-01: хост считал разность цитат по 023 вручную; «почему weak» по 035 — из памяти.

### Решения (по итогам разведки)
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| Новый §27 tool-spec «Уроки шестого прогона: проверка решения хоста без записи, statement прошлого решения, разность цитат ролей» (редакция 1.7): REQ-SA-047 «`validate CONFIG RUN_ID host DECISION`» + 27.1 `previous_host.statement/limitations` + 27.2 `only_here`. Зеркало `host-decision.md` (§14+§23+§25) не меняется — dry-run и таблица не меняют контракт решения; срез §25 по-прежнему до «## 26.» | Прецедент §19 (`validate` роли), §21 (`check`), §24 (`draft`); RULES | `TestHostDecisionMirror`; `shasum -a 256 -c acceptance/*.sha256` OK |
| `validate CONFIG RUN_ID host DECISION`: класс `validate` (без провенанса, stale-снимок — прежний отказ); выполняет `stageReview` целиком, включая повторную сверку snapshot и лимит 64, и **не** вызывает `invalidateReports`/запись/`publishToolVersion`. Ответ: `{valid:true, version, review_id, duplicate, review_state, requirements, own_citations:[…]}` — `duplicate:true` при тех же байтах уже записанного решения (`review_state` — текущее состояние журнала), иначе `duplicate:false`, `review_state:"current"` (таким оно станет после записи); отказы — те же тексты, что у `review`. `submitReview` использует `stageReview` — один путь проверок, второго набора условий нет | Хост писал в журнал вслепую; ошибка счётчика в run 03 стоила второго review_id | `TestReviewValidateHost`: v3 с цитатами хоста → `valid:true`, `own_citations`, журнал и `tool-versions.json` байт-в-байт прежние, затем `review` принимает те же байты; неверные counts / stale basis / неполная доставка / чужой quote → те же тексты отказов, что у `review`; после записи те же байты → `duplicate:true`; другие байты с тем же review_id → отказ «конфликт review_id»; `host` для роли (`submit … host …`) — «неизвестный TASK_ID» как прежде |
| `PreviousHost` + `Statement string json:"statement"`, `Limitations []string json:"limitations"` — из той же последней записи (`assessments[i]` v1 / `verdicts[i]` v2, v3); `row` лёгкой структуры расширяется двумя полями; `[]` при отсутствии | «Почему weak» брали из памяти сессии | `TestReviewPreviousHost`: `previous_host.statement == "host"` (v2 из теста), limitations `[]`; на пилоте — statement из run 18-01 |
| Разность цитат: `RoleOutcome` получает `Citations CitationCounts json:"citations"` (`{spec, code, tests int}`) и `OnlyHere CitationDiff json:"only_here"` (`{spec []CitationRef, code []CitationRef, tests []TestRef}`); `CitationRef{Path, LineStart, LineEnd}` (json `path,line_start,line_end`), `TestRef{TestID, Path, LineStart, LineEnd}`. `only_here` роли = её цитаты (по значению `Citation` без quote — ключ `path:line_start-line_end`; для tests — `test_id` + диапазон), которых нет у другой роли по той же норме; при отсутствии другой роли — все её цитаты. Массивы всегда присутствуют (`[]`). `entries` не меняются | Хост считал разность вручную, чтобы решить о собственных цитатах | `TestReviewOutcomes`: роли с общими цитатами → `only_here` пусты, `citations` совпадают; redteam без tests по первой норме → у mapper `only_here.tests` = 1, у redteam `citations.tests` 0; после `retry` mapper — у redteam `only_here` = все его цитаты |
| SKILL.md шаг 6: «run `BINARY validate CONFIG RUN_ID host DECISION` first — same checks as `review`, nothing written; fix until `valid:true`, then `review`»; «`outcomes[].roles.<role>.only_here` lists the lines only that role cites — decide about the host's own citations from it; `previous_host.statement` explains the earlier verdict». README «Первый запуск», «Приёмка и ограничения» (строка §27), ROADMAP (шестой прогон 1–3 `[x]`), adoption | Честный статус | `TestSkillFiles`; `rg -n 'validate CONFIG RUN_ID host|only_here' skills/spec-audit/SKILL.md README.md` |
| Поставка: релиз `v0.1.14`, `update`, `skill update` в синтетике/пилоте (коммиты по запросу); read-only на пилоте: `review CONFIG finance-2026-09-18-01` → `only_here` по 023 (строки, которые цитирует только redteam), `previous_host.statement` из run 04; `validate CONFIG finance-2026-09-18-01 host dispatch/host/host-review-001.decision.json` → `duplicate:true` (те же байты уже в журнале), журнал и провенанс прежние | §18 | `gh release view v0.1.14` — 5 ассетов; `git status` пилота — только tracked-копия skill |

### Поддерживаемые комбинации
| Комбинация | Вход / валидация | Состояние | Результат | Проверка |
| --- | --- | --- | --- | --- |
| `validate … host DECISION`, новое решение v2/v3, доставка полная, basis текущий | `stageReview` | без записи | `valid:true, duplicate:false, review_state:current, own_citations` | `TestReviewValidateHost` |
| те же байты уже в журнале | дубликат | без записи | `valid:true, duplicate:true`, `review_state` журнала | `TestReviewValidateHost` |
| тот же review_id, другие байты | конфликт | без записи | отказ «конфликт review_id» | `TestReviewValidateHost` |
| counts/quote/stale/pending | как у `review` | без записи | те же тексты отказов | `TestReviewValidateHost` |
| `submit … host …` | — | — | «неизвестный TASK_ID» (псевдо-задание только у `validate`) | `TestReviewValidateHost` |
| `previous_host` из v1/v2/v3 записи | лёгкий разбор | — | `statement`, `limitations` | `TestReviewPreviousHost`, пилот |
| обе роли есть, общие/различные цитаты | — | — | `citations` и `only_here` по группам | `TestReviewOutcomes` |
| одна роль без результата | — | — | у другой `only_here` = все цитаты | `TestReviewOutcomes` |

Представительные реальные артефакты: пилот `finance-2026-09-18-01` (журнал v3 с цитатами хоста — `validate … host` даёт `duplicate:true`; `only_here` по 023) и run 04 (`previous_host.statement` v1 → в run 18-01).

## Commit Plan
- **Commit 1** (after tasks 1-2): "feat(review): §27 — validate CONFIG RUN_ID host DECISION без записи"
- **Commit 2** (after task 3): "feat(review): statement прошлого решения и only_here в outcomes[]"
- **Commit 3** (after task 4): "docs(launcher): SKILL.md о validate host, only_here и previous_host.statement; README/ROADMAP/adoption"
- **Commit 4** (after task 5): "docs(dist): релиз v0.1.14"

## Tasks

### Phase 1: Контракт и dry-run решения
- [x] Task 1: §27 tool-spec (REQ-SA-047, 27.1, 27.2), редакция 1.7; AGENTS/ARCHITECTURE
  - Deliverable: шапка «1.7 добавляет §27 …». §27 перед «См. также»: причина (run 18-01: запись решения вслепую, run 03: второй review_id из-за счётчика; «почему weak» из памяти; разность цитат вручную). REQ-SA-047 «Проверка решения хоста без записи»: Условие — `validate CONFIG RUN_ID host DECISION`; Требование — те же проверки, что у `review CONFIG RUN_ID DECISION` (версия/идентичность, форма, counts, собственные цитаты хоста по snapshot, полнота §7 на объединении для version 3, полнота доставки, основание, дубликат/конфликт review_id, лимит журнала, повторная сверка snapshot), без записи журнала, отчётов и провенанса; ответ `{valid, version, review_id, duplicate, review_state, requirements, own_citations}`; отказы — те же тексты; `host` — псевдо-задание только этой команды, `submit` его не принимает. 27.1 «Statement прошлого решения»: `previous_host` получает `statement` и `limitations` последней записи соседнего run (v1 assessments / v2–v3 verdicts). 27.2 «Разность цитат ролей»: `roles.<role>.citations {spec, code, tests}` — числа; `roles.<role>.only_here {spec[], code[], tests[]}` — ссылки `{path, line_start, line_end}` (`tests` с `test_id`) на цитаты, которых нет у другой роли по той же норме; при отсутствии другой роли — все цитаты; quote не копируется; `entries` не меняются; подсказка для решения о собственных цитатах хоста, не оценка. Проверка — комбинации плана. Источник/Обоснование/Зависимость (SA-038/§19 validate, SA-044, SA-046, §14). AGENTS.md — строка §27, «§13–27»; ARCHITECTURE — review.go (`stageReview`, `validate host`, `only_here`).
  - Files: `docs/tool-spec.md`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
  - Verify: `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; `shasum -a 256 -c acceptance/*.sha256 acceptance/legacy/baseline.sha256` OK; `rg -n 'smsplace|FinanceSystem' docs/tool-spec.md` без новых вхождений.
  - Logging: документ фиксирует: INFO «validate: решение хоста проверено» {run_id, review_id, version, duplicate, own_citations}; DEBUG «review: разность цитат» {requirements, only_here}.
  - REQ: REQ-SA-038, REQ-SA-044, REQ-SA-046, §14

- [x] Task 2: `stageReview`, `validate … host DECISION`, ответ dry-run
  - Deliverable: `tool/review.go`: `type stagedReview struct{ record reviewRecord; view ReviewSummary; journal reviewJournal; data []byte; duplicate bool }`; `stageReview(run *os.Root, runID string, m Manifest, state State, path string) (stagedReview, error)` — тело `submitReview` до `invalidateReports` включительно с проверками: форма (`validateReview(..., nil, true)`), `summarizeReviews`, дубликат (те же байты → `duplicate:true`, другие → «конфликт review_id: исходные байты отличаются»), `pending`/basis, повторная валидация со `state` при `Version >= 2`, лимит 64, размер журнала после append (без записи), `snapshot(m.Config)` сверка; `submitReview` = `stageReview` → при `duplicate` прежний ответ → `invalidateReports` → запись → `publishToolVersion` → прежний ответ. `validateHostDecision(run, runID, m, state, path) (map[string]any, error)`: `stageReview` → `{valid:true, version, review_id, duplicate, review_state (view.State при duplicate, иначе "current"), requirements: len(assessments), own_citations: sortedKeys(record.Own) или []}`; INFO «validate: решение хоста проверено». `tool/main.go`: перед `index := -1` (`:1361`): `if command == "validate" && args[3] == "host" { return validateHostDecision(run, runID, m, state, args[4]) }`; usage-строка: `validate CONFIG RUN_ID host DECISION`. Тесты `tool/review_test.go` `TestReviewValidateHost` (по образцу `TestReviewV3`): v3 с цитатами хоста → `valid:true`, `own_citations == ["REQ-DEMO-001"]`, `duplicate:false`, `review_state:"current"`; журнал отсутствует/прежний и `tool-versions.json` байт-в-байт прежний; затем `review` принимает те же байты; повторный `validate` тех же байт → `duplicate:true`, `review_state:"current"`; другие байты того же review_id → отказ «конфликт review_id»; counts+1 → «counts.relevant», quote испорчена → «цитата не совпадает», stale basis → «текущая база», до полной доставки → «нужны все ответы ролей»; `submit … host …` → «неизвестный TASK_ID».
  - Files: `tool/review.go`, `tool/main.go`, `tool/review_test.go`
  - Depends: 1
  - Verify: `go test ./tool -run 'TestReview' -count=1`; `go vet ./tool`.
  - Logging: INFO «validate: решение хоста проверено» {run_id, review_id, version, duplicate, own_citations}; отказы — прежние тексты.
  - REQ: REQ-SA-047, REQ-SA-038, §14
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: `outcomes[]` — statement прошлого решения и разность цитат
- [x] Task 3: `previous_host.statement/limitations`, `citations`/`only_here` у ролей
  - Deliverable: `tool/review.go`: `PreviousHost` + `Statement string json:"statement"`, `Limitations []string json:"limitations"`; `previousHostRun`: `row` + `Statement`, `Limitations` → в `PreviousHost` (`[]` при nil). `type CitationRef struct{Path string; LineStart, LineEnd int}` (json `path,line_start,line_end`), `type TestRef struct{TestID, Path string; LineStart, LineEnd int}`, `type CitationCounts struct{Spec, Code, Tests int}`, `type CitationDiff struct{Spec []CitationRef; Code []CitationRef; Tests []TestRef}`; `RoleOutcome` + `Citations CitationCounts json:"citations"`, `OnlyHere CitationDiff json:"only_here"`. `outcomes`: для нормы собрать оценки по ролям (`map[role]Assessment`), затем для каждой роли `citations` и `only_here` = цитаты, ключ которых (`path:start-end`; tests — `test_id|path:start-end`) отсутствует у другой роли (при её отсутствии — все); массивы инициализированы (`[]`); DEBUG «review: разность цитат» {requirements, only_here} (число норм с непустой разностью). Тесты: `TestReviewOutcomes` — при общих цитатах `only_here` пусты и `citations` равны; redteam без tests по первой норме → `mapper.only_here.tests` = 1 запись с `test_id`, `redteam.citations.tests == 0`; после `retry` mapper — `redteam.only_here.code` = все его code-цитаты; `TestReviewPreviousHost` — `previous_host.statement == "host"`, `limitations == []`.
  - Files: `tool/review.go`, `tool/review_test.go`
  - Depends: 2
  - Verify: `go test ./tool -run 'TestReview' -count=1`; `go vet ./tool`; на копии журналов пилота (scratch) — `only_here` по REQ-AI-023 в run 18-01.
  - Logging: DEBUG «review: разность цитат» {requirements, only_here}.
  - REQ: 27.1, 27.2, §25.2
<!-- Commit checkpoint: task 3 -->

### Phase 3: Документация и поставка
- [x] Task 4: launcher SKILL.md; README, ROADMAP, документ применения
  - Deliverable: `skills/spec-audit/SKILL.md` шаг 6: перед `review … DECISION` — «run `BINARY validate CONFIG RUN_ID host DECISION` first: the same checks as `review`, nothing is written; fix the file until it answers `valid:true`, then call `review`»; `outcomes[]`: «`roles.<role>.only_here` lists the lines only that role cites — use it to decide whether the host's own citations are needed; `previous_host.statement` explains the earlier verdict, still a reading aid». README «Первый запуск» — `validate … host`, `only_here`, `previous_host.statement`; «Приёмка и ограничения» — строка §27 (реализовано; регрессии; не проверено до релиза: пилот). ROADMAP «Уроки шестого прогона» — 1, 2, 3 `[x]`; строка таблицы этапов. docs/smsplace-adoption.md «рабочий цикл» — с 0.1.14 `validate … host` перед `review`.
  - Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`, `docs/smsplace-adoption.md`
  - Depends: 2, 3
  - Verify: `go test ./tool -run 'TestSkillFiles|TestHostDecisionMirror' -count=1`; `rg -n 'validate CONFIG RUN_ID host|only_here' skills/spec-audit/SKILL.md README.md`; полный `go test ./tool`, `go vet ./tool`, `CGO_ENABLED=0 go build -o bin/spec-audit ./tool`.
  - Logging: нет нового.
  - REQ: REQ-SA-037, REQ-SA-047, 27.1, 27.2

- [x] Task 5: релиз `v0.1.14`, обновление хоста/пилота/синтетики, read-only проверка на пилоте
  - Deliverable: merge в `codex/bootstrap` (по подтверждению владельца), тег `v0.1.14`, approve deployment; `spec-audit update` → 0.1.14; `skill update --host both` в синтетике и пилоте (коммиты — по запросу); read-only на пилоте: `review CONFIG finance-2026-09-18-01` → `only_here` по REQ-AI-023 (цитаты только redteam), `previous_host.statement` из run 04; `validate CONFIG finance-2026-09-18-01 host <dispatch/host/host-review-001.decision.json>` → `valid:true, duplicate:true`; журнал и `tool-versions.json` прежние. README «Приёмка и ограничения» — строка релиза 0.1.14.
  - Files: `README.md`; (пилот и синтетика — вне репозитория)
  - Depends: 4
  - Verify: `gh release view v0.1.14` — 5 ассетов; `spec-audit version` → 0.1.14; `spec-audit update` → `updated:false`; `git -C <pilot> status --porcelain` — только tracked-копия skill.
  - Logging: нет нового.
  - REQ: REQ-SA-035, REQ-SA-037, REQ-SA-047
<!-- Commit checkpoint: tasks 4-5 -->

## Открытые вопросы (не блокируют)
1. До T2: `validate … host` выполняет и повторную сверку snapshot, и проверку размера журнала после append — да, чтобы ответ `valid:true` означал «`review` с этими байтами сейчас запишет».
2. До T3: `only_here` — по значению без quote (path + диапазон); две цитаты одного диапазона с разными quote невозможны в одном snapshot.

## Риски
- Единый `stageReview` меняет структуру `submitReview`; поведение пинуют `TestReviewV2/V3/Lifecycle` и `release-control.json` (SA-019…021).
- `only_here` увеличивает `outcomes[]` (ссылки без quote) — на пилоте ≈ 51 × десятки ссылок, приемлемо; `entries` остаются полным источником.
- Псевдо-задание `host` у `validate`: реальный TASK_ID `host` невозможен (ID = `<scope>-<role>`), конфликт имён исключён.
