# Implementation Plan: уроки одиннадцатого прогона — остаток пакета в ответах, `anchors` вне snapshot, `oversize_reason` (§38)

Branch: codex/eleventh-run-lessons
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run `activation-2026-09-18-01`: (а) `check`/`reconcile`/`index summary` не называют deferred/rejected; (б) `anchors` не резолвит §-ссылки в файлы корпуса вне snapshot; (в) advisory «>24» на неделимом подразделе — подавлять явной пометкой scope; (д) multi-raw пакет — контракт приёмки, владельцу. Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: лимит 64 и контракт приёмки не меняются; frozen §1–10.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки одиннадцатого прогона 2026-09-18"

## Tasks
- [x] Task 1: `decidedCandidates` в ответах `check`/`reconcile`, `last_deferred`/`last_rejected` в `index summary`; `anchorIndex(cfg, extra)` — `outside_files`, `outside`, `nested`; `Scope.OversizeReason`; тесты `TestCheckAcceptance`, `TestAnchors`, `TestScopeOversizeReason`, `TestIndexSummary`
- [x] Task 2: §38 tool-spec, AGENTS, SKILL.md, README, ROADMAP, adoption «десятый цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.24`, update хоста, skill в синтетике и пилоте, коммит scope `07-allocation-activation` в пилоте локально
