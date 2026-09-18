# Развитие spec-audit

Обновлено: 2026-09-18 (§30–§48: 16 чистых прогонов, 11 scope и 681 норма на пилоте, `keep`, карта корпуса с GAP и динамикой, разбор внешнего review). Очередь — раздел «Приоритетная очередь» ниже; записи «Уроки N-го прогона» — журнал решений, в них открыты только пункты, помеченные `[ ]` внутри.


## Приоритетная очередь

Критерии порядка: (1) что мешает применять инструмент к полному ТЗ в рабочем режиме; (2) надёжность уже сделанного; (3) стоимость прогона; (4) исследования. Берётся первый незакрытый пункт сверху; переставлять — решение владельца.

**P1 — рабочий режим на полном ТЗ**
- [ ] **P1.1 Инкрементальный перепрогон после изменения кода.** Сейчас после фикса продукта нужен полный run по scope (12 ролей, ~3–4 M токенов на 100 норм), хотя изменились 3–5 файлов. Нужно: `prepare CONFIG RUN_ID since PREV_RUN` — задания только на нормы, чьи цитируемые файлы кода/тестов (по решению хоста PREV_RUN) изменились или чей вердикт был GAP; остальные нормы переносятся в новый run как `carried` с провенансом (run, review_id, hash файлов) и без переоценки; `review`/`report`/`corpus` показывают перенесённые отдельно от переоценённых; полный run по-прежнему доступен. Это ROADMAP-этап «Инкрементальная актуальность»; контракт — новый раздел tool-spec, §1–10 не меняются. Критерий: перепрогон finance после правки одного сервиса стоит ≤ ⅓ полного и даёт ту же динамику GAP.
- [ ] **P1.2 Перепрогон затронутых scope одной командой.** После P1.1: `rerun CONFIG... since baseline` — по каждому scope из списка подготовить инкрементальный run и вывести очередь заданий; хост запускает роли как обычно. Критерий: MR по рынкам проверяется одной командой на шести scope.
- [ ] **P1.3 Свежесть accepted-индекса при смене группы файла (review 2.1).** Перенос неизменённого файла между `specs` и `references` оставляет индекс `fresh` вопреки §20 — версионированно включить классификацию источников в основание пакета; старые ledger не переписывать. Критерий: регрессия «переклассификация без изменения байтов → stale».

**P2 — надёжность сделанного**
- [ ] **P2.1 Релизная квалификация без `-short` на обеих платформах (review 2.5):** `TestNativeDistribution` и реальный runtime в `release.yml`; Actions по полным SHA; литеральные векторы `counts` с мутацией каждого поля.
- [ ] **P2.2 Установка skill через staging (review 2.3):** прерывание до receipt не должно делать каталог `foreign`; `install --replace` завершает частичную копию (§18).
- [ ] **P2.3 Фильтр трейтов в `sdk-typed.php` (review 2.7):** факты трейтов по множеству `files`; контейнерная приёмка на пилоте (Docker есть).
- [ ] **P2.4 Первая справочная цитата и секция отчёта (review 2.6):** решить в контракте §16.1/§20 — первая нормативная цитата для производной сводки.

**P3 — стоимость прогона**
- [ ] **P3.1 Бенчмарки и повторное чтение файлов (review 4):** `Benchmark*` на цитаты/`lineQuote`/`status` большого run; затем переиспользование проверенных байтов в пределах команды. Только после измерений.
- [ ] **P3.2 Токены ролей:** 0,25–0,42 M на роль при 15–25 нормах; измерить долю чтения `files.json`/протокола/промпта и кода; решить, что можно сократить без сужения контекста (§20 запрещает сужать список файлов).

**P4 — исследования (без обязательства реализовать)**
- [ ] **P4.1 Сравнение метода с простым AI-baseline (review 7):** один агент / две роли без инфраструктуры / полный spec-audit на независимых наборах; метрики — найденные и ложные дефекты, пропуски, завышение relevant, общие ошибки ролей, правки хоста, время, токены. Если полный процесс не выигрывает — упрощать.
- [ ] **P4.2 `php-typed` на scope с интерфейсами/биндингами** — единственное условие возврата к SDK (решение владельца 2026-09-16).
- [ ] **P4.3 Пробное преобразование бизнес-ТЗ** и **приёмка замены старого AI re-audit** — этапы ниже, решения владельца и пилота.

**Вне очереди инструмента (владелец):** остальные `04-pricing-*` и `00-overview-common` на пилоте; продуктовые дефекты (рынки A-014/A-015/A-214 в 9 scope, 03/011, 09/001+019+063+065 и др.) и вопросы к ТЗ из разборов; отклонённые пункты — предзаполнение `draft` (шестой прогон 8), переименование `concur` (9, только с новой формой), версия в summary решения.

## Журнал уроков

Записи «Уроки N-го прогона», «Ближайшие этапы», «Исследования» и «Учёт планов и истории» с решениями владельца по каждому пункту вынесены без изменений в [LESSONS.md](LESSONS.md). Открытые пункты из них, требующие работы над инструментом, стоят в очереди выше; остальное — решения владельца и продуктовые наблюдения пилота.

## Завершённые этапы

| Завершённый этап | Статус подтверждён | Основание |
| --- | --- | --- |
| Ограниченный аудит и самоприменение | 2026-09-16 | README: приёмка, ограничения, неизменённые контроли |
| HTML-карта одного run | 2026-09-16 | README и подтверждение владельца |
| Контекст разработки и AI Factory | 2026-09-16 | Локальные skills, config и проектная документация |
| PHP/Laravel SDK, п.1–3 §17.2 | 2026-09-16 | docs/php-sdk-contract.md, acceptance/php-sdk-control.json, `TestTyped*`, внешние тесты синтетических примеров; README «Приёмка и ограничения» |
| Сравнение с SDK и без него на пилоте, п.4 §17.2 | 2026-09-16 | README «Приёмка и ограничения»; `.local/sdk-comparison/study.json` (вне Git) |
| Уроки чистой сессии — `validate`, протокол роли, журнал версий, ответ `reconcile` (§19) | 2026-09-16 | tool-spec §19, `TestContract/validate_*`, `TestToolVersions`, `TestReconcileApplyView`, `TestSkillFiles`; README «Приёмка и ограничения» |
| Рабочий цикл из чистой AI-сессии (пилот `02-finance` + синтетический проект) | 2026-09-16 | README «Приёмка и ограничения», docs/smsplace-adoption.md «Чистая AI-сессия», run `finance-2026-09-16-01` в пилоте и `run-20260916-215141` в `spec-audit-synthetic` (вне Git) |
| Host DECISION version 2 и список ролей в `review` (§23, пункты 5–6) | 2026-09-17 | tool-spec §23, `TestReviewV2`, `TestHostDecisionMirror`; README «Приёмка и ограничения»; живой цикл — run `finance-2026-09-17-03` (adoption «третий цикл») |
| Таблица расхождений `outcomes[]`, черновик `draft`, dispatch из `prepare` (§24, пункты 1–3 четвёртого прогона) | 2026-09-17 | tool-spec §24, `TestReviewOutcomes`, `TestReviewDraft`, `TestPrepareDispatch`; README «Приёмка и ограничения»; живой цикл — следующий прогон |
| Подпись свидетельств хоста, `help`/`--version`, `--dir` у skill (§24.3–24.5, пункты 6, 9, 10) | 2026-09-17 | tool-spec 1.4, `TestReviewV2`, `TestHelpVersion`, `TestSkillDirNormalization`; README «Приёмка и ограничения» |
| DECISION version 3 с цитатами хоста, limitations и `previous_host` в `outcomes[]` (§25, пункты 1–3 пятого прогона) | 2026-09-18 | tool-spec 1.5, `TestReviewV3`, `TestReviewPreviousHost`; README «Приёмка и ограничения»; живой цикл — следующий прогон |
| `freshness` в `index`, advisory о якорях ТЗ вне snapshot, общий `dispatch/files.json` (§26, пункты 4, 5, 7 пятого прогона) | 2026-09-18 | tool-spec 1.6, `TestIndexFreshness`, `TestAnchorAdvisories`, `TestPrepareDispatch`; README «Приёмка и ограничения» |
| `validate … host DECISION` без записи, statement прошлого решения, `only_here` (§27, пункты 1–3 шестого прогона) | 2026-09-18 | tool-spec 1.7, `TestReviewValidateHost`, `TestReviewPreviousHost`, `TestReviewOutcomes`; README «Приёмка и ограничения»; живой цикл — следующий прогон |
| Списки `[]`, протокол mapper по веткам, пустой statement при совпадении (§28; пункты 4, 7 шестого и 5 четвёртого прогона) | 2026-09-18 | tool-spec 1.8, `TestReviewAgreedStatement`, `TestScopeAdvisories`, `TestTypedCLI`; README «Приёмка и ограничения» |
| `review … REQ-ID`, симметричный ответ записи, правило contradicts в §14 (§29; пункты 2, 4, 1 седьмого прогона) | 2026-09-18 | tool-spec 1.9, `TestReviewRequirementView`; README «Приёмка и ограничения» |
| Промпт роли и протокол от `prepare`, индекс цитат `review … citations`, счётчики доставки в `tasks` (§30; пункты 5, 3, 6 седьмого и 5 шестого прогона) | 2026-09-18 | tool-spec 1.10, `TestPrepareDispatch`, `TestReviewCitations`; README «Приёмка и ограничения» |
| `cite` — точная цитата, `validate.log` роли в промпте, политика постороннего симлинка (§31; пункты 8, 11 четвёртого и 6 шестого прогона) | 2026-09-18 | tool-spec 1.11, `TestCite`, `TestPrepareDispatch`, `TestSkillFiles`; README «Приёмка и ограничения» |
| Сводки `index … summary` и `review … summary` (§32; пункт 7 четвёртого прогона) | 2026-09-18 | tool-spec 1.12, `TestIndexSummary`, `TestReviewBrief`; README «Приёмка и ограничения» |
| Новый раздел на пилоте (`11-allocation-rent`, run `rent-2026-09-18-01`) и уроки восьмого прогона а/б/в/д (§33) | 2026-09-18 | tool-spec 1.13, `TestAnchors`, `TestReviewCitations`, `TestVersionDriftWarning`, `TestReviewRequirementView`; docs/smsplace-adoption.md «седьмой цикл»; README «Приёмка и ограничения» |
| Обзор нескольких scope `overview CONFIG...` (§34; первый шаг этапа «несколько разделов») | 2026-09-18 | tool-spec 1.14, `TestOverview`; README «Приёмка и ограничения» |
| Повтор rent с промптами от `prepare` (run `rent-2026-09-18-02`) и уроки девятого прогона (§35) | 2026-09-18 | tool-spec 1.15, `TestPrepareDispatch`, `TestReviewRequirementView`; adoption «восьмой цикл»; README «Приёмка и ограничения» |
| Третий раздел на пилоте (`05-inventory`, run `inventory-2026-09-18-01`), уроки синтетического и десятого прогонов (§36, §37) | 2026-09-18 | tool-spec 1.17, `TestAnchors`, `TestIndexSummary`, `TestReviewRequirementView`, `TestReviewV2`, `TestOverview`; adoption «девятый цикл»; README «Приёмка и ограничения» |
| Четвёртый раздел на пилоте (`07-allocation-activation`, run `activation-2026-09-18-01`), уроки одиннадцатого прогона (§38) | 2026-09-18 | tool-spec 1.18, `TestCheckAcceptance`, `TestAnchors`, `TestScopeOversizeReason`, `TestIndexSummary`; adoption «десятый цикл»; README «Приёмка и ограничения» |
| Второй пакет на том же scope — `keep` (§39, REQ-SA-048), диапазоны якорей | 2026-09-18 | tool-spec 1.19, `TestKeepPackage`, `TestAnchors`; README «Приёмка и ограничения»; живой цикл — пакет 2 `01-foundation` |
| Пятый раздел на пилоте (`01-foundation`, два пакета, run 01/02), уроки двенадцатого прогона (§40) | 2026-09-18 | tool-spec 1.20, `TestReviewPreviousHostAccepted`, `TestPrepareDispatch`; adoption «одиннадцатый цикл»; README «Приёмка и ограничения» |
| Шестой раздел на пилоте (`06-provider-integrations`, два пакета, run 01), уроки тринадцатого прогона (§41) | 2026-09-18 | tool-spec 1.21, `TestRequiredJSONNamesFields`, `TestReviewPreviousHostAccepted`, `TestCheckAcceptance`; adoption «двенадцатый цикл»; README «Приёмка и ограничения» |
| Разделы 09 и 10 на пилоте (по два пакета, run messaging-01/inbound-01), уроки четырнадцатого прогона (§42) | 2026-09-18 | tool-spec 1.22, `TestCheckAcceptance`, `TestOverview`; adoption «тринадцатый цикл»; README «Приёмка и ограничения» |
| Разделы 08 и 03 на пилоте (run webhooks-01/incident-01), уроки пятнадцатого прогона (§43) | 2026-09-18 | tool-spec 1.23, `TestReviewContradictedElsewhere`; adoption «четырнадцатый цикл»; README «Приёмка и ограничения» |
| Раздел `04-pricing-entry` на пилоте с `related` (run pricing-entry-01), уроки шестнадцатого прогона (§44) | 2026-09-18 | tool-spec 1.24, `TestOverview`, `TestReviewContradictedElsewhere`, `TestPrepareEmptyDirAndTypedJSONError`; adoption «пятнадцатый цикл»; README «Приёмка и ограничения» |
| Сводная HTML-карта корпуса `corpus` (§45) | 2026-09-18 | tool-spec 1.25, `TestCorpus`; README «Приёмка и ограничения»; `.spec-audit/corpus.html` на пилоте |
| Сужение statement при принятии — DECISION version 2 (§46, REQ-SA-049); GAP на карте корпуса (§45.1) | 2026-09-18 | tool-spec 1.27, `TestAcceptNarrowed`, `TestCorpus`; README «Приёмка и ограничения» |
| Динамика GAP между решёнными run, `baseline_run` (§47) | 2026-09-18 | tool-spec 1.28, `TestCorpusDelta`; README «Приёмка и ограничения» |
| Разбор внешнего review — подтверждённые дефекты (§48) | 2026-09-18 | tool-spec 1.29, `TestReviewV2`, `TestUpdate`, `TestInstallScript`; `ci.yml`; README «Приёмка и ограничения» |
| Уроки третьего прогона: `reanchor`, `unique_shared`, диагностика scopes, `base_index_current` (§22, пункты 1–4) | 2026-09-17 | tool-spec §22, `TestReanchor`, `TestCheckAcceptance`, `TestScopeAssignmentErrors`; README «Приёмка и ограничения»; пункты 5–7 открыты |
| Уроки второго прогона: `check`, подсказки сопоставления, изоляция ролей (§21, пункты 1–3) | 2026-09-17 | tool-spec §21, `TestCheckAcceptance`, `TestSkillFiles`; README «Приёмка и ограничения»; пункт 4 открыт |
| Справочные источники ТЗ и деление большого набора (§20) | 2026-09-17 | tool-spec §20, `TestReferenceSources`, `TestScopeAdvisories`; README «Приёмка и ограничения»; пилот `finance-2026-09-17-01` (docs/smsplace-adoption.md «Чистая AI-сессия, 2026-09-17») |
| Поставка: релиз `v0.1.0`, `update`, `skill install|update` (§18) | 2026-09-16 | tool-spec §18, `release.yml` run на теге `v0.1.0`, `TestUpdate`/`TestSkill`/`TestInstallScript`/`TestNativeDistribution`, README «Приёмка и ограничения», `.local/daily-smsplace/skill-install-2026-09-16.json` (вне Git) |
