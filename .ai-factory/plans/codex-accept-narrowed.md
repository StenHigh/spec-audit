# Implementation Plan: DECISION приёмки version 2 — сужение statement кандидата (§46, REQ-SA-049)

Branch: codex/accept-narrowed
Created: 2026-09-18

## Original Request
Решение владельца 2026-09-18: 2a — вариант B (сужение statement при принятии, только удаление смысла, оригинал в журнале); 2b — multi-raw не вводить, `keep` остаётся; pricing пока не продолжать; продуктовые вопросы — владелец. Ограничения: accepted-index.md не переписывается (новая версия формы DECISION + отдельный контроль); requiredJSON строг — новая форма = новая версия.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки десятого прогона 2026-09-18" (пункт г) и «Уроки одиннадцатого прогона» (пункт д — закрыт решением)

## Tasks
- [x] Task 1: `acceptedDecisionV2`/`decodeDecision`, `AcceptedTarget.Narrowed` (`json:"-"`), guard `narrowable`, `requiredJSON` без внутренних полей; `TestAcceptNarrowed`
- [x] Task 2: §46 tool-spec, AGENTS, SKILL.md, README, ROADMAP (решения владельца)
<!-- Commit checkpoint: tasks 1-2 -->
- [x] Task 3: релиз `v0.1.33`, update хоста, skill в синтетике и пилоте (push)
