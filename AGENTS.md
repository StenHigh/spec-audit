# Рабочий контекст spec-audit

`CLAUDE.md` — симлинк на этот файл. Правь `AGENTS.md`, не заменяй ссылку копией. Отвечай пользователю по-русски.

## Назначение и граница

spec-audit — самостоятельный, не привязанный к бизнес-проекту инструмент аудита Markdown-спецификаций по коду и assertions тестов. SMSPlace — текущий пилот для обкатки, не архитектурная зависимость и не предел назначения инструмента.

Ядро, общий skill и протоколы не должны содержать бизнес-норм, путей, ID задач или специальных исключений пилота. Источники и ограничения приходят из CONFIG и выбранной спецификации; языковые особенности — из явно ограниченного SDK/runtime. Полезные уроки пилота закрепляй независимыми синтетическими регрессиями. Не строй систему плагинов «для всех языков» до конкретной потребности.

Инструмент только аудирует. Он не исправляет проверяемый код/ТЗ, не управляет задачами и не заменяет workflow разработки. AI-хост — существующая сессия: независимые роли делают смысловой анализ, Go-бинарник валидирует данные, хранит состояние и строит отчёт. Модельных API/ключей в бинарнике нет.

## Что читать при старте

1. [.ai-factory/config.yaml](.ai-factory/config.yaml), затем контекст из таблицы ниже: DESCRIPTION, ARCHITECTURE, ROADMAP, RULES и rules/base. В новой сессии это делает локальный `aif-warmup` (Codex: `$aif-warmup`; Claude Code: `/aif-warmup`). Не используй skills из соседнего бизнес-проекта.
2. [README: состояние и следующие шаги](README.md#состояние-и-следующие-шаги) — реализованные возможности и ограничения; очередь развития теперь в ROADMAP.
3. [Спецификация инструмента](docs/tool-spec.md) — источник норм: цели/границы и приёмка §12, текущие расширения §13–52, контракт CLI §1–10; PHP/Laravel SDK — §17 и [контракт расширения](docs/php-sdk-contract.md) (п.1–3 реализованы, п.4 выполнен с нейтральным результатом). Сначала уточняй изменяемое требование и проверку, затем меняй реализацию.
4. Остальное — по задаче из карты ниже. Не загружай всю историю `.local/`, прототипы или документацию пилота по умолчанию. Отсутствие RESEARCH, нового active-плана и необязательных каталогов из конфига нормально; не создавай пустые заглушки.

Явные решения владельца имеют приоритет; конфликт спецификаций обозначай, не разрешай его молча кодом. README и планы не заменяют требования. Необоснованные требования и неизвестность нельзя превращать в PASS.

## Контекст AI Factory

| Файл | Назначение |
| --- | --- |
| [.ai-factory/DESCRIPTION.md](.ai-factory/DESCRIPTION.md) | Назначение, стек и команды разработки |
| [.ai-factory/ARCHITECTURE.md](.ai-factory/ARCHITECTURE.md) | Реальная структура и границы ответственности |
| [.ai-factory/ROADMAP.md](.ai-factory/ROADMAP.md) | Очередь milestones по приоритету и таблица Completed (формат AI Factory); критерии закрытия — в LESSONS.md |
| [.ai-factory/LESSONS.md](.ai-factory/LESSONS.md) | Журнал уроков прогонов, решения владельца по пунктам, детализация очереди и полная таблица завершённых этапов с основаниями (история, не очередь) |
| [.ai-factory/RULES.md](.ai-factory/RULES.md) | Обязательные границы, сохранность и разрешения |
| [.ai-factory/rules/base.md](.ai-factory/rules/base.md) | Соглашения кода и проверок |

AI Factory нужен только для разработки spec-audit, не для его использования в проверяемом проекте. Реестр установки — [.ai-factory.json](.ai-factory.json); инструкции — `.agents/skills/` для Codex и `.claude/skills/` для Claude Code. Начало рабочего цикла и назначение двух конфигов — в [README](README.md#разработка-с-ai-factory).

## Документация и карта проекта

| Путь | Назначение / когда читать |
| --- | --- |
| [README.md](README.md) | [Быстрый старт](README.md#быстрый-старт) (установка, `update`, `skill install`), текущее состояние и результаты приёмки |
| [docs/tool-spec.md](docs/tool-spec.md) | Нормы универсального инструмента и контракты CLI |
| [tool/](tool/) | Единственная поддерживаемая реализация и её регрессии |
| [PHP/Laravel SDK: §17](docs/tool-spec.md#17-php-и-laravel-sdk) | Статус этапа, требования SA-031…034; детали — в контракте расширения |
| [Поставка: §18](docs/tool-spec.md#18-поставка-версия-обновление-и-установка-skill) | Подписанный релиз, `update`, `install.sh`, `skill install|update`, автомат состояний; релиз — README «Релиз» |
| [Уроки чистой сессии: §19](docs/tool-spec.md#19-уроки-чистой-сессии-проверка-ответа-роли-провенанс-версии-ответ-reconcile) | `validate` без записи, журнал версий `tool-versions.json`, ответ `reconcile` после apply, протокол роли |
| [Справочные источники и деление набора: §20](docs/tool-spec.md#20-справочные-источники-тз-и-деление-большого-набора) | Группа `references` (цитируемые файлы ТЗ без кандидатов, guard нормативной цитаты в `reconcile`), `advisories` и WARN о большом scope, правило деления набора в launcher |
| [Уроки второго прогона: §21](docs/tool-spec.md#21-уроки-второго-прогона-проверка-приёмки-без-записи-подсказки-сопоставления-изоляция-ролей) | `check CONFIG RAW [DECISION]` — проверка приёмки без записи с предсказанием ID, подсказки сопоставления кандидат↔active ID, каталог роли `dispatch/<task_id>/` |
| [Уроки третьего прогона: §22](docs/tool-spec.md#22-уроки-третьего-прогона-перепривязка-с-сохранением-содержания-подсказки-диагностика-scopes) | `reanchor` — перепривязка нормы к новым цитатам без смены содержания/revision, `unique_shared` и четыре подсказки, диагностика `scopes` с ID, `base_index_current` в dry-run |
| [Host DECISION version 2: §23](docs/tool-spec.md#23-облегчённая-форма-host-decision-version-2-и-список-ролей) | Вердикты с `concur: mapper|redteam|both` и машиносверяемыми `counts` без копирования цитат; `roles[]`, `form`, `concur` в ответе `review` |
| [Уроки четвёртого прогона: §24](docs/tool-spec.md#24-уроки-четвёртого-прогона-таблица-расхождений-черновик-решения-материализация-dispatch) | `outcomes[]` — таблица расхождений ролей в `review`; `draft CONFIG RUN_ID` — пустой черновик решения version 2; `prepare`/`retry` пишут `dispatch/<task_id>/{task.json,files.json}`; подпись «свидетельства …» у решения хоста, `help`/`--version`, `--dir` = корень проекта |
| [Уроки пятого прогона: §25](docs/tool-spec.md#25-уроки-пятого-прогона-цитаты-хоста-в-решении-version-3-limitations-и-прошлый-вердикт-в-outcomes) | DECISION `version: 3` — собственные цитаты хоста в вердикте по правилам §7; `outcomes[].roles.<role>.limitations` и `previous_host` — прошлый вердикт хоста на том же snapshot/accepted head |
| [Мелочи пятого прогона: §26](docs/tool-spec.md#26-уроки-пятого-прогона-мелочи-freshness-в-index-якоря-тз-вне-snapshot-общий-filesjson) | `freshness: fresh` в `index` (accepted-режим); advisory о якорях `§N.N`/`A-NNN` без определения в spec-файлах snapshot; общий `dispatch/files.json` |
| [Уроки шестого прогона: §27](docs/tool-spec.md#27-уроки-шестого-прогона-проверка-решения-хоста-без-записи-statement-прошлого-решения-разность-цитат-ролей) | `validate CONFIG RUN_ID host DECISION` — проверка решения тем же путём, что `review`, без записи; `previous_host.statement/limitations`; `roles.<role>.citations` и `only_here` в `outcomes[]` |
| [Мелочи шестого прогона: §28](docs/tool-spec.md#28-мелочи-шестого-прогона-списки-всегда-присутствуют-протокол-mapper-по-веткам-пустой-statement-при-совпадении) | `advisories`/`sdk` всегда `[]`; протокол mapper — противоречие в ветке = contradicted; пустой statement вердикта только при `concur: both` и совпадении с обеими ролями |
| [Уроки седьмого прогона: §29](docs/tool-spec.md#29-уроки-седьмого-прогона-чтение-решения-по-одной-норме-симметричный-ответ-review) | `review CONFIG RUN_ID REQ-ID` — роли, previous_host и вердикт хоста по одной норме; ответ `review … DECISION` с `version`/`own_citations`; правило contradicts при relevant+contradicts — в §14 |
| [Мелочи седьмого прогона: §30](docs/tool-spec.md#30-мелочи-седьмого-прогона-промпт-роли-от-prepare-индекс-цитат-по-файлу-счётчики-доставки) | `prepare` пишет `dispatch/protocol.txt` и `dispatch/<task_id>/prompt.md` из встроенного протокола; `review CONFIG RUN_ID citations [PATH]` — кто и под какой нормой цитирует строки файла; `expected`/`submitted`/`delivery_complete` в `prepare`/`tasks` |
| [`cite`, журнал validate роли, посторонний симлинк: §31](docs/tool-spec.md#31-точная-цитата-cite-журнал-validate-роли-посторонний-симлинк) | `cite CONFIG PATH A B` — Citation с точной quote по §7 без записи; промпт роли называет CITE и `validate.log`; SKILL.md — когда симлинк внутри `.spec-audit/` останавливает аудит |
| [Сводки: §32](docs/tool-spec.md#32-сводки-index-config-summary-review-config-run_id-summary) | `index CONFIG summary` — счётчики, `active_ids`, `scopes`, advisories без тел норм и файлов; `review CONFIG RUN_ID summary` — `roles[]`, `outcomes[]`, `agree`/`disagree` и состояние решения без `entries` |
| [Уроки восьмого прогона: §33](docs/tool-spec.md#33-уроки-восьмого-прогона-индекс-якорей-фильтры-цитат-смена-версии-в-run-текст-нормы) | `anchors CONFIG` — упоминания и определения якорей по spec-файлам до приёмки; `citations` фильтруется по норме/роли; WARN о смене версии бинарника в run; `review … REQ-ID text` — читаемая сводка нормы в поле `text` |
| [Обзор нескольких scope: §34](docs/tool-spec.md#34-обзор-нескольких-scope-overview-config) | `overview CONFIG...` — по каждому scope freshness, принятый набор, последний run и последнее решение хоста со счётчиками, `totals`; read-only, без заявления о полноте корпуса |
| [Уроки девятого прогона: §35](docs/tool-spec.md#35-уроки-девятого-прогона-уточнения-промпта-роли-краткая-форма-нормы) | Уточнения `prompt.md` (test_id, запрещённые файлы по имени, scratchpad, ветки redteam, состояния, сводка счётчиками, подкаталоги); `review … REQ-ID brief` — текст нормы без списков цитат |
| [Уроки синтетического прогона: §36](docs/tool-spec.md#36-уроки-синтетического-прогона-0122-состояния-хоста-в-отчёте-unknown-при-выносе-за-срез) | `navigation.host_states` в `report.json`; протокол — unknown, не missing, при явном выносе за срез; SKILL.md о пустом statement/stderr `draft`/вердикте в отчёте; `overview.contradicted[]` |
| [Уроки десятого прогона: §37](docs/tool-spec.md#37-уроки-десятого-прогона-вид-определения-и-вложенные-ссылки-в-anchors-сводка-без-журнала-brief-без-запусков) | `anchors` — `kind` (heading/list/table/text) и `references` у определений; `index … summary` — `accepted: {head, history_total}`; `brief` без строк запусков |
| [Уроки одиннадцатого прогона: §38](docs/tool-spec.md#38-уроки-одиннадцатого-прогона-остаток-пакета-в-ответах-определения-вне-snapshot-oversize_reason) | `deferred`/`rejected` в ответах `check`/`reconcile`, `last_deferred` в `index … summary`; `anchors CONFIG [PATH...]` — определения вне snapshot (`outside`) и вложенные якоря (`nested`); `oversize_reason` у scope подавляет advisory о размере |
| [Второй пакет на том же scope: §39](docs/tool-spec.md#39-второй-пакет-на-том-же-scope-операция-keep-диапазоны-якорей) | REQ-SA-048 `keep` — норма продолжается без кандидата, пока её файлы не изменились; `kept[]` в ответах `check`/`reconcile`; диапазоны `A-051–A-055` в `anchors` |
| [Уроки двенадцатого прогона: §40](docs/tool-spec.md#40-уроки-двенадцатого-прогона-previous_host-по-содержанию-нормы-pending_ids-validate-как-есть) | `previous_host` — по одинаковым файлам и `content_hash`+`revision` нормы (переживает `keep`); `pending_ids` в `prepare`/`tasks`; VALIDATE только заданной командой; `matches` во втором пакете — шум |
| [Уроки тринадцатого прогона: §41](docs/tool-spec.md#41-уроки-тринадцатого-прогона-имена-полей-в-отказе-ambiguous_over_clear-likely_duplicate) | отказ формы называет `нет [..]`/`лишние [..]`; `outcomes[].ambiguous_over_clear` — роли, поставившие ambiguous принятой clear-норме; `matches[].likely_duplicate` при overlap ≥ 0,5 |
| [Уроки четырнадцатого прогона: §42](docs/tool-spec.md#42-уроки-четырнадцатого-прогона-дубликат-по-словам-кросс-scope-память-диапазоны-в-отказе) | `matches[].text_similarity` и словесные подсказки без общих строк; `overview.contradicted_code[]` — code-цитаты contradicted-вердиктов по всем scope; отказ цитаты называет допустимые диапазоны |
| [Уроки пятнадцатого прогона: §43](docs/tool-spec.md#43-уроки-пятнадцатого-прогона-related-и-contradicted_elsewhere) | `related` в CONFIG (соседние scope, вне manifest); `outcomes[].contradicted_elsewhere` — строки кода ролей, уже contradicted у соседей; строка в `brief`/`text` |
| [Уроки шестнадцатого прогона: §44](docs/tool-spec.md#44-уроки-шестнадцатого-прогона-точность-кросс-scope-памяти-свёртка-пределы) | `contradicted_code` только из contradicted-оценок ролей и цитат хоста, `role_here` в подсказке, свёртка строки памяти; предел словесной меры; отказ типа называет поле; `prepare` в заранее созданный каталог |
| [Сводная карта корпуса: §45](docs/tool-spec.md#45-сводная-карта-корпуса-corpus-out_html-config) | `corpus OUT_HTML CONFIG...` — одна статическая страница по всем scope: итоги, таблица scope со ссылками на `report.html`, нормы внимания со statement хоста, файлы под противоречиями; `overview.decided.attention[]`; §45.1 — `gap` по классам implementation/verification/specification |
| [DECISION приёмки version 2: §46](docs/tool-spec.md#46-decision-приёмки-version-2-сужение-statement-кандидата) | REQ-SA-049 — `narrowed_statement` у target: сужение statement кандидата словами оригинала при accept/revise/split/merge; multi-raw отклонён — `keep` остаётся |
| [Динамика GAP: §47](docs/tool-spec.md#47-динамика-gap-delta-и-baseline_run) | `overview.scopes[].delta` — closed/opened/changed между решёнными run по одинаковым нормам, `baseline_run` в CONFIG закрепляет точку «до»; раздел «Динамика» на карте корпуса |
| [Разбор внешнего review: §48](docs/tool-spec.md#48-разбор-внешнего-review-2026-09-18) | Полнота §7 и для version 2; права до commit point `update`; отмена процессов по SIGINT/SIGTERM; мелочи; перечень отложенного (ledger при смене группы, установка skill, PHP-фильтр трейтов, baseline-сравнение метода) |
| [Публикация для заказчика: §49](docs/tool-spec.md#49-публикация-для-заказчика-publish-out_dir-config-история-run) | `publish OUT_DIR CONFIG...` — статический сайт: `index.html`, `overview.json`, копии `report.html` всех решённых run; `scopes[].history[]` и список run со временем на карте |
| [Инкрементальный run: §50](docs/tool-spec.md#50-инкрементальный-run-после-изменения-кода) | REQ-SA-050 `prepare … since PREV_RUN` — роли переоценивают новые, GAP и затронутые нормы; остальное переносится (`manifest.incremental.carried`, `concur: carried`, флаг `carried` в отчёте); полный run — эталон |
| [Точность инкрементального run: §51](docs/tool-spec.md#51-точность-инкрементального-run) | отбор по цитируемым строкам и классам GAP, spec-gap переносится; `outcomes[].reassessed`; блок в промпте; `plan CONFIG PREV_RUN`; задания `incremental-N` по ≤ 24 норм |
| [Известные дефекты и причины: §52](docs/tool-spec.md#52-инкрементальный-run-известные-дефекты-причины-счётчики) | contradicted-норма на неизменных строках переносится как `known_defect`; причины `new|implementation_gap|verification_gap|changed`; `delta.carried` |
| [docs/php-sdk-contract.md](docs/php-sdk-contract.md) | Контракт typed SDK: `php-typed`, формат `sdk/3`, изоляция bootstrap, подсказки в отчёте |
| [docs/accepted-index.md](docs/accepted-index.md) | Принятие кандидатов, ID, редакции, reconcile |
| [docs/legacy-extraction.md](docs/legacy-extraction.md) | Формат извлечения из обычного Markdown и ограничения эксперимента |
| [skills/spec-audit/SKILL.md](skills/spec-audit/SKILL.md) | Поставляемый audit-лаунчер, не инструкция по разработке инструмента; протокол роли лежит рядом в references/ |
| [acceptance/](acceptance/) | Независимые эталоны, контракты и хэши приёмки |
| [docs/smsplace-adoption.md](docs/smsplace-adoption.md) | Только работа с пилотом: бизнес-границы, локальная установка и разрешения |
| [.ai-factory/PLAN.md](.ai-factory/PLAN.md) | Завершённый журнал 29 задач и исследований; исторические «следующие шаги» не являются текущей очередью |
| [prototypes/](prototypes/) | Исторические пробы, не второй поддерживаемый продукт |

`.local/` содержит приватные исходники/сырые результаты; `bin/` — локальные сборки. Оба исключены из Git и не восстанавливаются клонированием. Их отсутствие не блокирует разработку и синтетические тесты; не выдумывай недоступные свидетельства.

Обязательные правила и команды вынесены в контекст AI Factory выше, а не отменены. Перед изменениями прочти RULES и rules/base. Особенно важно: frozen-контракты и raw сохраняются, PHP не запускается на хосте, бизнес-проект не меняется без отдельного разрешения, коммит/push — только по запросу. Typed SDK (`php-typed`) реализован ограниченно: подсказки ≠ связи, доказанного улучшения точности нет.
