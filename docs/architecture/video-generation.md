# Video Generation (генерация видео)

Инструмент `generate_video` — короткие ролики (4–8 с, со звуком и речью) по
запросу пользователя: text-to-video и image-to-video («оживи фото»).

## Поток

```mermaid
sequenceDiagram
    participant LLM as Laplace (chat)
    participant Tool as generate_video
    participant VG as videogen agent
    participant API as /videos (async)
    participant ST as Storage / artifacts

    LLM->>Tool: generate_video(prompt, aspect_ratio?, duration?, audio?, input_artifact_ids?)
    Tool->>Tool: resolveInputImages() — фото из сообщения/памяти → первый (и последний) кадр
    Tool->>VG: Generate
    VG->>VG: центральный кроп кадра под 9:16 / 16:9
    VG->>API: POST /videos → 202 {id}
    loop каждые poll_interval до timeout
        VG->>API: GET /videos/{id}
    end
    VG->>API: GET /videos/{id}/content?index=0
    Tool->>ST: SaveFile + артефакт file_type=video (state=pending)
    Tool-->>LLM: artifact ID; ролик уйдёт отдельным видео после ответа
```

Ход блокируется на время генерации (~1–2 мин), пока стрим показывает статус
«🎬 Снимаю видео…». Таймаут задаёт сам агент (`timeout`, по умолчанию 5 мин):
у хода общего дедлайна нет. Транзиентные ошибки опроса не бросают оплаченную
задачу — опрос продолжается до таймаута. Несколько `generate_video` в одном ходе
идут параллельно (`max_concurrent`, по умолчанию 2).

## Почему кроп входного кадра

Veo принимает только 16:9 и 9:16, а кадр другой формы дополняет чёрными
полосами. Поэтому входное фото (JPEG/PNG) центрально обрезается под выбранный
`aspect_ratio` и перекодируется в JPEG; спан помечает это атрибутом
`videogen.first_frame_cropped`. Описание инструмента просит модель выбирать
ориентацию по фото.

## Доставка

Видео — отдельный wire kind `Video` (Telegram `sendVideo` с
`supports_streaming`), не фото и не документ:

- в private rich-ходе ролик идёт **после** rich-поста отдельной persistent-операцией
  (как оригиналы-документы); Rich Messages умеют вкладывать только фото;
- `MEDIA:n` видео не получает и нумерацию картинок не сдвигает
  (`artifactdelivery.Generated.Video`);
- один ролик = одна операция ledger'а (альбомы видео не собираются);
- legacy-путь (группы, rich выключен) распознаёт `video/*` MIME и тоже шлёт `sendVideo`;
  Mattermost прикладывает файл как есть;
- загрузка идёт через отдельный upload-клиент Telegram с таймаутом 3 мин
  (у обычного 30 с, чего мало для нескольких мегабайт через прокси).

Для видео-артефакта режим `preview` означает «нативное видео», `original` — документ.

## Ошибки

| Режим | Сигнал | Что говорит пользователю |
|-------|--------|--------------------------|
| `timeout` | задача не завершилась за `timeout` | слишком долго, попробовать позже |
| `safety_block` | `llm.IsSafetyBlock` на submit | заблокировано политикой, перефразировать |
| `job_failed` | статус failed/cancelled/expired | причина провайдера (если есть) своими словами |
| `upstream_error` | прочие ошибки API | временная ошибка |

Как и у картинок, tool-результат запрещает повторный вызов в этом ходе: ролик
стоит десятки центов и минуту ожидания.

## Конфигурация

```yaml
agents:
  video_generator:
    model: "google/veo-3.1-lite"   # "" → инструмент выключен
    timeout: "5m"
    poll_interval: "5s"
    resolution: "720p"
    default_aspect_ratio: "9:16"
    default_duration: 6
    supported_aspect_ratios: ["9:16", "16:9"]
    supported_durations: [4, 6, 8]
    generate_audio: true
    max_concurrent: 2
tools:
  - name: "generate_video"
```

Модель выбрана сравнением на реальных сценариях: `veo-3.1-lite` дословно
произносит русские реплики, ~75 с и ~$0.30 за 6 с в 720p. `veo-3.1-fast` даёт то
же качество вдвое дороже. Kling, Wan и Seedance на русской речи заметно хуже или
игнорируют `generate_audio`. Если в промпте упомянуть Reels или смартфон, Veo
дорисовывает интерфейс телефона, поэтому описание инструмента это запрещает.

## Связанные документы

- [image-generation.md](./image-generation.md) — генерация картинок
- [artifacts-system.md](./artifacts-system.md) — артефакты и RAG
- [message-processing-flow.md](./message-processing-flow.md) — tool loop и доставка
