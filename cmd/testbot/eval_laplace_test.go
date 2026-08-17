package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateEvalVariant_RequiresStrictPrivateRouting(t *testing.T) {
	t.Parallel()
	noFallback := false
	requireParams := true
	zdr := true
	valid := variantSpec{
		Model:             "openai/gpt-5.6-luna",
		ProviderOnly:      []string{"OpenAI"},
		AllowFallbacks:    &noFallback,
		DataCollection:    "deny",
		ZeroDataRetention: &zdr,
		RequireParameters: &requireParams,
	}
	require.NoError(t, validateEvalVariant("luna", valid))

	invalid := valid
	invalid.AllowFallbacks = nil
	require.ErrorContains(t, validateEvalVariant("luna", invalid), "allow_fallbacks=false")

	invalid = valid
	invalid.ProviderOnly = []string{"OpenAI", "Azure"}
	require.ErrorContains(t, validateEvalVariant("luna", invalid), "pin one provider")
}

func TestPrepareJudgeRequest_StripsModelIdentityAndReasoningDetails(t *testing.T) {
	t.Parallel()
	original := `{
      "model":"google/gemini-3.6-flash","models":["google/backup"],"route":"fallback","provider":{"only":["Google"]},
      "reasoning":{"effort":"high"},"trace":{"trace_id":"x"},"user":"u",
      "session_id":"sticky","safety_identifier":"stable","metadata":{"account":"private"},"stream":false,
      "tools":[{"type":"function","function":{"name":"search"}}],
      "messages":[
        {"role":"user","content":"question"},
        {"role":"assistant","content":null,"reasoning_details":[{"data":"opaque"}]},
        {"role":"tool","content":"frozen result"}
      ]
    }`
	body, err := prepareJudgeRequest(original, variantSpec{}, true)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	for _, key := range []string{
		"model", "models", "route", "provider", "reasoning", "trace", "user",
		"session_id", "safety_identifier", "metadata", "stream",
	} {
		require.NotContains(t, got, key)
	}
	require.Equal(t, "none", got["tool_choice"])
	require.Contains(t, got, "tools")
	messages := got["messages"].([]any)
	require.NotContains(t, messages[1].(map[string]any), "reasoning_details")
	require.Equal(t, "frozen result", messages[2].(map[string]any)["content"])
}

func TestPrepareJudgeRequest_NeutralizesFrozenToolCallIDs(t *testing.T) {
	t.Parallel()
	original := `{
      "messages":[
        {"role":"assistant","tool_calls":[
          {"id":"provider-specific-one","type":"function","function":{"name":"search","arguments":"{\"id\":\"semantic-target\"}"}},
          {"id":"provider-specific-two","type":"function","function":{"name":"read","arguments":"{}"}}
        ]},
        {"role":"tool","tool_call_id":"provider-specific-two","content":"second"},
        {"role":"tool","tool_call_id":"provider-specific-one","content":"first"}
      ]
    }`
	body, err := prepareJudgeRequest(original, variantSpec{}, false)
	require.NoError(t, err)
	require.NotContains(t, string(body), "provider-specific")
	require.Contains(t, string(body), "semantic-target")

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	messages := got["messages"].([]any)
	calls := messages[0].(map[string]any)["tool_calls"].([]any)
	firstID := calls[0].(map[string]any)["id"]
	secondID := calls[1].(map[string]any)["id"]
	require.Equal(t, "blind_tool_call_001", firstID)
	require.Equal(t, "blind_tool_call_002", secondID)
	require.Equal(t, secondID, messages[1].(map[string]any)["tool_call_id"])
	require.Equal(t, firstID, messages[2].(map[string]any)["tool_call_id"])
}

func TestPrepareJudgeRequest_FrozenLaneRequiresToolResult(t *testing.T) {
	t.Parallel()
	_, err := prepareJudgeRequest(`{"messages":[{"role":"user","content":"q"}]}`, variantSpec{}, true)
	require.ErrorContains(t, err, "no tool result")
}

func TestPrepareJudgeRequest_AppliesSharedSemanticTransform(t *testing.T) {
	t.Parallel()
	original := `{
      "messages":[{"role":"user","content":[
        {"type":"text","text":"transcript"},
        {"type":"file","file":{"file_data":"redacted:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:audio/ogg:1"}},
        {"type":"file","file":{"file_data":"redacted:sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:image/png:1"}}
      ]}]
    }`
	body, err := prepareJudgeRequest(original, variantSpec{
		DropMediaMIMETypes: []string{"audio/ogg"},
		MaxTokens:          321,
	}, false)
	require.NoError(t, err)
	require.NotContains(t, string(body), "audio/ogg")
	require.Contains(t, string(body), "image/png")
	require.Contains(t, string(body), "transcript")
	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	require.Equal(t, float64(321), got["max_tokens"])
}

func TestStageBlindJudgeMedia_CopiesOnlyVerifiedBlob(t *testing.T) {
	t.Parallel()
	raw := []byte("private-image-bytes")
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	filesDir := t.TempDir()
	mediaOut := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(filesDir, hash+".source"), raw, 0o600))
	request := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"redacted:sha256:%s:image/png:%d"}}]}]}`, hash, len(raw)))

	require.NoError(t, stageBlindJudgeMedia(request, filesDir, mediaOut))
	stagedPath := filepath.Join(mediaOut, hash+".png")
	staged, err := os.ReadFile(stagedPath)
	require.NoError(t, err)
	require.Equal(t, raw, staged)
	info, err := os.Stat(stagedPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Re-staging the same verified object is idempotent, but an existing
	// different object at the hash-derived destination must never be replaced.
	require.NoError(t, stageBlindJudgeMedia(request, filesDir, mediaOut))
	require.NoError(t, os.WriteFile(stagedPath, []byte("tampered"), 0o600))
	require.ErrorContains(t, stageBlindJudgeMedia(request, filesDir, mediaOut), "different content")

	conflicting := []byte(fmt.Sprintf(`{"messages":[{"content":[
	  "redacted:sha256:%s:image/png:%d",
	  "redacted:sha256:%s:image/png:%d"
	]}]}`, hash, len(raw), hash, len(raw)+1))
	require.ErrorContains(t, stageBlindJudgeMedia(conflicting, filesDir, t.TempDir()), "conflicting sizes")
}

func TestValidateEvalVariants_RejectsSemanticDifferencesAndPathCollisions(t *testing.T) {
	t.Parallel()
	noFallback := false
	requireParams := true
	zdr := true
	base := variantSpec{
		Model: "m1", ProviderOnly: []string{"P1"}, AllowFallbacks: &noFallback,
		DataCollection: "deny", ZeroDataRetention: &zdr, RequireParameters: &requireParams, MaxTokens: 100,
		DropMediaMIMETypes: []string{"audio/ogg"},
	}
	other := base
	other.Model = "m2"
	other.ProviderOnly = []string{"P2"}
	require.NoError(t, validateEvalVariants([]string{"a", "b"}, map[string]variantSpec{"a": base, "b": other}))

	different := other
	different.DropMediaMIMETypes = nil
	require.ErrorContains(t, validateEvalVariants([]string{"a", "b"}, map[string]variantSpec{"a": base, "b": different}), "changes task semantics")

	different = other
	different.MaxTokens++
	require.ErrorContains(t, validateEvalVariants([]string{"a", "b"}, map[string]variantSpec{"a": base, "b": different}), "changes task semantics")

	require.ErrorContains(t, validateEvalVariants([]string{"a/b", "a_b"}, map[string]variantSpec{"a/b": base, "a_b": other}), "collide")
}

func TestValidateEvalManifest_RejectsDuplicateCaseIDs(t *testing.T) {
	t.Parallel()
	caseOne := laplaceEvalCase{ID: "same", TraceFile: "one.json", Agent: "laplace", Lanes: []laplaceEvalLane{{Name: "lane"}}}
	caseTwo := caseOne
	caseTwo.TraceFile = "two.json"
	require.ErrorContains(t, validateEvalManifest(laplaceEvalManifest{SchemaVersion: 1, Cases: []laplaceEvalCase{caseOne, caseTwo}}), "duplicate")
}

func TestValidateEvalManifest_RejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	require.ErrorContains(t, validateEvalManifest(laplaceEvalManifest{SchemaVersion: 2}), "schema_version")
}

func TestEvalJSONLoadersRejectUnknownFields(t *testing.T) {
	t.Parallel()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"schema_version":1,"unknown_privacy_mode":true,"cases":[]}`), 0o600))
	_, _, err := loadLaplaceEvalManifest(manifestPath)
	require.ErrorContains(t, err, "unknown field")

	variantsPath := filepath.Join(t.TempDir(), "variants.json")
	require.NoError(t, os.WriteFile(variantsPath, []byte(`{"candidate":{"model":"m","zrd":true}}`), 0o600))
	_, err = loadVariantSpecs(variantsPath)
	require.ErrorContains(t, err, "unknown field")
}

func TestValidateDisjointNewEvalDirs(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	require.NoError(t, validateDisjointNewEvalDirs(filepath.Join(base, "private"), filepath.Join(base, "blind")))
	require.ErrorContains(t, validateDisjointNewEvalDirs(filepath.Join(base, "private"), filepath.Join(base, "private", "blind")), "disjoint")
}

func TestValidateDisjointNewEvalDirs_ResolvesSymlinkedParents(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realParent := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(realParent, 0o700))
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(realParent, alias))

	err := validateDisjointNewEvalDirs(
		filepath.Join(alias, "private"),
		filepath.Join(realParent, "private", "blind"),
	)
	require.ErrorContains(t, err, "disjoint")
}

func TestWritePrivateRootFileContainsWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, writePrivateRootFile(root, "request.json", []byte("private")))
	data, err := os.ReadFile(filepath.Join(root, "request.json"))
	require.NoError(t, err)
	require.Equal(t, []byte("private"), data)

	escape := filepath.Join(filepath.Dir(root), "escape.json")
	require.Error(t, writePrivateRootFile(root, "../escape.json", []byte("must not escape")))
	_, err = os.Stat(escape)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestValidateOpenRouterEvalEndpoint(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateOpenRouterEvalEndpoint("https://openrouter.ai/api/v1/chat/completions"))
	for _, endpoint := range []string{
		"http://openrouter.ai/api/v1/chat/completions",
		"https://gateway.example/v1/chat/completions",
		"https://openrouter.ai:444/api/v1/chat/completions",
		"https://identity@openrouter.ai/api/v1/chat/completions",
		"https://openrouter.ai/api/v1/chat/completions?proxy=1",
	} {
		require.Error(t, validateOpenRouterEvalEndpoint(endpoint))
	}
}

func TestValidateEvalCase_CapturedToolLoopOptions(t *testing.T) {
	t.Parallel()
	base := laplaceEvalCase{
		ID: "case-1", TraceFile: "trace.json", Agent: "laplace",
		Lanes: []laplaceEvalLane{{Name: "tool-loop", Kind: "captured_tool_loop", Gen: "all"}},
	}
	require.NoError(t, validateEvalCase(base))

	invalid := base
	invalid.Lanes = []laplaceEvalLane{{Name: "tool-loop", Kind: "captured_tool_loop", ForceToolChoiceNone: true}}
	require.ErrorContains(t, validateEvalCase(invalid), "cannot force tool_choice none")

	invalid = base
	invalid.Lanes = []laplaceEvalLane{{Name: "tool-loop", Kind: "captured_tool_loop", Gen: "first"}}
	require.ErrorContains(t, validateEvalCase(invalid), "gen must be empty or all")

	invalid = base
	invalid.Lanes = []laplaceEvalLane{{Name: "tool-loop", Kind: "future"}}
	require.ErrorContains(t, validateEvalCase(invalid), "unsupported kind")
}

func TestWriteBlindToolCandidate_DropsErrorContent(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "candidate.json")
	loop := toolLoopRunResult{Turns: []toolLoopTurnResult{{
		Turn: 0,
		Replay: replayRunResult{
			Err:          "generation failed",
			FinishReason: "error",
			RawFile:      filepath.Join(t.TempDir(), "must-not-be-read.json"),
		},
		Assessment: toolTurnAssessment{ActualDecision: toolDecisionMalformed},
	}}}
	require.NoError(t, writeBlindToolCandidate(out, loop))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	var packet blindToolCandidatePacket
	require.NoError(t, json.Unmarshal(data, &packet))
	require.Len(t, packet.Turns, 1)
	require.Equal(t, "error", packet.Turns[0].Status)
	require.Empty(t, packet.Turns[0].Content)
}

func TestBlindCandidatesInvalidateRouteMismatchWithoutReadingContent(t *testing.T) {
	t.Parallel()
	missingRaw := filepath.Join(t.TempDir(), "must-not-be-read.json")
	replay := replayRunResult{RouteMismatch: true, RawFile: missingRaw}

	singlePath := filepath.Join(t.TempDir(), "candidate.md")
	require.NoError(t, writeBlindCandidate(singlePath, replay))
	single, err := os.ReadFile(singlePath)
	require.NoError(t, err)
	require.Equal(t, "[generation invalidated]\n", string(single))

	toolPath := filepath.Join(t.TempDir(), "candidate.json")
	loop := toolLoopRunResult{Turns: []toolLoopTurnResult{{
		Turn: 0, Replay: replay,
		Assessment: toolTurnAssessment{ActualDecision: toolDecisionMalformed},
	}}}
	require.NoError(t, writeBlindToolCandidate(toolPath, loop))
	data, err := os.ReadFile(toolPath)
	require.NoError(t, err)
	var packet blindToolCandidatePacket
	require.NoError(t, json.Unmarshal(data, &packet))
	require.Len(t, packet.Turns, 1)
	require.Equal(t, "route_mismatch", packet.Turns[0].Status)
	require.Empty(t, packet.Turns[0].Content)
}
