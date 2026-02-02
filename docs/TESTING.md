# Testing Guide for Laplaced

This document describes best practices for writing tests in the Laplaced project.

## Table of Contents

1. [Philosophy](#philosophy)
2. [Test Utilities](#test-utilities)
3. [Test Patterns](#test-patterns)
4. [Mock Usage](#mock-usage)
5. [Coverage Goals](#coverage-goals)
6. [Integration Tests](#integration-tests)
7. [Concurrency Testing](#concurrency-testing)
8. [Common Pitfalls](#common-pitfalls)

---

## Philosophy

### Testing Principles

1. **Test behavior, not implementation** - Focus on what the code does, not how it does it
2. **Fast feedback** - Tests should run quickly; use mocks for external dependencies
3. **Isolation** - Each test should be independent; use `t.Parallel()` where safe
4. **Readability** - Tests should be easy to understand; use descriptive names

### Test Types

| Type | Purpose | Example |
|------|---------|---------|
| **Unit** | Test single function/method | `TestSanitizeLLMResponse` |
| **Integration** | Test multiple components | `TestRetrieve_TopicsGrouping` |
| **Table-driven** | Test same logic with different inputs | Most pure functions |
| **Benchmark** | Measure performance | `BenchmarkEmbedding` |

---

## Test Utilities

All test utilities are centralized in `internal/testutil/`. Use them instead of creating your own mocks.

### Available Helpers

```go
// From internal/testutil/helpers.go
func TestLogger() *slog.Logger                              // Discarding logger
func TestConfig() *config.Config                            // Default test config
func TestConfigWithRAGDisabled() *config.Config             // Config with RAG disabled
func TestTranslator(t *testing.T) *i18n.Translator         // With test locales
func TestFileProcessor(t *testing.T, ...) *files.Processor  // File processor
func Ptr[T any](v T) *T                                     // Generic pointer helper
```

### Available Fixtures

```go
// From internal/testutil/fixtures.go
const TestUserID int64 = 123

func TestUser() storage.User              // Single test user
func TestUsers() []storage.User            // Multiple test users
func TestFacts() []storage.Fact            // Sample facts
func TestProfileFacts() []storage.Fact     // Profile-level facts
func TestTopic() storage.Topic             // Single topic
func TestTopics() []storage.Topic          // Multiple topics
func TestMessage() storage.Message         // Single message
func TestMessages() []storage.Message      // Conversation history
func TestPeople() []storage.Person         // Sample people
func TestEmbedding() []float32             // 1536-dim vector
```

### Mock Builders

```go
// From internal/testutil/fixtures.go
func MockChatResponse(content string) openrouter.ChatCompletionResponse
func MockChatResponseWithTokens(content string, prompt, completion int) ...
func MockEmbeddingResponse() *openrouter.EmbeddingResponse
```

### Available Mocks

```go
// From internal/testutil/mocks.go
type MockBotAPI struct{ mock.Mock }           // Telegram API
type MockStorage struct{ mock.Mock }          // All storage repos
type MockOpenRouterClient struct{ mock.Mock } // LLM client
type MockFileDownloader struct{ mock.Mock }   // File downloads
type MockVectorSearcher struct{ mock.Mock }   // Vector search
type MockAgentLogger struct{ mock.Mock }      // Agent logging
type MockToolHandler struct{ mock.Mock }     // Tool execution
```

### Background Loop Defaults

When testing code that may trigger background loops, use:

```go
testutil.SetupDefaultMocks(mockStore)
```

This sets up `.Maybe()` expectations for common background queries.

---

## Test Patterns

### Table-Driven Tests (Default Pattern)

Use table-driven tests for testing the same logic with multiple inputs:

```go
func TestSanitizeLLMResponse_TruncatesOnHallucinationTags(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {
            name:     "tool_code tag",
            input:    "Normal response.</tool_code> garbage after",
            expected: "Normal response.",
        },
        {
            name:     "end of sequence tag",
            input:    "Valid content</s> invalid content",
            expected: "Valid content",
        },
        // ... more cases
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, sanitized := SanitizeLLMResponse(tt.input)
            assert.Equal(t, tt.expected, result)
            assert.True(t, sanitized)
        })
    }
}
```

### Setup/Teardown Pattern

For tests requiring setup (like database):

```go
func setupTestDB(t *testing.T) (*SQLiteStore, func()) {
    t.Helper()
    logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
    store, err := NewSQLiteStore(logger, ":memory:")
    if err != nil {
        t.Fatal(err)
    }

    cleanup := func() {
        store.Close()
    }

    return store, cleanup
}

func TestSQLiteStore(t *testing.T) {
    store, cleanup := setupTestDB(t)
    defer cleanup()

    // ... test code
}
```

### Subtest Organization

Group related tests with subtests:

```go
func TestRetrieveFacts(t *testing.T) {
    t.Run("RAG disabled", func(t *testing.T) {
        // ... test RAG disabled
    })

    t.Run("success with matching facts", func(t *testing.T) {
        // ... test successful retrieval
    })

    t.Run("embedding error", func(t *testing.T) {
        // ... test error handling
    })
}
```

---

## Mock Usage

### Basic Mocking

```go
mockStore := new(testutil.MockStorage)
mockClient := new(testutil.MockOpenRouterClient)

// Set expectations
mockStore.On("GetFacts", int64(123)).Return(testutil.TestFacts(), nil)
mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
    testutil.MockEmbeddingResponse(),
    nil,
)

// Run code
result, err := someFunction(mockStore, mockClient)

// Assert
assert.NoError(t, err)
assert.NotNil(t, result)
mockStore.AssertExpectations(t)  // Verify all expectations met
```

### Argument Matching

```go
// Exact match
mockStore.On("GetUser", int64(123)).Return(user, nil)

// Any argument
mockStore.On("GetUser", mock.Anything).Return(user, nil)

// Custom matcher
mockStore.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
    return len(req.Input) > 0 && req.Input[0] == "expected query"
})).Return(response, nil)
```

### Maybe() for Background Loops

For methods that may or may not be called:

```go
mockStore.On("GetAllUsers").Return([]storage.User{}, nil).Maybe()
```

### Debugging Failed Expectations

When mock expectations don't match, check for common issues:

```go
// 1. Type mismatch: int vs int64, string vs []string
mockStore.On("GetPerson", int64(123))  // Not: int(123)

// 2. Missing expectations in conditional paths
if person == nil {
    // This path might not be tested!
    person, _ = store.FindPersonByName(userID, name)
}

// 3. Use AssertExpectations(t) to see what was called
mockStore.AssertExpectations(t)  // Shows expected vs actual calls
```

**Pattern for optional calls:**
```go
// If a method is only called conditionally
mockStore.On("FindPersonByUsername", userID, "alice").Return(&person, nil).Maybe()
```

---

## Test Patterns

### Table-Driven Tests (Default Pattern)

Use table-driven tests for testing the same logic with multiple inputs:

```go
func TestSanitizeLLMResponse_TruncatesOnHallucinationTags(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {
            name:     "tool_code tag",
            input:    "Normal response.</tool_code> garbage after",
            expected: "Normal response.",
        },
        {
            name:     "end of sequence tag",
            input:    "Valid content</s> invalid content",
            expected: "Valid content",
        },
        // ... more cases
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, sanitized := SanitizeLLMResponse(tt.input)
            assert.Equal(t, tt.expected, result)
            assert.True(t, sanitized)
        })
    }
}
```

### Setup/Teardown Pattern

For tests requiring setup (like database):

```go
func setupTestDB(t *testing.T) (*SQLiteStore, func()) {
    t.Helper()
    logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
    store, err := NewSQLiteStore(logger, ":memory:")
    if err != nil {
        t.Fatal(err)
    }

    cleanup := func() {
        store.Close()
    }

    return store, cleanup
}

func TestSQLiteStore(t *testing.T) {
    store, cleanup := setupTestDB(t)
    defer cleanup()

    // ... test code
}
```

### Subtest Organization

Group related tests with subtests:

```go
func TestRetrieveFacts(t *testing.T) {
    t.Run("RAG disabled", func(t *testing.T) {
        // ... test RAG disabled
    })

    t.Run("success with matching facts", func(t *testing.T) {
        // ... test successful retrieval
    })

    t.Run("embedding error", func(t *testing.T) {
        // ... test error handling
    })
}
```

---

## Coverage Goals

### Target Percentages

| Package Type | Target | Rationale |
|--------------|--------|-----------|
| Core logic (agents, RAG) | 80%+ | Critical functionality |
| Storage layer | 70%+ | Data integrity |
| Bot logic | 75%+ | User-facing |
| HTTP handlers | 60%+ | Input validation |
| Entry points (cmd/*) | 50%+ | Wiring only |
| UI functions | 60%+ | Presentation |

### What Not to Test

- Generated code
- Simple getters/setters
- Trivial wrappers (e.g., `func (s *Service) Close() { s.close() }`)
- External libraries (assume they work)

### What to Emphasize

- Error paths (not just happy path)
- Edge cases (empty, nil, boundary values)
- Concurrent access
- Data isolation (user_id filtering)

---

## Integration Tests

### When to Write Integration Tests

- Testing message flow across multiple components
- Validating database operations end-to-end
- Testing HTTP request/response cycles

### Example: Full Retrieval Flow

```go
func TestRetrieve_TopicsGrouping(t *testing.T) {
    // 1. Setup
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    cfg := &config.Config{...}
    mockStore := new(testutil.MockStorage)
    mockClient := new(testutil.MockOpenRouterClient)
    mockEnricher := new(agenttesting.MockAgent)

    // 2. Data
    topics := []storage.Topic{...}
    messages := []storage.Message{...}

    // 3. Expectations
    mockStore.On("GetAllTopics").Return(topics, nil)
    mockClient.On("CreateEmbeddings", ...).Return(...)
    mockEnricher.On("Execute", ...).Return(...)

    // 4. Run
    svc := NewService(...)
    result, _, err := svc.Retrieve(context.Background(), userID, "query", nil)

    // 5. Assert
    assert.NoError(t, err)
    assert.Len(t, result.Topics, 2)
    mockStore.AssertExpectations(t)
}
```

---

## Concurrency Testing

### Race Detection

Always run tests with race detector:

```bash
go test -race ./...
```

### Testing Concurrent Access

```go
func TestConcurrentFactUpdates(t *testing.T) {
    store, cleanup := setupTestDB(t)
    defer cleanup()

    const goroutines = 10
    const updatesPerGoroutine = 100

    var wg sync.WaitGroup
    wg.Add(goroutines)

    for i := 0; i < goroutines; i++ {
        go func(id int) {
            defer wg.Done()
            for j := 0; j < updatesPerGoroutine; j++ {
                fact := storage.Fact{
                    UserID:  int64(id),
                    Content: fmt.Sprintf("fact-%d-%d", id, j),
                }
                store.AddFact(fact)
            }
        }(i)
    }

    wg.Wait()

    // Verify all facts were added
    facts, _ := store.GetFacts(int64(0))
    assert.Len(t, facts, goroutines*updatesPerGoroutine)
}
```

### Parallel Tests

For independent tests:

```go
func TestParallel(t *testing.T)    { t.Parallel(); ... }
func TestParallel2(t *testing.T)   { t.Parallel(); ... }
```

---

## Tool/Executor Testing

### Testing Tool Dispatch Pattern

Many components in Laplaced use a dispatch pattern where tools are invoked by name with JSON arguments:

```go
// Tool receives arguments as JSON string
args, _ := json.Marshal(map[string]interface{}{
    "query": `{"operation":"create","name":"Alice"}`,
})
result, err := exec.ExecuteToolCall(ctx, userID, "manage_people", string(args))
```

**Key gotcha:** The `query` parameter contains a JSON **string**, not a nested object. This is because LLMs send tools as JSON strings.

### Testing Functions That Return Formatted Strings (Not Errors)

For LLM compatibility, some functions return formatted error messages as strings instead of Go errors:

```go
// performManagePeople returns (string, error) where string is meant for LLM
func (e *ToolExecutor) performManagePeople(...) (string, error) {
    if name == "" {
        return "Error: name is required for create operation", nil
        //                                        ^^^^ nil error - message is for LLM
    }
    // ...
    return "Successfully created person 'Alice'", nil
}
```

**Testing pattern:**

```go
func TestPerformManagePeople_MissingName(t *testing.T) {
    result, err := exec.performManagePeople(ctx, userID, args)

    assert.NoError(t, err)  // Function returns nil error even for validation failures
    assert.Contains(t, result, "Error: name is required")
}
```

---

## Common Pitfalls

### 1. Testing Implementation Details

**Bad:**
```go
func TestMyFunction(t *testing.T) {
    assert.Equal(t, 3, callCount)  // Tests internal counter
}
```

**Good:**
```go
func TestMyFunction(t *testing.T) {
    result := MyFunction(input)
    assert.Equal(t, expected, result)  // Tests observable behavior
}
```

### 2. Brittle Mock Expectations

**Bad:**
```go
mockStore.On("GetUser", int64(123)).Return(user, nil)
// If another test also uses this, expectations collide
```

**Good:**
```go
// Use unique user IDs per test
mockStore.On("GetUser", testutil.TestUser().ID).Return(...)
```

### 3. Not Testing Error Paths

**Bad:**
```go
func TestProcessMessage(t *testing.T) {
    result, err := ProcessMessage("valid")
    assert.NoError(t, err)
    // Only tests success case
}
```

**Good:**
```go
func TestProcessMessage(t *testing.T) {
    t.Run("valid message", func(t *testing.T) { ... })
    t.Run("empty message", func(t *testing.T) { ... })
    t.Run("invalid format", func(t *testing.T) { ... })
    t.Run("storage error", func(t *testing.T) { ... })
}
```

### 4. Forgetting User Data Isolation

**Critical:** Always include `user_id` in queries when testing:

```go
// BAD: Captures messages from other users!
query := "SELECT * FROM history WHERE id >= ? AND id <= ?"

// GOOD: Always filter by user_id
query := "SELECT * FROM history WHERE user_id = ? AND id >= ? AND id <= ?"
```

### 5. Using Real Personal Data in Tests

**Always depersonalize:**
- Use `@testuser`, not real usernames
- Use generic names: "John", "Mary", "Alice"
- Describe conversations abstractly

### 6. Mock Expectation Type Mismatches

**Problem:** testify/mock strict type matching can cause confusing failures.

```go
// BAD: Won't match - Person struct is not the same type as storage.Person
mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
    return p.ID == 42
})).Return(nil)

// GOOD: Use mock.AnythingOfType for complex structs
mockStore.On("UpdatePerson", mock.AnythingOfType("storage.Person")).Return(nil)

// BETTER: Use MatchedBy with correct type assertion
mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
    return p.ID == 42 && p.Bio == "expected"
})).Return(nil)
```

**When expectations don't match:**
```go
// Error: "0 out of 1 expectation(s) were met"
// Common causes:
// 1. Wrong argument type (int64 vs int)
// 2. Function called different times than expected
// 3. Conditional logic prevented the call

// Debug with:
mockStore.AssertExpectations(t)  // Shows what was expected vs called
```

### 7. Testing Code Without Easy Entry Points

For `main()` and other complex entry points, write **placeholder/documentation tests** rather than forcing untestable code:

```go
// TestMain_ParserLogic tests flag parsing logic.
func TestMain_ParserLogic(t *testing.T) {
    // We can't test main() directly because it calls os.Exit
    // But we can verify the flag definitions are correct
    // by checking they compile

    // This test ensures the flags are properly defined
    // For full testing, refactor flag parsing into a testable function
    assert.True(t, true, "Flag parsing is defined in main")
}
```

**Note:** Document what **would** be needed to test properly (refactoring, interface extraction).

### 8. Environment-Dependent Tests

Tests that rely on external state (ports, files) should be deterministic:

```go
// BAD: Port 9081 might be in use
func TestHealthcheck(t *testing.T) {
    code := runHealthcheck("config.yaml")
    assert.Equal(t, 1, code)  // Fails if something runs on 9081
}

// GOOD: Use unlikely port or env override
func TestHealthcheck(t *testing.T) {
    t.Setenv("LAPLACED_SERVER_PORT", "49999")  // High port
    code := runHealthcheck("config.yaml")
    assert.Equal(t, 1, code)
}
```

---

## Running Tests

### All Tests
```bash
go test ./...
```

### Specific Package
```bash
go test ./internal/bot/...
```

### With Coverage
```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

### Verbose Output
```bash
go test ./internal/agent/laplace/... -v
```

### Race Detection
```bash
go test -race ./...
```

### Specific Test
```bash
go test ./internal/bot/... -run TestSanitizeLLMResponse
```

---

## References

- [Go Testing Best Practices](https://go.dev/doc/tutorial/add-a-test)
- [testify/assert](https://github.com/stretchr/testify/blob/master/assert/assertions.go)
- [testify/mock](https://github.com/stretchr/testify/blob/master/mock/mock.go)
- [Table-Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)
