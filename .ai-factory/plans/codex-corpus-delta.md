# Implementation Plan: динамика GAP между решёнными run, `baseline_run` (§47)

Branch: codex/corpus-delta
Created: 2026-09-18

## Original Request
Владелец: перед исправлением продукта зафиксировать «до», чтобы после перепрогона на карте корпуса красиво видеть, что закрылось. Разность двух решений хоста по одинаковым нормам (§40.1), закреплённый baseline в CONFIG, раздел «Динамика» и карточка на `corpus`. Ничего не записывается.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Несколько разделов и обзор корпуса"

## Tasks
- [x] Task 1: `Delta`/`NormChange`/`scopeDelta`, `RunOverview.verdicts/keys`, `Config.BaselineRun`, `totals.closed/opened`, раздел «Динамика» в `corpus.html`; `TestCorpusDelta`
- [x] Task 2: §47 tool-spec, AGENTS, SKILL.md, README, ROADMAP
<!-- Commit checkpoint: tasks 1-2 -->
- [ ] Task 3: релиз `v0.1.34`, update хоста, skill в синтетике и пилоте (push), `corpus` на пилоте
