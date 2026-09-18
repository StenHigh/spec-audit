# Implementation Plan: публикация для заказчика `publish OUT_DIR CONFIG...` и история run (§49)

Branch: codex/publish
Created: 2026-09-18

## Original Request
Владелец: отчёты живут на машине прогонов — нужна простая публикация для заказчика: HTML на любой сервер, карта подтягивает все проходы; подумать о выборе прохода по времени (история есть). Решение: статический сайт (`index.html` + `overview.json` + копии `report.html` всех решённых run), история run со временем в самой карте вместо JS-селекта.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Публикация для заказчика"

## Tasks
- [x] Task 1: `RunRef`/`ScopeOverview.History`, `renderCorpus`, `publish()`, диспетчер и usage, колонка «История» в `corpus.html`; `TestPublish`
- [x] Task 2: §49 tool-spec, AGENTS, SKILL.md, README, ROADMAP/LESSONS
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.36`, update хоста, skill в синтетике и пилоте (push), `publish` на пилоте
