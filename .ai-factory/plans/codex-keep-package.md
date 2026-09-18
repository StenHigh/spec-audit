# Implementation Plan: второй пакет на том же scope — операция `keep` (§39, REQ-SA-048), диапазоны якорей

Branch: codex/keep-package
Created: 2026-09-18

## Original Request
Блокер `sa-clean` на `01-foundation`: пакет 2 отклонён «нужен полный учёт кандидатов и прежних active ID» — 58 прежних ID требуют rebind кандидатом, которого второй raw (44 кандидата) содержать не может (лимит 64). Нужна операция продолжения нормы без кандидата с гарантией неизменности её источников; плюс диапазон «A-051–A-055» в anchors не разворачивался. Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: accepted-index.md не переписывается (расширение по прецеденту §22/REQ-SA-043); лимит 64 на raw не меняется.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки одиннадцатого прогона 2026-09-18" (пункт д)

## Tasks
- [x] Task 1: `keep` в `applyAccepted` с guard `keepable` (файлы нормы неизменны между source_set пакетов), `kept[]` в ответах; `anchorRangeKeys`; `TestKeepPackage`, `TestAnchors`
- [x] Task 2: §39 tool-spec (REQ-SA-048, 39.2), AGENTS, SKILL.md, README, ROADMAP
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.25`, update хоста, skill в синтетике и пилоте; `sa-clean` применяет пакет 2 `01-foundation`

## Риски
- `keep` опирается на sha файлов source_set: изменение любого цитируемого файла (даже вне строк нормы) требует rebind — консервативно, зато без сравнения строк.
