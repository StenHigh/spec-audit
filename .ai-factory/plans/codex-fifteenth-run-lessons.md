# Implementation Plan: уроки пятнадцатого прогона — `related` и `contradicted_elsewhere` (§43)

Branch: codex/fifteenth-run-lessons
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run webhooks-01/incident-01: `contradicted_code` полезнее обратным индексом «для этого code-citation роли уже contradicted в scope X» прямо под нормой в `review … brief`; метрика памяти на чистом разделе; оговорка окружения как exception. Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: `snapshot_id` не меняется (поле вне manifest); подсказка не вердикт.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки пятнадцатого прогона 2026-09-18"

## Tasks
- [x] Task 1: `Config.Related` (yaml, `json:"-"`, канонизация относительно CONFIG); `elsewhereIndex`/`elsewhereHits`; `Outcome.ContradictedElsewhere`; строка в `requirementText`; `TestReviewContradictedElsewhere`
- [x] Task 2: §43 tool-spec, AGENTS, SKILL.md, README, ROADMAP, adoption «четырнадцатый цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.29`, update хоста, skill в синтетике и пилоте, коммит конфигов scope 08/03 в пилоте локально
