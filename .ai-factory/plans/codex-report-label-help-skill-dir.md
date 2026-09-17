# Implementation Plan: Подпись свидетельств хоста в отчёте, `help`/`--version`, нормализация `skill update --dir` (§24.3–24.5)

Branch: codex/report-label-help-skill-dir
Created: 2026-09-17

## Original Request
Уроки четвёртого прогона 2026-09-17, пункты 6 + 9 + 10 (решение владельца 2026-09-17: мелочи одним планом после 2+3+1): (6) подпись решения хоста в отчёте — при concur: both вердикт хоста может не совпадать с оценкой одной из ролей (run 03, REQ-AI-035: mapper weak, redteam relevant, вердикт weak «по обеим ролям»), поэтому подпись `host_concur` в JSON/HTML должна говорить о свидетельствах, а не о согласии: «свидетельства обеих ролей» / «свидетельства mapper» / «свидетельства redteam» и текст в HTML-карточке и блоке «Согласование хоста», без изменения контракта DECISION (§23); (9) `spec-audit --help`, `-h`, `help` и `--version`, `-V` как синонимы списка команд и `version` — сейчас они отклоняются как неизвестная команда (§18/§1–10: `version` и usage-ошибка уже существуют); (10) `skill update --dir`: сейчас `--dir .spec-audit` и `--dir .spec-audit/skill` дают «каталог skill отсутствует или не установлен этим инструментом; сначала skill install», хотя skill установлен, а ожидается корень проекта — принимать корень проекта, каталог `.spec-audit` и сам каталог `skill` (нормализовать к корню по receipt `.spec-audit-skill.json`), либо сообщать точно «--dir — корень проекта, содержащий .spec-audit/skill»; frozen §1–10 не меняются; §18 (поставка) и §23 расширяются новым разделом или подпунктами §24 (редакция 1.4) по прецеденту; никаких путей/имён пилота (TestSkillFiles). См. ROADMAP «Уроки четвёртого прогона 2026-09-17» пункты 6, 9, 10; docs/smsplace-adoption.md «третий цикл»; tool-spec §18, §23, §24; tool/report.go hostConcurLabel, tool/report.html, tool/main.go usage/skill install|update.

## Settings
- Testing: yes
- Logging: standard
- Docs: yes

## Roadmap Linkage
Milestone: "Уроки четвёртого прогона 2026-09-17 (решение владельца по каждому пункту)"
Rationale: решение владельца 2026-09-17 — после 2+3+1 закрыть мелочи 6, 9, 10 одним планом; 4/5/7/8/11 — после следующего прогона.

## Requirements Reconciliation
Authority: [docs/tool-spec.md](../../docs/tool-spec.md) frozen §1–10 (позиционный CLI, `version`; usage-ошибка при неверных аргументах — не меняются; синонимы добавляются как расширение) > §18 REQ-SA-037 (`skill install|update --dir DIR` — «по умолчанию текущий каталог», раскладка `DIR/.spec-audit/skill/` с receipt `.spec-audit-skill.json`; `--dir` семантически — корень проекта) > §23 REQ-SA-044 (Проверка: «HTML подписывает решение хоста «по mapper / по redteam / по обеим ролям»» — единственное место, где фиксирована формулировка подписи; форма DECISION не меняется; `host-decision.md` — зеркало §14+§23, `TestHostDecisionMirror`) > §24 (прецедент подпунктов уроков прогона) > [RULES.md](../RULES.md) («не превращай … в доказательство»; universality; спецификация прежде реализации) > [rules/base.md](../rules/base.md) (stdout JSON, ошибки с контекстом, UI автономный) > ROADMAP (объём) > отчёт `sa-clean` (подпись «по обеим ролям» у вердикта, которого redteam не давал; `--help`/`--version` отклоняются; `--dir .spec-audit` — ложное «каталог skill отсутствует»).

Разведка по коду: `hostConcurLabel` (`tool/report.go:47-59`) → `RequirementNavigation.HostConcur` (`:321`); HTML — карточка нормы `report.html:58` («Свидетельства решения хоста — {{.Navigation.HostConcur}}: цитаты взяты из текущих результатов названных ролей, вердикт — хоста.») и блок «Согласование хоста» `:77` («Форма решения — вердикты (version 2): свидетельства взяты из результатов ролей, названных в concur.»); ассерты `TestReviewV2` (`tool/review_test.go:545-548`: `"по redteam"`, `"Свидетельства решения хоста — по redteam"`). CLI: `execute` (`tool/main.go:1045-1092`) — `version`/`update` только как одиночные слова; usage-строка команд — литерал в `errors.New` (`:1092`), `skill` → `runSkillCommand` (`tool/skill.go:104-124`: `flag` с `--dir` по умолчанию `""` → `filepath.Abs` → `runSkill(sub, abs, …)`; `readSkillState(dirRoot)` ищет `DIR/.spec-audit/skill` и receipt; `skillNeedInstall` — константа `:87` без пути). Тесты: `TestSkill` (`tool/skill_test.go:195`), `TestSkillCommandFlags` (`:453`), `TestReviewV2`.

### Решения (по итогам разведки)
| Решение | Обоснование | Проверка |
| --- | --- | --- |
| tool-spec редакция 1.4: в §23 Проверка REQ-SA-044 фраза «HTML подписывает решение хоста «по mapper / по redteam / по обеим ролям»» → «отчёт подписывает решение хоста «свидетельства mapper / свидетельства redteam / свидетельства обеих ролей»»; `host-decision.md` — тот же байт (зеркало); §24.3 «Подпись свидетельств хоста» фиксирует причину (run 03, REQ-AI-035: `concur: both` при вердикте, которого одна роль не давала — concur называет принятые свидетельства, не согласие роли с вердиктом), §24.4 «Синонимы `help` и `version`», §24.5 «`--dir` у `skill install|update`». §18 не редактируется — §24.5 уточняет семантику `--dir` как расширение | Владелец 2026-09-17: править фразу §23 + §24.3; RULES «спецификация прежде реализации» | `TestHostDecisionMirror`; `shasum -a 256 -c acceptance/*.sha256` OK; `git diff codex/bootstrap -- docs/tool-spec.md` — шапка, одна фраза §23, §24.3–24.5 |
| `hostConcurLabel`: `mapper` → «свидетельства mapper», `redteam` → «свидетельства redteam», `both` → «свидетельства обеих ролей»; JSON `host_concur` несёт ту же строку (потребители — только HTML и тесты). HTML карточка: «Решение хоста опирается на {{.Navigation.HostConcur}}: цитаты взяты из текущих результатов названных ролей; вердикт — хоста и может расходиться с оценкой роли.»; блок «Согласование хоста»: «…свидетельства взяты из результатов ролей, названных в concur; concur называет принятые свидетельства, не согласие роли с вердиктом.» | Подпись «по обеим ролям» рядом с weak, которого redteam не давал, вводит в заблуждение | `TestReviewV2`: `host_concur == "свидетельства redteam"`, HTML содержит «опирается на свидетельства redteam» и «не согласие роли с вердиктом»; для v1 `host_concur` отсутствует (как сейчас) |
| CLI: `help`, `--help`, `-h` → успешный JSON `{"usage": "<та же строка команд>"}` на stdout (exit 0); `--version`, `-V` → `runVersion()`. Строка команд выносится в константу `usage` и используется и в ошибке `len(args) < 3`, и в `help`; в неё добавляется `help`. Прочие неизвестные команды — прежняя ошибка | stdout JSON — соглашение rules/base; `version` уже JSON; ошибка при `--help` — ложный отказ | `TestHelpVersion`: `execute(["help"])`, `["--help"]`, `["-h"]` → map с `usage`, содержащей «draft CONFIG RUN_ID» и «skill install|update»; `["--version"]`, `["-V"]` == `runVersion()`; `["--bogus"]` — по-прежнему ошибка |
| `skillProjectRoot(abs)`: если `Base(abs) == "skill"` и `Base(Dir(abs)) == ".spec-audit"` → `Dir(Dir(abs))`; если `Base(abs) == ".spec-audit"` → `Dir(abs)`; иначе `abs`. Чисто по пути, без проверки ФС; применяется в `runSkillCommand` до `runSkill`; ответ `dir` — нормализованный корень. `skillNeedInstall` становится `fmt.Errorf("каталог %s отсутствует или не установлен этим инструментом; --dir — корень проекта, содержащий .spec-audit/skill; сначала skill install", filepath.Join(dir, skillInstallDir))` | Хост дважды получил ложное «каталог skill отсутствует»; корень выводится однозначно из имён каталогов | `TestSkillDirNormalization`: `install --dir ROOT`, затем `update --dir ROOT/.spec-audit` и `--dir ROOT/.spec-audit/skill` → `updated:false`/`dir == ROOT`, файлы прежние; `update --dir` пустого каталога → ошибка содержит путь `<dir>/.spec-audit/skill` и «корень проекта»; `--dir` без аргумента (текущий каталог) — как прежде (`TestSkillCommandFlags`) |
| Документы: README «Быстрый старт» — `help`, `--version`, `--dir` = корень проекта (принимаются `.spec-audit` и `skill`); README «Приёмка и ограничения» — строка §24.3–24.5; ROADMAP — 6, 9, 10 `[x]` с датой; SKILL.md не меняется (подпись — только в отчёте); AGENTS.md — строка §24 дополняется | Честный статус | `TestSkillFiles`; `rg -n 'help|--version|корень проекта' README.md` |
| Поставка: релиз `v0.1.10`, `update` хоста, `skill update` в синтетике и пилоте (коммиты — по запросу); на пилоте read-only: `spec-audit help`, `spec-audit --version`, `skill update --dir .spec-audit --host both` из корня пилота → `updated:true`, `dir` = корень; `report` на run 03 в копию? — нет: отчёт пилота пересобирать не нужно, подпись проверяется синтетикой | §18 | `gh release view v0.1.10` — 5 ассетов; `spec-audit version` → 0.1.10; `git status` пилота — только tracked-копия skill |

### Поддерживаемые комбинации
| Комбинация | Вход | Результат | Проверка |
| --- | --- | --- | --- |
| `host_concur` для v2 `mapper`/`redteam`/`both` | отчёт | «свидетельства mapper/redteam/обеих ролей» в JSON и HTML | `TestReviewV2` |
| решение v1 | отчёт | `host_concur` отсутствует, карточка без подписи | `TestReviewV2` (существующий ассерт) |
| `help`/`--help`/`-h` | CLI | JSON `{"usage": …}`, exit 0 | `TestHelpVersion` |
| `--version`/`-V` | CLI | как `version` | `TestHelpVersion` |
| неизвестная команда/флаг | CLI | прежняя ошибка usage | `TestHelpVersion` |
| `--dir ROOT` / `ROOT/.spec-audit` / `ROOT/.spec-audit/skill` | skill update | один и тот же корень, `updated` по состоянию файлов | `TestSkillDirNormalization` |
| `--dir` без skill | skill update | ошибка с полным путём и «корень проекта» | `TestSkillDirNormalization` |

Представительные реальные артефакты: пилот (`skill update --dir .spec-audit --host both` из корня — ранее давал ложный отказ), `spec-audit --help` на хосте.

## Commit Plan
- **Commit 1** (after tasks 1-2): "feat(cli): §24.3–24.5 — подпись свидетельств хоста, help/--version, нормализация skill --dir"
- **Commit 2** (after tasks 3-4): "docs(dist): релиз v0.1.10 — README/ROADMAP, обновление хоста, пилота и синтетики"

## Tasks

### Phase 1: Контракт и код
- [x] Task 1: tool-spec редакция 1.4 — фраза §23, §24.3–24.5; зеркало `host-decision.md`; AGENTS.md
  - Deliverable: шапка «Редакция документа: 1.4 … 1.4 уточняет подпись свидетельств хоста (§23, §24.3) и добавляет §24.4 синонимы `help`/`version`, §24.5 семантику `--dir` у `skill install|update` по решению владельца 2026-09-17». §23 Проверка REQ-SA-044: «HTML подписывает решение хоста «по mapper / по redteam / по обеим ролям»» → «отчёт подписывает решение хоста «свидетельства mapper / свидетельства redteam / свидетельства обеих ролей»». `skills/spec-audit/references/host-decision.md` — тот же байт (зеркало §14+§23). §24.3 «Подпись свидетельств хоста»: causa (run 03, REQ-AI-035), правило — `concur` называет принятые свидетельства, не согласие роли с вердиктом; подпись в JSON `host_concur` и HTML говорит о свидетельствах; исходные оценки ролей остаются рядом. §24.4 «Синонимы `help` и `version`»: `help`, `--help`, `-h` — успешный JSON `{"usage": …}` со списком команд; `--version`, `-V` — как `version`; прочие неизвестные — прежняя ошибка. §24.5 «`--dir` у `skill install|update`»: `--dir` — корень проекта; путь с базовым именем `.spec-audit` или `.spec-audit/skill` нормализуется к корню по именам каталогов (без чтения ФС); ответ `dir` — корень; отказ «не установлен» называет полный путь `DIR/.spec-audit/skill` и семантику `--dir`. AGENTS.md — строка §24 дополняется «подпись свидетельств, `help`/`--version`, `--dir`».
  - Files: `docs/tool-spec.md`, `skills/spec-audit/references/host-decision.md`, `AGENTS.md`
  - Verify: `go test ./tool -run 'TestHostDecisionMirror|TestSkillFiles' -count=1`; `shasum -a 256 -c acceptance/*.sha256 acceptance/legacy/baseline.sha256` OK.
  - Logging: документ фиксирует: DEBUG «skill: корень проекта» {dir, given}; `help` — без записи в журнал.
  - REQ: REQ-SA-044, REQ-SA-037, §24

- [x] Task 2: код — `hostConcurLabel`/HTML, `help`/`--version`, `skillProjectRoot`; тесты
  - Deliverable: `tool/report.go`: `hostConcurLabel` → «свидетельства mapper» / «свидетельства redteam» / «свидетельства обеих ролей». `tool/report.html:58`: `<p class="meta">Решение хоста опирается на {{.Navigation.HostConcur}}: цитаты взяты из текущих результатов названных ролей; вердикт — хоста и может расходиться с оценкой роли.</p>`; `:77`: «…свидетельства взяты из результатов ролей, названных в concur; concur называет принятые свидетельства, не согласие роли с вердиктом.». `tool/main.go`: константа `usage` (строка команд + `help`); в `execute` до `skill`: `len(args)==1 && oneOf(args[0], "help", "--help", "-h")` → `map[string]any{"usage": usage}`; `oneOf(args[0], "version", "--version", "-V")` → `runVersion()`; ошибка `len(args) < 3` использует `usage`. `tool/skill.go`: `skillProjectRoot(abs string) string` (по именам каталогов), вызов в `runSkillCommand` после `filepath.Abs`, DEBUG «skill: корень проекта» {dir, given} когда путь изменился; `skillNeedInstall` → `fmt.Errorf` в единственном месте использования (`runSkill`, `tool/skill.go:157`) с полным путём `filepath.Join(dir, skillInstallDir)` и «--dir — корень проекта, содержащий .spec-audit/skill; сначала skill install»; ответ `dir`/`skill` уже строится из переданного пути (`:179`) — после нормализации станет корнем без отдельной правки. Тесты: `tool/review_test.go` `TestReviewV2` — ассерты на новые строки; `tool/main_test.go` `TestHelpVersion`; `tool/skill_test.go` `TestSkillDirNormalization` (install в `t.TempDir()`, update с тремя формами `--dir` → `dir` равен корню и `updated:false`; пустой каталог → ошибка содержит `.spec-audit/skill` и «корень проекта»).
  - Files: `tool/report.go`, `tool/report.html`, `tool/main.go`, `tool/skill.go`, `tool/review_test.go`, `tool/main_test.go`, `tool/skill_test.go`
  - Depends: 1
  - Verify: `go test ./tool -run 'TestReviewV2|TestHelpVersion|TestSkill|TestReport' -count=1` (`TestSkillCommandFlags` — ожидания без изменений: `--dir <dir>/missing` по-прежнему отказ без изменения дерева; в него добавляется положительный случай `-dir ROOT/.spec-audit`); `go vet ./tool`; `./bin/spec-audit --help | jq .usage`; `./bin/spec-audit -V`; в `spec-audit-synthetic`: `spec-audit skill update --dir .spec-audit --host both` → `dir` = корень.
  - Logging: DEBUG «skill: корень проекта» {dir, given}; `help`/`version` — без логов состояния.
  - REQ: REQ-SA-044, REQ-SA-037, 24.3, 24.4, 24.5
<!-- Commit checkpoint: tasks 1-2 -->

### Phase 2: Документация и поставка
- [x] Task 3: README, ROADMAP, документ применения
  - Deliverable: README «Быстрый старт»/«Первый запуск»: `spec-audit help` (`--help`, `-h`) печатает список команд JSON, `--version` = `version`; `skill install|update --dir` — корень проекта, `.spec-audit` и `.spec-audit/skill` принимаются и нормализуются; абзац о `review`/HTML — подпись «свидетельства …». README «Приёмка и ограничения» — строка §24.3–24.5 (реализовано; регрессии `TestReviewV2`, `TestHelpVersion`, `TestSkillDirNormalization`; не проверено до релиза: пилот). ROADMAP «Уроки четвёртого прогона» — 6, 9, 10 `[x]` с датой и свидетельствами; строка таблицы этапов. docs/smsplace-adoption.md «третий цикл» — сноска, что ложный отказ `--dir` и подпись исправлены в 0.1.10.
  - Files: `README.md`, `.ai-factory/ROADMAP.md`, `docs/smsplace-adoption.md`
  - Depends: 2
  - Verify: `go test ./tool -run 'TestSkillFiles' -count=1`; `rg -n 'help|--version|корень проекта|свидетельства обеих ролей' README.md`; полный `go test ./tool`, `go vet ./tool`, `CGO_ENABLED=0 go build -o bin/spec-audit ./tool`.
  - Logging: нет нового.
  - REQ: REQ-SA-037, 24.3–24.5

- [ ] Task 4: релиз `v0.1.10`, обновление хоста/пилота/синтетики, проверка на пилоте
  - Deliverable: merge в `codex/bootstrap` (по подтверждению владельца), тег `v0.1.10`, approve deployment; `spec-audit update` → 0.1.10; `spec-audit --help`/`-V` на хосте; `skill update --dir .spec-audit --host both` из корня пилота и синтетики → `dir` = корень, `updated:true` (коммиты — по запросу); README «Приёмка и ограничения» — строка релиза 0.1.10 с фактами; журнал пилота не трогается.
  - Files: `README.md`; (пилот и синтетика — вне репозитория)
  - Depends: 3
  - Verify: `gh release view v0.1.10` — 5 ассетов; `spec-audit version` → 0.1.10; `spec-audit update` → `updated:false`; `git -C <pilot> status --porcelain` — только tracked-копия skill.
  - Logging: нет нового.
  - REQ: REQ-SA-035, REQ-SA-037
<!-- Commit checkpoint: tasks 3-4 -->

## Открытые вопросы (не блокируют)
1. До T2: `help` печатает только строку `usage` или структурированный список команд; выбрана строка — единый источник с ошибкой usage, без второго описания команд.
2. До T2: нужен ли `-v` как синоним `--version`; не добавляется — `-v` часто означает verbose.

## Риски
- Изменение строк `host_concur` ломает внешние потребители JSON, если они сравнивали «по mapper»; известных нет (только HTML и тесты).
- Нормализация `--dir` по именам каталогов: проект, чей корень сам называется `.spec-audit` или `skill` внутри `.spec-audit`, не поддерживается — граница названа в §24.5.
- `help` без аргументов пути не пишет журналы и не читает CONFIG — прежнее поведение неизвестных команд сохраняется.
