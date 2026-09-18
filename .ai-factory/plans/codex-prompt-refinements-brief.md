# Implementation Plan: уточнения промпта роли и `review … REQ-ID brief` (§35)

Branch: codex/prompt-refinements-brief
Created: 2026-09-18

## Original Request
Разбор `sa-clean` run `rent-2026-09-18-02` (повтор с промптами от `prepare`): (3а–ж) вернуть в prompt.md формат test_id, имена запрещённых файлов, запрет общего scratchpad, перечень веток у redteam, допустимые состояния, сводку счётчиками, подкаталоги в рабочем каталоге; (шаг 6) `text` без списков цитат. Очередь ведётся автономно по поручению владельца 2026-09-18. Ограничения: `validate` не сужает форму test_id (решение владельца); frozen §1–10.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки девятого прогона 2026-09-18"

## Tasks
- [x] Task 1: `rolePrompt` — семь уточнений; `requirementText(view, brief)`; диспетчер `REQ-ID brief`; `TestPrepareDispatch`, `TestReviewRequirementView`
- [x] Task 2: §35 tool-spec, AGENTS, SKILL.md, README, ROADMAP («Уроки девятого прогона»), adoption «восьмой цикл»
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.22`, update хоста, skill в синтетике и пилоте (локальный коммит)
