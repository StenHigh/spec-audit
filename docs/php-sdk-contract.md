[← Контракт ядра](tool-spec.md) · [README](../README.md) · [Принятый индекс →](accepted-index.md)

# PHP/Laravel SDK: типизированные факты PHPStan/Larastan

Версия контракта: 1 (формат `sdk/3`). Статус: контракт расширения [§17 спецификации инструмента](tool-spec.md#17-php-и-laravel-sdk), подготовлен до кода и подтверждён владельцем; п.1–3 §17.2 реализованы и приняты 2026-09-16 на синтетических примерах, п.4 выполнен один раз с нейтральным результатом (числа — в README). Владелец — StenHigh. Известное расхождение с контролем: Larastan разрешает `app(Contract::class)` в привязанную реализацию. Frozen §1–10, `php-facts`/`sdk/2`, контракты принятого индекса и legacy-извлечения не изменяются. Пункты с пометкой «подтверждено spike 2026-09-16» проверены ручным прогоном черновика экспортёра в проверенном контейнере пилота (PHP 8.4, PHPStan 2.2.x, Larastan 3.10.x); результаты хранятся только в локальной рабочей области инструмента.

## Назначение и границы

Роли аудита ищут реализацию нормы и связанные тесты по исходникам. Когда реализация спрятана за интерфейсом, общим сервисом или механизмом фреймворка, поиск по имени теряет типовой контекст. SDK возвращает адресуемые объявления и вызовы методов с доступной типовой информацией и происхождением, чтобы хост и роли быстрее находили конкретные методы и кандидатов тестов. Оценка выполнения бизнес-требования и достаточности assertions остаётся за ролями и хостом; факты SDK — подсказки, не связи и не свидетельства соответствия.

SDK — адаптер проекта: PHPStan даёт типы и статически разрешённые цели вызовов, Larastan — Laravel-типы поддерживаемых механизмов фреймворка, Go-ядро проверяет происхождение и хранит результат. В ядре нет бизнес-норм, путей и правил конкретного проекта; профиль и таймаут задаёт CONFIG. Не строятся: граф вызовов, автоматическая связь факт ↔ норма, автоматический PASS, plugin registry, второе ядро, кеш между запусками.

`php-typed` выполняется только по явному разрешению хоста (REQ-SA-034). Для профиля `laravel` требуется отдельное разрешение framework bootstrap: анализатор исполняет код приложения в контейнере. Разрешение на `php-facts` или `test` на `php-typed` не распространяется. Бинарник разрешение не проверяет; порядок запроса и подтверждения изоляции задаёт поставляемый skill.

Порядок работ [§17.2](tool-spec.md#172-порядок-реализации-и-приёмка) п.2 изменён решением владельца 2026-09-16: пилотный проект используется как среда отладки экспортёра с первой фазы, а приёмка SA-031…034 выполняется только на независимых синтетических PHP/Laravel-примерах. Спецификация не редактируется; наличие Larastan в чужом проекте не закрывает приёмку.

## Команда и CONFIG

CLI: `php-typed CONFIG RUN_ID FILE...` — 1–64 относительных путей к `.php`-файлам kind `code` или `tests` из manifest выбранного run, без повторов. Команда требует существующий run, свежий snapshot, `runtime.kind: docker-php` и блок `sdk`. Прежняя команда `php-facts` не меняется и остаётся `syntax_only`.

Ответ (один JSON в stdout): `{"snapshot_id", "artifact", "evidence_kind": "typed", "facts": N, "diagnostics": M}`. `artifact` — абсолютный путь к сохранённому envelope `reports_dir/RUN_ID/sdk-typed-<rand>.json`; `diagnostics` — число сообщений PHPStan помимо сообщения экспортёра, справочное значение, в state не сохраняется. Ошибка — non-zero exit и JSON error в stderr, фиксированная русская фраза без содержимого stdout/stderr контейнера и без путей проекта.

Блок CONFIG (необязательный; отсутствие сохраняет прежний `snapshot_id`):

```yaml
runtime: {kind: docker-php, service: app}
sdk:
  profile: laravel        # php | laravel, обязателен
  timeout_seconds: 300    # 1–900, по умолчанию 300 (spike: 64 файлов ≤ 8 с, ≤ 330 MB); отдельный от runtime.timeout_seconds (§8, 1–120)
```

Правила: блок допустим только при `runtime.kind: docker-php`; неизвестные ключи отклоняются (`KnownFields`); ключей `env`, `config`, `memory_limit` нет — предел памяти PHPStan задаётся константой `2G`, include проектного neon и переопределения окружения не поддерживаются до отдельной редакции контракта с подтверждённой потребностью (spike 2026-09-16: include проектного neon дал те же факты и нулевую пользу для SDK). Блок входит в `Config`, поэтому появляется в `manifest.json` внутри `config` и меняет `snapshot_id` run.

Предусловия по manifest: `composer.lock` присутствует в manifest run как файл-источник (например, перечислен в `code.paths`; если `code.include` непуст, он должен содержать паттерн `composer.lock` рядом с `*.php`, так как include сопоставляется только с именем файла; kind записи не важен). Для профиля `laravel` — также `bootstrap/app.php`, и фактически все файлы, которые bootstrap подключает (`bootstrap/`, включая `bootstrap/cache/*.php`, `config/`, провайдеры, миграции), должны входить в выбранные источники — иначе анализ отказывает по правилу основания ниже. Отказы предусловий: «composer.lock не входит в snapshot», «bootstrap/app.php не входит в snapshot».

## Запуск в контейнере

Контейнер проверяется прежней процедурой §8: ровно один работающий контейнер Compose-сервиса, фактический mount `project_root → /var/www/html`, отсутствие подменяющих mounts под выбранными источниками. Host PHP не используется; Composer не вызывается; проектный CI, `phpstan.neon*`, baseline и исходники не изменяются.

Рабочая область — уникальный каталог `/tmp/spec-audit-<rand>/` внутри контейнера (решение владельца 2026-09-16: для файлов внутри контейнера это разрешённая audit-область, аналог JUnit-файла PHPUnit в §8). В неё записываются `sdk-typed.php` и `phpstan.neon`, в `tmp/` — кеш PHPStan. После запуска каталог удаляется отдельным вызовом; неудача очистки — WARN и limitation, не подмена результата.

Последовательность (каждый служебный шаг — отдельный `docker exec` с внутренним `timeout 5` и уникальным маркером-комментарием; PHP-код служебных шагов не подключает проект):

1. Preflight `php -r`: `is_file('vendor/bin/phpstan')`; для `laravel` — `is_file('vendor/larastan/larastan/extension.neon')`, `is_file('bootstrap/app.php')`, `!is_file('bootstrap/cache/config.php')`. Отказы: «PHPStan не установлен в контейнере», «Larastan не установлен для профиля laravel», «закешированный config (bootstrap/cache/config.php) блокирует изоляцию». Ничего не устанавливается.
2. Setup: два `php -r`, создающие каталог (`0700`) и записывающие `sdk-typed.php` и `phpstan.neon` из stdin.
3. Analyse:

   ```text
   timeout -s TERM -k 1 <sdk.timeout_seconds> php -d display_errors=stderr vendor/bin/phpstan analyse \
     --error-format=json --no-progress --no-interaction --no-ansi --memory-limit=2G \
     --autoload-file /tmp/spec-audit-<rand>/sdk-typed.php \
     -c /tmp/spec-audit-<rand>/phpstan.neon -- FILE...
   ```

   Явные файлы в аргументах заменяют `paths` и отключают result cache PHPStan. Классы экспортёра загружаются через `--autoload-file`, потому что контейнер DI компилируется до исполнения `bootstrapFiles` и требует существования классов сервисов. `-d display_errors=stderr` — дублирующая страховка: PHPStan сам направляет ошибки PHP в stderr.
4. Cleanup: рекурсивное удаление рабочей области — отдельным контекстом 5 с, в том числе после отказа или таймаута analyse.
5. Повторный snapshot проекта: расхождение с исходным — отказ «снимок изменился во время SDK» без сохранения результата.

Окружение процесса (`docker exec -e`): всегда `XDEBUG_MODE=off`. Для профиля `laravel` дополнительно константа ядра `laravelIsolationEnv`, отводящая bootstrap от рабочих сервисов и делающая любое обращение к ним ошибкой (fail-fast), а не соединением с настоящей БД:

```text
DB_CONNECTION=spec_audit_disabled DB_URL= DATABASE_URL= REDIS_URL= DB_SOCKET=
DB_HOST=127.0.0.1 DB_PORT=1 REDIS_HOST=127.0.0.1 REDIS_PORT=1
CACHE_STORE=array CACHE_DRIVER=array QUEUE_CONNECTION=sync SESSION_DRIVER=array
MAIL_MAILER=array BROADCAST_CONNECTION=null BROADCAST_DRIVER=null LOG_CHANNEL=stderr
APP_CONFIG_CACHE=/tmp/spec-audit-none/config.php
```

`DB_SOCKET=` закрывает обход host/port через unix-сокет mysql/mariadb; `APP_CONFIG_CACHE` с несуществующим абсолютным путём заставляет Laravel читать `config/*.php` с переменными выше, даже если окружение контейнера задаёт собственный путь кеша (preflight по `bootstrap/cache/config.php` остаётся явным сигналом оператору).

Переменные процесса имеют приоритет над `.env` проекта; закешированный `config.php` их игнорирует, поэтому preflight отказывает при его наличии. Имена соответствуют skeleton Laravel 11+ и legacy-именам ≤10; проектные `config/*.php` могут читать иные ключи — это константная limitation. Строку `null` Laravel читает как PHP `null`, поэтому обращение к broadcast даёт исключение — намеренный fail-fast. Канал `stderr` должен существовать в `config/logging.php`; иначе Laravel пишет emergency-лог в `storage/logs`, что не обнаруживается, если `storage` вне источников.

Изоляция и её пределы. По REQ-SA-034 обращения к рабочим БД, очередям и внешним сервисам должны быть исключены. Переменные выше закрывают штатные драйверы, но средствами `docker exec` инструмент не ограничивает сеть контейнера: сетевая изоляция (например `network_mode: none` или эквивалент) — обязанность оператора и часть разрешения хоста на bootstrap, как ответственность оператора за тестовую среду в §8. Запуск без сетевой изоляции допускается только по явному решению владельца проекта, зафиксированному в разрешении. Константные limitations отчёта: «сеть не изолирована инструментом»; «mount проекта доступен на запись — запись в выбранные источники обнаруживается повторным snapshot, запись вне них (например `storage/`) не обнаруживается»; «рабочая область удаляется best-effort, неудача очистки — предупреждение в журнале»; «имена переменных изоляции соответствуют skeleton Laravel 11+/legacy»; «PHPStan выполняет анализ в отдельном worker-процессе и повторяет bootstrap в главном процессе перед правилами на собранных данных — двойной bootstrap ожидаем; при таймауте останавливается главный процесс, осиротевший worker не отслеживается». Проект отвечает за актуальные `bootstrap/cache/packages.php|services.php`: их перезапись при bootstrap — ожидаемый отказ «снимок изменился», не дефект инструмента.

Таймауты и коды. Внешний контекст `sdk.timeout_seconds + 20` секунд покрывает проверку контейнера, preflight, два setup и analyse (служебные шаги дополнительно ограничены 5 с внутри контейнера); cleanup — отдельный контекст и выполняется после отказов. Истечение внешнего контекста или коды `124`/`137` — отказ «таймаут SDK». Код `1` с присутствующим сообщением экспортёра — успех (экспортёр отдаёт результат как ошибку PHPStan). Код `0`, отсутствие сообщения с идентификатором `specAudit.envelope`, непустой массив `errors` верхнего уровня, пустой или не-JSON stdout, любой другой код — отказ «экспорт SDK не завершён». Усечение stdout или stderr по лимиту 4 MiB — отказ «вывод SDK превышает лимит; сократите выбор файлов». Успешный пустой результат без сообщения экспортёра невозможен.

## Neon-обёртка

Шаблон с плейсхолдерами. `{{includes}}` — для `laravel` без `phpstan/extension-installer` строка `includes:\n    - /var/www/html/vendor/larastan/larastan/extension.neon\n`; для `php` и для проектов, где `composer.lock` содержит `phpstan/extension-installer`, — пустая строка (секция отсутствует: установщик подключает расширения сам, повторный include PHPStan отклоняет как дубликат). `{{migrations}}`/`{{schema}}` — `%databaseMigrationsPath%`/`%squashedMigrationsPath%` для `laravel`, `[]` для `php`. Значения строк подставляются в одинарных кавычках.

```neon
{{includes}}parameters:
    level: 0
    tmpDir: '{{tmpdir}}/tmp'
    reportUnmatchedIgnoredErrors: false
    parallel:
        maximumNumberOfProcesses: 1
    specAudit:
        profile: '{{profile}}'
        sdkSha256: '{{sdk_sha256}}'
parametersSchema:
    specAudit: structure([profile: string(), sdkSha256: string()])
services:
    -
        class: SpecAudit\FactCollector
        tags: [phpstan.collector]
    -
        class: SpecAudit\FactSink
        arguments:
            allConfigFiles: %allConfigFiles%
            bootstrapFiles: %bootstrapFiles%
            scanFiles: %scanFiles%
            scanDirectories: %scanDirectories%
            migrationPaths: {{migrations}}
            schemaPaths: {{schema}}
            profile: %specAudit.profile%
            sdkSha256: %specAudit.sdkSha256%
        tags: [phpstan.rules.rule]
```

Скаляры обёртки переопределяют включённые файлы (`tmpDir`, `level`, `parallel`), поэтому штатный `tmpDir` проекта не используется. `sdk_sha256` — SHA-256 конкатенации встроенных `sdk-typed.php` и шаблона neon; экспортёр возвращает его в envelope, Go сверяет с собственной константой. Инъекция параметров `%allConfigFiles%`, `%bootstrapFiles%`, `%scanFiles%`, `%scanDirectories%`, `%databaseMigrationsPath%`, `%squashedMigrationsPath%` и autowire `StubFilesProvider` — подтверждено spike 2026-09-16.

## Экспортёр

`SpecAudit\FactCollector` — один Collector с `getNodeType(): Node::class` и диспетчеризацией по типу узла:

| Узел | Факт |
| --- | --- |
| `PHPStan\Node\FileNode` | файл учтён как проанализированный |
| `PHPStan\Node\InClassMethodNode` | `syntax: Stmt_ClassMethod`, `resolution: declared`; target — прототип метода (`getPrototype()->getDeclaringClass()`), если он объявлен в другом классе/интерфейсе |
| `Expr\MethodCall`, `Expr\StaticCall`, `Expr\NullsafeMethodCall` | вызов; имя не `Identifier` → `resolution: dynamic`, `targets: []` |

Разрешение вызова. Тип получателя: `getType($var)` для объектного вызова (для nullsafe — без `null`); для статического вызова с классом-`Name` — `resolveTypeByName`, с классом-выражением — `getType($class)->getClassStringObjectType()`. Для каждого класса `C` из `getObjectClassReflections()` при `hasMethod($name)` берётся `M = getMethod($name, $scope)`, `D = M->getDeclaringClass()`. Target: если `D->hasNativeMethod($name)`, `file`/`line` — из `D->getNativeReflection()->getMethod($name)` (`getFileName()`, `getStartLine()`; для трейтов это файл трейта); если `D->isBuiltin()`, файл нативного метода отсутствует или лежит внутри phar анализатора (stubs) — `native: true`, `file: ""`, `line: 0`; если нативного метода нет (метод предоставлен расширением: `@method`, Eloquent scope/builder, макрос) — `file: ""`, `line: 0`, `native: false`. `interface = D->isInterface() || M->isAbstract()`. Различные объявления считаются по паре (`class`, `method`). Число различных: 0 → `unresolved`; 1 → `resolved`; ≥2 → `ambiguous`; если все targets имеют `file == ""` и `native == false` — `virtual`. `origin: larastan`, если класс объекта `M` или его прототипа находится в пространстве имён `Larastan\`; иначе `phpstan`. Статический вызов фасада Laravel с `@method`-докблоком даёт `virtual` с `origin: phpstan` (аннотации), методы Eloquent Builder — `virtual` с `origin: larastan`, `@method` Carbon — `virtual`/`phpstan`; фасад без докблока может дать `resolved` на метод базового класса под `vendor/`. Ограничение атрибуции фиксируется в контроле SA-031 как допустимый диапазон (подтверждено spike 2026-09-16). Декларация интерфейса или абстрактного класса — target с флагом `interface`, а не «единственная runtime-реализация».

Путь и цитата. Путь цитаты — файл узла: внутри трейта — файл трейта (`$scope->getTraitReflection()->getFileName()`), иначе `$scope->getFile()`, относительно `/var/www/html`; факт экспортируется только если этот файл входит в `files`. Цитата — полные строки исходника: для декларации — строка идентификатора имени метода (не строка атрибутов `#[...]`); для вызова — строки от начала до конца выражения, не более 8; если выражение длиннее 8 строк, цитируется одна строка идентификатора имени метода (`line_start = line_end`), факт не отбрасывается. Файл, содержимое которого не является валидным UTF-8 целиком, не даёт фактов (Go сверяет цитаты через `lineQuote`, требующий валидного UTF-8 всего источника); файл остаётся в `files`. Одинаковые факты (трейт в контексте нескольких классов) схлопываются по (`citation`, `syntax`, `name`, `targets`). Экспортёр читает исходники только для цитат и хэшей, не подключает их.

`SpecAudit\FactSink` — правило на `CollectedDataNode`. Формирует envelope и выдаёт ровно одно сообщение `RuleErrorBuilder::message(<JSON>)->identifier('specAudit.envelope')->nonIgnorable()`. Go находит сообщение по идентификатору в любом элементе `files.*.messages[]` и берёт его строку как сырой envelope. Прочие сообщения PHPStan считаются в `diagnostics`, не сохраняются и к нормам не привязываются: lint-диагностика не становится GAP ТЗ. Экспортёр ничего не усекает: превышение любого лимита ниже отклоняет Go.

Детерминизм: `files`, `basis`, `bootstrap_files` отсортированы по `path`; `facts` — по (`citation.path`, `line_start`, `line_end`, `syntax`, `name`, `class` первого target). Go сравнивает множества и порядок не требует; сортировка нужна для стабильных фикстур.

## Формат `sdk/3`

Все поля обязательны, `null` и неизвестные поля отклоняются, пустые массивы допустимы. Все `sha256` — 64 символа hex в нижнем регистре (`hash('sha256', …)` без флагов).

```json
{
  "version": "sdk/3",
  "evidence_kind": "typed",
  "profile": "php|laravel",
  "runtime": {"php": "8.4.x", "os": "Linux", "arch": "aarch64", "composer_lock_sha256": "<hex>", "sdk_sha256": "<hex>"},
  "files": [{"path": "app/Service.php", "sha256": "<hex>", "bytes": 1234}],
  "bootstrap_files": ["vendor/larastan/larastan/bootstrap.php"],
  "basis": [{"path": "config/app.php", "sha256": "<hex>", "bytes": 512}],
  "facts": [
    {
      "citation": {"path": "app/Service.php", "line_start": 42, "line_end": 43, "quote": "…"},
      "syntax": "Expr_MethodCall",
      "name": "save",
      "origin": "phpstan",
      "resolution": "resolved",
      "receiver_type": "App\\Repository",
      "targets": [{"class": "App\\Repository", "method": "save", "file": "app/Repository.php", "line": 17, "interface": false, "native": false}]
    }
  ]
}
```

Нормализация путей экспортёром (общая для `files`, `bootstrap_files`, `basis`, `targets[].file`): префикс `phar://` отбрасывается; путь с сегментами `..`/`.` приводится `realpath` (composer autoload отдаёт `vendor/composer/../laravel/...`); путь под `/var/www/html/` записывается относительно него; пути рабочей области `/tmp/spec-audit-<rand>/` (обёртка, `sdk-typed.php`) и файлы внутри phar PHPStan не включаются — они покрыты `sdk_sha256` и версией `phpstan/phpstan` из `composer.lock`. Для `files`, `bootstrap_files` и `basis` иные пути вне mount не включаются, и Go отклоняет любую запись, не удовлетворяющую `localPath`; для `targets[].file` абсолютный путь вне mount без `..` допускается только для показа человеку («вне snapshot»), объявления внутри phar дают `native: true`.

Правила полей:

- `files` — проанализированные PHPStan файлы (`FileNode`), их SHA-256 и размер; множество путей обязано совпадать с запрошенным `FILE...`: пропущенный файл — отказ «SDK не проанализировал часть выбранных файлов», лишний или повторный — «SDK вернул другое множество файлов».
- `bootstrap_files` — значение `%bootstrapFiles%` после нормализации. Профиль `php`: массив пуст, иначе отказ «конфигурация подключает bootstrap вне профиля» (проект с `phpstan/extension-installer` и расширениями с bootstrap для профиля `php` неприменим — ожидаемый отказ). Профиль `laravel`: обязан содержать `vendor/larastan/larastan/bootstrap.php`; дополнительные записи допустимы только под `vendor/` (например bootstrap других расширений, подключённых установщиком) и перечисляются в записи SDK как limitation «дополнительные bootstrap расширений»; запись вне `vendor/` — отказ.
- `basis` — основание анализа после нормализации: `%allConfigFiles%`, `bootstrap_files`, stubs из `StubFilesProvider`, `%scanFiles%`, `%scanDirectories%`, для `laravel` — файлы миграций (`*.php` рекурсивно из `databaseMigrationsPath`, при пустом значении — `database/migrations`, как в Larastan) и схем (`*.sql`, `*.dump` из `squashedMigrationsPath`, при пустом — `database/schema`), а также `get_included_files()` под `/var/www/html` вне `vendor/` после анализа. Не более 4096 записей. Каждый путь либо начинается с `vendor/` (покрыт `composer.lock`), либо присутствует в manifest run с тем же SHA-256; иначе отказ «основание анализа вне snapshot; добавьте файлы в code.paths». Отдельного хэша основания нет: актуальность обеспечивается свежестью snapshot. Для профиля `php` основание обычно пусто (обёртка и phar исключены, проект не подключается); для `laravel` в него входят провайдеры, конфиги, маршруты, `bootstrap/*`, миграции и stubs Larastan (подтверждено spike 2026-09-16).
- `facts[].citation` — как `Citation` ядра: `1 ≤ line_start ≤ line_end`, `line_end − line_start ≤ 7`, путь ∈ `files`, `quote` равен строкам `line_start..line_end` исходника по правилу `lineQuote`: источник без одного завершающего `\n` делится по `\n`, берутся строки диапазона, соединяются `\n`, у результата удаляется один завершающий `\r`.
- `syntax` ∈ {`Stmt_ClassMethod`, `Expr_MethodCall`, `Expr_StaticCall`, `Expr_NullsafeMethodCall`}; `origin` ∈ {`phpstan`, `larastan`}; `resolution` ∈ {`declared`, `resolved`, `ambiguous`, `unresolved`, `virtual`, `dynamic`}; `name` непустой (для `dynamic` — `{dynamic}`); `receiver_type` — `Type::describe(VerbosityLevel::typeOnly())` получателя, в том числе при `dynamic`; пуст только при `declared` (Go проверяет).
- `targets[]` ≤ 16; `file` — относительный путь под `/var/www/html` (`localPath`), абсолютный путь вне mount (показывается человеку с пометкой «вне snapshot») или `""`; `line ≥ 0`; `line > 0 ⇔ file != ""`. Инварианты: `dynamic`/`unresolved` ⇒ `targets` пуст; `virtual` ⇒ ≥1 target, у всех `file == ""`, `line == 0`, `native == false`; `resolved` ⇒ ровно один target с (`file != ""` и `line > 0`) либо `native: true`; `ambiguous` ⇒ ≥2 targets; `declared` ⇒ ≤1 target (прототип). Цитат у targets нет.
- Лимиты: `files` 1–64, `facts` ≤ 20000, `targets` ≤ 16 на факт, `basis` ≤ 4096, строки `name`/`class`/`method`/`file` ≤ 512 байт, `receiver_type` ≤ 1024, `quote` ≤ 8 строк. Превышение любого из них — отказ «вывод SDK превышает лимит; сократите выбор файлов»; неверные значения (пустое имя, нарушение инвариантов, несовпадение цитаты) — отдельные фразы отказа.
- Envelope с сообщением экспортёра, `files` = запрошенному множеству и `facts: []` — успех с ответом `"facts": 0`; пустой результат считается отказом только при отсутствии сообщения экспортёра.

Проверки Go до записи: строгий разбор (без дублей ключей и неизвестных полей, все поля присутствуют); `version`, `evidence_kind`, `profile` = профилю CONFIG; `runtime.os == "Linux"`, `php ≥ 8.2` по первым двум числовым компонентам; `sdk_sha256` совпадает с константой бинарника; `composer_lock_sha256` равен SHA-256 `composer.lock` из manifest; `files` перечитываются из `project_root` и сверяются с manifest по SHA-256 и размеру; каждая цитата сверяется с исходником; `localPath` для всех путей; инварианты и лимиты выше. Ошибка любой проверки — отказ без сохранения; текст ошибки не содержит данных envelope.

## Совместимость версий

Go извлекает версии из уже прочитанного `composer.lock` (разделы `packages` и `packages-dev`): `phpstan/phpstan` — версия по регулярному выражению `^v?2\.` (проверено на 2.2.x); `larastan/larastan` — `^v?3\.` (проверено на 3.10.x), обязателен только для `laravel`; PHP ≥ 8.2 по `runtime.php`. Иная major-версия, иной формат версии (`dev-*`) или отсутствие пакета в `composer.lock` — отказ «несовместимая версия анализатора»; глобальные установки и phar вне Composer не принимаются. Версия `nikic/php-parser` не проверяется: PHPStan использует встроенный в phar парсер. Версии сохраняются в записи SDK и показываются в отчёте.

## Хранение, задания и отчёт

Артефакт `sdk-typed-<rand>.json` (режим `0400`, атомарная запись) — сырые байты сообщения экспортёра; его SHA-256 хранится в state. Чтение артефакта ограничено 4 MiB (`maxResult`): он не может превышать stdout analyse.

`state.json` получает необязательный массив `sdk` записей `{"artifact", "sha256", "phpstan_version", "larastan_version", "recorded_at"}`: `artifact` — имя файла без каталогов; `recorded_at` — UTC в формате RFC3339Nano, как у receipt; для run без SDK массив отсутствует, байты прежних state не меняются. При чтении run любой командой проверяются имя артефакта, формат хэша и совпадение SHA-256 содержимого; расхождение — отказ «повреждена запись SDK», как для повреждённого raw. Повторный `php-typed` добавляет новую запись; прежние остаются историей. Артефакты `php-facts` (`sdk/2`) в state не регистрируются и задним числом не обогащаются.

Запись в state меняет основание согласования хоста: текущий review становится `outdated`, повторное решение с прежним `basis_sha256` отклоняется. Поэтому факты импортируются до `review`. Подача ответов ролей не блокируется.

`TaskBatch` получает тот же массив `sdk` (только записи, без фактов), где `artifact` — абсолютный путь `reports_dir/RUN_ID/<имя>`. Хост открывает артефакт последней записи, отбирает факты по разрешённым файлам роли и передаёт их роли как `SDK_HINTS` с пометкой: подсказки анализатора, не цитаты, не assertions, не доказательство `relevant`/`missing`; роль цитирует исходники сама. `Task`, `Result`, `Citation` не расширяются; `Manifest` не получает новых полей — блок `sdk` появляется внутри `config` и входит в `snapshot_id`.

В отчёте факты последней записи `sdk` видны как подсказки в карточке файла с бейджем «подсказка SDK», происхождением (`origin · resolution · name → targets`) и признаком актуальности (`fresh` snapshot); прежние записи показываются только в истории. Подсказки не входят в `Links`, не меняют `Unlinked`, `has_current_links` и метрики «код + достаточные тесты» §16.1; автосвязь факт ↔ норма не строится. Раздел истории показывает записи SDK (профиль, версии, хэши `composer.lock`/SDK, число файлов и фактов, limitations). При отсутствии записей отчёт явно сообщает: «SDK-факты не импортированы; связи только из цитат ролей и хоста». Если хэш артефакта верен, но содержимое не разбирается как `sdk/3` (например после изменения схемы бинарника), отчёт строится с limitation «артефакт SDK недоступен» и без подсказок.

## Поддерживаемые конструкции

Поддерживаются: объявления методов классов, интерфейсов и трейтов (методы трейтов и вызовы внутри них экспортируются, когда в `FILE...` входят и файл трейта, и хотя бы один использующий его класс — PHPStan анализирует тело трейта только в контексте класса); вызовы `$obj->m()`, `Class::m()`, `parent::m()`, `static::m()`, `self::m()`, `$cls::m()`, `$obj?->m()` с литеральным именем; получатели с типом объекта, union/intersection объектов, `static`/`$this`, generic-типами PHPStan; для `laravel` — типы Eloquent-моделей, отношений, builder/scope, коллекций, фасадов и `app()`/`resolve()` с константным классом в объёме возможностей Larastan.

Не поддерживаются и не выдаются за факты: вызовы функций, `new`, доступ к свойствам, callables/first-class callable syntax, `__call`/`__callStatic` без `@method`, макросы без стабов, callbacks внутри `with()`/`when()` и подобных, `make()`/`app()` с неконстантным аргументом, динамические имена методов (остаются `dynamic`), биндинги контейнера в runtime (интерфейс остаётся target с флагом `interface`). Неизвестность остаётся явной: `unresolved`/`ambiguous`/`virtual` не сводятся к одному файлу.

## Отрицательные ожидания приёмки

- SA-031: одноимённые методы разных классов дают разные targets по типу получателя; наследование указывает на объявление в родителе; интерфейс без известной реализации — `resolved` с `interface: true`, без выдуманной реализации; динамическое имя — `dynamic` без targets; внешний метод — target под `vendor/`; нативный — `native: true` без файла; `@method`, Eloquent scope, макрос — `virtual` без файла; фасад с `@method` — `virtual` (без файла), без докблока — `resolved` под `vendor/`; биндинг — интерфейс с флагом, по ожиданиям контроля.
- SA-032: тест с нужным вызовом и слабым assertion остаётся `weak`, метрики не растут; одноимённый посторонний метод не становится target; общий helper остаётся контекстом; подсказка и подтверждённая связь различимы в TaskBatch и HTML; без SDK прежний аудит работает с явной лимитацией.
- SA-033: подмена цитаты, строки, пути, `composer.lock`, `sdk_sha256`, неполный список `files`, основание вне snapshot, путь с `..`, отсутствующее сообщение экспортёра, непустой `errors`, чужой snapshot, тело `sdk/2` под видом `sdk/3`, нарушение инвариантов, лишнее/пропущенное поле, невалидный UTF-8, PHPStan 1.x или отсутствие пакета — отказ без записи; прежние run и raw не «зеленеют».
- SA-034: чужой mount, отсутствующий PHPStan, отсутствующий Larastan при `laravel` (при `php` — успех без bootstrap), bootstrap вне профиля, закешированный config, запись bootstrap в источники, обращение bootstrap к БД, ошибка bootstrap, таймаут, превышение вывода, внутренняя ошибка PHPStan — явный отказ; Composer не вызывается, установка не выполняется, рабочая область удаляется, в исходники проекта ничего не записывается.

## Приёмка

Семантические ожидания фиксируются до реализации в [независимом контроле](../acceptance/php-sdk-control.json) с хэшем; runtime-негативы проверяются Go-регрессиями на имитации Docker без исполнения PHP. Реальные прогоны экспортёра выполняются только в проверенных контейнерах: пилотный проект — для отладки по решению владельца, независимые синтетические PHP- и Laravel-примеры с собственным Compose — для приёмки. Прежние `acceptance` gold/хэши, `declared-v01`, старые raw, `php-facts`/`sdk/2` и §1–10 не переписываются; расхождение ожиданий с фактическим поведением анализатора фиксируется как известное ограничение, а не правкой контроля.

## См. также

- [Основной контракт: §8 runtime, §17 PHP/Laravel SDK](tool-spec.md)
- [Принятый индекс](accepted-index.md)
