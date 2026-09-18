# Implementation Plan: уроки шестнадцатого прогона — точность памяти, свёртка, отказ типа, prepare в каталог (§44)

Branch: codex/sixteenth-run-lessons
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run pricing-entry-01: (1) `contradicted_code` без разметки даёт ~⅓ ложных подсказок; (2) 36 строк памяти подряд в brief; (3) text_similarity не ловит pseudo-code vs проза; (4) отказ типа JSON не называет поле; (5) `prepare` отказывает на заранее созданном каталоге. Владелец вернулся; очередь продолжается по прежнему циклу.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки шестнадцатого прогона 2026-09-18"

## Tasks
- [x] Task 1: `runOverview` — цитаты только contradicted-ролей и хоста; `ElsewhereHit.RoleHere`; свёртка в `requirementText`; `strictJSON` — имя поля; `prepare` — существование по manifest; тесты
- [x] Task 2: §44 tool-spec, AGENTS, SKILL.md, README, ROADMAP, adoption «пятнадцатый цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.30`, update хоста, skill в синтетике и пилоте (push — как одобрено владельцем)
