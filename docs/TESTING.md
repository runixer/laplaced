# Testing Guide v2 for Laplaced

**Version:** 2.0 (v0.6.1+)
**Supersedes:** [TESTING.md](./TESTING.md)

This document is the authoritative guide for writing tests in laplaced. Follow these patterns for all new tests and when refactoring legacy tests.

---

## Table of Contents

1. [Philosophy](#1-philosophy)
2. [Test Architecture](#2-test-architecture)
3. [testutil Deep Dive](#3-testutil-deep-dive)
4. [Agent Testing Strategy](#4-agent-testing-strategy)
5. [The Right Way vs Wrong Way](#5-the-right-way-vs-wrong-way)
6. [Migration Guide](#6-migration-guide)
7. [Running Tests](#7-running-tests)

---

## 1. Philosophy

### Core Principles

1. **Test behavior, not implementation** - Assert observable outcomes, not internal state
2. **One source of truth for mocks** - All mocks live in `internal/testutil/`
3. **Table-driven by default** - Use `[]struct` + `t.Run` for multiple cases
4. **Fast feedback** - Mock external dependencies, avoid real I/O
5. **User data isolation** - Always filter by `user_id` in storage tests

### Test Pyramid

```
        /\
       /  \  E2E (rare, via testbot)
      /----\
     /      \ Integration (component boundaries)
    /--------\
   /          \ Unit (pure functions, single components)
  /------------\
```

| Level | When | Example |
|-------|------|---------|
| **Unit** | Pure functions, isolated logic | `TestSanitizeLLMResponse` |
| **Integration** | Multiple components together | `TestRetrieve_WithRAG` |
| **E2E** | Full flow via testbot | Manual testing |

---

## 2. Test Architecture

### Directory Structure

```
internal/testutil/
├── mocks.go          # ALL mock definitions (MockStorage, MockRetriever, etc.)
├── fixtures.go       # Test data factories (TestUser, TestFacts, etc.)
├── helpers.go        # Setup helpers (TestLogger, TestConfig, etc.)
├── assertions.go     # Custom assertions (AssertFactExists, etc.)
├── builders.go       # Fluent builders (AgentRequestBuilder, etc.)
├── log_capture.go    # Log testing utilities
├── metrics.go        # Prometheus testing utilities
├── http.go           # HTTP handler testing utilities
└── bot_test.go       # Integration test wrapper (build tag: test)
```

### Golden Rules

1. **NEVER define mocks outside testutil/** - Use existing mocks or add new ones to testutil
2. **ALWAYS use builders for complex objects** - `NewAgentRequestBuilder()` not manual struct construction
3. **ALWAYS extract repeated setup** - Create `setup*Test(t)` helpers when setup > 5 lines
4. **ALWAYS include `t.Helper()`** - In all test helper functions

---

## 3. testutil Deep Dive

### Mock Selection Guide

| Need to mock... | Use This Mock |
|-----------------|---------------|
| Any storage operation | `testutil.MockStorage` |
| LLM calls (chat, embeddings) | `testutil.MockOpenRouterClient` |
| Telegram API | `testutil.MockBotAPI` |
| Vector similarity search | `testutil.MockVectorSearcher` |
| File downloads | `testutil.MockFileDownloader` |
| Agent logging | `testutil.MockAgentLogger` |
| Tool execution | `testutil.MockToolHandler` |
| RAG retrieval | `testutil.MockRetriever` |

### Fixture Functions

```go
// User fixtures
testutil.TestUserID       // const int64 = 123
testutil.TestUser()       // Single user with ID 123
testutil.TestUsers()      // Multiple users for multi-user tests

// Data fixtures
testutil.TestFacts()         // Sample facts (5 items)
testutil.TestProfileFacts()  // Profile-level facts
testutil.TestTopic()         // Single topic
testutil.TestTopics()        // Multiple topics (3 items)
testutil.TestMessage()       // Single message
testutil.TestMessages()      // Conversation history (4 items)
testutil.TestPeople()        // Sample people
testutil.TestArtifact()      // Single artifact
testutil.TestArtifacts()     // Multiple artifacts

// LLM response fixtures
testutil.MockChatResponse("Hello")           // Simple response
testutil.MockChatResponseWithTokens("Hi", 10, 5)  // With token counts
testutil.MockEmbeddingResponse()             // Embedding vector

// Embedding fixture
testutil.TestEmbedding()  // 1536-dim float32 vector
```

### Helper Functions

```go
// Logging
logger := testutil.TestLogger()  // Discards all output

// Configuration
cfg := testutil.TestConfig()              // Full config with all agents
cfg := testutil.TestConfigWithRAGDisabled()  // RAG disabled

// Translation
translator := testutil.TestTranslator(t)  // Loads test locales

// Pointer helper
ptr := testutil.Ptr(42)  // Returns *int with value 42

// Background loop safety
testutil.SetupDefaultMocks(mockStore)  // Sets .Maybe() for background queries
```

### Fluent Builders

```go
// Agent request builder
req := testutil.NewAgentRequestBuilder().
    WithUserID(123).
    WithRawQuery("test query").
    WithMessages(testutil.TestMessages()).
    WithSharedContext(
        testutil.NewSharedContextBuilder().
            WithProfile(testutil.TestProfileFacts()).
            WithRecentTopics(testutil.TestTopics()).
            Build(),
    ).
    Build()

// Agent response builder
resp := testutil.NewAgentResponseBuilder().
    WithContent("response text").
    WithTokensUsed(100, 50).
    Build()

// Reranker result builder
result := testutil.NewRerankerResultBuilder().
    WithTopicIDs([]int64{1, 2, 3}).
    WithPeopleIDs([]int64{10}).
    Build()
```

### Custom Assertions

```go
// Fact assertions
testutil.AssertFactExists(t, store, userID, "expected content")
testutil.AssertFactCount(t, store, userID, 5)
testutil.AssertFactContains(t, store, userID, "partial")

// Topic assertions
testutil.AssertTopicExists(t, store, userID, topicID)
testutil.AssertTopicCount(t, store, userID, 3)

// Message assertions
testutil.AssertMessageExists(t, store, userID, "content")
testutil.AssertMessageCount(t, store, userID, 10)

// Person assertions
testutil.AssertPersonExists(t, store, userID, "Alice")
testutil.AssertPersonCount(t, store, userID, 2)
testutil.AssertPersonHasAlias(t, store, userID, personID, "nickname")

// Log assertions
capture := testutil.NewLogCapture()
// ... run code that logs ...
testutil.AssertLogContains(t, capture, "expected message")
testutil.AssertLogHasField(t, capture, "key", "value")
testutil.AssertNoErrorLogs(t, capture)
```

---

## 4. Agent Testing Strategy

### Test Levels for Agents

| Level | Mock | Test Focus |
|-------|------|------------|
| **Unit** | LLM + Storage | Pure agent logic (parsing, validation) |
| **Component** | Storage only | Agent + real LLM simulation |
| **Integration** | None | Full agent orchestration |

### Agent Unit Test Template

```go
func TestEnricherAgent_Execute(t *testing.T) {
    tests := []struct {
        name           string
        query          string
        llmResponse    string
        expectedQuery  string
        expectError    bool
    }{
        {
            name:          "simple query expansion",
            query:         "test",
            llmResponse:   "expanded test query",
            expectedQuery: "expanded test query",
        },
        {
            name:        "LLM error",
            query:       "test",
            llmResponse: "",
            expectError: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // 1. Setup mocks
            mockClient := new(testutil.MockOpenRouterClient)

            // 2. Configure expectations
            if tt.expectError {
                mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
                    Return(openrouter.ChatCompletionResponse{}, errors.New("LLM error"))
            } else {
                mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
                    Return(testutil.MockChatResponse(tt.llmResponse), nil)
            }

            // 3. Create agent
            agent := enricher.New(testutil.TestLogger(), mockClient, testutil.TestConfig())

            // 4. Build request with builder
            req := testutil.NewAgentRequestBuilder().
                WithRawQuery(tt.query).
                Build()

            // 5. Execute
            resp, err := agent.Execute(context.Background(), req)

            // 6. Assert
            if tt.expectError {
                assert.Error(t, err)
                return
            }
            assert.NoError(t, err)
            assert.Equal(t, tt.expectedQuery, resp.EnrichedQuery)
            mockClient.AssertExpectations(t)
        })
    }
}
```

### Testing Tool Calls

```go
func TestLaplaceAgent_ToolLoop(t *testing.T) {
    mockClient := new(testutil.MockOpenRouterClient)
    mockStore := new(testutil.MockStorage)
    testutil.SetupDefaultMocks(mockStore)

    // First call returns tool call
    mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
        return len(req.Messages) == 2  // system + user
    })).Return(openrouter.ChatCompletionResponse{
        Choices: []openrouter.Choice{{
            Message: openrouter.Message{
                ToolCalls: []openrouter.ToolCall{{
                    ID:   "call_1",
                    Type: "function",
                    Function: openrouter.FunctionCall{
                        Name:      "search_history",
                        Arguments: `{"query": "test"}`,
                    },
                }},
            },
        }},
    }, nil).Once()

    // Second call returns final response
    mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
        return len(req.Messages) == 4  // system + user + assistant + tool_result
    })).Return(testutil.MockChatResponse("Final answer"), nil).Once()

    // ... setup agent and execute ...
}
```

---

## 5. The Right Way vs Wrong Way

### Mock Definition

```go
// WRONG: Inline mock in test file
type mockRetriever struct {
    topics []storage.Topic
}
func (m *mockRetriever) Retrieve(...) (*rag.RetrievalResult, error) {
    return &rag.RetrievalResult{Topics: m.topics}, nil
}

// RIGHT: Use centralized testutil mock
mockRetriever := new(testutil.MockRetriever)
mockRetriever.On("Retrieve", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
    Return(&rag.RetrievalResult{Topics: testutil.TestTopics()}, &rag.RetrievalDebugInfo{}, nil)
```

### Test Setup

```go
// WRONG: Copy-paste setup
func TestFeatureA(t *testing.T) {
    logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
    cfg := &config.Config{Bot: config.BotConfig{...}, RAG: config.RAGConfig{...}}
    store := new(testutil.MockStorage)
    client := new(testutil.MockOpenRouterClient)
    store.On("GetFacts", mock.Anything).Return([]storage.Fact{}, nil)
    // ... 15 more lines of identical setup
}

func TestFeatureB(t *testing.T) {
    logger := slog.New(slog.NewJSONHandler(io.Discard, nil))  // Same
    cfg := &config.Config{Bot: config.BotConfig{...}, RAG: config.RAGConfig{...}}  // Same
    // ... copy-paste continues
}

// RIGHT: Extract helper
func setupBotTest(t *testing.T) (*Bot, *testutil.MockStorage, *testutil.MockOpenRouterClient) {
    t.Helper()
    store := new(testutil.MockStorage)
    client := new(testutil.MockOpenRouterClient)
    testutil.SetupDefaultMocks(store)
    bot := NewBot(testutil.TestLogger(), nil, client, store, testutil.TestConfig(), nil)
    return bot, store, client
}

func TestFeatureA(t *testing.T) {
    bot, store, client := setupBotTest(t)
    // ... test-specific setup only
}

func TestFeatureB(t *testing.T) {
    bot, store, client := setupBotTest(t)
    // ... test-specific setup only
}
```

### Naming Convention

```go
// WRONG: Unclear names
func TestFoo(t *testing.T) {}
func TestFoo2(t *testing.T) {}
func TestFooWithError(t *testing.T) {}
func TestFooNew(t *testing.T) {}

// RIGHT: Behavior-focused pattern: Test<Function>_<Scenario>_<Expected>
func TestRetrieve_EmptyQuery_ReturnsError(t *testing.T) {}
func TestRetrieve_WithMatchingTopics_ReturnsGroupedResults(t *testing.T) {}
func TestRetrieve_StorageError_PropagatesError(t *testing.T) {}
func TestRetrieve_RateLimited_RetriesWithBackoff(t *testing.T) {}
```

### Struct Construction

```go
// WRONG: Manual struct with many fields
req := &agent.Request{
    UserID:   123,
    RawQuery: "test",
    Messages: []storage.Message{
        {ID: 1, UserID: 123, Role: "user", Content: "hello"},
        {ID: 2, UserID: 123, Role: "assistant", Content: "hi"},
    },
    SharedContext: &agent.SharedContext{
        Profile: []storage.Fact{{ID: 1, UserID: 123, Content: "fact1"}},
        RecentTopics: []storage.TopicExtended{{Topic: storage.Topic{ID: 1}}},
    },
}

// RIGHT: Fluent builder
req := testutil.NewAgentRequestBuilder().
    WithUserID(123).
    WithRawQuery("test").
    WithMessages(testutil.TestMessages()).
    WithSharedContext(
        testutil.NewSharedContextBuilder().
            WithProfile(testutil.TestProfileFacts()).
            WithRecentTopics(testutil.TestTopics()).
            Build(),
    ).
    Build()
```

### Testing Internal State

```go
// WRONG: Manipulating internal fields
func TestArtifactLoading_MissingPath(t *testing.T) {
    agent := NewLaplace(...)
    agent.storagePath = ""  // Directly accessing internal field
    result, err := agent.loadArtifact(...)
    assert.Error(t, err)
}

// RIGHT: Test via public API with proper configuration
func TestArtifactLoading_MissingPath(t *testing.T) {
    cfg := testutil.TestConfig()
    cfg.Artifacts.StoragePath = ""  // Configure via public config
    agent := NewLaplace(..., cfg)
    result, err := agent.Execute(ctx, reqWithArtifact)
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "storage path not configured")
}
```

---

## 6. Migration Guide

### When to Refactor vs Keep

| Condition | Action |
|-----------|--------|
| Inline mock used once, test passes | Add `// TODO: migrate to testutil` comment |
| Inline mock duplicated in 2+ files | **Refactor NOW** |
| Test manipulates internal state | **Refactor** to test via public API |
| Test has >50 lines of setup | **Extract** setup helper |
| Test name unclear | **Rename** to `Test<Func>_<Scenario>_<Expected>` |
| Test is flaky (timing-dependent) | Mark with `// Flaky: timing` + create issue |

### Migration Checklist

For each legacy test file:

- [ ] Replace inline mocks with testutil equivalents
- [ ] Use builders for complex struct construction
- [ ] Extract repeated setup into `setup*Test(t)` helper
- [ ] Add `t.Helper()` to all helper functions
- [ ] Rename tests to behavior-focused names
- [ ] Verify test passes with `-race` flag
- [ ] Verify test passes with `-shuffle=on`
- [ ] Remove any direct internal state access

### Adding New Mocks to testutil

When you need a mock that doesn't exist:

1. Check if `MockStorage` already covers the interface
2. If not, add to `internal/testutil/mocks.go`:

```go
// MockNewInterface implements package.Interface for tests.
type MockNewInterface struct {
    mock.Mock
}

func (m *MockNewInterface) Method1(arg1 Type1) (ReturnType, error) {
    args := m.Called(arg1)
    if args.Get(0) == nil {
        return nil, args.Error(1)
    }
    return args.Get(0).(ReturnType), args.Error(1)
}
```

3. Add usage example in this document
4. Update TEST_SYSTEM_AUDIT.md inventory

---

## 7. Running Tests

### Basic Commands

```bash
# All tests
go test ./...

# Specific package
go test ./internal/bot/...

# Specific test function
go test ./internal/bot/... -run TestProcessMessage

# With coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out  # Open in browser
go tool cover -func=coverage.out  # Summary in terminal
```

### Quality Checks

```bash
# Race detector (REQUIRED before merge)
go test -race ./...

# Shuffle tests (detect order dependencies)
go test -shuffle=on ./...

# Verbose output
go test ./internal/agent/laplace/... -v

# Short mode (skip slow tests)
go test -short ./...
```

### Coverage Analysis

```bash
# Per-package coverage
go test ./internal/bot/... -cover
go test ./internal/rag/... -cover
go test ./internal/storage/... -cover

# Overall coverage summary
go test ./... -coverprofile=coverage.out && \
  go tool cover -func=coverage.out | grep total

# Find untested code
go tool cover -html=coverage.out
# Red = not covered, Green = covered
```

### Verification After Refactoring

```bash
# Verify no inline mocks remain
grep -r "type mock" --include="*_test.go" internal/ | grep -v testutil
# Should return 0 lines

# Verify all tests pass
go test ./...

# Verify race-safe
go test -race ./...

# Verify order-independent
go test -shuffle=on ./...
```

---

## References

- [Test Coverage Roadmap v2](./plans/test-coverage-roadmap-v2.md)
- [Test System Audit](./plans/TEST_SYSTEM_AUDIT.md)
- [Go Testing Best Practices](https://go.dev/doc/tutorial/add-a-test)
- [testify/assert](https://github.com/stretchr/testify/blob/master/assert/assertions.go)
- [testify/mock](https://github.com/stretchr/testify/blob/master/mock/mock.go)
- [Table-Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)
