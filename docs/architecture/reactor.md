# Reactor (эмодзи-реакции)

Этот документ описывает Reactor agent — компонент, который решает, стоит ли
поставить эмодзи-реакцию на сообщение пользователя, и выбирает подходящий эмодзи.

## Обзор

До v0.10.1 реакция ставилась случайно (~10% сообщений, случайный эмодзи). Теперь
этим занимается LLM-агент, который видит сообщение (включая голос и фото),
профиль пользователя и недавнюю историю, и реагирует **только когда это уместно**
(по умолчанию — не реагирует).

Reactor запускается **параллельно** основному ответу (fire-and-forget), поэтому
не добавляет задержки к ответу бота.

## Поток

`internal/bot/react.go`, `internal/agent/reactor/reactor.go`:

1. `maybeReact()` вызывается из `processMessageGroup` и сразу возвращается,
   запустив горутину (учтённую в shutdown-WaitGroup бота). No-op, если: агент не
   подключён, `agents.reactor.enabled = false`, или транспорт не поддерживает
   реакции / у него пустой список доступных.
2. Агент (тайм-аут 30s, чтобы не держать shutdown) получает:
   - профиль пользователя (`SharedContext.Profile`);
   - последние 6 сообщений истории;
   - текст текущего сообщения и медиа (голос/фото — multimodal);
   - список разрешённых реакций транспорта (`Capabilities.AvailableReactions`).
3. Возвращает JSON; пустой/`none` → не реагировать.
4. Эмодзи валидируется против списка транспорта (`ValidateEmoji`): срезаются
   пробелы и `:шорткоды:`, нормализуются вариационные селекторы U+FE0F (модели
   шлют `❤️`, Telegram ждёт `❤`), сравнение регистронезависимое. Возвращается
   **форма из списка транспорта**, а не то, что выдала модель. Невалидный кандидат
   → реакция молча пропускается.
5. `Transport.SetReaction()` ставит реакцию.

Любая ошибка (LLM, парсинг, transport) логируется на уровне debug/warn и **не**
ломает основной ход.

## Наблюдаемость

Спан `reactor.Execute` (`internal/agent/reactor/reactor.go`) с атрибутами:
`reactor.allowed_count`, `reactor.media_count`, `reactor.reacted`,
`reactor.emoji`, `reactor.parse_error`, `reactor.json_repaired`. Логи —
`/ui/agents/reactor`.

## Конфигурация

```yaml
agents:
  reactor:
    name: "Reactor"
    enabled: true
    # model: наследует agents.default.model
```

## Связанные документы

- [transports.md](./transports.md) — `AvailableReactions`, формы эмодзи по транспортам
- [message-processing-flow.md](./message-processing-flow.md) — где Reactor встроен в поток
