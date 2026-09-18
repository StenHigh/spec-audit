# Implementation Plan: уроки четырнадцатого прогона — дубликат по словам, кросс-scope память, диапазоны в отказе (§42)

Branch: codex/fourteenth-run-lessons
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run messaging-01/inbound-01: (1) `likely_duplicate` по строкам не ловит свод своими строками — нужна мера по statement/condition; (2) один дефект решается в каждом scope заново — «этот code-citation уже contradicted в scope X»; (3) отказ «цитата вне блока» — печатать допустимые диапазоны; (4) одинаковый base_index пустых ledger — документировать. Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: калибровка §22.1, REQ-SA-015; previous_host внутри ledger не меняется.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки четырнадцатого прогона 2026-09-18"

## Tasks
- [x] Task 1: `textTokens`/`tokenJaccard`, словесные подсказки и `TextSimilarity`; `overview.contradicted_code`; `sourceRanges` в отказе цитаты; тесты
- [x] Task 2: §42 tool-spec, AGENTS, SKILL.md, README, ROADMAP, adoption «тринадцатый цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.28`, update хоста, skill в синтетике и пилоте, коммит конфигов scope 09/10 в пилоте локально
