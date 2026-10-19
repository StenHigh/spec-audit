# Change: аудит с сохранением цитат без архива исходников

Branch: codex/citation-retention
Created: 2026-10-19
Issue: https://github.com/StenHigh/spec-audit/issues/4

## Original Request

У нас срочная задача в [https://github.com/StenHigh/spec-audit/issues/4](https://github.com/StenHigh/spec-audit/issues/4) нужно подготовить для нее change и реализовать.

## Settings

- Testing: yes — независимая синтетика и существующие регрессии.
- Logging: standard — ошибки с контекстом, slog.Debug для переходов без содержимого источников.
- Docs: yes — обновить существующие контракты и README в рамках поручения.

## Roadmap Linkage

Milestone: "Аудит и GAP-сайт без обязательного архива исходников (§79, issue №4)"
Rationale: новая универсальная возможность из issue №4; приёмка producer→consumer остаётся отдельной.

## Requirements Reconciliation

Authority: явный запрос владельца issue №4 расширяет §78; frozen §1–10 не меняются. Нормативное расширение — docs/tool-spec.md §79 и docs/external-input.md.

| Сочетание / решение | Основание | Проверка |
| --- | --- | --- |
| Обычный CONFIG без source_retention | §78: полные источники и bundle v1 прежние | существующие TestExternal/Bundle/QuoteOnly |
| source_retention: citations, declared и accepted | №4 п.1–2: хэши, история, точные цитаты без полных файлов; отдельный reports_dir, без миграции архивов | marker вне цитат, принятие и повторный run |
| Локальный checkout и внешний отдельный Markdown | №4 п.1: локальный режим не копирует bytes; внешний import даёт временное tree/corpus, external release завершает их цикл | полный цикл, отказ при открытом run, идемпотентный release, повторный import |
| Источники отсутствуют / изменились | №4 п.4: сохранённое читается, новый анализ требует доступного точного основания; актуальность не объявляется текущей | status/review/report/overview/publish offline, prepare/submit отказ |
| Результат/сайт/пакет citations | №4 п.3–5: full=false, schema2 результата и bundle, без workspace; экспорт после release внешних копий | поиск marker во всех bytes и распакованном архиве; restore без checkout |
| Ошибки / чужие пути / порча | №4 приёмка: сохранность старого результата, хэши и границы прежние | negative tests: чужая схема, hash, symlink, небезопасная очистка |

## Tasks

- [x] 1. Зафиксировать §79 до изменения кода: выбор хранения, версии, lifecycle, совместимость и проверка.
- [x] 2. CONFIG/source inventory/report/result: citations без записи полных bytes, immutable policy; offline чтение и честные счётчики. Файлы tool/main.go, retention.go, sources.go, overview.go, external_result.go. Логи DEBUG без bytes; зависит от 1.
- [x] 3. Внешний lifecycle import/release/reimport: сохранять metadata и origin, удалять только проверенные временные копии при завершённых решениях/перепроверках. Файлы tool/external.go, retention.go, workspace.go. Ошибки до удаления, DEBUG переходов; зависит от 1–2.
- [x] 4. Bundle v2 citations: результат без workspace, строгий verify/restore offline, не ослабляя v1. Файлы tool/bundle.go и регрессии. Ошибки с контекстом и прежний atomic staging; зависит от 1–3.
- [x] 5. Сквозные регрессии полного/повторного/инкрементального declared/accepted, marker в состоянии/сайте/пакете, offline restore, отрицательные проверки и прежний архивный режим. Файлы tool/*_test.go; зависит от 2–4.
- [x] 6. Обновить контракт клиента, launcher/help, README, ARCHITECTURE, ROADMAP и LESSONS; go test ./tool, go vet ./tool, сборка, доступный UI-check. Тестовые роли явно fixture; зависит от 5.

## Commit Plan

Предлагаемые группы: задачи 1–3 `feat: add citation-only source retention`; 4–6 `feat: preserve citation-only audits in portable bundles`. Коммит и push только по отдельному запросу пользователя.

Проверка 2026-10-19: Linux/amd64, Go 1.27.2. Итоговый `go test ./tool` — PASS (185.884s); `go vet ./tool`, сборка и `git diff --check` — PASS. TestCitation* — PASS; отдельный HTTP/Playwright/Chromium citations-check и штатный UI-check — PASS, external-result/2 проверен JSON Schema. В штатном UI-скрипте macOS-пути скриншотов заменены на /tmp только во временной копии скрипта. Роли — deterministic fixtures. Нативный macOS, реальный producer→consumer, релиз, установка, commit/push не выполнялись.
