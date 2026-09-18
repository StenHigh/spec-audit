# Implementation Plan: уроки тринадцатого прогона — имена полей в отказе, `ambiguous_over_clear`, `likely_duplicate` (§41)

Branch: codex/thirteenth-run-lessons
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run provider-integrations-01: (1) отказ «неполный или неизвестный набор полей» не называет поле; (2) роль поднимает ambiguous над принятой clear — validate пропускает (верно), review не подсвечивает; (3) `matches` при своде — подсказывать дубликат при unique_shared 0 и overlap > 50 %; (4) per-scope implementation в overview — уже есть в `decided`. Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: калибровка §22.1 и REQ-SA-015 не меняются.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки тринадцатого прогона 2026-09-18"

## Tasks
- [x] Task 1: `requiredJSON` — списки полей; `Outcome.AmbiguousOverClear`; `matchHint.LikelyDuplicate`; тесты
- [x] Task 2: §41 tool-spec, AGENTS, SKILL.md, README, ROADMAP, adoption «двенадцатый цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [x] Task 3: релиз `v0.1.27`, update хоста, skill в синтетике и пилоте, коммит scope `06-provider-integrations` в пилоте локально
