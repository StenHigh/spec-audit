# Implementation Plan: обзор нескольких scope `overview CONFIG...` (§34)

Branch: codex/overview
Created: 2026-09-18

## Original Request
ROADMAP «После ближайших этапов — несколько разделов и обзор корпуса»: на пилоте появился второй scope (`11-allocation-rent`), нужна сводная карта того, что принято и решено по каждому scope. Первый минимальный шаг (очередь ведётся автономно по поручению владельца 2026-09-18): read-only `overview CONFIG...` без заявления о полноте корпуса; инвентарь полноты, противоречия норм и неописанное поведение — отдельно. Ограничения: frozen §1–10, ничего не пишется, никаких путей пилота.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Несколько разделов и обзор корпуса"

## Requirements Reconciliation
Authority: tool-spec §1–10 (позиционный CLI, вариативные аргументы как у `php-facts`) > §12 (никакого PASS/полноты) > §19 (`tool-versions.json` — время `prepare`) > §23–§25 (`summarizeReviews`) > accepted-index.md (`readAccepted`, `acceptedFreshness`) > RULES/base. Переиспользуются `snapshot`, `scanSnapshot`, `readToolVersions`, `readReviews`, `summarizeReviews`, `makeStatus`, `outcomes`.

## Tasks
- [x] Task 1: `tool/overview.go` — `Overview`/`ScopeOverview`/`RunOverview`, `overview`, `scopeOverview`, `runOverview`; диспетчер и usage; `TestOverview`
- [x] Task 2: §34 tool-spec, AGENTS, ARCHITECTURE, SKILL.md, README, ROADMAP
<!-- Commit checkpoint: tasks 1-2 -->
- [x] Task 3: релиз `v0.1.21`, update хоста, skill в синтетике и пилоте (локальный коммит), `overview` на пилоте релизным бинарником

## Риски
- `latest` выбирается по `recorded_at` записи `prepare`; run без журнала версий (до §19) сравнивается по имени — на пилоте таких нет.
