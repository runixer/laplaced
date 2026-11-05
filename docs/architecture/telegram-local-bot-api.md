# Собственный Telegram Bot API сервер

Публичный `api.telegram.org` ограничивает файлы: бот скачивает не больше 20 МБ
(`getFile`) и отправляет не больше 50 МБ. Собственный сервер
[tdlib/telegram-bot-api](https://github.com/tdlib/telegram-bot-api), запущенный
с `--local`, поднимает отправку до 2000 МБ и снимает лимит на скачивание. Бот
говорит с ним тем же Bot API, меняется только адрес.

## Как это устроено

```
Telegram DC ──MTProto──► telegram-bot-api ──webhook (HTTP)──► laplaced
                              ▲                                   │
                              └────── Bot API вызовы (HTTP) ──────┘
                         общий том /var/lib/telegram-bot-api
```

- Сервер сам держит исходящее MTProto-соединение с Telegram; входящих портов
  снаружи не нужно — ни для long polling, ни для webhook.
- В `--local` режиме `getFile` скачивает файл **на диск сервера** и возвращает
  абсолютный путь вместо ссылки. По HTTP сервер такие файлы не раздаёт, поэтому
  бот читает их с общего тома.
- Webhook в `--local` режиме может быть обычным HTTP на частный адрес и любой
  порт, поэтому публичный HTTPS для него не нужен.

## Конфигурация

```yaml
telegram:
  api_base_url: "http://127.0.0.1:8081"   # адрес своего сервера
  local_mode: true                         # сервер запущен с --local
  local_files_dir: "/var/lib/telegram-bot-api"
  max_download_mb: 100                     # входящие файлы (сумма за ход)
  max_upload_mb: 2000                      # исходящий документ/видео
  webhook_url: "http://<адрес бота, видимый серверу>:<порт>"  # опционально
```

| Поле | По умолчанию | Смысл |
|------|--------------|-------|
| `api_base_url` | `https://api.telegram.org` | Адрес Bot API |
| `local_mode` | `false` | Сервер запущен с `--local`; без этого абсолютные пути из `getFile` отвергаются |
| `local_files_dir` | `/var/lib/telegram-bot-api` | Рабочий каталог сервера, смонтированный в бота **по тому же пути**; пути вне него (в т.ч. через symlink) отвергаются |
| `max_download_mb` | `20` | Лимит одного входящего файла и суммы всех файлов хода; больше 20 — только с `local_mode` |
| `max_upload_mb` | `50` | Лимит исходящего документа/видео; больше 50 (до 2000) — только с `local_mode` |

Переменные окружения: `LAPLACED_TELEGRAM_API_BASE_URL`,
`LAPLACED_TELEGRAM_LOCAL_MODE`, `LAPLACED_TELEGRAM_LOCAL_FILES_DIR`,
`LAPLACED_TELEGRAM_MAX_DOWNLOAD_MB`, `LAPLACED_TELEGRAM_MAX_UPLOAD_MB`.

**`max_download_mb` — это ещё и бюджет LLM-запроса.** Входящие файлы хода
встраиваются в один запрос к модели; поднимать лимит имеет смысл только в
пределах того, что принимает провайдер.

## Таймауты

С `local_mode` сервер отвечает только после полной передачи: `getFile` — когда
файл целиком скачан к нему на диск, отправка — когда Telegram принял файл.
Поэтому `getFile` и все multipart-отправки идут через отдельный HTTP-клиент с
бюджетом 30 минут (с публичным API — 3 минуты на отправку).

## Webhook и порядок сообщений

При заданном `webhook_url` бот регистрирует webhook с `max_connections: 1`:
сервер доставляет обновления по одному, в порядке поступления — как пачка
`getUpdates` при long polling. Значение по умолчанию (40) позволяет быстрой
серии сообщений прийти в обработчик вперемешку.

## Пример docker-compose

```yaml
services:
  telegram-bot-api:
    image: aiogram/telegram-bot-api:<версия>
    environment:
      TELEGRAM_API_ID: ${TELEGRAM_API_ID}       # https://my.telegram.org
      TELEGRAM_API_HASH: ${TELEGRAM_API_HASH}
      TELEGRAM_LOCAL: "1"
    volumes:
      - ./telegram-bot-api:/var/lib/telegram-bot-api
    ports:
      - "127.0.0.1:8081:8081"   # только локально: сервер принимает любой токен

  laplaced:
    image: ghcr.io/runixer/laplaced:latest
    volumes:
      - ./telegram-bot-api:/var/lib/telegram-bot-api:ro
    environment:
      LAPLACED_TELEGRAM_API_BASE_URL: http://telegram-bot-api:8081
      LAPLACED_TELEGRAM_LOCAL_MODE: "true"
```

Порт сервера нельзя открывать наружу: он обслуживает запросы с **любым**
токеном бота.

## Переезд бота

1. Вызвать `logOut` на публичном сервере:
   `curl https://api.telegram.org/bot<TOKEN>/logOut`. После этого публичный
   API для бота недоступен 10 минут.
2. Запустить свой сервер и переключить `api_base_url` / `local_mode`.

Откат: `logOut` на своём сервере, подождать 10 минут, вернуть публичный адрес
(и лимиты 20/50).

## Ограничения

- Сервер не удаляет скачанные файлы сам; каталог нужно периодически чистить.
- `--proxy` сервера относится только к исходящим webhook-запросам; направить
  MTProto-трафик через прокси можно только средствами сети.
