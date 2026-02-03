# Test Coverage Roadmap v2

**Project:** Laplaced v0.6.1+
**Created:** 2026-02-03
**Updated:** 2026-02-03 (Phase 6 Extension complete)
**Target:** 80% Code Coverage + Test Maintainability
**Predecessor:** [test-coverage-improvement.md](./test-coverage-improvement.md) (Phases 1-3 complete)

---

## Executive Summary

This roadmap builds on completed Phases 1-3 (tools, RAG, files) and addresses:
1. **Modernization** - Migrate legacy tests to testutil patterns ✅ **Done**
2. **New Coverage** - Fill remaining gaps to reach 80% ✅ **Bot Core & Storage Done**
3. **Quality** - Enforce consistent testing standards

**Progress:** Phase 4 ✅ | Phase 5 ✅ | Phase 6 ✅ | Phase 6 Extension ✅ | Phase 7 Pending
**Timeline:** ~15-20 hours total work (~14 hours completed)

**Current Coverage (2026-02-03 after Phase 6 Extension):**
- Overall: **~66%** (target: 80%)
- Bot: **80.3%** ✅ (target: 80% - achieved!)
- Bot/tools: **81.1%** ✅
- Storage: **84.4%** ✅ (target: 80% - exceeded!)

---

## RICEF Prioritization

| Task | Reach | Impact | Confidence | Effort | Score | Priority |
|------|-------|--------|------------|--------|-------|----------|
| Modernize inline mocks | High (6 files) | High (maintenance) | High | Low (3h) | **27** | **P1** |
| Bot core coverage +11% | High (all users) | High (reliability) | Medium | Medium (5h) | **18** | **P1** |
| Storage edge cases +12% | Medium (data layer) | High (integrity) | High | Medium (4h) | **16** | **P2** |
| Config coverage +29% | Low (startup) | Medium (reliability) | High | Low (2h) | **9** | **P2** |
| Web handlers +26% | Low (dashboard) | Medium (debuggability) | Medium | High (5h) | **8** | **P3** |
| cmd/bot entry point | Low (main()) | Low (wiring) | Low | High | **2** | **P4** |

---

## Phase 4: MODERNIZATION (P1) ✅ **COMPLETED**

**Goal:** Consolidate all mocks to testutil, eliminate duplication
**Effort:** ~3-4 hours → **Actual: ~3 hours**
**Coverage Delta:** 0% (quality improvement)

**Status:** All duplicate mocks migrated. Remaining inline mocks are legitimate (avoid import cycles or minimal stubs).

### Completed Actions

| File | Action | Status |
|------|--------|--------|
| `laplace_artifact_test.go` | ✅ Migrated to `testutil.MockStorage` | Done |
| `service_integration_test.go` | ✅ Deleted `MockArtifactRepository`, use `testutil.MockStorage` | Done |
| `pipeline_test.go` | ✅ Deleted `MockArtifactRepository`, use `testutil.MockStorage` | Done |
| `artifacts_processing_test.go` | ✅ Deleted `MockArtifactRepository` (9 usages) | Done |
| `processor_test.go` | ✅ Migrated to `testutil.MockFileDownloader`, `testutil.MockFileSaver` | Done (2026-02-03) |
| `message_grouper_test.go` | ✅ Extracted `setupMessageGrouper()` helper | Done (2026-02-03) |
| `people_test.go` | ✅ Extracted `setupBotForPeopleTests()` helper | Done (2026-02-03) |
| `bot/mocks_test.go` | ✅ Created for shared `mockMemoryService` (import cycle) | Done (2026-02-03) |
| `bot_errors_test.go` | ✅ Removed duplicate `mockMemoryService` | Done (2026-02-03) |
| `bot_rpc_test.go` | ✅ Removed duplicate mocks, use `testutil.MockFileSaver` | Done (2026-02-03) |
| `laplace_context_test.go` | ⚠️ `mockRetriever` - LEGITIMATE (import cycle avoidance) | Kept |
| `builder_test.go` | ⚠️ `mockAgent`, `mockMemoryService` - LEGITIMATE (minimal stubs) | Kept |
| `registry_test.go` | ⚠️ `mockAgent` - LEGITIMATE (minimal stub) | Kept |
| `reranker_test.go` | ⚠️ `MockMessageRepository` - LEGITIMATE (map-based stub) | Kept |
| `server_test.go` | ⚠️ `MockBotInterface` - LEGITIMATE (import cycle) | Kept |

### 4.3 Extract Setup Helpers ✅ DONE (2026-02-03)

**File:** `internal/bot/message_grouper_test.go`

```go
func setupMessageGrouper(t *testing.T, turnWait time.Duration) (*MessageGrouper, func() *MessageGroup) {
    t.Helper()
    logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
    var groupProcessed *MessageGroup
    var mu sync.Mutex
    onGroupReady := func(ctx context.Context, group *MessageGroup) {
        mu.Lock()
        groupProcessed = group
        mu.Unlock()
    }
    grouper := NewMessageGrouper(nil, logger, turnWait, onGroupReady)
    getGroup := func() *MessageGroup {
        mu.Lock()
        defer mu.Unlock()
        return groupProcessed
    }
    return grouper, getGroup
}
```

**File:** `internal/bot/people_test.go`

```go
func setupBotForPeopleTests(t *testing.T) (*Bot, *testutil.MockStorage, *testutil.MockOpenRouterClient, *slog.Logger) {
    t.Helper()
    translator := testutil.TestTranslator(t)
    logger := testutil.TestLogger()
    mockAPI := new(testutil.MockBotAPI)
    mockStore := new(testutil.MockStorage)
    mockORClient := new(testutil.MockOpenRouterClient)
    cfg := testutil.TestConfig()
    mockDownloader := new(testutil.MockFileDownloader)

    bot := &Bot{
        api:             mockAPI,
        userRepo:        mockStore,
        msgRepo:         mockStore,
        statsRepo:       mockStore,
        factRepo:        mockStore,
        factHistoryRepo: mockStore,
        peopleRepo:      mockStore,
        orClient:        mockORClient,
        downloader:      mockDownloader,
        fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", logger),
        cfg:             cfg,
        logger:          logger,
        translator:      translator,
    }
    return bot, mockStore, mockORClient, logger
}
```

### 4.4 Verification

```bash
# Should return 0 after migration
grep -r "type mock" --include="*_test.go" internal/ | grep -v testutil | wc -l

# All tests still pass
go test ./internal/agent/laplace/... ./internal/agent/reranker/... ./internal/bot/... ./internal/files/... ./internal/rag/...
```

---

## Phase 5: NEW COVERAGE - Bot Core (P1) ✅ **COMPLETED**

**Goal:** 68.8% -> 80% coverage for internal/bot
**Effort:** ~4-5 hours → **Actual: ~5 hours**
**Coverage Delta:** +11.5% (68.8% -> 80.3%)
**Date Completed:** 2026-02-03

### Completed Actions

| Test File | Tests Added | Coverage |
|-----------|-------------|----------|
| `bot_handlers_test.go` | 14 tests | HandleUpdate, HandleUpdateAsync, ProcessUpdateAsync, authorization, concurrent processing |
| `bot_forwarded_people_test.go` | 12 tests | extractForwardedPeople logic, new/existing person handling, filtering |
| `bot_rpc_test.go` | 12 tests | RPC methods (GetActiveSessions, ForceCloseSession, SetWebhook), DI setters |
| `bot_errors_test.go` | 8 tests | Error paths in processMessageGroup, file errors, LLM errors, context cancellation |
| `bot_test.go` (extended) | 5 tests | sendResponses edge cases, SendTestMessage error paths |
| `metrics_test.go` (extended) | 1 test | RecordLLMAnomaly |

**Total New Tests:** 52 tests

### 5.1 New Test File: bot_handlers_test.go

```go
package bot

// Tests for HandleUpdate authorization and user upsert
func TestHandleUpdate_AllowedUser_Success(t *testing.T)
func TestHandleUpdate_UnauthorizedUser_ReturnsEarly(t *testing.T)
func TestHandleUpdate_UpsertUserError_LogsAndContinues(t *testing.T)
func TestHandleUpdate_NilMessage_ReturnsEarly(t *testing.T)

// Tests for async processing
func TestHandleUpdateAsync_ConcurrentCalls(t *testing.T)
func TestHandleUpdateAsync_PanicInGoroutine_Recovered(t *testing.T)
func TestProcessUpdateAsync_QueueMechanics(t *testing.T)
func TestProcessUpdateAsync_ContextCancellation_StopsProcessing(t *testing.T)

// Tests for message routing
func TestHandleUpdate_VoiceMessage_RoutedToGrouper(t *testing.T)
func TestHandleUpdate_PhotoMessage_RoutedToGrouper(t *testing.T)
func TestHandleUpdate_InvalidJSON_LogsError(t *testing.T)

// Tests for authorization check
func TestIsAllowed_EmptyAllowedList_ReturnsFalse(t *testing.T)
func TestIsAllowed_UserInAllowedList_ReturnsTrue(t *testing.T)

// Tests for sendAction
func TestSendAction_Success(t *testing.T)
func TestSendAction_APIError_LogsWarning(t *testing.T)
func TestSendAction_WithContextCancellation_StopsGracefully(t *testing.T)
func TestSendAction_AllActionTypes(t *testing.T)
func TestSendAction_ZeroMessageThreadID_SendsNil(t *testing.T)
func TestSendAction_NonZeroMessageThreadID_SendsValue(t *testing.T)
func TestSendTypingActionLoop_Cancellation_StopsLoop(t *testing.T)
func TestSendTypingActionLoop_SendsPeriodicActions(t *testing.T)
```

### 5.2 New Test File: bot_forwarded_people_test.go

```go
package bot

// Tests for new person creation
func TestExtractForwardedPeople_NewPerson_CreatesPerson(t *testing.T)
func TestExtractForwardedPeople_DisplayNameFromUsername(t *testing.T)

// Tests for existing person updates
func TestExtractForwardedPeople_ExistingPersonByTelegramID_UpdatesPerson(t *testing.T)
func TestExtractForwardedPerson_ExistingPersonByUsername_AddsTelegramID(t *testing.T)
func TestExtractForwardedPerson_ExistingPersonByName_AddsTelegramID(t *testing.T)

// Tests for filtering
func TestExtractForwardedPeople_ChannelForward_Ignored(t *testing.T)
func TestExtractForwardedPeople_BotForward_Ignored(t *testing.T)
func TestExtractForwardedPeople_SelfForward_Ignored(t *testing.T)
func TestExtractForwardedPeople_NoForwardOrigin_Skips(t *testing.T)

// Tests for edge cases
func TestExtractForwardedPeople_NilPeopleRepo_DoesNothing(t *testing.T)
func TestExtractForwardedPeople_MultipleMessages_ProcessesAll(t *testing.T)
func TestExtractForwardedPeople_DuplicateForwards_Deduplicates(t *testing.T)
func TestExtractForwardedPeople_UpdateError_Continues(t *testing.T)
```

### 5.3 New Test File: bot_rpc_test.go

```go
package bot

// Tests for RPC methods
func TestSetCommands_ClearsCommands(t *testing.T)
func TestSetCommands_APIError_ReturnsError(t *testing.T)
func TestGetActiveSessions_DelegatesToRAG(t *testing.T)
func TestGetActiveSessions_RAGServiceNil_Panics(t *testing.T)
func TestForceCloseSession_DelegatesToRAG(t *testing.T)
func TestForceCloseSession_RAGServiceNil_Panics(t *testing.T)
func TestForceCloseSessionWithProgress_RAGServiceNil_Panics(t *testing.T)
func TestSetWebhook_CallsAPI(t *testing.T)
func TestSetWebhook_APIError_PropagatesError(t *testing.T)
func TestAPI_ReturnsAPI(t *testing.T)

// Tests for DI setters (smoke tests)
func TestSetArtifactRepo_VerifiesAssignment(t *testing.T)
func TestSetFileProcessor_VerifiesAssignment(t *testing.T)
func TestSetAgentLogger_VerifiesAssignment(t *testing.T)
func TestSetLaplaceAgent_VerifiesAssignment(t *testing.T)
func TestSetFileHandler_VerifiesAssignment(t *testing.T)

// Tests for Stop behavior
func TestStop_StopsGracefully(t *testing.T)
func TestStop_NilMessageGrouper_Panics(t *testing.T)
```

### 5.4 New Test File: bot_errors_test.go

```go
package bot

// Tests for error paths in processMessageGroup
func TestProcessMessageGroup_EmptyMessageGroup_ReturnsEarly(t *testing.T)
func TestProcessMessageGroup_UnsupportedFileType_SendsErrorMessage(t *testing.T)
func TestProcessMessageGroup_FileTooLarge_SendsErrorMessage(t *testing.T)
func TestProcessMessageGroup_AddMessageToHistoryError_ReturnsEarly(t *testing.T)
func TestProcessMessageGroup_LaplaceAgentNil_SendsGenericError(t *testing.T)
func TestProcessMessageGroup_LLMExecutionError_SendsErrorMessage(t *testing.T)
func TestProcessMessageGroup_AddStatFailure_Continues(t *testing.T)
func TestProcessMessageGroup_ContextCancellation_CompletesProcessing(t *testing.T)
```

### 5.5 Verification

```bash
go test ./internal/bot/... -cover
# Result: coverage: 80.3% of statements ✅

go test ./internal/bot/... -race
# Result: all tests pass ✅
```

---

## Phase 6: NEW COVERAGE - Storage (P2) ✅ **COMPLETED**

**Goal:** 58.3% -> 70% coverage for internal/storage
**Effort:** ~3-4 hours → **Actual: ~3 hours**
**Coverage Delta:** +13.7% (58.3% -> 72.0%)
**Date Completed:** 2026-02-03

### Completed Actions

| Test File | Tests Added | Coverage |
|-----------|-------------|----------|
| `format_test.go` | 6 test functions + 30+ subtests | All format.go functions |
| `sqlite_agent_log_test.go` | 6 test functions + 20+ subtests | GetAgentLogs, GetAgentLogsExtended, GetAgentLogFull, scanAgentLogs |
| `sqlite_artifact_test.go` (extended) | 6 test functions + 15+ subtests | GetPendingArtifacts, UpdateArtifact, GetArtifactsByIDs, IncrementContextLoadCount, UpdateMessageID |
| `metrics_test.go` | 5 test functions + 15+ subtests | SetStorageSize, SetTableSize, RecordCleanupDeleted, RecordCleanupDuration |

**Total New Tests:** 23 test functions with 80+ subtests

### Completed Actions

| Test File | Tests Added | Coverage |
|-----------|-------------|----------|
| `format_test.go` | 6 test functions + 30+ subtests | All format.go functions |
| `sqlite_agent_log_test.go` | 6 test functions + 20+ subtests | GetAgentLogs, GetAgentLogsExtended, GetAgentLogFull, scanAgentLogs |
| `sqlite_artifact_test.go` (extended) | 6 test functions + 15+ subtests | GetPendingArtifacts, UpdateArtifact, GetArtifactsByIDs, IncrementContextLoadCount, UpdateMessageID |
| `metrics_test.go` | 5 test functions + 15+ subtests | SetStorageSize, SetTableSize, RecordCleanupDeleted, RecordCleanupDuration |

**Total New Tests:** 23 test functions with 80+ subtests

### 6.1 New Test File: format_test.go

```go
package storage

func TestFormatUserProfile(t *testing.T)     // Empty, single, multiple facts
func TestFormatUserProfileCompact(t *testing.T) // Empty, single, multiple facts
func TestFormatRecentTopics(t *testing.T)     // Empty, single, multiple topics
func TestFilterProfileFacts(t *testing.T)     // Identity, high importance, mixed
func TestFormatPeople(t *testing.T)           // Empty, with tag, all fields, multiple
func TestFilterInnerCircle(t *testing.T)       // Work_Inner, Family, mixed
func TestTagConstants(t *testing.T)           // Constant values
```

### 6.2 New Test File: sqlite_agent_log_test.go

```go
package storage

func TestAddAgentLog(t *testing.T)                      // Insert log with all fields
func TestGetAgentLogs(t *testing.T)                     // Filter by user/agent type, limit
func TestGetAgentLogsExtended(t *testing.T)             // Filter by multiple criteria, pagination
func TestGetAgentLogFull(t *testing.T)                  // Get by ID with user validation
func TestAgentLogConversationTurns(t *testing.T)         // Multi-turn conversation tracking
```

### 6.3 Extended sqlite_artifact_test.go

```go
package storage

func TestGetPendingArtifacts(t *testing.T)              // Pending, retry with backoff, max retries
func TestUpdateArtifact(t *testing.T)                   // Update to ready, failed, not found
func TestGetArtifactsByIDs(t *testing.T)                 // Batch load, user isolation
func TestIncrementContextLoadCount(t *testing.T)        // Increment load counters
func TestUpdateMessageID(t *testing.T)                   // Link artifact to message
```

**Added helper functions:**
- `setArtifactRetryCount()` - Set retry_count directly in DB
- `setArtifactLastFailedAt()` - Set last_failed_at directly in DB

### 6.4 New Test File: metrics_test.go

```go
package storage

func TestSetStorageSize(t *testing.T)       // Set size, zero, negative
func TestSetTableSize(t *testing.T)         // Various tables, updates, empty name
func TestRecordCleanupDeleted(t *testing.T)  // Record deletions, accumulate, panic on negative
func TestRecordCleanupDuration(t *testing.T) // Record durations, various values
func TestMetricsConstants(t *testing.T)      // Namespace constant
```

### 6.5 Verification

```bash
go test ./internal/storage/... -cover
# Result: coverage: 72.0% of statements ✅

go test ./internal/storage/... -race
# Result: all tests pass ✅
```

---

## Phase 6 Extension: Storage to 80% (P2) ✅ **COMPLETED**

**Goal:** 72.0% -> 80% coverage for internal/storage
**Effort:** ~2-3 hours → **Actual: ~2.5 hours**
**Coverage Delta:** +12.4% (72.0% -> 84.4%)
**Date Completed:** 2026-02-03

### Completed Actions

| Test File | Tests Added | Functions Covered |
|-----------|-------------|-------------------|
| `sqlite_topic_test.go` (extended) | 4 test functions + 10+ subtests | AddTopicWithoutMessageUpdate, DeleteAllTopics, DeleteTopicCascade, GetTopicsAfterID |
| `sqlite_message_test.go` (extended) | 3 test functions + 10+ subtests | GetMessagesByTopicID, UpdateMessagesTopicInRange, GetRecentSessionMessages |
| `sqlite_fact_history_test.go` (extended) | 1 test function + 3 subtests | UpdateFactHistoryTopic |
| `sqlite_person_test.go` (extended) | 1 test function + 3 subtests | DeleteAllPeople |
| `sqlite_maintenance_test.go` (extended) | 9 test functions + 25+ subtests | CountAgentLogs, CountFactHistory, CountOrphanedTopics, GetOrphanedTopicIDs, CountOverlappingTopics, GetOverlappingTopics, CountFactsOnOrphanedTopics, RecalculateTopicRanges, RecalculateTopicSizes |

**Total New Tests:** 18 test functions with 50+ subtests

### 6E.1 Topic Operations Tests

```go
package storage

func TestAddTopicWithoutMessageUpdate(t *testing.T)
// - Creates topic without updating message topic_ids
// - Compares behavior with AddTopic (which does update)

func TestDeleteAllTopics(t *testing.T)
// - Deletes all topics for a user
// - User isolation (other user's topics intact)
// - No error when none exist

func TestDeleteTopicCascade(t *testing.T)
// - Cascade delete removes topic + linked facts + history
// - User isolation check
// - Non-existent topic handling

func TestGetTopicsAfterID(t *testing.T)
// - Cross-user incremental loading
// - Empty result for non-existent ID
// - Ordering by ID ASC
```

### 6E.2 Message Operations Tests

```go
package storage

func TestGetMessagesByTopicID(t *testing.T)
// - Get messages by topic (cross-user)
// - Empty result for non-existent topic

func TestUpdateMessagesTopicInRange(t *testing.T)
// - Bulk update with user_id filter (critical for security)
// - User isolation verification
// - Invalid range handling

func TestGetRecentSessionMessages(t *testing.T)
// - Get recent unprocessed messages
// - Exclude message IDs (for deduplication)
// - Zero/negative limit handling
// - User isolation
```

### 6E.3 Fact History & Person Tests

```go
package storage

func TestUpdateFactHistoryTopic(t *testing.T)
// - Update topic_id in fact_history (cross-user)
// - Non-existent topic handling

func TestDeleteAllPeople(t *testing.T)
// - Delete all people for user
// - User isolation
// - Removes all circles
```

### 6E.4 Maintenance Tests

```go
package storage

func TestCountAgentLogs(t *testing.T)
func TestCountFactHistory(t *testing.T)
func TestOrphanedTopics(t *testing.T)           // Count & get topics with no messages
func TestOverlappingTopics(t *testing.T)        // Detect overlapping message ranges
func TestCountFactsOnOrphanedTopics(t *testing.T)
func TestRecalculateTopicRanges(t *testing.T)   // Fix start/end_msg_id from actual messages
func TestRecalculateTopicSizes(t *testing.T)    // Fix size_chars from actual content
```

### 6E.5 Verification

```bash
go test ./internal/storage/... -cover
# Result: coverage: 84.4% of statements ✅ (exceeded 80% target!)

go test ./internal/storage/... -race
# Result: all tests pass ✅
```

### 6E.6 Key Findings

1. **User Data Isolation**: All range-based operations properly include `user_id` filter
2. **Cross-User Functions**: `GetMessagesByTopicID`, `UpdateFactHistoryTopic`, `GetOrphanedTopicIDs` are intentionally cross-user
3. **Maintenance Tools**: Orphaned topic detection and recalculation functions now fully tested

### 6.1 New Test File: sqlite_concurrent_test.go

```go
package storage

func TestConcurrentFactInserts(t *testing.T) {
    store, cleanup := setupTestDB(t)
    defer cleanup()

    const goroutines = 10
    const insertsPerGoroutine = 50

    var wg sync.WaitGroup
    wg.Add(goroutines)

    for i := 0; i < goroutines; i++ {
        go func(workerID int) {
            defer wg.Done()
            for j := 0; j < insertsPerGoroutine; j++ {
                fact := storage.Fact{
                    UserID:  testutil.TestUserID,
                    Content: fmt.Sprintf("fact-%d-%d", workerID, j),
                    Type:    "test",
                }
                _, err := store.AddFact(fact)
                assert.NoError(t, err)
            }
        }(i)
    }

    wg.Wait()

    facts, err := store.GetFacts(testutil.TestUserID)
    assert.NoError(t, err)
    assert.Len(t, facts, goroutines*insertsPerGoroutine)
}
```

### 6.2 New Test File: sqlite_edge_cases_test.go

```go
package storage

func TestUserDataIsolation_CrossUserQuery(t *testing.T) {
    store, cleanup := setupTestDB(t)
    defer cleanup()

    // Insert messages for two users with interleaved IDs
    user1, user2 := int64(100), int64(200)

    store.AddMessageToHistory(user1, storage.Message{Content: "user1-msg1"})
    store.AddMessageToHistory(user2, storage.Message{Content: "user2-msg1"})
    store.AddMessageToHistory(user1, storage.Message{Content: "user1-msg2"})
    store.AddMessageToHistory(user2, storage.Message{Content: "user2-msg2"})

    // Query user1's messages - should NOT include user2's
    msgs, err := store.GetRecentHistory(user1, 10)
    assert.NoError(t, err)
    assert.Len(t, msgs, 2)
    for _, msg := range msgs {
        assert.Contains(t, msg.Content, "user1")
    }
}
```

### 6.3 Tests to Add

| Test Function | Scenario | Coverage Target |
|---------------|----------|-----------------|
| `TestConcurrentFactInserts` | Parallel writes | WAL mode |
| `TestConcurrentTopicUpdates` | Parallel updates | Locking |
| `TestTransactionRollback` | Error during tx | Atomicity |
| `TestGetFacts_EmptyResult` | No facts exist | Edge case |
| `TestGetTopics_LargeDataset` | 1000+ topics | Performance |
| `TestUserDataIsolation_CrossUserQuery` | Multi-user | Security |

---

## Phase 7: NEW COVERAGE - Config & Web (P2/P3)

**Goal:** Config 40.8%->70%, Web 34.3%->60%
**Effort:** ~4-5 hours
**Coverage Delta:** Config +29.2%, Web +25.7%

### 7.1 Config Tests

**File:** `internal/config/config_test.go` (extend)

```go
func TestConfig_EnvOverrides(t *testing.T) {
    tests := []struct {
        name     string
        envVars  map[string]string
        validate func(*testing.T, *Config)
    }{
        {
            name: "telegram token override",
            envVars: map[string]string{
                "LAPLACED_TELEGRAM_TOKEN": "test-token",
            },
            validate: func(t *testing.T, cfg *Config) {
                assert.Equal(t, "test-token", cfg.Telegram.Token)
            },
        },
        {
            name: "openrouter api key override",
            envVars: map[string]string{
                "LAPLACED_OPENROUTER_API_KEY": "test-key",
            },
            validate: func(t *testing.T, cfg *Config) {
                assert.Equal(t, "test-key", cfg.OpenRouter.APIKey)
            },
        },
        // ... all LAPLACED_* vars
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            for k, v := range tt.envVars {
                t.Setenv(k, v)
            }
            cfg, err := Load("testdata/config.yaml")
            assert.NoError(t, err)
            tt.validate(t, cfg)
        })
    }
}
```

### 7.2 Web Test Helpers

**File:** `internal/testutil/http.go` (NEW)

```go
package testutil

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/stretchr/testify/assert"
)

// NewTestRequest creates an HTTP request for handler testing.
func NewTestRequest(t *testing.T, method, url string, body interface{}) *http.Request {
    t.Helper()
    var buf bytes.Buffer
    if body != nil {
        json.NewEncoder(&buf).Encode(body)
    }
    req, err := http.NewRequest(method, url, &buf)
    if err != nil {
        t.Fatalf("failed to create request: %v", err)
    }
    req.Header.Set("Content-Type", "application/json")
    return req
}

// AssertJSONResponse verifies JSON response body.
func AssertJSONResponse(t *testing.T, rr *httptest.ResponseRecorder, expected interface{}) {
    t.Helper()
    var actual interface{}
    err := json.Unmarshal(rr.Body.Bytes(), &actual)
    assert.NoError(t, err)
    expectedJSON, _ := json.Marshal(expected)
    actualJSON, _ := json.Marshal(actual)
    assert.JSONEq(t, string(expectedJSON), string(actualJSON))
}

// AssertErrorResponse verifies error response.
func AssertErrorResponse(t *testing.T, rr *httptest.ResponseRecorder, code int, messageContains string) {
    t.Helper()
    assert.Equal(t, code, rr.Code)
    assert.Contains(t, rr.Body.String(), messageContains)
}
```

### 7.3 Web Handler Tests

**File:** `internal/web/handlers_test.go` (NEW)

```go
package web

func TestStatsHandler(t *testing.T) {
    mockBot := new(MockBotInterface)
    mockBot.On("GetStats").Return(Stats{Users: 5, Messages: 100})

    server := NewServer(testutil.TestLogger(), mockBot)

    req := testutil.NewTestRequest(t, "GET", "/api/stats", nil)
    rr := httptest.NewRecorder()

    server.ServeHTTP(rr, req)

    assert.Equal(t, http.StatusOK, rr.Code)
    testutil.AssertJSONResponse(t, rr, map[string]interface{}{
        "users":    5,
        "messages": 100,
    })
}
```

---

## Success Criteria

### Coverage Targets

| Package | Before | After | Delta | Status | Date |
|---------|--------|-------|-------|--------|------|
| `internal/bot` | 68.8% | **80.3%** | +11.5% | ✅ Done | 2026-02-03 |
| `internal/bot/tools` | - | 81.1% | - | ✅ Done | 2026-02-03 |
| `internal/storage` | 58.3% | **84.4%** | +26.1% | ✅ Done | 2026-02-03 |
| `internal/config` | 40.8% | 70%+ | +29.2% | Pending | - |
| `internal/web` | 34.3% | 60%+ | +25.7% | Pending | - |
| **Overall** | ~61% | **~66%** | **+5%** | In Progress | - |

### Quality Targets

| Metric | Target | Status |
|--------|--------|--------|
| Inline mocks outside testutil/mocks_test.go | 0 | ✅ Done |
| Tests passing with `-race` | 100% | ✅ Pass |
| Tests passing with `-shuffle=on` | 100% | ✅ Pass |
| golangci-lint warnings | 0 | ✅ Pass |

---

## Verification Commands

```bash
# Full coverage check
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out | grep -E "(total|internal/)"

# Quality checks
go test -race ./...
go test -shuffle=on ./...
golangci-lint run ./...

# Specific package coverage
go test ./internal/bot/... -cover
go test ./internal/storage/... -cover
go test ./internal/config/... -cover
go test ./internal/web/... -cover

# Verify no inline mocks (except legitimate package-local mocks)
grep -r "type mock" --include="*_test.go" internal/ | grep -v testutil | grep -v mocks_test.go
# Should return only legitimate inline mocks (import cycle avoidance, single-use stubs)
```

---

## Timeline

| Phase | Effort | Status | Date Completed |
|-------|--------|--------|----------------|
| Phase 4: Modernization | ~3-4h | ✅ Done | 2026-02-03 |
| Phase 5: Bot Core | ~4-5h | ✅ Done | 2026-02-03 |
| Phase 6: Storage | ~3h | ✅ Done | 2026-02-03 |
| Phase 6 Extension: Storage to 80% | ~2.5h | ✅ Done | 2026-02-03 |
| Phase 7: Config & Web | ~4-5h | Pending | - |

**Completed:** ~14 hours (Phases 4-6 + Extension)
**Remaining:** ~4-5 hours (Phase 7)

**Recommended Order:** 4 -> 5 -> 6 -> 6 Extension -> 7

---

**Related Documents:**
- [TEST_SYSTEM_AUDIT.md](./TEST_SYSTEM_AUDIT.md) - Current state analysis
- [TESTING_V2.md](../TESTING.md) - Testing standards guide
