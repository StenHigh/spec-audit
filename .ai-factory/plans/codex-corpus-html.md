# Implementation Plan: сводная HTML-карта корпуса `corpus OUT_HTML CONFIG...` (§45)

Branch: codex/corpus-html
Created: 2026-09-18

## Original Request
Владелец: «мы хотим видеть в одном месте состояние по проекту» — один HTML по всем scope вместо одиннадцати `report.html`. Продолжение `overview`: таблица scope, нормы внимания со statement хоста, файлы под противоречиями, ссылки на `report.html`. Ограничения: без скриптов, без усреднений и заявлений о полноте; страница — файл хоста, не внутри run.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Несколько разделов и обзор корпуса"

## Tasks
- [x] Task 1: `NormBrief`, `RunOverview.Attention`/`Report`, `corpus.html`, `corpus()`, `reportLink`, `corpusFiles`, диспетчер и usage; `TestCorpus`
- [x] Task 2: §45 tool-spec, AGENTS, ARCHITECTURE, SKILL.md, README, ROADMAP; страница на пилоте `.spec-audit/corpus.html`
<!-- Commit checkpoint: tasks 1-2 -->
- [x] Task 3: релиз `v0.1.31`, update хоста, skill в синтетике и пилоте, `corpus` релизным бинарником на пилоте
