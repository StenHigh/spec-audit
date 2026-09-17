# Implementation Plan: Списки всегда `[]`, протокол mapper по веткам, пустой statement при совпадении с ролями (§28)

Branch: codex/lists-mapper-statement
Created: 2026-09-18

## Original Request
Уроки шестого прогона 2026-09-18, пункты 4 + 7, и пункт 5 уроков четвёртого прогона (решение владельца 2026-09-18: мелочи одним планом после 1+2+3): (4) `advisories` (index/check/reconcile) и `sdk` (prepare) — всегда присутствующие списки `[]`, а не отсутствие ключа; (7) протокол mapper: прямое требование «противоречащая ветка — contradicted и у mapper; systematic ≠ confirming» (mapper не дошёл до 023 ни в одном из шести прогонов, по 060 понизил до limitation вопреки 0.1.13); (5 четвёртого) statement при `concur: both` без расхождений — 34 из 51 statement в run 04–06 были шаблонными: пустой statement допустим только при concur both и вердикте, равном оценкам обеих ролей (бинарник проверяет при записи), в отчёте — стандартный текст; любое расхождение — statement обязателен (решение владельца 2026-09-18). Ограничения: frozen §1–10; §23 получает уточнение правила statement (зеркало host-decision.md), новый §28; никаких путей/имён пилота.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки шестого прогона 2026-09-18 (решение владельца по каждому пункту)"
Rationale: решение владельца 2026-09-18 — после 1+2+3 закрыть мелочи 4 и 7 шестого прогона и пункт 5 четвёртого одним планом; 8 — отдельно после следующего прогона; 9 — только с новой версией формы.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 (ответы `index`/`prepare` — поля добавляются, не меняются) > §14/§23/§25 (формы решения; §23 Проверка называет «пустой statement» отказом — уточняется владельцем: отказ при расхождении с ролью) > §19.2/§26.4 (протокол роли) > §20 (`advisories` — список подсказок) > [RULES.md](../RULES.md) > [rules/base.md](../rules/base.md) > ROADMAP > отчёт `sa-clean` run 18-01.

Разведка по коду: `advisories` пишется только при непустом списке — `tool/specs.go:240`, `tool/accepted.go:530,723,729`; `TaskBatch.SDK []SDKRecord json:"sdk,omitempty"` (`tool/main.go:166`), `sdkRecordsFor` возвращает `nil` при пустом (`tool/php.go:773-776`); `State.SDK` (`:156`) — файл состояния, не трогается. `checkVerdict` (`tool/review.go:~758`) отказывает «пустой statement вердикта» для всех версий; `validateVerdicts` при `state != nil` знает оценки ролей через `adoptEvidence`/`state.Entries` — там можно проверить совпадение; при replay (`state == nil`) форма принимается структурно. Протокол: `references/protocol.txt` строка 3 — «Mapper: систематически оцени …»; строка 11 — общее правило 0.1.13 о ветках.

### Решения
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| §28 (редакция 1.8): 28.1 списки `advisories` (`index`, `check`, `reconcile` без RAW) и `sdk` (`prepare`/`tasks`) всегда присутствуют (`[]`); 28.2 протокол mapper — прямое требование; 28.3 пустой statement при совпадении. §23: фраза Проверки «пустой statement» → «пустой statement при расхождении с оценкой роли или при `concur` ≠ both»; Требование §23 дополняется одним предложением о пустом statement (зеркало `host-decision.md` обновляется байт-в-байт) | Владелец 2026-09-18 | `TestHostDecisionMirror`; хэши acceptance |
| Код: `index["advisories"]`/`view["advisories"]` — всегда (пустой `[]string{}`); `TaskBatch.SDK json:"sdk"` + `sdkRecordsFor` возвращает `[]SDKRecord{}`; `checkVerdict` не отказывает на пустой statement для версий ≥ 2 (v1 — как прежде через `validateResult`); `validateVerdicts` при `state != nil`: пустой statement → требуется `concur == "both"`, обе роли с результатом по норме и равенство трёх состояний с обеими — иначе отказ «пустой statement допустим только при concur both и совпадении с оценками обеих ролей»; в синтезированной оценке `Statement = "Совпадает с оценками обеих ролей."` (константа `agreedStatement`); при replay — форма | Минимальный контракт | `TestReviewV3`/новый `TestReviewAgreedStatement`: пустой statement при both и совпадении → принято, отчёт показывает стандартный текст; пустой при расхождении с одной ролью → отказ; пустой при `concur: mapper` → отказ; `TestIndexFreshness`/`TestAnchorAdvisories`: `advisories == []` без подсказок; `TestPrepareDispatch`: `sdk == []` в ответе `prepare` |
| `protocol.txt` строка 3: после «Mapper: систематически оцени каждое назначенное требование.» — «Систематически не значит подтверждающе: если ты нашёл противоречие в любой достижимой ветке, ставь implementation=contradicted с цитатой этой ветки — как и redteam; не понижай его до limitation при supported.» | mapper 0/6 по 023, понижение по 060 | `TestSkillFiles`; текст в `references/protocol.txt` |
| Docs: SKILL.md шаг 6 — «leave `statement` empty only for a norm where `concur: both` and your verdict equals both roles' states; the binary refuses an empty statement anywhere else»; README, ROADMAP (4, 7 шестого и 5 четвёртого `[x]`), adoption; релиз `v0.1.15`, update, skill update + коммиты | §18 | `gh release view v0.1.15` |

## Tasks

### Phase 1: Контракт и код
- [x] Task 1: §28 tool-spec, уточнение §23, зеркало; AGENTS/ARCHITECTURE
  - Deliverable: шапка «1.8 …»; §23 Требование: после «…`concur`, `statement`, `limitations` с теми же допустимыми состояниями …, но без цитат.» — «`statement` может быть пустым только когда `concur: both` и вердикт совпадает с оценками обеих ролей по specification/implementation/assertion — тогда отчёт показывает стандартный текст «Совпадает с оценками обеих ролей.»; при любом расхождении или ином `concur` statement обязателен.»; Проверка §23: «пустой statement» → «пустой statement при расхождении с оценкой роли или при `concur` ≠ both». §28 перед «См. также»: 28.1 списки всегда присутствуют; 28.2 протокол mapper (текст фразы); 28.3 пустой statement при совпадении (правило, replay — форма). Зеркало `host-decision.md` — §14+§23+§25 байт-в-байт. AGENTS — строка §28, «§13–28»; ARCHITECTURE — review.go (agreedStatement).
  - Files: `docs/tool-spec.md`, `skills/spec-audit/references/host-decision.md`, `AGENTS.md`, `.ai-factory/ARCHITECTURE.md`
  - Verify: `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; `shasum -a 256 -c acceptance/*.sha256 acceptance/legacy/baseline.sha256` OK.
  - Logging: документ фиксирует DEBUG «review: стандартный statement» {requirement_id}.
  - REQ: REQ-SA-044, REQ-SA-046, §20, §26.4

- [x] Task 2: код и тесты — списки `[]`, `sdk`, пустой statement, protocol.txt
  - Deliverable: `tool/specs.go`/`tool/accepted.go`: `advisories` всегда в ответах `index`, `check` (оба режима), `reconcile` без RAW; `anchorAdvisories`/`scopeAdvisories` уже возвращают `[]string{}`. `tool/main.go`: `TaskBatch.SDK json:"sdk"`; `tool/php.go` `sdkRecordsFor` — `[]SDKRecord{}` при пустом. `tool/review.go`: `checkVerdict` — пустой statement не отказ (проверка перенесена); `validateVerdicts`: при `state != nil` для пустого `strings.TrimSpace(statement)`: собрать оценки обеих ролей по норме из `state.Entries`; условие `concur == both && обе есть && состояния равны` → `assessment.Statement = agreedStatement`, DEBUG «review: стандартный statement»; иначе `fmt.Errorf("%s: пустой statement допустим только при concur both и совпадении с оценками обеих ролей", id)`; при `state == nil` — принять форму. `skills/spec-audit/references/protocol.txt` строка 3 — фраза из плана. Тесты: `TestReviewAgreedStatement` (`tool/review_test.go`, по образцу `TestReviewV3`): пустой statement у согласной нормы при both → принято, `latest.assessments[i].statement == agreedStatement`, HTML содержит текст; пустой при расхождении (redteam с другим assertion) → отказ; пустой при `concur: mapper` → отказ; журнал прежний. `TestIndexFreshness`: `index["advisories"]` — пустой массив при отсутствии подсказок; `TestAnchorAdvisories` — `reconcile`/`check` тоже; `TestPrepareDispatch`: в ответе `prepare` `sdk` — `[]` (через json round-trip).
  - Files: `tool/specs.go`, `tool/accepted.go`, `tool/main.go`, `tool/php.go`, `tool/review.go`, `tool/review_test.go`, `tool/specs_test.go`, `tool/main_test.go`, `skills/spec-audit/references/protocol.txt`
  - Depends: 1
  - Verify: `go test ./tool -count=1`; `go vet ./tool`; `./bin/spec-audit index` на acceptance-копии → `advisories: []`.
  - Logging: DEBUG «review: стандартный statement» {requirement_id}.
  - REQ: 28.1–28.3, REQ-SA-044
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Документация и поставка
- [x] Task 3: SKILL.md, README, ROADMAP, adoption
  - Deliverable: SKILL.md шаг 6 — правило пустого statement; шаг 1 — `advisories` всегда список. README «Первый запуск» и «Приёмка и ограничения» (строка §28); ROADMAP — шестой прогон 4, 7 `[x]`, четвёртый 5 `[x]`, строка таблицы; adoption — сноска о 0.1.15.
  - Files: `skills/spec-audit/SKILL.md`, `README.md`, `.ai-factory/ROADMAP.md`, `docs/smsplace-adoption.md`
  - Depends: 2
  - Verify: `go test ./tool -run 'TestSkillFiles|TestHostDecisionMirror' -count=1`; полный `go test ./tool`, `go vet`, сборка.
  - Logging: нет нового.
  - REQ: REQ-SA-037

- [x] Task 4: релиз `v0.1.15`, update хоста/пилота/синтетики (коммиты skill), read-only на пилоте
  - Deliverable: merge, тег, approve; `update`; `skill update` + коммиты; read-only на пилоте: `index CONFIG` → `advisories: []`; `prepare`-ответ не проверяется (создаёт run) — `tasks CONFIG finance-2026-09-18-01` → `sdk: []`; README — строка релиза.
  - Files: `README.md`
  - Depends: 3
  - Verify: `gh release view v0.1.15` — 5 ассетов; `spec-audit version` → 0.1.15.
  - Logging: нет нового.
  - REQ: REQ-SA-035, REQ-SA-037

## Открытые вопросы (не блокируют)
1. `tasks` — тоже возвращает `TaskBatch` → `sdk: []` появится и там; допустимо.

## Риски
- Изменение `json:"sdk"` меняет ответ `prepare`/`tasks` для внешних потребителей (появляется пустой список) — SKILL.md уже читает `sdk` как список.
- Пустой statement при replay без state принимается структурно — как и остальные state-зависимые проверки v2/v3.
