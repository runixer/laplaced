package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/fetch"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// mockFetcher - local mock; fetch.Fetcher has exactly one consumer, so it
// stays package-local instead of testutil (same as ImageGenerator mocks).
type mockFetcher struct {
	res *fetch.Result
	err error
}

func (m *mockFetcher) Fetch(_ context.Context, _ string) (*fetch.Result, error) {
	return m.res, m.err
}

// setupReadURLTest builds an executor with the given fetcher and a mock
// agent-log store that accepts any entry.
func setupReadURLTest(t *testing.T, f fetch.Fetcher) (*ToolExecutor, *testutil.MockStorage) {
	t.Helper()
	mockStore := new(testutil.MockStorage)
	mockStore.On("AddAgentLog", mock.Anything).Return(nil).Maybe()
	exec := NewToolExecutor(new(testutil.MockLLMClient), mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
	exec.SetAgentLogger(agentlog.NewLogger(mockStore, testutil.TestLogger(), true))
	if f != nil {
		exec.SetFetcher(f)
	}
	return exec, mockStore
}

func readURLCallContext() CallContext {
	return CallContext{UserID: testutil.TestUserID}
}

func TestPerformReadURL_Success(t *testing.T) {
	f := &mockFetcher{res: &fetch.Result{
		Content:    "page body text",
		Title:      "Docs Page",
		FinalURL:   "https://docs.example.com/final",
		StatusCode: 200,
	}}
	exec, mockStore := setupReadURLTest(t, f)

	res, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://short.link/x"})
	require.NoError(t, err)

	assert.Contains(t, res.Content, "# Docs Page")
	assert.Contains(t, res.Content, "**Source:** https://docs.example.com/final")
	assert.Contains(t, res.Content, "page body text")
	assert.Contains(t, res.Content, `<source note=`)
	assert.Contains(t, res.Content, "https://docs.example.com/final</source>")

	// Both the requested and the resolved URL must be citable.
	require.Len(t, res.Citations, 2)
	assert.Equal(t, "https://short.link/x", res.Citations[0].URL)
	assert.Equal(t, "https://docs.example.com/final", res.Citations[1].URL)

	mockStore.AssertExpectations(t)
}

func TestPerformReadURL_SameURL_SingleCitation(t *testing.T) {
	f := &mockFetcher{res: &fetch.Result{Content: "text", FinalURL: "https://e.com/p"}}
	exec, _ := setupReadURLTest(t, f)

	res, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://e.com/p"})
	require.NoError(t, err)
	require.Len(t, res.Citations, 1)
	// No title -> no heading line.
	assert.NotContains(t, res.Content, "# \n")
	assert.True(t, strings.HasPrefix(res.Content, "**Source:**"))
}

func TestPerformReadURL_TruncatesByRunes(t *testing.T) {
	// Cyrillic content: rune-boundary truncation must not split characters.
	content := strings.Repeat("я", 200)
	f := &mockFetcher{res: &fetch.Result{Content: content, FinalURL: "https://e.com"}}
	exec, _ := setupReadURLTest(t, f)
	exec.cfg.Fetcher.MaxContentChars = 100

	res, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://e.com"})
	require.NoError(t, err)
	assert.Contains(t, res.Content, strings.Repeat("я", 100))
	assert.NotContains(t, res.Content, strings.Repeat("я", 101))
	assert.Contains(t, res.Content, "[... truncated: page content exceeds 100 characters]")
	assert.True(t, strings.Contains(res.Content, "�") == false, "no replacement chars from split runes")
}

func TestPerformReadURL_NoTruncationUnderCap(t *testing.T) {
	f := &mockFetcher{res: &fetch.Result{Content: "short", FinalURL: "https://e.com"}}
	exec, _ := setupReadURLTest(t, f)

	res, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://e.com"})
	require.NoError(t, err)
	assert.NotContains(t, res.Content, "truncated")
}

func TestPerformReadURL_MissingURL_ReturnsError(t *testing.T) {
	exec, _ := setupReadURLTest(t, &mockFetcher{})

	tests := []map[string]interface{}{
		{},
		{"url": ""},
		{"url": "   "},
		{"query": "https://e.com"}, // wrong parameter name
	}
	for _, args := range tests {
		_, err := exec.performReadURL(context.Background(), readURLCallContext(), args)
		assert.ErrorContains(t, err, "url argument is required")
	}
}

func TestPerformReadURL_UnwiredFetcher(t *testing.T) {
	exec, _ := setupReadURLTest(t, nil)

	res, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://e.com"})
	require.NoError(t, err)
	assert.Contains(t, res.Content, "READ FAILED")
	assert.Contains(t, res.Content, "not configured")
	assert.Empty(t, res.Citations)
}

func TestPerformReadURL_FailureKinds(t *testing.T) {
	tests := []struct {
		name         string
		fErr         *fetch.Error
		wantContains []string
	}{
		{
			name:         "refused (Reddit policy)",
			fErr:         &fetch.Error{Kind: fetch.KindRefused, StatusCode: 403, Msg: "policy"},
			wantContains: []string{"READ FAILED", "site policy", "do NOT hunt", "internet_search"},
		},
		{
			name:         "blocked marketplace with resolved shortlink",
			fErr:         &fetch.Error{Kind: fetch.KindBlocked, StatusCode: 403, FinalURL: "https://ozon.ru/product/iron-9000", Msg: "captcha"},
			wantContains: []string{"captcha", "resolves to https://ozon.ru/product/iron-9000"},
		},
		{
			name:         "not found",
			fErr:         &fetch.Error{Kind: fetch.KindNotFound, StatusCode: 404, Msg: "gone"},
			wantContains: []string{"HTTP 404", "dead or mistyped"},
		},
		{
			name:         "timeout",
			fErr:         &fetch.Error{Kind: fetch.KindTimeout, Msg: "deadline"},
			wantContains: []string{"too long to load", "did not respond"},
		},
		{
			name:         "rate limited",
			fErr:         &fetch.Error{Kind: fetch.KindRateLimited, StatusCode: 429, Msg: "429"},
			wantContains: []string{"rate-limited", "answer from what you have"},
		},
		{
			name:         "quota exhausted",
			fErr:         &fetch.Error{Kind: fetch.KindQuota, StatusCode: 402, Msg: "no credits"},
			wantContains: []string{"out of quota", "could not open the link"},
		},
		{
			name:         "config fault",
			fErr:         &fetch.Error{Kind: fetch.KindConfig, StatusCode: 401, Msg: "bad key"},
			wantContains: []string{"misconfigured", "cannot open links"},
		},
		{
			name:         "invalid url",
			fErr:         &fetch.Error{Kind: fetch.KindInvalidURL, Msg: "bad"},
			wantContains: []string{"not a valid http(s) URL"},
		},
		{
			name:         "upstream",
			fErr:         &fetch.Error{Kind: fetch.KindUpstream, Msg: "boom"},
			wantContains: []string{"boom", "retry ONCE"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec, _ := setupReadURLTest(t, &mockFetcher{err: tt.fErr})

			res, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://requested.example.com"})
			// Fetch failures are content, not errors — an error would render
			// as a bare "Tool execution failed" and trigger retry spirals.
			require.NoError(t, err)
			for _, want := range tt.wantContains {
				assert.Contains(t, res.Content, want)
			}
			// The requested URL must always be citable: an honest "couldn't
			// open [link](url)" about the user's own link must survive the
			// citation guard. The resolved URL joins it when present.
			require.NotEmpty(t, res.Citations)
			assert.Equal(t, "https://requested.example.com", res.Citations[0].URL)
			if tt.fErr.FinalURL != "" {
				require.Len(t, res.Citations, 2, "resolved URL must be citable even on failure")
				assert.Equal(t, tt.fErr.FinalURL, res.Citations[1].URL)
			} else {
				assert.Len(t, res.Citations, 1)
			}
		})
	}
}

func TestPerformReadURL_AgentLog(t *testing.T) {
	t.Run("success entry", func(t *testing.T) {
		f := &mockFetcher{res: &fetch.Result{Content: "body", FinalURL: "https://e.com", StatusCode: 200}}
		mockStore := new(testutil.MockStorage)
		var logged storage.AgentLog
		mockStore.On("AddAgentLog", mock.MatchedBy(func(l storage.AgentLog) bool {
			logged = l
			return true
		})).Return(nil).Once()
		exec := NewToolExecutor(new(testutil.MockLLMClient), mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
		exec.SetAgentLogger(agentlog.NewLogger(mockStore, testutil.TestLogger(), true))
		exec.SetFetcher(f)

		_, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://e.com"})
		require.NoError(t, err)

		assert.Equal(t, string(agentlog.AgentFetcher), logged.AgentType)
		assert.Equal(t, "https://e.com", logged.InputPrompt)
		assert.True(t, logged.Success)
		assert.Contains(t, logged.Metadata, `"final_url":"https://e.com"`)
		assert.Nil(t, logged.TotalCost, "fetch is not an LLM call — no cost")
		mockStore.AssertExpectations(t)
	})

	t.Run("failure entry", func(t *testing.T) {
		f := &mockFetcher{err: &fetch.Error{Kind: fetch.KindBlocked, StatusCode: 403, Msg: "captcha"}}
		mockStore := new(testutil.MockStorage)
		var logged storage.AgentLog
		mockStore.On("AddAgentLog", mock.MatchedBy(func(l storage.AgentLog) bool {
			logged = l
			return true
		})).Return(nil).Once()
		exec := NewToolExecutor(new(testutil.MockLLMClient), mockStore, mockStore, testutil.TestConfig(), testutil.TestLogger())
		exec.SetAgentLogger(agentlog.NewLogger(mockStore, testutil.TestLogger(), true))
		exec.SetFetcher(f)

		_, err := exec.performReadURL(context.Background(), readURLCallContext(), map[string]interface{}{"url": "https://e.com"})
		require.NoError(t, err)

		assert.Equal(t, string(agentlog.AgentFetcher), logged.AgentType)
		assert.False(t, logged.Success)
		assert.NotEmpty(t, logged.ErrorMessage)
		assert.Contains(t, logged.Metadata, `"error_kind":"blocked"`)
		mockStore.AssertExpectations(t)
	})
}

func TestExecuteToolCall_ReadURLDispatch(t *testing.T) {
	f := &mockFetcher{res: &fetch.Result{Content: "dispatched", FinalURL: "https://e.com"}}
	exec, _ := setupReadURLTest(t, f)
	exec.cfg.Tools = append(exec.cfg.Tools, config.ToolConfig{Name: "read_url"})

	res, err := exec.ExecuteToolCall(context.Background(), readURLCallContext(), "read_url", `{"url":"https://e.com"}`)
	require.NoError(t, err)
	assert.Contains(t, res.Content, "dispatched")
}
