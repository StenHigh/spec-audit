# Implementation Plan: разбор внешнего review 2026-09-18 (§48)

Branch: codex/review-findings
Created: 2026-09-18

## Original Request
Владелец: ознакомиться с `REVIEW.md` (статическое review до §30–§47), закрыть актуальные ошибки и идеи. Подтверждённые и малорисковые — исправить; контрактные и требующие измерений/контейнера — вынести владельцу.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Разбор внешнего review 2026-09-18"

## Tasks
- [x] Task 1: полнота §7 для version 2 (`validateVerdicts`), `atomicWrite` chmod до rename и `update` без Chmod после, `signal.NotifyContext` → `processContext` в runtime/php, regexp hoisting, `slices.Equal`, подпись reference-группы, curl-таймауты, `ci.yml`; `TestReviewV2`
- [x] Task 2: §48 tool-spec, AGENTS, README, ROADMAP (открытые пункты — владельцу)
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.35`, update хоста, skill в синтетике и пилоте (push)
