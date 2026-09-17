# Implementation Plan: `freshness` в `index`, подсказка о якорях ТЗ вне snapshot, общий `dispatch/files.json` (§26)

Branch: codex/index-freshness-anchors-files
Created: 2026-09-18

## Original Request
Уроки пятого прогона 2026-09-17, пункты 4 + 5 + 7 (решение владельца 2026-09-18: мелочи одним планом после 1+2+3; пункт 6 — protocol.txt — отдельно): (4) явное поле `freshness` в ответе `index` — сейчас хост выводит свежесть индекса из отсутствия stale-признаков и сравнения snapshot_id соседних run; `reconcile CONFIG` без RAW уже отдаёт `freshness` (uninitialized/fresh/stale) — `index` должен отдавать то же поле с той же семантикой, без изменения прочих полей ответа `index`; (5) подсказка на `check`/`index`: текст принятой нормы (statement/condition/verification, а также exceptions/unresolved) ссылается на якорь (A-NNN, §X.Y или иной идентификатор раздела ТЗ), которого нет в `source_set`+`references` snapshot — в run 04 исключение по REQ-AI-060 было определено A-204 в файле вне scope, redteam честно поставил contradicted, хост опроверг чтением вне snapshot; подсказка должна сработать ещё на приёмке (`check`/`reconcile` dry-run) и повторяться в `index` как advisory (по прецеденту §20 `advisories`), не отказ и не оценка; форму якоря взять узко и проверяемо (регулярное выражение по шаблонам «A-NNN» и «§N.N…»), сопоставление — по тексту всех spec-файлов snapshot (`source_set`+`references`): якорь считается присутствующим, если его строка встречается в любом spec-файле; ложные срабатывания допустимы как advisory; (7) `files.json` в каталогах `dispatch/<task_id>/` — одна копия на run вместо шести (529 KB × 6): `prepare` пишет `dispatch/files.json` один раз, а в `dispatch/<task_id>/` — только `task.json`; SKILL.md шаг 3 и §24.2 обновляются; роли читают общий `files.json` рядом с каталогом (файл не принадлежит другой роли, протокол изоляции не нарушается); `retry` без изменений. Ограничения: frozen §1–10 не меняются; §20/§24 расширяются новым разделом tool-spec (редакция 1.6) или подпунктами §25 по прецеденту; никаких путей/имён пилота (TestSkillFiles); acceptance-хэши не переписываются. См. ROADMAP «Уроки пятого прогона 2026-09-17» пункты 4, 5, 7; docs/smsplace-adoption.md «четвёртый цикл»; tool-spec §20 (advisories), §21/§24.2 (dispatch), §22 (диагностика scopes); tool/specs.go (scopeAdvisories, index), tool/accepted.go (check/stageAcceptance), tool/main.go (writeDispatch, index/reconcile dispatch).

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки пятого прогона 2026-09-17 (решение владельца по каждому пункту)"
Rationale: решение владельца 2026-09-18 — после 1+2+3 закрыть мелочи 4, 5, 7 одним планом; 6 — protocol.txt отдельно. Владелец 2026-09-18 уточнил пункт 5: якорь считается присутствующим только при определении (строка spec-файла начинается с якоря), не при упоминании в прозе.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 (ответ `index` §1–10 — новые поля добавляются, существующие не меняются; `prepare`/`retry` без изменения контракта заданий) > §20 REQ-SA-041/`advisories` (прецедент: advisory — подсказка в ответе `index`, не отказ и не оценка) > §21.2 и §24.2 (каталог роли `dispatch/<task_id>/`, «`prepare` создаёт … `files.json` (список файлов snapshot)» — §26 меняет место `files.json` на `dispatch/files.json`; §24.2 получает одну уточняющую фразу) > §22 (диагностика `scopes`) > [RULES.md](../RULES.md) («не превращай … в доказательство» — advisory не оценка; спецификация прежде реализации; universality) > [rules/base.md](../rules/base.md) (ранние проверки, `os.Root`, лимиты чтения `maxFile`) > ROADMAP (объём) > отчёт `sa-clean` run 04.

Разведка по коду: `indexConfig(cfg)` (`tool/specs.go:222-238`) строит ответ из `snapshot(cfg)`; в accepted-режиме `snapshot` **отказывает** при stale/отсутствующем индексе («принятый индекс отсутствует или stale; нужен reconcile», `tool/main.go:478`) — успешный `index` уже означает fresh, но поле не названо; `reconcile CONFIG` и `check` отдают `freshness` через `acceptedFreshness(ledger, state, files)` (`tool/accepted.go:498-506`). `scopeAdvisories(m)` → `index["advisories"]` (`tool/specs.go:209-236`); `checkAcceptance` (`tool/accepted.go:673-`) строит `view{valid, base_index, freshness, source_set, candidates}` и при DECISION — `staged.state.Records` (следующее состояние). Текст норм: `Requirement{Condition, Statement, Verification}` + `Accepted{Exceptions, Unresolved}` (`tool/specs.go:27-36`, `tool/accepted.go:17-24`); кандидаты `legacyCandidate{ID, Condition, Statement, Exceptions, Clarity, Unresolved, Citations}` (`tool/legacy.go:11-19`). Spec-файлы snapshot — `m.Files` с `Kind == "spec"` (включая references, `Reference: true`); текст читается `readRoot(root, path, maxFile)` по `os.OpenRoot(cfg.ProjectRoot)`. `writeDispatch(run, task, files)` (`tool/main.go:876-891`) пишет `task.json` и `files.json` в каталог задания; вызовы в `prepare` (для каждого entry с `m.Files`) и `retry` (`nil`). Пилот (read-only): в тексте 51 нормы 6 якорей вида `§N…` (§8, §6.1, §5.2, §4.2, §1.7.6A, §1.6.10A); все они упоминаются в spec-файлах snapshot, но `§1.6.10A` и `§1.7.6A` определены заголовками `## 1.6.10A …`/`## 1.7.6A …` в `02-finance.md`, а A-204 (случай 060) в тексте нормы не назван вовсе — текстовая подсказка его не поймала бы; якоря `A-NNN` определяются строками `* A-204: …` в файле вне source_set. Форматы определения: заголовок `#… 1.6.10A …`, пункт `* A-204: …`, ячейка `| A-164 |`.

### Решения (по итогам разведки)
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| Новый §26 tool-spec «Уроки пятого прогона, мелочи: `freshness` в `index`, якоря ТЗ вне snapshot, общий `files.json`» (редакция 1.6): 26.1 `freshness`, 26.2 подсказка о якорях, 26.3 `dispatch/files.json`; §24.2 — одна фраза «`files.json` … в каталоге задания» → «общий `dispatch/files.json` (§26.3)». `host-decision.md` — зеркало §14+§23+§25 без изменений; срез §25 в `TestHostDecisionMirror` теперь до «## 26.» | Прецедент §19–§25; §25 остаётся контрактом решения, мелочи — отдельно | `TestHostDecisionMirror`; `shasum -a 256 -c acceptance/*.sha256` OK |
| `index` в accepted-режиме отдаёт `"freshness": "fresh"` (успех возможен только при fresh — stale отказывает по-прежнему); в declared-режиме поле отсутствует (понятия свежести индекса нет). Прочие поля без изменений | Хост выводил свежесть косвенно; честнее назвать инвариант | `TestIndexFreshness`: accepted-fixture после apply → `freshness: fresh`; правка spec-файла → `index` отказ «stale»; declared fixture — поля нет |
| Подсказка о якорях: `anchorAdvisories(root *os.Root, files []SourceFile, norms []anchorSource) []string`, где `anchorSource{ID string; Texts []string}` строится из норм (`Condition, Statement, Verification, Accepted.Exceptions, Accepted.Unresolved`) или кандидатов (`Condition, Statement, Exceptions, Unresolved`). Якоря извлекаются регулярным выражением `\bA-\d{2,4}\b` и `§\s?(\d+(?:\.\d+)*[A-Za-zА-Яа-я]?)`; для `A-NNN` ключ — сам токен, для `§…` — номер без `§`. Определение = строка любого spec-файла snapshot (`Kind == "spec"`, читается `readRoot(root, path, maxFile)`), у которой после необязательных `#`, `*`, `-`, `|`, пробелов и `**` стоит якорь, а за ним конец строки, пробел, `:`, `.`, `)`, `|` или `*`. Отсутствующие якоря → одна advisory на норму: `«REQ-…: якоря §1.6.10A, A-204 не определены в spec-файлах snapshot (source_set+references); норма может опираться на раздел вне scope»`; порядок — по нормам manifest, якоря в порядке появления. Ложные срабатывания допустимы (advisory); подсказка не оценка и не отказ. Честно: якорь, не названный в тексте нормы (как A-204 для 060), подсказка не находит | Решение владельца 2026-09-18 «только определение»; §20 прецедент advisories; RULES | `TestAnchorAdvisories`: fixture с нормой, чей statement ссылается на `§9.9` и `A-777`, а spec-файл содержит только упоминание `§9.9` в прозе → обе в advisory; добавить строку `## 9.9 …` → остаётся только `A-777`; строка `* A-777: …` в файле references → advisory исчезает; `§1.2` без записи → нет (нормы без якорей не упоминаются); нормы без якорей → advisories нет |
| Точки вывода: `index` — `index["advisories"]` = `scopeAdvisories` + `anchorAdvisories` (по `m.Requirements`); `check CONFIG RAW` — `view["advisories"]` по кандидатам raw; `check CONFIG RAW DECISION` — по `staged.state.Records` (следующее состояние); `reconcile CONFIG` без RAW — по текущим `state.Records` (дополняет существующий ответ); ключ `advisories` появляется только при непустом списке (как в `index`) | Подсказка должна сработать на приёмке и повторяться в `index` | `TestAnchorAdvisories` — все четыре точки; `TestScopeAdvisories` без изменений |
| `prepare`: `writeDispatch(run, task, nil)` для каждого задания + один `writeJSON(run, "dispatch/files.json", m.Files, 0600)`; сигнатура `writeDispatch(run, task Task)` (параметр `files` убирается); `retry` без изменений. DEBUG «dispatch: список файлов» {files} | 529 KB × 6 идентичных копий; роли читают общий файл рядом с каталогом — он не принадлежит другой роли | `TestPrepareDispatch`: `dispatch/files.json` == `batch.Files`, в каталогах заданий `files.json` отсутствует; `retry` не трогает общий файл |
| SKILL.md шаг 3: «`prepare` writes `reports_dir/RUN_ID/dispatch/files.json` (the allowed source file list, one per run) and each task's `dispatch/<task_id>/task.json`; give the role its task directory, the shared files.json path, SOURCE_ROOT and this protocol»; шаг 2/приёмка: «`check`/`index` list `advisories` — an anchor named in a norm (§N.N, A-NNN) with no definition in the snapshot's spec files means the norm may rest on a section outside scope: read it before judging, or add the file to `references`»; README, ROADMAP (4, 5, 7 `[x]`), adoption | Честный статус | `TestSkillFiles`; `rg -n 'files.json|advisories' skills/spec-audit/SKILL.md README.md` |
| Поставка: релиз `v0.1.12`, `update`, `skill update` в синтетике/пилоте (коммиты по запросу); read-only на пилоте: `index CONFIG` → `freshness: fresh`, `advisories` — ожидаем подсказки по якорям, определённым вне snapshot (§8/§6.1/§5.2/§4.2 — проверить, где определены), без изменения ledger; `check` не запускается (нужен RAW) | §18 | `gh release view v0.1.12` — 5 ассетов; `git status` пилота — только tracked-копия skill |

### Поддерживаемые комбинации
| Комбинация | Вход | Результат | Проверка |
| --- | --- | --- | --- |
| `index`, accepted-режим, индекс fresh | — | `freshness: fresh` + прежние поля | `TestIndexFreshness` |
| `index`, accepted-режим, индекс stale | — | прежний отказ «stale; нужен reconcile» | `TestIndexFreshness` |
| `index`, declared-режим | — | поля `freshness` нет | `TestIndexFreshness` |
| норма с якорем, определённым в spec-файле (заголовок/пункт/ячейка) | текст норм + spec-файлы | advisory нет | `TestAnchorAdvisories` |
| норма с якорем, лишь упомянутым в прозе | — | advisory | `TestAnchorAdvisories` |
| якорь определён в файле `references` | — | advisory нет | `TestAnchorAdvisories` |
| `check RAW` / `check RAW DECISION` / `reconcile` без RAW | кандидаты / следующее состояние / текущее | `advisories` при непустом списке | `TestAnchorAdvisories` |
| `prepare` | — | `dispatch/files.json` один, `task.json` в каталогах | `TestPrepareDispatch` |
| `retry` | — | `task.json` перезаписан, общий `files.json` цел | `TestPrepareDispatch` |

Представительные реальные артефакты: пилот `index CONFIG` после релиза (read-only) — `freshness` и подсказки по 6 якорям текста норм; `acceptance/declared-v01` — `index` без `freshness`.

## Commit Plan
- **Commit 1** (after tasks 1-2): "feat(index): §26 — freshness в index, подсказка о якорях ТЗ вне snapshot"
- **Commit 2** (after task 3): "feat(prepare): общий dispatch/files.json вместо копии в каждом каталоге"
- **Commit 3** (after task 4): "docs(launcher): SKILL.md о files.json и advisories; README/ROADMAP/adoption"
- **Commit 4** (after task 5): "docs(dist): релиз v0.1.12"

## Tasks

### Phase 1: Контракт, freshness и якоря
- [x] Task 1: §26 tool-spec (26.1–26.3), редакция 1.6; фраза §24.2; граница среза §25 в `TestHostDecisionMirror`; AGENTS/ARCHITECTURE
  - Deliverable: шапка «1.6 добавляет §26 …». §24.2: «`task.json` (объект задания, равный `tasks[i]` ответа `prepare`) и `files.json` (список файлов snapshot, равный `files` ответа `prepare`)» → «`task.json` (…); список файлов snapshot, равный `files` ответа `prepare`, пишется один раз в `dispatch/files.json` (§26.3)». §26 перед «См. также»: причина (run 04: свежесть индекса выводилась косвенно; исключение нормы определено в файле вне scope — честно: подсказка ловит только якоря, названные в тексте нормы; 529 KB × 6 копий). 26.1 «`freshness` в ответе `index`»: в accepted-режиме `index` успешен только на fresh-индексе и называет это полем `freshness: fresh`; stale — прежний отказ; declared-режим — поля нет. 26.2 «Подсказка о якорях ТЗ вне snapshot»: форма якорей (`A-NNN`, `§N.N…`), источник текста (condition/statement/verification/exceptions/unresolved нормы или кандидата), определение = строка spec-файла snapshot (source_set+references), начинающаяся с якоря после markdown-маркеров; advisory на норму с перечнем якорей; точки вывода `index`, `check RAW`, `check RAW DECISION`, `reconcile` без RAW; не оценка и не отказ, ложные срабатывания допустимы; ограничение: якорь, не названный в тексте, не находится. 26.3 «Общий `dispatch/files.json`»: одна копия на run, каталоги заданий — только `task.json`; роли читают общий файл; `retry` без изменений; исторические run с `files.json` в каталогах обслуживаются как прежде. `tool/skill_test.go`: срез §25 — до «## 26.». AGENTS.md — строка §26, «§13–26»; ARCHITECTURE — specs.go (advisories о якорях), main.go (общий files.json).
  - Files: `docs/tool-spec.md`, `tool/skill_test.go`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
  - Verify: `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; `shasum -a 256 -c acceptance/*.sha256 acceptance/legacy/baseline.sha256` OK; `rg -n 'smsplace|FinanceSystem' docs/tool-spec.md` без новых вхождений.
  - Logging: документ фиксирует: DEBUG «index: якоря вне snapshot» {requirements, anchors}; DEBUG «dispatch: список файлов» {files}.
  - REQ: REQ-SA-041, §21.2, §24.2, RULES «Свидетельства»

- [x] Task 2: `freshness` в `index`; `anchorAdvisories` и точки вывода
  - Deliverable: `tool/specs.go`: `indexConfig` — при `m.Accepted != nil` `index["freshness"] = "fresh"`; `anchorRE = regexp.MustCompile(`\bA-\d{2,4}\b|§\s?\d+(?:\.\d+)*[A-Za-zА-Яа-я]?`)`; `type anchorSource struct{ ID string; Texts []string }`; `requirementAnchorSources(reqs []Requirement) []anchorSource`, `candidateAnchorSources(cands []legacyCandidate) []anchorSource`; `anchorAdvisories(root *os.Root, files []SourceFile, sources []anchorSource) []string`: собрать все якоря из текстов (ключ: `A-NNN` как есть; `§…` → номер), прочитать каждый spec-файл (`readRoot(root, file.Path, maxFile)`, ошибка чтения — пропуск файла с DEBUG), по строкам: `definedRE = ^\s*(?:#{1,6}\s+|[*-]\s+|\|\s*)?(?:\*\*)?(A-\d{2,4}|\d+(?:\.\d+)*[A-Za-zА-Яа-я]?)(?:\*\*)?(?:[\s:.)|*]|$)` → множество определённых; для каждой нормы недостающие якоря → advisory `fmt.Sprintf("%s: якоря %s не определены в spec-файлах snapshot (source_set+references); норма может опираться на раздел вне scope", id, strings.Join(missing, ", "))`; DEBUG «index: якоря вне snapshot» {requirements, anchors}. Вызовы: `indexConfig` — `advisories = append(scopeAdvisories(m), anchorAdvisories(root, m.Files, requirementAnchorSources(m.Requirements))...)` (root по `cfg.ProjectRoot`); `tool/accepted.go` `checkAcceptance`: без DECISION — по `staged.raw.Candidates` (`candidateAnchorSources`), с DECISION — по `staged.state.Records` (их `Requirement`), в `view["advisories"]` при непустом; `reconcile CONFIG` без RAW (функция, возвращающая `base_index/source_set/freshness…`, `tool/accepted.go:~520`) — по `state.Records` при `scanErr == nil`. Тесты: `tool/specs_test.go` `TestIndexFreshness` (accepted-fixture + apply → `index` содержит `freshness: fresh`; правка `rules.md` → `runFail index`; declared `fixture` → ключа нет), `TestAnchorAdvisories` (accepted-fixture: кандидат со statement «Срок по §9.9 и A-777 …»; `check RAW` → advisories с обоими; apply → `index` и `reconcile` содержат ту же advisory; дописать в `rules.md` строку `## 9.9 Сроки` (это меняет snapshot → индекс stale: поэтому сначала подготовить файлы, затем apply — порядок в тесте: (1) файл с прозой «см. §9.9» → check показывает оба; (2) добавить `## 9.9 …` и файл references с `* A-777: …` (config `references`), новый raw/apply → `index` без advisories); минимально: две конфигурации fixture — «упоминание в прозе» и «определения» — и проверка на `check` до apply и на `index`/`reconcile` после apply).
  - Files: `tool/specs.go`, `tool/accepted.go`, `tool/specs_test.go`
  - Depends: 1
  - Verify: `go test ./tool -run 'TestIndexFreshness|TestAnchorAdvisories|TestScopeAdvisories|TestCheckAcceptance|TestAcceptedLifecycle' -count=1`; `go vet ./tool`; `./bin/spec-audit index` на копии acceptance declared — без `freshness`.
  - Logging: DEBUG «index: якоря вне snapshot» {requirements, anchors}; DEBUG «index: spec-файл пропущен» {path, error}.
  - REQ: 26.1, 26.2, REQ-SA-041
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Общий files.json
- [x] Task 3: `prepare` пишет `dispatch/files.json` один раз
  - Deliverable: `tool/main.go`: `writeDispatch(run *os.Root, task Task) error` (без `files`), `prepare`: `run.MkdirAll("dispatch", 0700)`, `writeJSON(run, "dispatch/files.json", m.Files, 0600)` + DEBUG «dispatch: список файлов» {files}, затем `writeDispatch` для каждого задания; `retry` — `writeDispatch(run, entry.Task)`. `tool/main_test.go` `TestPrepareDispatch`: `dispatch/files.json` разбирается в `[]SourceFile`, равный `batch.Files`; в каталогах заданий нет `files.json`; после `retry` общий файл байт-в-байт прежний.
  - Files: `tool/main.go`, `tool/main_test.go`
  - Depends: 1
  - Verify: `go test ./tool -run 'TestPrepareDispatch|TestPrepare|TestRetry' -count=1`; `go vet ./tool`.
  - Logging: DEBUG «dispatch: список файлов» {files}; DEBUG «dispatch: каталог задания» {task_id}.
  - REQ: 26.3, §24.2
<!-- Commit checkpoint: task 3 -->

### Phase 3: Документация и поставка
- [x] Task 4: launcher SKILL.md; README, ROADMAP, документ применения
  - Deliverable: `skills/spec-audit/SKILL.md` шаг 3 (общий `dispatch/files.json` + `task.json` в каталоге) и шаг приёмки/индекса («`check`/`index`/`reconcile` list `advisories`; an anchor named in a norm (`§N.N`, `A-NNN`) with no definition in the snapshot's spec files means the norm may rest on a section outside scope — read that section before judging or add its file to `references`; an anchor the norm does not name is not detected»); README «Первый запуск» — `freshness` в `index`, advisories о якорях, `dispatch/files.json`; «Приёмка и ограничения» — строка §26 (реализовано; регрессии; ограничение подсказки; не проверено до релиза: пилот). ROADMAP «Уроки пятого прогона» — 4, 5, 7 `[x]`; строка таблицы этапов. docs/smsplace-adoption.md «четвёртый цикл»/«рабочий цикл» — сноска о 0.1.12 и о том, что случай 060 (якорь не назван в норме) подсказкой не покрывается.
  - Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`, `docs/smsplace-adoption.md`
  - Depends: 2, 3
  - Verify: `go test ./tool -run 'TestSkillFiles|TestHostDecisionMirror' -count=1`; `rg -n 'files.json|advisories|freshness' skills/spec-audit/SKILL.md README.md`; полный `go test ./tool`, `go vet ./tool`, `CGO_ENABLED=0 go build -o bin/spec-audit ./tool`.
  - Logging: нет нового.
  - REQ: REQ-SA-037, 26.1–26.3

- [x] Task 5: релиз `v0.1.12`, обновление хоста/пилота/синтетики, read-only `index` на пилоте
  - Deliverable: merge в `codex/bootstrap` (по подтверждению владельца), тег `v0.1.12`, approve deployment; `spec-audit update` → 0.1.12; `skill update --host both` в синтетике и пилоте (коммиты — по запросу); read-only на пилоте: `index CONFIG` → `freshness: fresh`, `advisories` (какие из 6 якорей текста норм определены заголовками в snapshot, какие — нет); ledger не тронут. README «Приёмка и ограничения» — строка релиза 0.1.12.
  - Files: `README.md`; (пилот и синтетика — вне репозитория)
  - Depends: 4
  - Verify: `gh release view v0.1.12` — 5 ассетов; `spec-audit version` → 0.1.12; `spec-audit update` → `updated:false`; `git -C <pilot> status --porcelain` — только tracked-копия skill.
  - Logging: нет нового.
  - REQ: REQ-SA-035, REQ-SA-037
<!-- Commit checkpoint: tasks 4-5 -->

## Открытые вопросы (не блокируют)
1. До T2: `§`-якорь без `§` в определении («## 1.6.10A …») — сравнение по номеру; якоря вида «раздел 5.2» без `§` не извлекаются (узкая форма по решению владельца).
2. До T3: исторические run с `files.json` в каталогах заданий — не переписываются; launcher читает общий файл только у новых run.

## Риски
- Подсказка о якорях читает все spec-файлы snapshot при каждом `index`/`check`/`reconcile` — линейно по объёму (пилот: два файла ≈ 0,1 MB); при большом ТЗ приемлемо.
- Ложные advisory (номер вроде `§8` совпадает с заголовком другого раздела или не совпадает из-за иной нотации) — allowed by design; SKILL.md называет их подсказкой.
- Изменение места `files.json` требует обновления launcher одновременно с бинарником — оба поставляются одним релизом.
