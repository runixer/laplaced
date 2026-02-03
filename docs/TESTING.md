# Testing Standards

**Version:** 3.0 | **Target:** 80% coverage on core packages

---

## Core Rules

1. **Test behavior, not implementation** - Assert outcomes, not internal state
2. **One source of truth** - All mocks in `internal/testutil/`
3. **Table-driven by default** - `[]struct{}` + `t.Run()`
4. **Always `t.Helper()`** - In every test helper function
5. **Always `AssertExpectations(t)`** - At end of every mock test

---

## Quick Reference

### Mock Selection

```go
// Storage (all repos)
mockStore := new(testutil.MockStorage)

// LLM client
mockOR := new(testutil.MockOpenRouterClient)

// Telegram
mockAPI := new(testutil.MockBotAPI)

// Files
mockDownloader := new(testutil.MockFileDownloader)
mockSaver := new(testutil.MockFileSaver)

// RAG
mockRetriever := new(testutil.MockRetriever)
```

### Fixtures

```go
// Entities
testutil.TestUserID           // 123
testutil.TestUser()           // Single user
testutil.TestFacts()          // Sample facts
testutil.TestTopic()          // Single topic
testutil.TestMessages()       // 4-message history
testutil.TestPeople()         // 3 people

// LLM responses
testutil.MockChatResponse("content")
testutil.MockEmbeddingResponse()
testutil.TestEmbedding()      // 1536-dim vector
```

### Helpers

```go
testutil.TestLogger()         // Discarding logger
testutil.TestConfig()         // Full config
testutil.TestTranslator(t)    // i18n with test locales
testutil.SetupDefaultMocks(s) // Safe background ops
testutil.Ptr(42)              // *int with value 42
```

---

## Test Patterns

### Table-Driven Test

```go
func TestFunction(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"success", "valid", "result", false},
        {"error", "invalid", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Function(tt.input)
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            assert.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

### Mock Setup

```go
func TestWithMocks(t *testing.T) {
    mockStore := new(testutil.MockStorage)
    mockOR := new(testutil.MockOpenRouterClient)

    // Setup expectations
    mockStore.On("GetFacts", int64(123)).Return(testutil.TestFacts(), nil)
    mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).
        Return(testutil.MockChatResponse("response"), nil)

    // Execute
    svc := NewService(testutil.TestLogger(), mockStore, mockOR)
    result, err := svc.Process(context.Background(), 123)

    // Assert
    assert.NoError(t, err)
    assert.Equal(t, expected, result)
    mockStore.AssertExpectations(t)
    mockOR.AssertExpectations(t)
}
```

### Setup Helper

```go
func setupServiceTest(t *testing.T) (*Service, *testutil.MockStorage) {
    t.Helper()
    mockStore := new(testutil.MockStorage)
    testutil.SetupDefaultMocks(mockStore)
    svc := NewService(testutil.TestLogger(), mockStore, testutil.TestConfig())
    return svc, mockStore
}

func TestFeature(t *testing.T) {
    svc, mockStore := setupServiceTest(t)
    mockStore.On("GetFacts", mock.Anything).Return(testutil.TestFacts(), nil)
    // ... test logic
}
```

---

## HTTP Testing

```go
// Create request
req := testutil.NewTestRequest(t, "GET", "/api/stats?user_id=123", nil)
req := testutil.NewTestRequest(t, "POST", "/api/data", map[string]any{"key": "value"})

// Execute
rr := testutil.ExecuteRequest(t, handler, req)

// Assert
testutil.AssertStatusCode(t, rr, http.StatusOK)
testutil.AssertJSONResponse(t, rr, http.StatusOK, &response)
testutil.AssertErrorResponse(t, rr, http.StatusBadRequest, "invalid")
```

### SSE Testing

```go
rec := testutil.NewSSERecorder()
handler(rec, req)

testutil.AssertSSEHeaders(t, rec)
events := testutil.ParseSSEEvents(t, rec.Body.String())
assert.GreaterOrEqual(t, len(events), 1)
```

---

## Naming Convention

```go
// Pattern: Test<Function>_<Scenario>
func TestRetrieve_EmptyQuery_ReturnsError(t *testing.T) {}
func TestRetrieve_WithTopics_ReturnsGrouped(t *testing.T) {}
func TestRetrieve_StorageError_Propagates(t *testing.T) {}
```

---

## Import Cycle Handling

If adding mock to testutil creates import cycle:

1. Create `package/mocks_test.go` for package-local mocks
2. Document reason: `// mockFoo - local mock, avoids import cycle with testutil`

**Existing legitimate local mocks:**
- `bot/mocks_test.go` - `mockMemoryService` (bot → memory)
- `laplace_context_test.go` - `mockRetriever` (laplace → rag)
- `server_test.go` - `MockBotInterface` (web → bot)

---

## Commands

```bash
# Run all tests
go test ./...

# Coverage report (same as CI - excludes cmd/*)
go test ./... -coverprofile=coverage.out -coverpkg=./internal/...
go tool cover -func=coverage.out | grep total

# Package coverage
go test ./internal/bot/... -cover

# Quality checks (required before merge)
go test -race ./...
go test -shuffle=on ./...
```

---

## Coverage Exclusions

These packages are excluded from 80% target (see `codecov.yml`):

| Package | Reason |
|---------|--------|
| `cmd/*` | Entry point wiring |
| `internal/testutil` | Test utilities |
| `internal/agent/testing` | Test mocks |
| `internal/agent/prompts` | Type definitions |
| `internal/app` | App wiring |
| `internal/gen/**/*` | Generated code |

**Target:** 78% overall coverage (excludes above packages)

---

## Anti-Patterns

**DON'T:**
```go
// Inline mock (use testutil instead)
type mockStore struct{}

// Direct internal access
svc.internalField = "test"

// Unclear names
func TestFoo(t *testing.T) {}
func TestFoo2(t *testing.T) {}
```

**DO:**
```go
// Centralized mock
mockStore := new(testutil.MockStorage)

// Configure via public API
cfg := testutil.TestConfig()
cfg.Feature.Enabled = true
svc := NewService(cfg)

// Descriptive names
func TestProcess_InvalidInput_ReturnsError(t *testing.T) {}
```

---

## References

- [Audit Report](./plans/AUDIT_REPORT_PHASE_2.md)
- [Coverage Roadmap](./plans/test-coverage-roadmap-v3.md)
