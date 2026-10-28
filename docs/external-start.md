[Контракт файлового входа](external-input.md) · [Схема запроса](schemas/audit-input.schema.json)

# Подключение внешнего корпуса

Эта инструкция нужна только при явно выбранном внешнем входе. Для обычного локального CONFIG продолжайте `index` и обычный аудит. Отсутствие файла не доказывает перенос документов во внешний источник.

Корпус — подготовленное дерево документов. Манифест описывает файлы, роли и hashes. AUDIT_INPUT связывает комплект с точным основанием выбранного кода.

```text
Готовый комплект клиента
          |
CONFIG + AUDIT_INPUT
          |
external check -> объяснение конфликтов
          |
external import
          |
рабочий CONFIG -> индекс -> обычный аудит
```

## 1. Установить вход и границу

Получите разрешённый CONFIG и точные пути `corpus/`, `source-manifest.json`, `profile.json`. Клиент готовит документы и метаданные по r1. Способ получения документов находится за границей spec-audit.

Если клиент предоставляет `audit-input.json`, используйте его. Иначе оператор может составить запрос из точных предоставленных данных. Не подставляйте предполагаемые редакции, hashes и решения клиента. Если данных нет, перечислите недостающее и запросите его у владельца интеграции.

Не ищите хранилище по домашнему каталогу и не запускайте неизвестные команды поставщика. Не устанавливайте среду проекта ради проверки файлового входа. PHP, SDK, модельный API и доступ к порталу здесь не требуются.

CONFIG и reports_dir должны находиться внутри разрешённой области аудита. Источники и отчёты разделены; исключение создаёт только штатный import. Проверьте отсутствие пользовательских симлинков. JSON-метаданные находятся вне `corpus/`.

## 2. Сверить выбор и составить запрос

### CONFIG

Ниже полный синтетический CONFIG. Относительные project_root/reports_dir разрешаются от файла CONFIG. Пути specs/references для импорта относятся к корпусу; code/tests — к project_root.

```yaml
version: 1
project_root: source
specs:
  paths: [rules.md]
references:
  paths: [context.json]
code:
  paths: [logic.py]
tests:
  paths: [test_logic.py]
reports_dir: reports
runtime: {kind: none}
scopes: []
external:
  project: example-project
  corpus: example-corpus
  line: null
  scope: example-scope
  profile_id: example-markdown
  profile_version: "1"
```

Значения external совпадают с предоставленным манифестом и профилем. Имена примера не являются defaults инструмента. Runtime none означает статический аудит; инструмент не исполняет Python.

| Роль манифеста | Требуемая группа CONFIG |
| --- | --- |
| normative | Только specs |
| reference | Только references |
| archive | Ни specs, ни references |

Выбор должен совпадать точно для каждого файла манифеста. Include/exclude проверяют имя файла. Например, `include: ['*.md']` исключит обязательный reference `.json`. Укажите точные пути либо осознанные шаблоны; не копируйте выбор другого проекта.

`external.scope` закрепляет область индекса. `scopes[]` распределяет все выбранные нормы между заданиями и не фильтрует корпус. Если глобальные роли несовместимы с частичным CONFIG, согласуйте поставку и границу с владельцем. Не меняйте роли манифеста и не расширяйте аудит молча.

### AUDIT_INPUT с Git

Это полный шаблон r1. Замените абсолютные пути, нулевой manifest_sha256 и пример commit точными значениями. Hash вычисляется от исходных bytes манифеста, без пересохранения JSON.

```json
{
  "schema_version": 1,
  "corpus_dir": "/absolute/input/corpus",
  "manifest_path": "/absolute/input/source-manifest.json",
  "profile_path": "/absolute/input/profile.json",
  "manifest_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
  "code_basis": {"kind": "git", "repository": "/absolute/project/source", "commit": "1111111111111111111111111111111111111111"},
  "freshness": {"status": "unknown", "checked_at": null, "observation_ref": null, "reason": null},
  "acceptance_ref": null,
  "applicability_ref": null,
  "publication_base": null
}
```

Repository — физический корень Git checkout либо точный origin. Commit — полный ID текущего HEAD. Выбранные code/tests должны побайтно совпадать с этим commit. Это проверка среза, а не требование чистоты всех файлов проекта.

Для сверки используйте разрешённые команды чтения:

```sh
git -C /absolute/project/source rev-parse HEAD
git -C /absolute/project/source rev-parse --show-toplevel
sha256sum /absolute/input/source-manifest.json
```

На macOS вместо sha256sum можно использовать `shasum -a 256`. Команды не получают документы из сети.

Выбранный ignored-файл также отсутствует в commit. Обычный `git status` может его не показать. Ошибка check различает tracked, untracked и ignored. Оператор решает, исправить выбор или предоставить подходящее основание; инструмент не меняет Git, ignore-правила и генерируемые файлы.

### AUDIT_INPUT без Git

Snapshot-basis использует идентичность клиента и digest выбранных code/tests. Репозиторий и Git-команды не нужны. Ниже полный шаблон с проверяемым синтетическим digest.

```json
{
  "schema_version": 1,
  "corpus_dir": "/absolute/input/corpus",
  "manifest_path": "/absolute/input/source-manifest.json",
  "profile_path": "/absolute/input/profile.json",
  "manifest_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
  "code_basis": {"kind": "snapshot", "snapshot_id": "example-code-1", "sha256": "51cf91521eade76eb21897699e47d29664ac01ef74ab3aa959af30b5d1884fcd"},
  "freshness": {"status": "unknown", "checked_at": null, "observation_ref": null, "reason": null},
  "acceptance_ref": null,
  "applicability_ref": null,
  "publication_base": null
}
```

Замените пути и manifest_sha256. Digest выше подходит только двум файлам с точными UTF-8 bytes, LF и завершающим переводом строки:

`logic.py`:

```python
LIMIT = 3
```

`test_logic.py`:

```python
from logic import LIMIT
assert LIMIT == 3
```

Для другого среза клиент или оператор вычисляет SHA-256 компактного JSON массива SourceFile. Записи отсортированы по path; поля идут в порядке path, kind, sha256, bytes. Поле reference отсутствует. JSON использует штатное Go encoding/json-представление строк, без отступов и завершающего LF. Не хэшируйте архив, CONFIG или JSON с другим порядком полей.

Проверяемый вектор этих двух файлов:

```json
[{"path":"logic.py","kind":"code","sha256":"bc906ae8d33b59b5fcb83a8e3d79f134dce37031fdc7bd7c9e58288ebd8ced8f","bytes":10},{"path":"test_logic.py","kind":"tests","sha256":"1d72611911819d6da375028c12cf031cc6cbe276e9f834088f24283522532029","bytes":42}]
```

Snapshot_id задаёт клиент; он не вычисляется из имени проекта. Неизвестное наблюдение живого источника остаётся unknown. Acceptance_ref/applicability_ref остаются null без явных решений клиента. Наличие запроса не означает принятия норм или соответствия кода.

## 3. Проверить и импортировать

```sh
spec-audit external check CONFIG AUDIT_INPUT
spec-audit external import CONFIG AUDIT_INPUT
```

Check ничего не записывает. При конфликте выбора он показывает ожидаемые и фактические множества, недостающие, лишние пути и выбор в обе группы. Множества ограничены проверенным манифестом и учитывают include/exclude. Независимые конфликты Git показываются вместе, когда вход позволяет проверить их безопасно. Повреждённый hash, опасный путь или симлинк сохраняют ранний отказ.

Исправления выполняет оператор в разрешённой области. Import повторяет проверки и публикует установку одним rename. Исходный комплект клиента не меняется. Возьмите поле `config` из успешного ответа: дальнейший аудит использует этот рабочий CONFIG.

Сохраните исходный CONFIG и AUDIT_INPUT для повторного подключения. Рабочий CONFIG содержит управляемую привязку; не исправляйте его дерево и внутренние журналы вручную.

## 4. Продолжить аудит или прочитать историю

```sh
spec-audit index WORKING_CONFIG summary
spec-audit overview WORKING_CONFIG
```

Import не выполняет reconcile и не выдаёт ID автоматически. Для accepted-профиля проверьте состояние индекса. Используйте штатные keep/relocate при подходящем основании; изменение смысла требует работы с кандидатом. Не объявляйте обязательной переприёмку всех scope только по факту переноса файлов.

Продолжите обычный цикл: prepare, независимые роли, validate/submit, согласование хоста, report. Внешняя публикация требует независимой review-check по точному решению. Подробности — в [контракте](external-input.md).

В режиме citations после завершения всех зависимых run и подтверждённых перепроверок `external release WORKING_CONFIG` освобождает проверенные временные копии. Он не возвращает источники в checkout и не удаляет вход клиента.

Чтение сохранённого решения после release не требует нового import. Непроверенная актуальность — unchecked, доступность — released либо unavailable. Полное решение по неизменному основанию остаётся saved. Новый анализ требует доступного точного входа: повторите check/import из исходного CONFIG.

CLI external result использует schema_version 3; компактный result.json сайта — schema_version 4; новый переносимый bundle без сайтов — schema_version 3. Прежние результаты остаются историей. Отсутствие источника не доказывает изменение bytes и не закрывает GAP.

Публикация по §83 использует сохранённые отчёты:

```sh
spec-audit publish NEW_OUT --from AUDIT_ROOT
spec-audit publish HISTORY_OUT --run RUN_ID WORKING_CONFIG
```

Первый вызов собирает последние полные аудиты всех областей, второй — один исторический run.
Оба OUT должны отсутствовать.
Каждая область показывает свою проверенную редакцию; новый аудит запускает человек.
Передайте `--corpus-control FILE` повторно, если нужен выбранный контроль, включая после restore.

## См. также

- [Файловый обмен, сохранение цитат и восстановление](external-input.md)
- [Схема манифеста](schemas/source-manifest.schema.json)
- [Схема AUDIT_INPUT](schemas/audit-input.schema.json)
