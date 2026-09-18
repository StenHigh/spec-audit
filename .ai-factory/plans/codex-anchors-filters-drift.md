# Implementation Plan: уроки восьмого прогона — `anchors`, фильтры `citations`, WARN о версии, `review … REQ-ID text` (§33)

Branch: codex/anchors-filters-drift
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run `rent-2026-09-18-01` (первый новый раздел): (а) чтение нормы требует jq-форматтера → текстовая форма; (б) `citations` без пути — 861 строка без фильтра по норме/роли; (в) extractor читает 220-KB reference выборочно, хост перечисляет якоря руками → индекс якорей бинарником; (д) версия бинарника меняется между `prepare` и `submit` без предупреждения; (г) accept с редакцией и (е) версия в summary — не делаем (контракт приёмки — владелец; журнал версий — источник). Плюс незарелизенный фикс `b5325a3` (VALIDATE через `tee -a`). Ограничения: frozen §1–10 (stdout — один JSON: текст лежит в поле), формы решения не меняются, никаких путей пилота.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки восьмого прогона 2026-09-18"

## Requirements Reconciliation
Authority: tool-spec §1–10 > §26.2 (правило определения якоря — переиспользуется `anchorRE`/`anchorDefinedRE`) > §29.1 (`RequirementView`) > §30.2 (`citations`) > REQ-SA-040 (журнал версий) > RULES/base > отчёт `sa-clean`. `scanSnapshot` даёт файлы без принятого индекса — `anchors` работает до первого `reconcile`.

## Tasks
- [x] Task 1: код и тесты — `anchorIndex` (`tool/specs.go`), фильтры в `citationIndex`, `warnVersionDrift`, `requirementText`; `TestAnchors`, `TestReviewCitations`, `TestVersionDriftWarning`, `TestReviewRequirementView`
- [x] Task 2: §33 tool-spec, AGENTS, ARCHITECTURE, SKILL.md (anchors перед extraction, `text`, фильтры, WARN)
<!-- Commit checkpoint: tasks 1-2 -->
- [x] Task 3: запись цикла — adoption «седьмой цикл», ROADMAP «Уроки восьмого прогона» и строка таблицы, README §33
- [ ] Task 4: релиз `v0.1.20`, update хоста, `skill update` в синтетике и пилоте (коммит локально; push пилота — по решению владельца, п. (з))

## Риски
- `anchors` тянет определение-заголовок до следующего заголовка любого уровня — для вложенных подразделов диапазон короче раздела; это подсказка для extractor'а, не граница цитаты.
