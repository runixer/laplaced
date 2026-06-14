# Database Backends (SQLite / PostgreSQL)

Этот документ описывает слой хранилища: как один и тот же код репозиториев
работает поверх SQLite и PostgreSQL через абстракцию диалектов, и как
устроены миграции.

## Обзор

С v0.10.0 поддерживаются два бэкенда: **SQLite** (по умолчанию, чистый Go без
CGO — `modernc.org/sqlite`) и **PostgreSQL** (`jackc/pgx`). Выбор — по
`database.driver`. Код репозиториев пишется один раз; различия SQL прячет
`Dialect`.

## Dialect

`internal/storage/dialect.go` инкапсулирует то, что отличается между движками:

```go
type Dialect interface {
    Name() string                 // "sqlite" | "postgres"
    Rebind(query string) string   // ? → ?  | ? → $1, $2, ...
    BindTime(t time.Time) any      // строка | time.Time
    SinceDaysPredicate(col, days) string
    DateExpr(col) string
    MinutesAgoExpr(minutes) string
    BoolLit(b bool) string         // "0"/"1" | "FALSE"/"TRUE"
    // ...выражения дат/времени
}
```

- **SQLite:** `Rebind` оставляет `?`; время форматируется строкой
  `2006-01-02 15:04:05.999` (UTC).
- **PostgreSQL:** `Rebind` нумерует плейсхолдеры `$1, $2, …`; время — нативный
  `time.Time` (`timestamptz`).

> Порядок важен: `ExpandIn` (раскрытие `(?)` → `(?,?,?)` для IN-списков) идёт
> **до** `Rebind`. Никогда не `Rebind` раньше `ExpandIn`.

## Выбор бэкенда

`storage.NewStore(cfg, logger)` (`internal/storage/storage.go`) смотрит на
`cfg.Driver` (пусто → sqlite):

- **SQLite:** `PRAGMA journal_mode=WAL`, `busy_timeout=5000`, один коннект
  (`SetMaxOpenConns(1)` — SQLite не для параллельной записи).
- **PostgreSQL:** libpq DSN (значения экранированы — безопасны спецсимволы в
  пароле), `sslmode` (по умолчанию `require`), пул 10/5/30m, обязательный Ping
  при инициализации.

## ScopeID и динамическая типизация

Ключ раздела — `ScopeID` (UUID-строка, см. [transports.md](./transports.md)). Он
реализует `sql.Scanner`/`driver.Valuer`, потому что SQLite имеет affinity-типизацию:
числовая строка `"123"` пишется как INTEGER и читается обратно как `int64`.
Scanner принимает `int64`/`string`/`[]byte`, Valuer всегда пишет строкой — так
round-trip lossless на обоих движках. Это нужно было заложить **до** перехода с
`int64 user_id`, а не после первой паники в рантайме.

## Миграции

`internal/storage/migrations/`:

- Версия отслеживается таблицей `schema_version`; runner выполняет все pending
  миграции в одной транзакции (all-or-nothing).
- Миграция `000` (bootstrap) определяет уже существующую БД.
- **SQLite:** legacy bootstrap DDL + инкрементальный runner.
- **PostgreSQL:** консолидированная end-state схема из встроенного
  `schema/postgres.sql` (идемпотентно, `CREATE TABLE IF NOT EXISTS`).

Каждая миграция регистрируется в `init()` через `migrations.Register(...)`.

## Именование репозиториев

После появления слоя диалектов файлы репозиториев перестали быть
SQLite-специфичными, и префикс `sqlite_` был снят (`git mv`, чтобы сохранить
историю): `sqlite_fact.go → fact.go`, `sqlite_artifact.go → artifact.go` и т.д.
SQLite-специфичные имена остаются только там, где код действительно специфичен
(например, открытие файла БД).

## Конфигурация

```yaml
database:
  driver: "sqlite"            # или "postgres"
  path: "data/laplaced.db"    # для sqlite
  # postgres:                 # для driver: postgres
  #   host: "<host>"
  #   port: 5432
  #   database: "laplaced"
  #   user: "laplaced"
  #   sslmode: "require"
  #   # пароль — только через LAPLACED_DATABASE_PASSWORD (или vault:), не литералом
```

## Связанные документы

- [transports.md](./transports.md) — `ScopeID` как ключ раздела
- [embedding-storage.md](./embedding-storage.md) — хранение векторов
- [artifacts-system.md](./artifacts-system.md) — блоб-хранилище файлов (диск / S3)
