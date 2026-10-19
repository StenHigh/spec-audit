# Change: сохранённое решение и непроверенная актуальность

Branch: codex/saved-snapshot-freshness
Created: 2026-10-20

## Original Request

но нам добавили еще один issue [https://github.com/StenHigh/spec-audit/issues/8](https://github.com/StenHigh/spec-audit/issues/8) помсмотри его и тоже создай change и реализуй

## Settings

- Testing: yes — сначала синтетическое воспроизведение, затем общий Go-набор и доступный browser-check.
- Logging: verbose — структурированный Debug через существующий slog/LOG_LEVEL, без новых зависимостей.
- Docs: yes — обновить существующие контракт/launcher/контекст по запросу issue; отдельное согласование документации не требуется.

## Roadmap Linkage

Milestone: "Разделить сохранённое решение, доступность источников и текущую актуальность (§80, issue №8)"
Rationale: Продолжение приёмки §79; штатный release не доказывает изменение источников.

## Requirements Reconciliation

Приоритет: запрос владельца реализовать issue №8 → уточнение tool-spec §80 → прежние §65/78/79 в непротиворечащей части. Frozen §1–10, прежние raw/журналы и схемы результата v1/v2 не переписываются.

| Наблюдение / основание | Freshness / доступность | Решение | Проверка |
| --- | --- | --- | --- |
| Точное доступное основание, тот же snapshot | fresh / available | current при Complete | declared/accepted + archive/citations |
| Проверенный успешный external release | unchecked / released | saved при Complete | полный confirmed run → release → все представления |
| Нет источника / отказ чтения без mismatch | unchecked / unavailable | saved при Complete | удалённый checkout, запрещённая ссылка, offline restore |
| Доказанное изменение bytes/состава/CONFIG/индекса/runtime | stale / available либо unavailable | outdated относительно текущего источника; Complete сохраняет пригодность по прежнему основанию | изменённый код, invalid Markdown после чтения новых bytes, внешний runtime |
| Retry / новые свидетельства изменили reviewBasis | независимо от источника | outdated, Complete=false | retry и новое решение, receipt перепроверки |

Общее наблюдение используется status/review/report/overview/review-check. Новый анализ и запись свидетельств требуют строго fresh. Поля availability/reason добавляются явно; false старого snapshot_current не служит доказательством изменения. Навигация host-saved и нейтральные тексты отделены от host-stale. GAP/цитаты/история/independent review сохраняются. Метаданные/описи/hashes и boundary-проверки bundle/release не обходятся.

Внешний результат schema v3 явно версионирует новые enum и наблюдение; старые v1/v2 схемы остаются. Исторические bytes не исправляются автоматически: новые представления вычисляются при чтении/формировании отчёта, новые публикации используют общий renderer. Bundle v1/v2 и source inventory сохраняют прежнюю проверку и форматы.

## Tasks

- [x] 1. Уточнить §80 и воспроизвести release→stale/outdated отдельной синтетической регрессией до правки реализации.
- [x] 2. Ввести общее наблюдение источников, отличить mismatch от недоступности и saved от outdated во всех core-readers; сохранить строгие write guards и отсутствие записи наблюдений в snapshot/raw.
- [x] 3. Согласовать report/navigation/HTML/corpus, внешний результат v3 и публикацию/восстановление с общим наблюдением; обновить контракт, схему и launcher.
- [x] 4. Проверить release/unavailable/real change/retry/restore/rehydrate/legacy archive, полный Go test/vet/build, доступный browser-check и frozen hashes; обновить README/ROADMAP/LESSONS фактическими результатами.

Коммит, push, релиз и сообщение в issue №8 в этот change не входят без отдельного поручения владельца.

Воспроизведение до реализации: `go test ./tool -run ^TestSavedSnapshotReleaseIsNotSourceChange$ -count=1` — FAIL: release дал stale/outdated при прежнем snapshot и полной доставке (0.430s). Журнал: /tmp/spec-audit-issue8-before.log.

Core-регрессии declared archive/citations: отсутствие, запрет symlink, изменение, invalid Markdown, retry; `TestSavedSnapshot*` и сохранность исторической публикации — PASS (5.715s). Наблюдение исключено из persisted Manifest/snapshot identity.

Итоговая проверка на Linux: полный `go test ./... -count=1` — PASS (173.647s); `go vet ./...`, сборка и frozen acceptance/legacy hashes — PASS. После финального уточнения документации повторены `TestSkillFiles` и сборка — PASS. JSON Schema v3 проверена на archive/citations, текущих и исторических результатах после restore; схемы v1/v2 сохранены. Chromium/HTTP-проверки citations и archive сайтов, а также общего report/corpus UI — PASS: история, цитаты, фильтры, мобильный режим и no-JS. README/ROADMAP/LESSONS обновлены. Native macOS и реальный внешний producer в этой итерации не проверялись.
