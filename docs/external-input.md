# Внешний корпус: файловый обмен r1

Для новой сессии используйте [подключение внешнего корпуса](external-start.md): четыре шага, полные примеры CONFIG/AUDIT_INPUT и разбор отказов. Этот маршрут необязателен: локальный аудит без external продолжает обычный цикл.

spec-audit потребляет подготовленный корпус. Получение редакций, проверка автономности, выбор линии/комплекта, принятие для разработки и наблюдение живого источника принадлежат внешнему клиенту. Сетевой провайдер, credentials и API трекера в бинарнике отсутствуют. Markdown остаётся обычным самостоятельным текстом, без служебных вставок.

## Публикация без архивов сайтов (§83)

Новый контракт §83 заменяет ниже исторические правила обязательного сохранения сайта. `publish OUT_DIR --from AUDIT_ROOT` собирает последние полные результаты всех областей; `publish OUT_DIR --run RUN_ID CONFIG` выбирает один исторический аудит. Новый OUT должен отсутствовать. Редакция проверенной спецификации видна отдельно для каждой области, даже без corpus-control. Генерация сайта не запускает аудит и не меняет решения.

HTML генерируется из сохранённых JSON-отчётов. Новый result — компактная schema 4 со ссылками; CLI external result остаётся v3. Новый bundle — schema 3 без сайтов. Request schema 2: `{"schema_version":2,"configs":["/absolute/config.yaml"]}`. Старые bundles v1/v2 продолжают читаться. Старые внутренние копии освобождает `cleanup plan ARCHIVE --publications ID...` с независимой проверкой восстановления аудита, без обязательного backup сайта.

## Вход и установка

Каталог передачи содержит `corpus/`, `source-manifest.json`, `profile.json`, `audit-input.json`. Три JSON находятся вне corpus. Поля и типы определены схемами [source-manifest/1](schemas/source-manifest.schema.json) и [audit-input/1](schemas/audit-input.schema.json). Дополнительно CLI проверяет уникальность/каноничность путей, отсутствие симлинков во всех компонентах, полный фактический состав, file_count, размеры, hashes и соответствие CONFIG. Unknown fields, duplicate keys и неподдерживаемая версия отвергаются.

`source-manifest.json` содержит schema_version=1, spec_version, project, corpus, line и previous_spec_version (строки либо null), profile={id,version,sha256}, files и file_count. Каждая запись files содержит path, role=`normative|reference|archive`, export (boolean), media_type, size_bytes, sha256 и origin. Происхождение:

- `trucker`: строковые project_id/document_id/revision_id, file_version_id (null для Markdown, строка для материала), source_url (HTTP(S) без credentials либо null). Это данные одного из источников, а не подключение к серверу.
- `git`: repository, commit, path реального прежнего основания.
- `snapshot`: snapshot_id, snapshot_sha256, path сохранённого дерева.

Hash манифеста передаётся отдельно и вычисляется от его исходных bytes, без канонизации JSON. `profile.json` — объект JSON с точными настройками producer. r1 не определяет внутренние поля этого объекта: consumer проверяет bytes/hash и ID/version, явно закреплённые CONFIG, и применяет существующий declared/accepted Markdown-профиль CONFIG. Он не подтверждает автономность или поддержку произвольной грамматики по одному имени профиля. Другую грамматику следует сначала согласовать как версионируемое расширение.

CONFIG сохраняет прежние specs/references/code/tests/runtime/scopes/related/baseline_run и добавляет явное закрепление интеграции:

```yaml
external:
  project: fixture-project
  corpus: fixture-corpus
  line: null
  scope: fixture-scope
  profile_id: fixture-markdown
  profile_version: "1"
```

`scope` — стабильная область индекса, отдельная от прямых ID документа, его редакции и spec_version. Один reports_dir закрепляется за project/corpus/scope. Роли normative/reference должны в точности соответствовать выбранным specs/references; archive не становится нормативным источником. Include/exclude работают по прежним правилам. Источники корпуса не пересекаются с выбранными code/tests.

Запрос содержит абсолютные corpus_dir/manifest_path/profile_path, manifest_sha256 и code_basis. Для `kind: git` проверяются полный commit ID, HEAD, repository (физический корень checkout с учётом стандартных системных путей macOS либо точный origin) и побайтовое соответствие выбранных code/tests этому коммиту, включая отказ на dirty/untracked выбранный файл. Для `kind: snapshot` sha256 вычисляется от компактного JSON отсортированного по path массива штатных SourceFile выбранных code/tests (`path,kind,sha256,bytes`; reference отсутствует). snapshot_id — отдельная идентичность клиента. Это основание выбранного среза, не утверждение чистоты всех невыбранных файлов репозитория.

`freshness` имеет status=`unknown|unchanged|changed`, checked_at (ISO8601 с timezone либо null), observation_ref/reason (строки либо null). Проверенные unchanged/changed требуют времени. Неполный/недоступный обход producer обязан передать как unknown. acceptance_ref/applicability_ref — отдельные решения клиента либо null, без автоматического признания принятия. publication_base — вспомогательный HTTP(S) base path без credentials/query/fragment либо null; относительные ресурсы сайта не требуют его встраивания в HTML.

```sh
spec-audit external check CONFIG AUDIT_INPUT
spec-audit external import CONFIG AUDIT_INPUT
```

На macOS разрешены только стандартные системные ссылки `/var`, `/tmp`, `/etc` в соответствующие `/private/*`; произвольные ссылки внутри входных путей и каталогов назначения отклоняются.

`check` ничего не устанавливает. `import` проверяет всё до публикации и одним rename устанавливает дерево в reports_dir/._inputs/KEY. Возвращается путь к рабочему `config.yaml`; далее обычные команды используют его. Внутри — corpus, отдельное объединённое дерево выбранных источников, исходные manifest/profile/request/config и опись code/tests. Исходные файлы клиента не меняются. CONFIG-поле `external.input` указывает на сохранённый запрос; это управляемая локальная привязка, не новый транспорт. Её физический путь исключён из snapshot ID. Сохранённые наблюдение freshness, ссылки решений клиента, publication_base и hash запроса также не меняют штатный snapshot: они доступны отдельно, в исходном запросе и provenance run. Новый запрос устанавливается отдельно; принятый индекс остаётся в прежнем reports_dir. На прежний вызов с теми же входами возвращается существующая проверенная установка.

Это единственное узкое исключение из разделения sources/reports: управляемое дерево имеет форму `reports_dir/._inputs/<SHA-256>/tree`, а его metadata лежат рядом, вне tree. Произвольное пересечение исходников/отчётов по-прежнему запрещено. Установка не является accept/reconcile и не выдаёт ID/retire автоматически. При изменении смысла или координат работают прежние check/reconcile/relocate. Зафиксированный вход не обновляется из живого источника во время run. `external.code_root` сохраняет исходный разрешённый корень runtime, а `code_sha256` — проверенную опись выбранных code/tests. Эти поля создаёт import. Физический code_root не входит в snapshot ID: изменение выбранных bytes в этом корне делает run stale и требует нового импорта; недоступный корень не объявляется свежим. Старые полные источники остаются доступными для просмотра. Для нового кода/комплекта повторите импорт из исходного CONFIG и нового запроса; не редактируйте управляемую копию.

## Контрольная редакция корпуса (§82)

Контрольная редакция — версия корпуса, которую вызывающая сторона выбрала для сравнения аудитов областей. Версия использует существующий `spec_version`.

Создайте FILE по [corpus-control/1](schemas/corpus-control.schema.json):

```json
{"schema_version":1,"controls":[{"project":"example","corpus":"requirements","line":null,"spec_version":"R2"}]}
```

Передайте этот файл через публичный CLI:

```sh
spec-audit overview --corpus-control FILE CONFIG_A CONFIG_B
spec-audit corpus OUT_HTML --corpus-control FILE CONFIG_A CONFIG_B
spec-audit publish NEW_OUT_DIR --corpus-control FILE CONFIG_A CONFIG_B
```

Контекст сравнения включает точные `project`, `corpus` и `line`. Значение null обозначает неназванную линию, а не любую линию. Файл может задавать несколько контекстов.

Карта сравнивает контроль с основанием сохранённого полного решения каждой области. Совпадение даёт `checked`. Другая версия даёт `not_checked`: «проверено на R1; на R2 не проверено». Отсутствие решения, версии или подходящего контекста даёт `incomparable` с причиной. JSON overview содержит `corpus_control` и отдельный `scopes[].corpus_revision`.

Версии сравниваются только на равенство. Различие не доказывает влияние правки или ошибочность старого аудита. Проверка кода и доступность источников остаются отдельными показателями.

Новый импорт или поздний исторический аудит не выбирает контрольную редакцию. Для следующей публикации снова передайте выбранный файл. Команда без параметра сохраняет прежнее поведение и не выбирает контроль автоматически.

Сравнение работает после citations release по сохранённой metadata. Оно не запускает AI-задания и не меняет решения, GAP, цитаты или отчёты run. Bundle сохраняет контроль и метки внутри публикации. При новой публикации после restore передайте тот же файл. Если исходный FILE недоступен, восстановите его из публичного overview:

```sh
jq '.corpus_control' RESTORED_SITE/overview.json > control.json
spec-audit publish NEW_OUT_DIR --corpus-control control.json RESTORED_CONFIG_A RESTORED_CONFIG_B
```

Это чтение публичных данных публикации; внутренние журналы и результаты не редактируются. Формат external result остаётся v3.

## Аудит, перепроверка, результат и сайт

Полный и инкрементальный циклы сохраняются: index/check/reconcile, prepare/tasks/submit/validate, plan/prepare since/rerun, test/SDK при прежнем разрешении, review/report и overview/corpus/publish. Вход не запускает код и не расширяет runtime-права. Новый комплект, доставка ролей, зелёный тест и принятие комплекта не закрывают GAP/вопросы. Для accepted остаются прежние ID, revisions, parents, retired, история и граница accepted_requirements_only.

Новый run в режиме archive сохраняет полные исходные bytes по SHA-256 и опись соответствия. Просмотрщик имеет предел 32 MiB сформированного HTML и при его превышении явно отказывает; исходные bytes сохраняются в объектах. Report JSON содержит `sources[]` с path/kind/hash/bytes, origin, full, url/raw_url/current_url и limitation. Исторические страницы выделяют строки с 1 включительно (`?start=A&end=B#LA`); raw download сохраняет BOM/CRLF/Unicode. Один hash не объединяет разные логические пути/происхождения. Код/тесты имеют проверенный code_basis. Текущий URL документа — отдельная дополнительная ссылка. У старого run без полного источника full=false и явная limitation; текущий файл не используется как подстановка.

Перед публикацией внешнего run независимая сессия проверяет точное решение хоста и подаёт [review-check/1](schemas/review-check.schema.json): run/snapshot/review ID, SHA-256 исходного решения из review.history, basis_sha256 самого решения, отдельный reviewer/context_ref, conclusion=`confirmed|changes_required`, summary/limitations. Содержательная перепроверка и свежесть независимого контекста обеспечиваются оператором; CLI проверяет форму/основание и отличное имя reviewer, а не доказывает независимость именем.

```sh
spec-audit review-check CONFIG RUN_ID RECEIPT
spec-audit review-check CONFIG RUN_ID
spec-audit report CONFIG RUN_ID
spec-audit external result CONFIG RUN_ID
spec-audit publish OUT_DIR CONFIG...
```

`review-check` сохраняет исходные bytes, включая отрицательный вывод, в истории и записывает версию валидатора в штатный tool-versions. Retry, новые свидетельства или другое решение требуют новой перепроверки. Внешний report без подтверждённой перепроверки не публикуется. Старые локальные отчёты сохраняют прежние ограничения; им не выдумывается новая независимая проверка.

`external result` читает уже созданный report. Новое представление по §80 возвращает schema_version=3, tool_version экспортёра, scope, run_id, snapshot_id, accepted_head, basis (project/corpus/line/spec_version/manifest hash/profile/request hash/code_basis/freshness/решения клиента), decision, report и paths. Полная структура аудита — прежний публичный report.json, включая tool_versions, роли/расхождения, ограничения, raw provenance и host decision basis; Оболочка описана [external-result/3](schemas/external-result-v3.schema.json); [external-result/1](schemas/external-result.schema.json) остаётся прежней схемой исторического формата. Это публичный интерфейс, приватные journals клиенту читать не нужно.

`publish NEW_OUT_DIR --from AUDIT_ROOT` генерирует последние полные результаты всех обнаруженных областей. Без --from корень поиска — текущий каталог. Явные CONFIG... ограничивают подборку. Автопоиск объединяет эквивалентные CONFIG одной reports_dir и отклоняет несовместимые привязки до создания OUT. `publish NEW_OUT_DIR --run RUN_ID CONFIG` генерирует один исторический аудит отдельно. Карта и отчёт показывают проверенную редакцию именно выбранного run; свежий импорт её не заменяет. Новые pending run не вытесняют завершённые результаты. Исторический выбор проверяет только metadata выбранного run; повреждённые соседние manifests и журналы версий не блокируют его. Default сохраняет проверку всех run. Новый сайт не включает HTML всех исторических запусков. Прежние результаты и raw остаются в audit state.

Сайт генерируется в новый OUT через staging без записи копии сайта в reports_dir. HTML восстанавливается из сохранённого JSON, архивные source-viewers — из объектов источников. Полные источники citations не появляются. Archive/citations могут находиться на одной карте с отдельной политикой каждой области. Исторические сайты, уже записанные в старые bundles v1/v2, читаются по прежнему контракту; новые bundles не содержат сайтов.

## Режим без архива исходников (§79)

В новом отдельном `reports_dir` задайте `source_retention: citations` в исходном CONFIG. Отсутствие поля либо `archive` сохраняет обычный режим; переключение существующей рабочей области запрещено, старые архивы не удаляются. Входной обмен остаётся r1: producer не меняет схемы manifest/audit-input. Локальный CONFIG читает существующий checkout без копирования. Внешний корпус по-прежнему может находиться отдельно от checkout.

```yaml
source_retention: citations
reports_dir: citation-results
```

Сохраняются полные результаты и история: требования/ID/revision, точные цитаты, координаты/hashes/origin, обоснования, raw ролей и хоста, accepted ledger и перепроверки. `sources.json` имеет schema_version=2 и `full:false` с limitation. Полные файлы code/tests/Markdown/archive не помещаются в общие объекты и source-HTML/bin не создаются. Метаданные обмена и CONFIG сохраняются побайтово: не используйте profile/origin/request как контейнер полных исходников. Цитата короткой целой нормы остаётся допустимым свидетельством.

Для внешнего корпуса `import` временно создаёт управляемые tree/corpus. Штатный цикл:

```sh
spec-audit external import CONFIG AUDIT_INPUT       # взять config из ответа
# index/reconcile, prepare, роли/submit, review, независимая review-check
spec-audit external release IMPORTED_CONFIG
spec-audit report IMPORTED_CONFIG RUN_ID
spec-audit external result IMPORTED_CONFIG RUN_ID
spec-audit publish NEW_OUT_DIR IMPORTED_CONFIG
spec-audit bundle export REQUEST OUT.tar.gz
spec-audit bundle verify OUT.tar.gz
```

`release` проверяет все версии входа и все зависимые run рабочей области: точный состав/hashes временных файлов, metadata, raw и решения, доставку и подтверждённую review-check; исключительная блокировка запрещает конкурентную операцию CLI. До завершения всех проверок файлы не удаляются. Затем удаляются только принадлежащие import временные tree/corpus и проверенные остатки прерванного rehydrate. Симлинки, неизвестные/изменённые файлы, открытые задания или отсутствующая перепроверка дают отказ. Исходный checkout/корпус клиента и metadata/history сохраняются. Receipt позволяет повторить прерванный release; успешный повтор идемпотентен. Неизвестный staging не признаётся освобождённым: экспорт отказывает. Это не механизм преобразования прежнего archive.

Повторный `external import` из исходного CONFIG и точного AUDIT_INPUT восстанавливает временные копии и может привязать проверенный код из перенесённого checkout. Генерируемый рабочий CONFIG содержит текущие физические пути; original-config, запрос и прежние run не переписываются. Новые bytes требуют соответствующего нового manifest/code_basis и нового run. Для инкремента используйте прежний `prepare ... since PREV_RUN`; новая редакция, импорт или зелёный тест не закрывают GAP автоматически.

Сохранённые `status`, `review` (включая citations), `report`, `overview` и `publish` работают после release и удаления тестовых входов. Нормы, вопросы и история не обнуляются. По §80 отсутствие источников означает `freshness: unchecked`, `source_availability: released|unavailable`, а не обнаруженное изменение. `fresh` требует успешной сверки; `stale` — доказанного изменения bytes/основания. Полное решение с прежним reviewBasis имеет state `saved` и complete=true; retry/новые свидетельства по-прежнему дают outdated/complete=false. Drift без проверки остаётся unchecked. Сохранённая external freshness остаётся наблюдением своего времени. `cite`, приём нового ответа, новый/инкрементальный анализ требуют доступного точного основания; отсутствие или подмена источника не маскируются цитатами.

`current_freshness` описывает сверку доступного локального входа/runtime с сохранённым снимком. Это не запрос к исходному хранилищу документов: `basis.freshness` остаётся отдельным наблюдением внешнего клиента на указанное им время. Отсутствие сетевого наблюдения не заменяется локальным fresh.

CLI `external result` сохраняет самостоятельную [schema 3](schemas/external-result-v3.schema.json), включая report и decision с наблюдением актуальности. Публикуемый `result.json` имеет отдельную [schema 4](schemas/external-result-v4.schema.json): `report_ref:{path,sha256,bytes}` и `decision_ref:{path,pointer}` вместо вложенных полных данных. Цель report_ref — соседний report.json; decision_ref указывает `/host_review` либо равен null. Потребитель проверяет hash, размер и совпадение run/snapshot/basis. Новая форма не выдаётся за schema 3.

`report_basis` равен `saved_run` для обычной публикации и `historical_publication` при явном --run. `current_freshness` отдельно называет freshness и source availability. Дата аудита и редакция спецификации относятся к выбранному run; дата генерации сайта их не обновляет. После release решение остаётся saved/complete при неизменном основании. Ни публикация, ни перенос не запускают новый аудит.

Run без независимой проверки или с согласованной с отчётом `changes_required` остаётся незавершённым. Default выбирает прежний полный аудит; `latest.publication_pending` объясняет ожидание. Явный `--run` для незавершённого аудита отказывает. Повреждённый журнал или несовпадение сохранённого receipt с отчётом даёт отказ без fallback.

Новый bundle имеет schema_version=3 и сохраняет audit state без site areas и производных report/viewer-копий. Для citations workspace отсутствует; перед export внешние временные входы нужно освободить через release. Все metadata, описи, quotes, raw, попытки, решения и независимые проверки сохраняются. Archive-области сохраняют свои полные источники. Старые bundles v1/v2 читаются по их строгим историческим правилам; их прежний HTML не регенерируется при restore.

## Компактный пакет состояния

Запрос нового export (schema_version=2) содержит абсолютные `configs`, без sites. Укажите все связанные рабочие CONFIG из import; related вне набора даёт отказ. Legacy request schema 1 с пустым sites допустим, но непустой sites новый export отклоняет. Runtime продукта и сеть не запускаются.
```json
{"schema_version":2,"configs":["/absolute/scope/config.yaml"]}
```

```sh
spec-audit bundle export REQUEST OUT.tar.gz
spec-audit bundle verify OUT.tar.gz
spec-audit bundle restore OUT.tar.gz NEW_DIR
```

Новая запись `schema_version: 3`: gzip/tar содержит index.json и objects/SHA256. Index перечисляет логические области (configs/workspace/reports; без site), пути, точные размеры/hashes/modes и исходные настройки. bundle_id — digest логического index с пустым ID; archive_sha256 — digest конкретного физического архива. Общие bytes хранятся один раз, разные события не сливаются. Области источников содержат только переданный срез, не всю .git/зависимости проекта. Git-происхождение и точные сохранённые code/tests остаются доступными без Git checkout.

Сохраняются данные аудита: accepted ledger, extraction/reconcile raw, история run/попыток/ролей/receipts/reviews/checks, исходные JSON/config, описи и необходимые source-объекты archive. Производные report/viewer HTML/bin, publication manifests и publication-only objects не входят в новый bundle; неизвестные данные сохраняются. Эфемерные штатные dot-lock файлы не являются историей. Неполная старая quote-only история перечисляется в limitations; отсутствие текущего источника старого локального run не превращается в полный новый run. Повреждение общего объекта, потеря зависимости, чужая схема, опасный путь, повтор объекта/пути или gzip checksum дают отказ до создания NEW_DIR.

Restore требует отсутствующий NEW_DIR. Данные устанавливаются атомарно через staging. `areas/` содержит исходные bytes, в том числе оригинальные YAML и JSON-отчёты; `configs/` — отдельные рабочие YAML с перенесёнными физическими корнями, related и baseline. Ответ перечисляет configs, пустой sites для schema 3, bundle_id, archive_sha256 и limitations. После restore создайте нужный сайт через publish. Логические snapshot/accepted ID сохраняются. Незавершённый run остаётся незавершённым; для полного восстановленного индекса можно штатно выполнить prepare. Restore привязывает runtime-корень к восстановленному выбранному дереву и не обращается к старому checkout. Дальнейший тестовый runtime может требовать заново разрешённой среды/зависимостей, которые не входили в выбранный срез. Restore не исполняет продукт для их проверки.

## Проверяемая очистка

```sh
spec-audit cleanup plan ARCHIVE > PLAN
spec-audit cleanup verify ARCHIVE PLAN > PROOF
spec-audit cleanup apply ARCHIVE PLAN PROOF
```

Plan не удаляет файлы. Без дополнительных параметров cleanup сохраняет очистку доказанных dispatch-дублей с общей защитой физических потребителей всех областей. Опасный прежний план не проходит повторную проверку, даже если у него есть PROOF. Локальные пути specs/references/code/tests защищены и в режиме citations без workspace-копии. `cleanup plan ARCHIVE --publications ID...` выбирает старые управляемые копии сайтов. План schema 2 связывает точный backup с полной live-описью и метаданными публикаций; сохранять сам сайт в backup не требуется. В пакете schema 3 вложенные reports_dir из выбранных CONFIG учитываются отдельными областями. Export и cleanup не обходят дочернее хранилище внутри родительского; неизвестные вложенные каталоги сохраняются. Корень внутри настоящего run родителя вызывает отказ проверки владения. Автопоиск различает корневые manifest/state и повреждённый run после определения областей; признаки сохранённого аудита не подавляются найденным вложенным run или отсутствием manifest. Verify независимо восстанавливает аудит и генерирует обычное/историческое представление. Apply удаляет выбранные manifests, затем только их объекты без оставшихся source/publication-потребителей. Физические пути сохраняемых файлов всех областей и исходных CONFIG защищают кандидаты, даже если область и логический путь отличаются. Сохраняемый manifest защищает все свои объекты. Одинаковые bytes по разным физическим путям не запрещают удаление дубля. Verify/apply сверяют состав, bytes и mode выбранных деревьев исходников. Неизвестные файлы, raw, решения, исходники и OUT хостинга не удаляются. Посторонняя пара report.json/report.html сохраняется в backup и после очистки, если она не принадлежит настоящему прямому run. После прерывания тот же plan/proof допускает только уже выполненные удаления; новые зависимости требуют нового плана. PLAN ограничен 256 MiB с учётом форматирования CLI; генератор и verify/apply используют один лимит. Manifest и PROOF сохраняют лимит 32 MiB. Эксплуатационный apply требует отдельного решения оператора.

Verify независимо восстанавливает пакет во временный пустой каталог, сверяет все bytes, индекс и переносимость текущего snapshot. Proof связан с точным планом/архивом/версией инструмента. Apply повторяет восстановление и полную сверку состояния, исключает конкурентные операции поддерживаемого CLI общей блокировкой рабочей области, проверяет каждый кандидат перед удалением и сохраняет последнюю восстановимую копию. Новые файлы/ссылки/зависимости, изменённый план, порча архива и занятая область блокируют действие. Прямой сторонний процесс должен соблюдать эту блокировку; это не ОС-песочница против произвольной записи в каталог. Повтор допускает уже удалённые кандидаты и не затрагивает новый состав. stdout — машиночитаемый JSON результата с plan_id, bundle_id, removed/skipped, freed_bytes и complete/errors. При частичном отказе CLI сохраняет этот JSON и возвращает ненулевой exit code. Schema 1 дополнительно возвращает archive_sha256 и restore_proof; в schema 2 точная связь с архивом и proof хранится в отдельных PLAN/PROOF. Сохраняйте все три документа с эксплуатационными свидетельствами.

В рамках разработки apply проверяется только на одноразовых fixture-каталогах. Реальное удаление, retention, независимые резервные копии, hosting/ACL и переключение источника истины принадлежат оператору.

## Проверки и границы поставки

`tool/testdata/external/` содержит синтетические A/B; `TestExternal*`, `TestBundle*`, `TestCleanup*`, `TestQuoteOnly*` проверяют механику. Роли и independent review в них — явно обозначенные детерминированные test data, не модельная приёмка продукта. Отдельный browser-check открывает опубликованные JSON/HTML/исходники через HTTP под base path. Сквозной прогон с фактической golden-поставкой producer выполняется отдельно: локальный fixture не выдаётся за интеграцию с работающим порталом.
