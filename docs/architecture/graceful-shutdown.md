# Graceful Shutdown

Этот документ описывает механизм корректного завершения работы бота и принятые архитектурные решения.

## Обзор

При получении сигнала `SIGTERM` или `SIGINT` бот должен:
1. Прекратить приём новых сообщений
2. Завершить обработку уже принятых сообщений
3. Отправить ответы пользователям
4. Корректно закрыть все соединения

## Архитектура

### Компоненты и их роли

```mermaid
flowchart TB
    subgraph main["main.go"]
        signal["signal.NotifyContext() → ctx"]
    end

    subgraph server["Web Server (webhook)"]
        s1["Получает updates через HTTP"]
        s2["Вызывает bot.HandleUpdateAsync()"]
        s3["Хранит ctx для передачи в handler"]
    end

    subgraph bot["Bot"]
        b1["wg sync.WaitGroup для отслеживания горутин"]
        b2["ProcessUpdateAsync() добавляет wg.Add(1) ДО старта"]
        b3["HandleUpdateAsync() для webhook режима"]
        b4["Stop() ждёт завершения всех горутин"]
    end

    subgraph grouper["MessageGrouper"]
        g1["Группирует сообщения с задержкой turnWait (2s)"]
        g2["parentCtx для отмены child contexts"]
        g3["wg для отслеживания активных processGroup"]
        g4["Stop() отменяет таймеры и ждёт завершения"]
    end

    subgraph process["processMessageGroup"]
        p1["llmCtx := context.WithoutCancel(ctx)"]
        p2["LLM генерация НЕ отменяется при shutdown"]
        p3["Ответ ГАРАНТИРОВАННО отправляется"]
    end

    main --> server
    server --> bot
    bot --> grouper
    grouper --> process
```

### Поток данных при shutdown

```mermaid
flowchart TD
    A[SIGTERM получен] --> B[ctx.Cancel]

    B --> C[Long Polling]
    B --> D[Webhook]
    B --> E[MessageGrouper.Stop]
    B --> F[Bot.Stop]

    C --> C1["polling loop проверяет ctx.Done()"]
    C1 --> C2["Выходит из цикла"]
    C2 --> C3["Новые updates НЕ получаем"]

    D --> D1["server.Shutdown()"]
    D1 --> D2["Новые HTTP запросы НЕ принимаем"]

    E --> E1["parentCancel() — отменяет child contexts"]
    E1 --> E2["Останавливает pending таймеры"]
    E2 --> E3["mg.wg.Wait() — ждёт активные processGroup"]

    F --> F1["messageGrouper.Stop()"]
    F1 --> F2["bot.wg.Wait() — ждёт все горутины"]

    C3 --> G[Процесс завершается]
    D2 --> G
    E3 --> G
    F2 --> G
```

### Жизненный цикл сообщения

```mermaid
sequenceDiagram
    participant TG as Telegram
    participant Bot as Bot
    participant MG as MessageGrouper
    participant RAG as RAG Service
    participant LLM as OpenRouter

    TG->>Bot: Update (сообщение)
    Bot->>MG: AddMessage()

    Note over MG: Ждём turnWait (2s)<br/>для группировки

    MG->>Bot: processMessageGroup(ctx)

    Note over Bot: shutdownSafeCtx := context.WithoutCancel(ctx)

    Bot->>RAG: buildContext(shutdownSafeCtx)
    RAG-->>Bot: контекст с историей

    Bot->>LLM: CreateChatCompletion(shutdownSafeCtx)

    Note over Bot: SIGTERM получен<br/>ctx отменён, но shutdownSafeCtx — нет

    LLM-->>Bot: Response
    Bot->>TG: SendMessage (ответ)

    Note over Bot: bot.wg.Done()
```

## Ключевые решения

### 1. Вся обработка сообщения не отменяется при shutdown

**Проблема:** При отмене контекста HTTP запросы (RAG, LLM, отправка) прерываются, пользователь не получает ответ.

**Решение:** Используем `context.WithoutCancel(ctx)` для всех операций обработки:

```go
// process_group.go
func (b *Bot) processMessageGroup(ctx context.Context, group *MessageGroup) {
    // Создаём non-cancellable контекст в начале обработки
    shutdownSafeCtx := context.WithoutCancel(ctx)

    // Typing action использует этот контекст
    typingCtx, cancelTyping := context.WithCancel(shutdownSafeCtx)
    go b.sendTypingActionLoop(typingCtx, chatID, threadID)

    // RAG retrieval использует этот контекст
    orMessages, ragInfo, err := b.buildContext(shutdownSafeCtx, userID, ...)

    // LLM генерация использует этот контекст
    resp, err := b.orClient.CreateChatCompletion(shutdownSafeCtx, req)

    // Отправка ответа тоже
    b.sendResponses(shutdownSafeCtx, chatID, responses, logger)
}
```

**Обоснование:**
- Пользователь должен получить ответ на уже принятое сообщение
- RAG + LLM генерация может занимать до минуты
- Typing action должен показываться пока идёт обработка
- Новые сообщения после shutdown будут доставлены Telegram после рестарта

### 2. WaitGroup.Add() вызывается ДО старта горутины

**Проблема:** Race condition — если `wg.Wait()` вызван до `wg.Add(1)`, произойдёт panic.

**Неправильно:**
```go
go func() {
    wg.Add(1)  // ← Race condition!
    defer wg.Done()
    process()
}()
```

**Правильно:**
```go
wg.Add(1)
go func() {
    defer wg.Done()
    process()
}()
```

### 3. Pending группы обрабатываются при shutdown

**Проблема:** Если пользователь отправил сообщение и сразу произошёл shutdown (в течение turnWait), сообщение может быть потеряно — Telegram считает его доставленным.

**Решение:** При shutdown обрабатываем все pending группы немедленно:

```go
// message_grouper.go - Stop()
for userID, group := range mg.groups {
    if group.Timer.Stop() {
        // Таймер остановлен ДО срабатывания — обрабатываем группу сейчас
        pendingGroups = append(pendingGroups, group)
    }
}

// Запускаем обработку pending групп
for _, group := range pendingGroups {
    go func(g *MessageGroup) {
        defer mg.wg.Done()
        mg.onGroupReady(context.Background(), g)
    }(group)
}

mg.wg.Wait() // Ждём завершения всех обработок
```

### 4. Webhook использует server context, не request context

**Проблема:** `r.Context()` отменяется когда HTTP handler возвращается, но обработка продолжается асинхронно.

**Решение:**
```go
// server.go
type Server struct {
    ctx context.Context  // Сохраняем контекст сервера
}

func (s *Server) Start(ctx context.Context) error {
    s.ctx = ctx  // Используем для webhook handler
}

func (s *Server) webhookHandler(w http.ResponseWriter, r *http.Request) {
    // Используем s.ctx, не r.Context()
    s.bot.HandleUpdateAsync(s.ctx, body, r.RemoteAddr)
}
```

### 5. Typing action использует detached context

**Проблема:** При отмене основного контекста typing action возвращает ошибку "context canceled", которая засоряет логи.

**Решение:**
```go
// bot.go
func (b *Bot) sendAction(ctx context.Context, chatID int64, action string) {
    // Используем отдельный контекст с таймаутом, не привязанный к parent
    actionCtx, cancel := context.WithTimeout(context.Background(), actionTimeout)
    defer cancel()
    b.api.SendChatAction(actionCtx, req)
}
```

## Что происходит с сообщениями

| Момент отправки | Long Polling | Webhook |
|-----------------|--------------|---------|
| До shutdown | Обрабатывается, ответ отправляется | Обрабатывается, ответ отправляется |
| Во время обработки (RAG + LLM) | Обработка завершится, ответ отправится | Обработка завершится, ответ отправится |
| В turnWait (ожидание группировки) | Группа обработается немедленно | Группа обработается немедленно |
| После shutdown | Придёт после рестарта | Telegram повторит доставку |

## Тестирование

Тесты в `internal/bot/graceful_shutdown_test.go`:

1. **TestProcessMessageGroup_CompletesOnContextCancel** — проверяет, что `SendMessage` вызывается даже после отмены контекста

2. **TestProcessMessageGroup_LLMContextNotCancelled** — проверяет, что контекст для LLM не отменяется (через `context.WithoutCancel`)

## Связанные файлы

- `cmd/bot/main.go` — точка входа, signal handling
- `internal/bot/bot.go` — Bot.Stop(), ProcessUpdateAsync(), HandleUpdateAsync()
- `internal/bot/message_grouper.go` — MessageGrouper.Stop(), управление таймерами
- `internal/bot/process_group.go` — processMessageGroup(), llmCtx
- `internal/web/server.go` — webhook handler, server context
