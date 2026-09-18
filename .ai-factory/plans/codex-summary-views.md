# Implementation Plan: сводки `index CONFIG summary` и `review CONFIG RUN_ID summary` (§32)

Branch: codex/summary-views
Created: 2026-09-18

## Original Request
Уроки четвёртого прогона 2026-09-17, пункт 7 (владелец: «отдельно после следующего прогона»; очередь ведётся автономно по поручению 2026-09-18): компактные выводы `index`/`reconcile`/`review` — сейчас 0,9–1,6 MB читаются только jq. Форма — ключевое слово, а не флаг (контракт CLI позиционный; прецеденты §27 `host`, §30.2 `citations`). Ограничения: полные ответы не меняются; frozen §1–10; никаких путей пилота.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки четвёртого прогона 2026-09-17 (решение владельца по каждому пункту)"

## Requirements Reconciliation
Authority: tool-spec §1–10 (позиционный CLI) > §24.1/§27.2 (`outcomes[]`) > §26.1 (`freshness`) > §28.1 (`advisories` всегда) > RULES/base > ROADMAP. `indexConfig` (`tool/specs.go`) возвращает map — сводка строится из него (`indexSummary`); `reviewContext` (`tool/review.go`) → `ReviewBrief` без `entries`/`executions`/`requirements`/`latest`; `reconcile CONFIG` без RAW сводки не получает — в accepted-режиме `index summary` даёт те же counts/advisories/freshness.

## Tasks
- [x] Task 1: §32 tool-spec, AGENTS, ARCHITECTURE — Files: `docs/tool-spec.md`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
- [x] Task 2: `indexSummary`, `ReviewBrief`/`reviewBrief`, диспетчер (`index CONFIG summary`, `review … summary` read-only), usage; `TestIndexSummary`, `TestReviewBrief` — Files: `tool/specs.go`, `tool/review.go`, `tool/main.go`, `tool/specs_test.go`, `tool/review_test.go`
<!-- Commit checkpoint: tasks 1-2 -->
- [x] Task 3: SKILL.md шаги 1 и 6, README, ROADMAP (четвёртый 7 `[x]`, строка таблицы) — Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`
- [ ] Task 4: релиз `v0.1.19`, update хоста/синтетики; пилот — после завершения run на новом разделе — Files: `README.md`

## Риски
- DECISION-файл, буквально названный `summary` или `citations`, не открыть через `review` — как и `host` у `validate`.
