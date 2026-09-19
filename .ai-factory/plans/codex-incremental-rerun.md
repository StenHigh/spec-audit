# Implementation Plan: инкрементальный перепрогон после изменения кода (§50, REQ-SA-050)

Branch: codex/incremental-rerun
Created: 2026-09-19

## Original Request
Milestone «Инкрементальный перепрогон после изменения кода» (первый в ROADMAP): после фикса продукта не гонять полный run по scope (12 ролей, ~3–4 M токенов на 100 норм) — `prepare CONFIG RUN_ID since PREV_RUN` даёт ролям только нормы, чьи цитируемые файлы изменились или которые были GAP; остальные переносятся в новый run как `carried` с провенансом без переоценки; `review`/`report`/`corpus` показывают перенесённые отдельно. Критерий: перепрогон finance после правки одного сервиса ≤ ⅓ полного при той же динамике GAP. Повод сейчас: пилот изменил 84 файла, из них 13 под contradicted; перепрогон восьми scope полностью — ~25 M токенов.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Инкрементальный перепрогон после изменения кода"

## Requirements Reconciliation
Authority: tool-spec §1–10 (позиционный CLI, `prepare CONFIG RUN_ID` сохраняется; `since PREV_RUN` — расширение) > §14/§23/§25 (решение покрывает все нормы; формы не меняются, `concur` получает значение `carried`) > §40.1 (сравнение норм по `content_hash`+`revision`) > §45.1 (GAP) > §47 (динамика) > RULES/base. Snapshot_id считается до добавления `incremental` в manifest — свежесть не меняется. Ограничение честно названо: перенос опирается на неизменность цитируемых строк, транзитивные зависимости не отслеживаются; полный run остаётся эталоном.

### Решения
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| `prepare CONFIG RUN_ID since PREV_RUN`: PREV_RUN — run того же scope с решением хоста и полной доставкой; по каждой норме текущего manifest: assess, если (а) её ключа (`id`+`content_hash`+`revision`) нет в PREV, (б) вердикт PREV — GAP (§45.1), (в) любой файл, процитированный объединённой оценкой хоста в PREV (spec/code/tests), изменил sha256 или исчез; иначе carried. `manifest.incremental = {since_run, review_id, assessed[], carried[]}`, где carried — вердикт хоста PREV (состояния, statement, limitations, объединённые цитаты) и `files` (path+sha256) | Минимальная честная эвристика; ролям — только затронутое | `TestIncrementalRerun` |
| Задания: `newState` строит задачи только по assessed-нормам; scope без них не получает задач; run без задач сразу `delivery_complete` | §24.2 | тот же тест |
| Решение: `concur: carried` в version 2/3 — свидетельства из `manifest.incremental.carried`; состояния должны совпадать с перенесёнными, иначе требуются собственные цитаты хоста (version 3) — вердикт хоста остаётся его суждением; `mapper/redteam/both` на carried-норме — отказ (роль её не оценивала); version 1 — как прежде. `draft` предзаполняет carried-нормы (`concur: carried`, состояния, statement, limitations) — это прежнее суждение того же хоста, не роли (решение владельца о предзаполнении ролевых вердиктов не нарушается) | §14, §23, §25 | тот же тест |
| Чтение: `outcomes[].carried = {run_id, review_id}` без ролей, `agree` не считается; `review … summary` — `carried` число; `report` — флаг `carried` и basis `carried`; `overview.decided.carried`; динамика §47 работает как обычно (перенесённое = без изменений) | §24.1, §15, §34 | тест + `TestCorpusDelta` |

## Tasks
- [x] Task 1: контракт §50 (REQ-SA-050) в tool-spec; зеркало host-decision.md не меняется (§14/§23/§25 без правок текста — значение `carried` описано в §50)
- [x] Task 2: код — manifest `Incremental`, отбор норм, `newState`, `prepare … since`, TaskBatch/status/outcomes/draft/adoptEvidence/report/overview; тесты `TestIncrementalRerun` (полный run r1 → правка одного файла → `prepare r2 since r1`: задания только по затронутым и GAP-нормам; carried с провенансом; роли; `draft` с carried; отказ `concur: mapper` на carried; `review` принят; `report`/`summary`/`corpus` показывают carried; PREV без решения — отказ)
<!-- Commit checkpoint: tasks 1-2 -->
- [x] Task 3: SKILL.md (когда и как запускать `since`; полный run периодически), README, ROADMAP/LESSONS
- [ ] Task 4: релиз `v0.1.37`, update хоста/синтетики/пилота; живая проверка на пилоте после run rent-2026-09-19-01 — инкрементальный run одного из оставшихся scope

## Риски
- Перенос по неизменности цитируемых файлов не видит транзитивных изменений — документировано, полный run остаётся эталоном.
- `concur: carried` расширяет допустимые значения `concur` — зеркало host-decision.md не трогаем, значение описано в §50 (расширение, не правка §14/§23).

## Уточнение по первому живому инкременту (§51, v0.1.39)
Критерий «≤ ⅓» на inventory не выполнен (90 %): переоценивался весь класс GAP, `changed` по sha файла, стоимость роли фиксирована на задание. Исправлено: spec-gap переносится, verification-gap — только при изменении тестов, `changed` — по цитируемым строкам, задания регруппированы в `incremental-N` (по семи scope пилота 24 задания вместо 60), `plan` без создания run, причины в outcomes/brief/промпте, `same_files` по файлам нормы.
