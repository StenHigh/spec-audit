# Implementation Plan: уроки десятого прогона — `anchors` kind/references, сводка без журнала, `brief` без запусков (§37)

Branch: codex/tenth-run-lessons
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run `inventory-2026-09-18-01`: (а) `anchors` не отличает строку таблицы покрытия от определения и не показывает якоря, на которые ссылается само определение; (б) `index summary` печатает `accepted.history` целиком; (в) `brief` заканчивается `not_recorded` на каждый тест; (г) `overview` — contradicted_ids (уже сделано, §36.3). Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: §26.2 (строка таблицы — определение) не меняется, хост отличает по `kind`.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки десятого прогона 2026-09-18"

## Tasks
- [x] Task 1: `AnchorDefinition{kind, references}`, `indexSummary` accepted `{head, history_total}`, `requirementText` brief без запусков; `TestAnchors`, `TestIndexSummary`, `TestReviewRequirementView`
- [x] Task 2: §37 tool-spec, AGENTS, SKILL.md, README, ROADMAP («Уроки десятого прогона»), adoption «девятый цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.23` (вместе с §36), update хоста, skill в синтетике и пилоте, коммит scope `05-inventory` в пилоте локально
