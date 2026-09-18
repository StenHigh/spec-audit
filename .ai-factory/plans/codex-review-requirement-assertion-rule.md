# Implementation Plan: Чтение решения по одной норме, правило assertion при relevant+contradicts, симметричный ответ `review … DECISION` (§29)

Branch: codex/review-requirement-assertion-rule
Created: 2026-09-18

## Original Request
Уроки седьмого прогона 2026-09-18, пункты 2 + 4 + 1 (решение владельца 2026-09-18: сначала эти три, затем 5 + 3 + 6 мелочами; следующий прогон — новый раздел ТЗ): (2) чтение по одной норме — `review CONFIG RUN_ID REQ-ID`: statement, limitations и цитаты обеих ролей плюс previous_host и текущий вердикт хоста — вместо jq по 2 MB `review.json` (четвёртый аргумент вида `REQ-XXX-NNN` читается как ID нормы, read-only; иначе — путь DECISION как сейчас); (4) правило assertion нормы при одновременных relevant-тестах штатного пути и contradicts-тесте достижимой ветки — хост применил worst-wins; записать в контракт решения §14 (зеркало host-decision.md); (1) ответ `review CONFIG RUN_ID DECISION` повторяет `version` и `own_citations` (асимметрия с `validate … host`). Ограничения: frozen §1–10; §14 получает одно предложение; новый §29 (редакция 1.9); никаких путей/имён пилота.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки седьмого прогона 2026-09-18 (решение владельца по каждому пункту)"
Rationale: решение владельца 2026-09-18 — 2 + 4 + 1 первыми (последний jq-скрипт хоста, единственный пробел контракта решения, тривиальная симметрия), затем 5 + 3 + 6; следующий чистый прогон — на новом разделе ТЗ.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 (`review CONFIG RUN_ID [DECISION]` — позиционный; четвёртый аргумент-ID нормы — расширение, не изменение: путь DECISION никогда не имеет вида `REQ-XXX-NNN`) > §14 (контракт решения; правило assertion добавляется одним предложением; зеркало) > §23–§27 (`outcomes[]`, `previous_host`, `only_here`, `validate … host` и его ответ) > RULES/rules-base > ROADMAP > отчёт `sa-clean` run 18-02.

Разведка по коду: `execute` — `argc["review"] = -2` (3 или 4 аргумента), `readOnly` при 3 аргументах (`tool/main.go:1101-1105`), `case "review"` (`:1264-1268`); `reviewContext(reports, run, runID, m, state, fresh)` строит `ReviewContext{…, Requirements, Roles, Outcomes, Entries, Executions}`; `submitReview` возвращает `{accepted, duplicate, review_id, review_state[, latest_review_id]}`; `stagedReview.record` знает `Version`, `Own`, `Assessments`. ID норм: declared `REQ-DEMO-001`, accepted `REQ-AI-%03d` (`tool/accepted.go:202`) — шаблон `^REQ-[A-Z0-9]+-[0-9]+$`.

### Решения
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| §29 (редакция 1.9): 29.1 `review CONFIG RUN_ID REQ-ID`; 29.2 симметрия ответа `review … DECISION`; §14 — предложение «Если по норме одновременно есть relevant-тесты штатного пути и contradicts-тест достижимой ветки, assertion нормы — contradicts: зелёный тест, закрепляющий противоречащее норме поведение, важнее подтверждающих тестов; weak и missing contradicts не отменяют.»; зеркало `host-decision.md` регенерируется (§14+§23+§25) | Владелец 2026-09-18 | `TestHostDecisionMirror`; хэши acceptance |
| CLI: `requirementIDRE = ^REQ-[A-Z0-9]+-[0-9]+$`; `readOnly` для `review` с 4 аргументами, если `args[3]` соответствует шаблону; `case "review"`: при совпадении — `requirementView(reports, run, runID, m, state, fresh, id)`; иначе — `submitReview`. Ответ `RequirementView{run_id, snapshot_id, freshness, requirement (Requirement), outcome (Outcome — roles с limitations/citations/only_here, agree, previous_host), roles: {<role>: {task_id, attempt, statement, limitations, spec[], code[], tests[]}}, host: *{review_id, state, form, specification, implementation, assertion, statement, own_citations bool, spec[], code[], tests[]} (из latest), executions по test_id нормы}`; неизвестный ID — отказ «неизвестный requirement_id» | jq по 2 MB на каждую норму | `TestReviewRequirementView`: после submit и решения v3 → `roles.mapper.statement`, цитаты обеих, `outcome.agree`, `host.own_citations`; до решения — `host` nil; неизвестный ID — отказ; `review … DECISION` с путём как прежде; `tool-versions.json` без записи |
| `submitReview`: ответ дополняется `version`, `requirements`, `own_citations` (как у `validate … host`) — и в duplicate-ветке | Асимметрия с validate | `TestReviewV3`/`TestReviewValidateHost`: ключи в ответе `review` |
| Docs: SKILL.md шаг 6 — «read one norm with `BINARY review CONFIG RUN_ID REQ-ID` instead of digging in `entries`»; правило assertion в SKILL.md одной фразой; README, ROADMAP (седьмой прогон 2, 4, 1 `[x]`), adoption; релиз `v0.1.16`, update, skill update + коммиты | §18 | `gh release view v0.1.16` |

## Tasks

### Phase 1: Контракт и код
- [x] Task 1: §29 tool-spec, предложение в §14, зеркало; AGENTS/ARCHITECTURE
  - Deliverable: шапка «1.9 …»; §14 — предложение о contradicts (после текста о состояниях решения); §29 перед «См. также»: 29.1 чтение по норме (форма ответа, шаблон ID, read-only, отказ на неизвестный ID, путь DECISION не меняется); 29.2 ответ `review … DECISION` содержит `version`, `requirements`, `own_citations`. Зеркало регенерируется. AGENTS — строка §29, «§13–29»; ARCHITECTURE — review.go (`requirementView`).
  - Files: `docs/tool-spec.md`, `skills/spec-audit/references/host-decision.md`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
  - Verify: `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; хэши acceptance OK.
  - Logging: DEBUG «review: норма» {run_id, requirement_id, roles, host}.
  - REQ: §14, REQ-SA-044, REQ-SA-047

- [x] Task 2: `requirementView`, CLI-диспетчер, симметрия ответа; тесты
  - Deliverable: `tool/review.go`: типы `RoleView{TaskID, Attempt, Statement, Limitations, Spec, Code, Tests}`, `HostView{ReviewID, State, Form, Specification, Implementation, Assertion, Statement, OwnCitations bool, Spec, Code, Tests}`, `RequirementView{RunID, SnapshotID, Freshness, Requirement, Outcome, Roles map[string]RoleView, Host *HostView, Executions []TestExecution?}` — executions: список receipts, где встречается test_id из цитат нормы (`assessmentExecutions` в report.go принимает Assessment и receipts — переиспользовать по оценкам ролей и хоста); `requirementView(...)`: `reviewContext` → найти `Outcome` по ID → роли из `state.Entries` → `Host` из `summary.Latest` (assessment по ID, `Own[id]`); неизвестный ID — `errors.New("неизвестный requirement_id")`. `tool/main.go`: `requirementIDRE`, `readOnly` расширен, `case "review"` ветвится. `submitReview`: `version`, `requirements`, `own_citations` в обоих ответах. Тесты: `TestReviewRequirementView` (`tool/review_test.go`), правки ожиданий в `TestReviewValidateHost` (ключи `review`).
  - Files: `tool/review.go`, `tool/main.go`, `tool/review_test.go`
  - Depends: 1
  - Verify: `go test ./tool -run 'TestReview' -count=1`; `go vet ./tool`; на копии журналов пилота — `review … REQ-AI-023`.
  - Logging: DEBUG «review: норма» {run_id, requirement_id, roles, host}.
  - REQ: 29.1, 29.2
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Документация и поставка
- [x] Task 3: SKILL.md, README, ROADMAP, adoption
  - Deliverable: SKILL.md шаг 6 — чтение по норме и правило contradicts; README «Первый запуск» и «Приёмка и ограничения» (строка §29); ROADMAP — седьмой прогон 2, 4, 1 `[x]`, строка таблицы; adoption — сноска о 0.1.16.
  - Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`, `docs/smsplace-adoption.md`
  - Depends: 2
  - Verify: `go test ./tool -run 'TestSkillFiles|TestHostDecisionMirror' -count=1`; полный `go test ./tool`, `go vet`, сборка.
  - Logging: нет нового.
  - REQ: REQ-SA-037

- [ ] Task 4: релиз `v0.1.16`, update хоста/пилота/синтетики (коммиты skill), read-only на пилоте
  - Deliverable: merge, тег, approve; `update`; `skill update` + коммиты; read-only на пилоте: `review CONFIG finance-2026-09-18-02 REQ-AI-023` → роли, previous_host, host с own_citations; журнал прежний. README — строка релиза.
  - Files: `README.md`
  - Depends: 3
  - Verify: `gh release view v0.1.16` — 5 ассетов; `spec-audit version` → 0.1.16.
  - Logging: нет нового.
  - REQ: REQ-SA-035, REQ-SA-037

## Риски
- Расширение `readOnly` по шаблону аргумента — путь DECISION, случайно совпавший с `REQ-XXX-NNN`, недостижим (у путей есть расширение или разделители).
- Изменение ответа `review … DECISION` добавляет ключи; существующие потребители (SKILL.md, тесты) читают `accepted`/`review_state` — совместимо.
