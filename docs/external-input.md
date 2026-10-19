# Внешний корпус: файловый обмен r1

spec-audit потребляет подготовленный корпус. Получение редакций, проверка автономности, выбор линии/комплекта, принятие для разработки и наблюдение живого источника принадлежат внешнему клиенту. Сетевой провайдер, credentials и API трекера в бинарнике отсутствуют. Markdown остаётся обычным самостоятельным текстом, без служебных вставок.

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

`external result` читает уже созданный report и возвращает schema_version=1, tool_version экспортёра, scope, run_id, snapshot_id, accepted_head, basis (project/corpus/line/spec_version/manifest hash/profile/request hash/code_basis/freshness/решения клиента), decision, report и paths. Полная структура аудита — прежний публичный report.json, включая tool_versions, роли/расхождения, ограничения, raw provenance и host decision basis; schema [external-result/1](schemas/external-result.schema.json) задаёт оболочку. Это публичный интерфейс, приватные journals клиенту читать не нужно.

Для внешнего входа publish сохраняет все доступные исторические решённые отчёты вместе с report.json/result.json и источниками. Адрес: `external/<hash(project,corpus)>/<scope>/<run>/report.html`. Scope разных проектов не смешиваются. История ссылается на сохранённое основание; новый незавершённый run не становится решением. Уже существующий исторический файл с другими bytes не перезаписывается: используйте новый OUT_DIR. Обычный локальный вход сохраняет режим последних отчётов §77; JSON и ресурсы этого последнего отчёта также копируются, чтобы его ссылки на сохранённые исходники работали. Сайт работает под произвольным base path; старые страницы явно являются статическими наблюдениями своего времени, без онлайн-проверки. Успешная внешняя публикация также сохраняется в рабочей области как отдельное событие с точными bytes; дальнейший export не зависит от сохранности её каталога хостинга.

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

Сохранённые `status`, `review` (включая citations), `report`, `overview` и `publish` работают после release и удаления тестовых входов. Нормы, вопросы и история не обнуляются. Без источников snapshot — stale/unavailable, drift — unchecked. Сохранённая external freshness остаётся наблюдением своего времени. `cite`, приём нового ответа, новый/инкрементальный анализ требуют доступного точного основания; отсутствие или подмена источника не маскируются цитатами.

Публичная оболочка результата имеет **schema_version=2**, `source_retention:"citations"`, `report_basis:"saved_run"`, `current_freshness:{status:"unchecked",reason:...}`. `report` и `decision` относятся к сохранённому run; `report.generated_at` указывает момент формирования. Их прежний `fresh/current` не означает наблюдение живого источника сейчас. Поля v1 остаются, новая [external-result/2](schemas/external-result-v2.schema.json) публикуется отдельно; [v1](schemas/external-result.schema.json) не расширяется молча. В `paths` находятся пути к report JSON/HTML и inventory; полные источники через них не выдаются.

Сайт сохраняет существующие GAP, историю внешних scope, цитаты, фильтры, поиск и навигацию. `publish` требует **отсутствующий NEW_OUT_DIR**, собирает сайт в staging и публикует rename; существующий каталог не изменяется при отказе. Archive и citations публикуются отдельно: смешанный сайт мог бы сохранить полные archive-источники в событии citations. Сайт статический, без сетевого наблюдения источников; для обновления используйте новый каталог публикации.

Пакет результатов имеет **schema_version=2**. Запрос export остаётся schema_version=1; ответ export/verify/restore возвращает фактическую версию пакета. Формат index/objects, размеры/hashes/modes, bundle_id и archive_sha256 остаются проверяемыми; политика каждой CONFIG зафиксирована в index и manifest. Для citations workspace отсутствует; экспорт внешней области требует release всех временных входов. Метаданные всех исторических входов сверяются с run; quote-only опись не принимается за прежнюю полную. В смешанном пакете archive-области продолжают сохранять/проверять полные исходники — такой пакет целиком не является пакетом без исходников. Дополнительные `sites` — явные данные оператора: для пакета без исходников передавайте только соответствующие публикации citations.

Restore в новый каталог сохраняет историю, цитаты, исходные metadata/config и созданные публикации без обращения к checkout. Отдельные рабочие configs привязаны к отсутствующему `unavailable/...` либо освобождённому управляемому tree; они не обращаются к прежнему runtime-корню. Результаты читаются, повторный анализ честно отказывает до новой привязки источников. Для нового внешнего анализа повторите import из CONFIG с доступным проверенным checkout и восстановленным reports_dir. `cleanup` дублей проверяет восстановимость результатов, не обещая восстановление отсутствующих исходников. Прежние bundle v1 и старые quote-only результаты читаются по прежним правилам.

## Компактный пакет состояния

Запрос export (schema_version=1) содержит массивы абсолютных `configs` и `sites`. Укажите все связанные рабочие CONFIG, возвращённые import (`related` вне набора даёт отказ); ссылки related должны вести к выбранным рабочим конфигурациям соседних scope, а не к шаблонам без external.input. Дополнительные каталоги sites включаются побайтово; успешные внешние публикации включаются автоматически из сохранённых событий, даже если исходный каталог сайта уже исчез. Runtime продукта и сеть при экспортировании/восстановлении не запускаются.

```json
{"schema_version":1,"configs":["/absolute/scope/config.yaml"],"sites":[]}
```

```sh
spec-audit bundle export REQUEST OUT.tar.gz
spec-audit bundle verify OUT.tar.gz
spec-audit bundle restore OUT.tar.gz NEW_DIR
```

Формат `schema_version: 1`: gzip/tar содержит index.json и objects/SHA256. Index перечисляет логические области (configs/workspace/reports/site), пути, точные размеры/hashes/modes и исходные настройки. bundle_id — digest логического index с пустым ID; archive_sha256 — digest конкретного физического архива. Общие bytes хранятся один раз, разные события не сливаются. Области источников содержат только переданный срез, не всю .git/зависимости проекта. Git-происхождение и точные сохранённые code/tests остаются доступными без Git checkout.

Сохраняются полный reports_dir с accepted ledger, extraction/reconcile raw, история run/попыток/ролей/receipts/reviews/checks, исходные JSON/config и уже созданные HTML/JSON/ресурсы. Эфемерные штатные dot-lock файлы не являются историей. Неполная старая quote-only история перечисляется в limitations; отсутствие текущего источника старого локального run не превращается в полный новый run. Повреждение общего объекта, потеря зависимости, чужая схема, опасный путь, повтор объекта/пути или gzip checksum дают отказ до создания NEW_DIR.

Restore требует отсутствующий NEW_DIR. Данные устанавливаются атомарно через staging. `areas/` содержит исходные bytes, в том числе оригинальные YAML и прежний HTML; `configs/` — отдельные рабочие YAML с перенесёнными физическими корнями, related и baseline. Ответ перечисляет configs/sites, bundle_id, archive_sha256 и limitations. Логические snapshot/accepted ID сохраняются. Незавершённый run остаётся незавершённым; для полного восстановленного индекса можно штатно выполнить prepare. Restore привязывает runtime-корень к восстановленному выбранному дереву и не обращается к старому checkout. Дальнейший тестовый runtime может требовать заново разрешённой среды/зависимостей, которые не входили в выбранный срез. Restore не исполняет продукт для их проверки.

## Проверяемая очистка

```sh
spec-audit cleanup plan ARCHIVE > PLAN
spec-audit cleanup verify ARCHIVE PLAN > PROOF
spec-audit cleanup apply ARCHIVE PLAN PROOF
```

Plan не удаляет файлы. Он перечисляет точное состояние, archive/bundle/plan IDs, кандидатов, hashes/размеры, класс, причину, потребителей, сохранённый raw и ожидаемое освобождение bytes. Текущая поддерживаемая очистка намеренно ограничена побайтовым дублем `dispatch/<task>/result.json`, когда весь run доставлен и согласован, а исходный raw той же попытки сохранён отдельно. Другие/неизвестные файлы не становятся кандидатами по имени, возрасту или похожему содержанию. Открытые задания, источники, ledger, решения и публикации не удаляются.

Verify независимо восстанавливает пакет во временный пустой каталог, сверяет все bytes, индекс и переносимость текущего snapshot. Proof связан с точным планом/архивом/версией инструмента. Apply повторяет восстановление и полную сверку состояния, исключает конкурентные операции поддерживаемого CLI общей блокировкой рабочей области, проверяет каждый кандидат перед удалением и сохраняет последнюю восстановимую копию. Новые файлы/ссылки/зависимости, изменённый план, порча архива и занятая область блокируют действие. Прямой сторонний процесс должен соблюдать эту блокировку; это не ОС-песочница против произвольной записи в каталог. Повтор допускает уже удалённые кандидаты и не затрагивает новый состав. stdout — машиночитаемый журнал результата с plan/archive IDs, removed/skipped, freed_bytes, complete/errors и restore_proof (при частичном отказе сохраняется этот JSON и возвращается ненулевой exit code); сохраняйте его с эксплуатационными свидетельствами.

В рамках разработки apply проверяется только на одноразовых fixture-каталогах. Реальное удаление, retention, независимые резервные копии, hosting/ACL и переключение источника истины принадлежат оператору.

## Проверки и границы поставки

`tool/testdata/external/` содержит синтетические A/B; `TestExternal*`, `TestBundle*`, `TestCleanup*`, `TestQuoteOnly*` проверяют механику. Роли и independent review в них — явно обозначенные детерминированные test data, не модельная приёмка продукта. Отдельный browser-check открывает опубликованные JSON/HTML/исходники через HTTP под base path. Сквозной прогон с фактической golden-поставкой producer выполняется отдельно: локальный fixture не выдаётся за интеграцию с работающим порталом.
