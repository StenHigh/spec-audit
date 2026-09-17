# Implementation Plan: Облегчённая форма host DECISION (version 2) и список ролей в `review` (§23)

Branch: codex/review-decision-v2
Created: 2026-09-17

## Original Request
Уроки третьего прогона 2026-09-17, пункты 5–6 (решение владельца 2026-09-17, «доделать остальное перед прогонами»): (5) облегчённая форма host DECISION для `review` — вердикт по каждой норме со ссылкой на принятое свидетельство роли («concur: mapper|redteam|both|none») и statement хоста без копирования чужих цитат, плюс машиносверяемые counts (доводы третьего прогона: 823 KB DECISION, чтение 102 оценок и повторная запись из-за арифметической ошибки хоста в summary); §14 не frozen §1–10, но `host-decision.md` — зеркало §14, проверяемое тестом, `acceptance/release-control.json` пинит SA-019…021 — прежняя полная форма (version 1) остаётся действительной, новая форма вводится как version 2 расширением по прецеденту §19–§22; (6) `review CONFIG RUN_ID` — производный список ролей с task_id/role/scope/attempt/submitted на верхнем уровне ответа (entries остаются как есть). Пункт 7 (вопросы к ТЗ пилота) — вне инструмента. См. ROADMAP «Уроки третьего прогона 2026-09-17», docs/smsplace-adoption.md «Чистая AI-сессия, 2026-09-17 (второй цикл)», tool-spec §14/§21/§22, skills/spec-audit/references/host-decision.md.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки третьего прогона 2026-09-17 (решение владельца по каждому пункту)"
Rationale: решение владельца 2026-09-17 — пункты 5–6: DECISION version 2 «вердикт + concur без цитат» с машиносверяемыми counts (полная форма version 1 остаётся для собственных свидетельств хоста) и производный список ролей в ответе `review`; пункт 7 — вопросы к ТЗ пилота, вне инструмента.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 (§7 контракт ответа роли — не меняется; §9) > §14 «Контракт согласования аудита» (не frozen, но зеркалится в `host-decision.md` и пинится `TestHostDecisionMirror`; DECISION v1: «assessments содержит ровно одну обычную Assessment §7 на каждую норму»; основание CAS `{snapshot_id, state, review_head}`; журнал `host-reviews.json` с точными байтами, до 64 записей; состояния missing/current/outdated) и §13 REQ-SA-019 (явное согласование), REQ-SA-020 (актуальность), REQ-SA-021 (итог рядом со свидетельствами) > `acceptance/release-control.json` (frozen хэшем — сценарии SA-019…021 для v1 должны оставаться зелёными) > §21/§22 (прецедент расширений) > [RULES.md](../RULES.md), [rules/base.md](../rules/base.md), [ARCHITECTURE.md](../ARCHITECTURE.md) > ROADMAP (объём) > отчёт сессии `sa-clean` (docs/smsplace-adoption.md «Чистая AI-сессия, 2026-09-17 (второй цикл)»: 823 KB DECISION, ≈15 мин на чтение 102 оценок, повторная запись из-за арифметической ошибки в summary). Разведка по коду: `ReviewDecision`/`validateReview`/`readReviews`/`summarizeReviews`/`submitReview` (`tool/review.go:14-192`: DECISION валидируется как `Result` роли `host` через `validateResult`, журнал перечитывается с `validateReview(raw, runID, m, false)` на каждую запись, `Latest *ReviewDecision` идёт в `ReviewSummary` → `report.host_review` и HTML); `requiredJSON` (`tool/main.go:676-`) требует **ровно** все поля структуры — необязательных полей нет, поэтому новая форма — отдельный тип и `version: 2`, а не поле в `Assessment`; отчёт берёт host-оценки из `report.HostReview.Latest.Assessments` (`tool/report.go:296-300, 353-356`), HTML — `Navigation.Host` и `HostReviewState` (`tool/report.html:55-58,77`); `TestHostDecisionMirror` (`tool/skill_test.go:118-138`) сравнивает `host-decision.md` с §14 между `\n## 14. ` и `\n## 15. `.

### Решения (по итогам разведки)
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| Новый §23 tool-spec «Облегчённая форма host DECISION (version 2) и список ролей» (редакция 1.2): REQ-SA-044 «Вердикт хоста по свидетельствам роли» и 23.1 «Список ролей в `review`». §14 не редактируется, v1 остаётся действительной; `host-decision.md` становится зеркалом §14 **и** §23 (тест сравнивает оба раздела) | RULES: спецификация прежде реализации; прецедент §19–§22; SA-019…021 в `release-control.json` пинят v1 | `shasum -a 256 -c` всех списков `acceptance/` OK; `git diff codex/bootstrap -- docs/tool-spec.md` — шапка и §23; `TestHostDecisionMirror` с двумя разделами |
| DECISION v2 — отдельный строгий тип `ReviewDecisionV2{version:2, review_id, run_id, snapshot_id, basis_sha256, reviewer, summary, verdicts, counts, limitations}`; `verdicts[]: {requirement_id, specification, implementation, assertion, concur, statement, limitations}`, `concur ∈ mapper|redteam|both`; `counts: {supported, contradicted, implementation_unknown, relevant, weak, contradicts, missing, assertion_unknown, ambiguous}` — все ключи обязательны (requiredJSON), бинарник пересчитывает по verdicts и отклоняет расхождение с именем поля и обоими числами. Свидетельства хоста = цитаты (spec/code/tests) названных ролей из текущих результатов run для этой нормы (объединение при `both`, дедуп по значению); собственных цитат в v2 нет — для них v1. Разбор по `version`: первым читается только поле `version` (лёгкая структура `{version int}` без строгости), затем строгий разбор v1 или v2; иная версия — отказ | Хост тратил ≈15 мин на чтение оценок и копирование цитат в 823 KB; арифметика summary ошиблась; `requiredJSON` не допускает необязательных полей | `TestReviewV2`: v2 с concur both/mapper/redteam принят, журнал хранит точные байты, `latest.assessments[i].spec/code/tests` == объединение цитат названных ролей; `counts` с ошибкой → отказ «counts.relevant: 41 ≠ 39»; неизвестный concur / пропущенная норма / повторный ID / execution status — отказы; v1 из существующих тестов без изменений |
| Правила вердикта — те же, что у Assessment §7 без цитат: enum состояний, принятая ambiguous-норма требует `specification: ambiguous` (`checkAssessment`, `main.go:767`), statement непуст, ровно одна запись на норму, limitations без пустых строк; `concur` требует, чтобы названная роль(и) имела текущий результат по scope нормы (запись возможна только при полной доставке — `pending == 0` — поэтому при apply всегда есть; при replay журнала проверяется только форма) | REQ-SA-019/020; `validateResult` пропускает цитаты только для host v2 | `TestReviewV2`: accepted ambiguous-норма с `specification: clear` → отказ; `concur: redteam` → в отчёте только цитаты redteam |
| Нормализация для отчёта: `summarizeReviews` строит `Latest *ReviewDecision` (v1-форма) из v2 — Assessments с принятыми цитатами из **текущего** state; `ReviewSummary` получает `Concur map[string]string` (omitempty; requirement_id → concur) и `Form string` (`"assessments"`/`"verdicts"`); отчёт/HTML: у host-оценки подпись «по mapper/redteam/обеим ролям» (`RequirementNavigation.HostConcur`). Для outdated-решения принятые цитаты берутся из текущих результатов, а состояние `outdated` уже помечено (REQ-SA-020) — синтез не выдаётся за цитаты момента решения, что записано в §23 | REQ-SA-021 «итог рядом со свидетельствами»: свидетельства — ссылки на результаты ролей, зафиксированные основанием §14; отчёт не меняет исходные роли | `TestReviewV2`: `report.json.host_review.concur`, `navigation.host` с цитатами роли, HTML содержит «по mapper»; после `retry` состояние `outdated`, отчёт остаётся читаемым |
| Точка 6: `ReviewContext.Roles []RoleEntry` (`task_id, role, scope, attempt, submitted, raw_sha256`) — производный список из `state.Entries`; `entries` не меняются | Хосту не хватало task_id/role/scope на верхнем уровне | `TestReviewV2/roles`: список из 2N записей с `submitted` по факту результата |
| Launcher: `host-decision.md` (зеркало §14+§23) через `TestHostDecisionMirror`; `SKILL.md` шаг 6 — «prefer DECISION version 2 (verdicts with `concur` and `counts`); use version 1 only when the host cites evidence the roles did not» | Поставляемая инструкция должна вести хост к лёгкой форме | `TestSkillFiles`, `rg -n 'version 2' skills/spec-audit/SKILL.md` |
| Поставка: релиз `v0.1.8`, `update` хоста, `skill update` в пилоте/синтетике; read-only проверка на пилоте — `review CONFIG RUN_ID` (список ролей) на run `finance-2026-09-17-02`; v2 в живом цикле — следующий чистый прогон | §18 | `gh release view v0.1.8` — 5 ассетов; `git status` пилота неизменен |

### Поддерживаемые комбинации
| Комбинация | Вход / валидация | Состояние | Результат | Проверка |
| --- | --- | --- | --- | --- |
| DECISION v1 (полная форма) | как сейчас | журнал | без изменений | существующие `TestReview*`, `release-control.json` |
| v2, `concur: both`, counts верны, доставка полная, basis текущий | `validateReviewV2` | запись | `accepted:true`, `review_state: current`; отчёт: host-цитаты = объединение ролей, `concur` в `host_review` | `TestReviewV2` |
| v2, `concur: mapper` / `redteam` | — | запись | только цитаты названной роли | `TestReviewV2` |
| v2, counts расходятся с verdicts | пересчёт | без записи | отказ «counts.<поле>: <указано> ≠ <по verdicts>» | `TestReviewV2` |
| v2, недопустимый concur / пропущена норма / повторный ID / пустой statement / принятая ambiguous с `clear` | форма | без записи | отказы с текстами | `TestReviewV2` |
| v2, stale basis / незавершённая доставка / повтор review_id | как v1 | без записи | прежние отказы | `TestReviewV2` |
| версия ≠ 1/2 | разбор `version` | без записи | «неверная версия/идентичность host review» | `TestReviewV2` |
| журнал с записями v1 и v2 вперемешку | `readReviews` | — | оба вида валидируются и суммируются; `history[]` для v2 без потерь | `TestReviewV2` |
| после `retry` одной роли | basis | — | `outdated`; отчёт показывает решение v2 с текущими цитатами роли и пометкой outdated | `TestReviewV2` |
| `review CONFIG RUN_ID` без DECISION | — | — | `roles[]` с task_id/role/scope/attempt/submitted/raw_sha256; `entries` прежние | `TestReviewV2/roles` |

Представительные реальные артефакты: `acceptance/release-control.json` (frozen ожидания SA-019…021 для v1), `acceptance/declared-v01` (исторический run без журнала — `review` read-mode с `roles[]`), пилот `finance-2026-09-17-02` (read-only `review CONFIG RUN_ID` после релиза).

## Commit Plan
- **Commit 1** (after tasks 1-2): "feat(review): §23 — DECISION version 2 с concur и counts"
- **Commit 2** (after tasks 3-4): "feat(review): список ролей в review, concur в отчёте и HTML"
- **Commit 3** (after task 5): "docs(launcher): host-decision.md с §23, SKILL.md о version 2, README/ROADMAP/adoption"
- **Commit 4** (after task 6): "docs(dist): релиз v0.1.8"

## Tasks

### Phase 1: Контракт и валидация v2
- [x] Task 1: §23 tool-spec «Облегчённая форма host DECISION (version 2) и список ролей» (REQ-SA-044, 23.1), редакция 1.2; зеркало `host-decision.md`; AGENTS/ARCHITECTURE
  - Deliverable: новый раздел перед «См. также», по профилю §4. Причина редакции 1.2 — третий прогон (823 KB DECISION, ≈15 мин чтения 102 оценок, повторная запись из-за ошибки в summary). REQ-SA-044 «Вердикт хоста по свидетельствам роли»: Условие — хост подаёт DECISION с `version: 2`; Требование — форма `verdicts[]`/`counts`, `concur ∈ mapper|redteam|both`, свидетельства хоста = цитаты названных ролей из текущих результатов run (объединение при both), собственных цитат нет — для них v1; те же правила состояний/ambiguity/полноты, что у §7 без цитат; counts пересчитываются бинарником, расхождение — отказ с именем поля; основание, CAS, журнал, лимиты, состояния — по §14 без изменений; при outdated отчёт показывает решение с текущими цитатами ролей и пометкой outdated, не выдавая их за цитаты момента решения; журнал хранит точные байты и может содержать v1 и v2. Проверка — комбинации плана. Источник/Обоснование/Зависимость (SA-019…021, §7, §14). 23.1: `roles[]` в ответе `review CONFIG RUN_ID`. Шапка: редакция 1.2. `skills/spec-audit/references/host-decision.md`: заголовок «Mirror of sections 14 and 23…», тело = §14 + §23 байт-в-байт; `TestHostDecisionMirror` сравнивает оба раздела (§14 между `## 14.`/`## 15.`, §23 между `## 23.`/`## См. также`). AGENTS.md — строка §23, «§13–23»; ARCHITECTURE.md — review.go (v1/v2, нормализация).
  - Files: `docs/tool-spec.md`, `skills/spec-audit/references/host-decision.md`, `tool/skill_test.go`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
  - Verify: `mdq tree docs/tool-spec.md` показывает §23; `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; `shasum -a 256 -c acceptance/*.sha256 acceptance/legacy/baseline.sha256` OK; `rg -n 'smsplace|FinanceSystem' docs/tool-spec.md` без новых вхождений.
  - Logging: документ фиксирует: `review` — INFO «согласование сохранено» дополняется {form: verdicts|assessments}; отказ counts — ошибка с именем поля; DEBUG «review: свидетельства по concur» {requirement_id, concur, citations}.
  - REQ: REQ-SA-019, REQ-SA-020, REQ-SA-021, §7, §14

- [x] Task 2: типы и валидация DECISION v2, разбор по версии, запись в журнал
  - Deliverable: `tool/review.go`: типы `ReviewVerdict{RequirementID, Specification, Implementation, Assertion, Concur, Statement, Limitations}`, `ReviewCounts{Supported, Contradicted, ImplementationUnknown, Relevant, Weak, Contradicts, Missing, AssertionUnknown, Ambiguous}` (json snake_case), `ReviewDecisionV2{Version, ReviewID, RunID, SnapshotID, BasisSHA256, Reviewer, Summary, Verdicts, Counts, Limitations}`; `reviewVersion(data) (int, error)` — нестрогий разбор только `version`; `validateReviewV2(data, runID, m, state) (ReviewDecisionV2, error)`: identity как v1; `verdicts` ровно по одному на каждую `m.Requirements` (REQ-SA-005-подобно), enum состояний, принятая ambiguous → `specification: ambiguous`, statement непуст, limitations без пустых, `concur` ∈ mapper|redteam|both; counts пересчитываются (`countVerdicts(verdicts)`) и сравниваются по полям — первое расхождение → `fmt.Errorf("counts.%s: %d ≠ %d", field, given, computed)`; общая часть валидации (identity, summary/reviewer) выносится в helper для v1/v2. `validateReview` становится диспетчером: `version 1` → прежний путь, `2` → v2 (при `checkSources=false`/replay — только форма), иначе «неверная версия/идентичность host review». Внутреннее представление после разбора — `reviewRecord{ID, Reviewer, Basis, Summary, Form, Assessments []Assessment, Concur map[string]string, Limitations}`, где для v2 `Assessments` синтезируются `adoptEvidence(verdicts, state)` (цитаты названных ролей из `state.Entries` по scope нормы; объединение с дедупом по значению; `both` — mapper затем redteam); `submitReview` при v2 требует полной доставки (уже есть) и что у названных ролей есть результат для нормы. Журнал — точные байты (без изменений), `readReviews` валидирует записи обоих видов. Тесты `tool/review_test.go` `TestReviewV2`: fixture с двумя ролями и результатами (по образцу `TestReviewLifecycle`); v2 `both` принят → `accepted:true`, `review_state: current`, файл журнала содержит точные байты v2; `concur: mapper` → в `reviewContext().Latest.assessments[i]` только цитаты mapper; counts с ошибкой → отказ с текстом `counts.relevant`; неизвестный concur, пропущенная норма, повторный ID, пустой statement, ambiguous-норма с `clear` (accepted-режим — можно синтетически через `Requirement.Accepted.Clarity`), `version: 3`, stale basis → отказы, журнал без изменений; журнал v1+v2 читается, `history` — 2 записи; `retry` роли → `outdated`.
  - Files: `tool/review.go`, `tool/review_test.go`
  - Depends: 1
  - Verify: `go test ./tool -run 'TestReview|TestHostDecisionMirror' -count=1`; `go vet ./tool`.
  - Logging: INFO «согласование сохранено» {run_id, review_id, requirements, form}; DEBUG «review: свидетельства по concur» {requirement_id, concur, spec, code, tests} (числа цитат, не содержимое); ошибки — тексты с именем поля, без цитат.
  - REQ: REQ-SA-044, REQ-SA-019, REQ-SA-020, §14
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Отчёт, HTML и список ролей
- [x] Task 3: `roles[]` в `review CONFIG RUN_ID`; `concur`/`form` в `ReviewSummary`
  - Deliverable: `tool/review.go`: `type RoleEntry struct{TaskID, Role, Scope string; Attempt int; Submitted bool; RawSHA256 string}` (json snake_case, `raw_sha256,omitempty`); `ReviewContext.Roles []RoleEntry json:"roles"` — из `state.Entries` в порядке entries; `ReviewSummary` получает `Form string json:"form,omitempty"` (`assessments`|`verdicts` для latest) и `Concur map[string]string json:"concur,omitempty"`; `summarizeReviews` заполняет их из нормализованной записи; `ReviewHistory` без изменений. Тесты: `TestReviewV2/roles` — `review CONFIG RUN_ID` до и после submit одной роли: `roles[i].submitted` меняется, `entries` прежние; `form`/`concur` после v2.
  - Files: `tool/review.go`, `tool/review_test.go`
  - Depends: 2
  - Verify: `go test ./tool -run 'TestReview' -count=1`; `go vet ./tool`; `./bin/spec-audit review acceptance/config.yaml blind-v1` на копии `acceptance/declared-v01` → `roles` с двумя записями `submitted:true`.
  - Logging: DEBUG «review: контекст» {roles, submitted}.
  - REQ: 23.1, REQ-SA-013

- [x] Task 4: отчёт и HTML — подпись «по mapper/redteam/обеим ролям» у решения хоста
  - Deliverable: `tool/report.go`: `RequirementNavigation.HostConcur string json:"host_concur,omitempty"` из `report.HostReview.Concur[req.ID]`; `assess(...)` для host без изменений (синтезированные цитаты дают ссылки). `tool/report.html`: в карточке «Решение хоста» — `{{if .Navigation.HostConcur}}<small class="metric">свидетельства: {{…}}</small>{{end}}` с подписью «по mapper» / «по redteam» / «по обеим ролям»; в блоке «Согласование хоста» — форма решения (`verdicts`/`assessments`). Тесты: `TestReviewV2/report` — `report.json`: `navigation.host_concur == "both"`, host-цитаты присутствуют и ведут к файлам (`navigation.files[].evidence[].links` с origin `host · …`); HTML содержит «по обеим ролям»; для v1-решения `host_concur` отсутствует.
  - Files: `tool/report.go`, `tool/report.html`, `tool/review_test.go`
  - Depends: 3
  - Verify: `go test ./tool -run 'TestReview|TestReportProjection|TestNavigation|TestHTML' -count=1`; `go vet ./tool`; ручной просмотр HTML fixture (UI-check Playwright — только если Chromium доступен).
  - Logging: DEBUG «карта отчёта: решение хоста» {form, concur_count}.
  - REQ: REQ-SA-021, REQ-SA-025, 15.1
<!-- Commit checkpoint: tasks 3-4 -->

### Phase 3: Документация и поставка
- [ ] Task 5: launcher SKILL.md; README, ROADMAP, документ применения
  - Deliverable: `skills/spec-audit/SKILL.md` шаг 6: «Save a DECISION `version: 2`: one verdict per norm with `concur: mapper|redteam|both` naming whose current evidence the host adopts, the host's statement (including disagreements), and `counts` the binary recomputes; use `version: 1` (full assessments with citations) only when the host cites evidence the roles did not. Read [the host decision contract](references/host-decision.md) — sections 14 and 23»; ограничения `TestSkillFiles` (без `#<цифры>`, `smsplace`, `/Users/`). README: «Первый запуск»/review-абзац — v2 и `roles[]`; «Приёмка и ограничения» — строка §23 (реализовано; регрессии; не проверено до релиза: v2 в живом цикле). ROADMAP: «Уроки третьего прогона» — 5–6 `[x]` с датой и свидетельствами, 7 остаётся `[ ]` (вне инструмента); строка таблицы этапов. docs/smsplace-adoption.md «Обычный рабочий цикл» п.5 — DECISION v2 в следующем цикле.
  - Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`, `docs/smsplace-adoption.md`
  - Depends: 2, 4
  - Verify: `go test ./tool -run 'TestSkillFiles|TestHostDecisionMirror|TestSkill' -count=1`; `rg -n 'version: 2|verdicts' skills/spec-audit/SKILL.md README.md`; `shasum -a 256 -c acceptance/*.sha256` OK; полный `go test ./tool`, `go vet ./tool`, `CGO_ENABLED=0 go build -o bin/spec-audit ./tool`.
  - Logging: нет нового.
  - REQ: REQ-SA-037, REQ-SA-044, 23.1

- [ ] Task 6: релиз `v0.1.8`, обновление хоста/пилота/синтетики, read-only `review` на пилоте
  - Deliverable: merge в `codex/bootstrap` (по подтверждению владельца), тег `v0.1.8`, approve deployment; `spec-audit update` → 0.1.8; `skill update --host both` в `spec-audit-synthetic` и пилоте (коммиты — по запросу); read-only `review CONFIG finance-2026-09-17-02` на пилоте → `roles[]` из 6 записей `submitted:true`, `host_review.state`, `form: assessments` (v1); журнал и `git status` пилота неизменны; v2 в живом цикле — следующий чистый прогон `sa-clean`. README «Приёмка и ограничения» — строка релиза 0.1.8.
  - Files: `README.md`; (пилот и синтетика — вне репозитория)
  - Depends: 5
  - Verify: `gh release view v0.1.8` — 5 ассетов; `spec-audit version` → 0.1.8; `spec-audit update` → `updated:false`; `git -C <pilot> status --porcelain` пуст до/после (кроме tracked-копии skill до коммита).
  - Logging: нет нового.
  - REQ: REQ-SA-035, REQ-SA-037, REQ-SA-044
<!-- Commit checkpoint: tasks 5-6 -->

## Открытые вопросы (не блокируют T1–T6)
1. До T2: состав `counts` — девять полей по состояниям §7 (implementation ×3, assertion ×5, ambiguous); при желании владельца добавить `total`.
2. До T4: подпись концентрации в HTML — «по mapper» / «по redteam» / «по обеим ролям»; альтернатива — показывать обе оценки ролей рядом без объединения цитат.
3. После T6: v2 в живом цикле пилота — следующий чистый прогон; если хосту снова понадобятся собственные цитаты в отдельных нормах, рассмотреть смешанную форму (v3) отдельно.

## Риски
- Синтез цитат хоста из текущих результатов ролей: для outdated-решения они могут не совпадать с цитатами момента решения — §23 честно называет это и отчёт помечает outdated; основание §14 по-прежнему привязывает решение к state момента записи.
- Второй тип DECISION удваивает поверхность валидации; общая часть (identity, полнота ID, enum, ambiguity) выносится в helper, тексты ошибок v1 не меняются (пинов нет, но `release-control.json` ожидания должны остаться зелёными).
- `host-decision.md` растёт (§14 + §23) — это поставляемый файл skill; `TestSkillFiles` продолжает проверять отсутствие путей/имён пилота.
- Хост может назвать `concur: both` при расходящихся оценках ролей — вердикт всё равно один (хоста); объединённые цитаты не означают согласие ролей; отчёт сохраняет исходные оценки ролей рядом.
