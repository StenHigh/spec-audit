# Implementation Plan: уроки двенадцатого прогона — `previous_host` по содержанию нормы, `pending_ids`, VALIDATE как есть (§40)

Branch: codex/twelfth-run-lessons
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run foundation-01/02: (а) после `keep` `previous_host` исчезает — искать по content_hash+revision, не по head; (б) `matches` шумят заголовками во втором пакете; (в) роли гоняют validate без tee; (г) `tasks` — очередь ID для волн. Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: калибровка `matches` §22.1 не меняется; frozen §1–10.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки двенадцатого прогона 2026-09-18"

## Tasks
- [x] Task 1: `previousHostRun(reports, other, m)` — `filesDigest` + `sameNorm`; `PendingIDs` в TaskBatch; п.5 промпта; тесты
- [x] Task 2: §40 tool-spec, AGENTS, SKILL.md, README, ROADMAP, adoption «одиннадцатый цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.26`, update хоста, skill в синтетике и пилоте, коммит scope `01-foundation` в пилоте локально
