package reactor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
)

var testAllowed = []string{"👍", "❤", "🔥", "🤣", "❤‍🔥", "🤷‍♂"}

func TestValidateEmoji(t *testing.T) {
	shortcodes := []string{"+1", "fire", "thinking_face"}

	tests := []struct {
		name      string
		candidate string
		allowed   []string
		want      string
	}{
		{"exact match", "🔥", testAllowed, "🔥"},
		{"variation selector stripped", "❤️", testAllowed, "❤"},
		{"zwj with variation selector", "❤️‍🔥", testAllowed, "❤‍🔥"},
		{"zwj shrug male", "🤷‍♂️", testAllowed, "🤷‍♂"},
		{"none lowercase", "none", testAllowed, ""},
		{"none uppercase", "NONE", testAllowed, ""},
		{"empty", "", testAllowed, ""},
		{"whitespace only", "  ", testAllowed, ""},
		{"not in list", "💀", testAllowed, ""},
		{"whitespace trimmed", " 👍 ", testAllowed, "👍"},
		{"shortcode exact", "fire", shortcodes, "fire"},
		{"shortcode with colons", ":fire:", shortcodes, "fire"},
		{"shortcode unknown", "rocket", shortcodes, ""},
		{"empty allowed list", "👍", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ValidateEmoji(tt.candidate, tt.allowed))
		})
	}
}

func setupReactorTest(t *testing.T, llmResponse string) (*Reactor, *testutil.MockLLMClient, *config.Config) {
	t.Helper()
	mockClient := &testutil.MockLLMClient{}
	if llmResponse != "" {
		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
			Return(testutil.MockChatResponse(llmResponse), nil)
	}
	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Reactor.Model = "test-model"
	return New(executor, testutil.TestTranslator(t), cfg), mockClient, cfg
}

func reactorRequest(allowed []string) *agent.Request {
	return &agent.Request{
		Query: "We shipped the release!",
		Shared: &agent.SharedContext{
			UserID:  "123",
			Profile: "<user_profile>\n</user_profile>",
		},
		Params: map[string]any{
			ParamAllowedReactions: allowed,
		},
	}
}

func TestReactor_Execute_PicksEmoji(t *testing.T) {
	r, mockClient, _ := setupReactorTest(t, `{"emoji": "🔥"}`)

	resp, err := r.Execute(context.Background(), reactorRequest(testAllowed))
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.Equal(t, "🔥", result.Emoji)
	assert.Equal(t, "🔥", resp.Metadata["emoji"])
	mockClient.AssertExpectations(t)
}

func TestReactor_Execute_None(t *testing.T) {
	r, mockClient, _ := setupReactorTest(t, `{"emoji": "none"}`)

	resp, err := r.Execute(context.Background(), reactorRequest(testAllowed))
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.Empty(t, result.Emoji)
	mockClient.AssertExpectations(t)
}

func TestReactor_Execute_NormalizesVariationSelector(t *testing.T) {
	r, _, _ := setupReactorTest(t, `{"emoji": "❤️"}`)

	resp, err := r.Execute(context.Background(), reactorRequest(testAllowed))
	require.NoError(t, err)

	result := resp.Structured.(*Result)
	assert.Equal(t, "❤", result.Emoji, "must return the allowed-list form, not the model's")
}

func TestReactor_Execute_FencedJSON(t *testing.T) {
	r, _, _ := setupReactorTest(t, "```json\n{\"emoji\": \"👍\"}\n```")

	resp, err := r.Execute(context.Background(), reactorRequest(testAllowed))
	require.NoError(t, err)

	result := resp.Structured.(*Result)
	assert.Equal(t, "👍", result.Emoji)
}

func TestReactor_Execute_MalformedJSON_SkipsWithoutError(t *testing.T) {
	r, _, _ := setupReactorTest(t, `definitely not json`)

	resp, err := r.Execute(context.Background(), reactorRequest(testAllowed))
	require.NoError(t, err, "malformed model output must degrade to no-reaction, not an error")

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.Empty(t, result.Emoji)
}

func TestReactor_Execute_NoModel_NoLLMCall(t *testing.T) {
	r, mockClient, cfg := setupReactorTest(t, "")
	cfg.Agents.Reactor.Model = ""
	cfg.Agents.Default.Model = ""

	resp, err := r.Execute(context.Background(), reactorRequest(testAllowed))
	require.NoError(t, err)

	result := resp.Structured.(*Result)
	assert.Empty(t, result.Emoji)
	mockClient.AssertNotCalled(t, "CreateChatCompletion")
}

func TestReactor_Execute_NoAllowedReactions_NoLLMCall(t *testing.T) {
	r, mockClient, _ := setupReactorTest(t, "")

	resp, err := r.Execute(context.Background(), reactorRequest(nil))
	require.NoError(t, err)

	result := resp.Structured.(*Result)
	assert.Empty(t, result.Emoji)
	mockClient.AssertNotCalled(t, "CreateChatCompletion")
}

func TestReactor_Execute_AllowedListInSystemPrompt(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}
	var captured llm.ChatCompletionRequest
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(llm.ChatCompletionRequest)
		}).
		Return(testutil.MockChatResponse(`{"emoji": "none"}`), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Reactor.Model = "test-model"
	r := New(executor, testutil.TestTranslator(t), cfg)

	_, err := r.Execute(context.Background(), reactorRequest(testAllowed))
	require.NoError(t, err)

	require.NotEmpty(t, captured.Messages)
	systemPrompt, ok := captured.Messages[0].Content.(string)
	require.True(t, ok)
	for _, emoji := range testAllowed {
		assert.Contains(t, systemPrompt, emoji)
	}
}

func TestReactor_Execute_MediaPartsAppended(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}
	var captured llm.ChatCompletionRequest
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(llm.ChatCompletionRequest)
		}).
		Return(testutil.MockChatResponse(`{"emoji": "👍"}`), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Reactor.Model = "test-model"
	r := New(executor, testutil.TestTranslator(t), cfg)

	req := reactorRequest(testAllowed)
	filePart := llm.FilePart{Type: "file"}
	req.Params[ParamMediaParts] = []interface{}{filePart}

	_, err := r.Execute(context.Background(), req)
	require.NoError(t, err)

	require.Len(t, captured.Messages, 2)
	parts, ok := captured.Messages[1].Content.([]interface{})
	require.True(t, ok, "user message must be multimodal when media is attached")
	require.Len(t, parts, 2)
	assert.IsType(t, llm.TextPart{}, parts[0])
	assert.IsType(t, llm.FilePart{}, parts[1])
}

func TestReactor_Type(t *testing.T) {
	r, _, _ := setupReactorTest(t, "")
	assert.Equal(t, agent.TypeReactor, r.Type())
}
