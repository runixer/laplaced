package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// replay-laplace re-runs a single captured laplace LLM generation against the
// live model, optionally applying prompt "variants" so a fix hypothesis can be
// A/B-tested before it is ported into the prompt/code.
//
// The captured request lives verbatim in the gen span's `llm.request` event
// body; only base64 media is redacted to "redacted:sha256:<hash>:<mime>:<size>".
// We rehydrate those by sha256 from --files-dir, apply the variant transform,
// and POST the raw JSON to the OpenAI-compatible endpoint N times.
//
// Variant wording (often non-English experimental prompt text) is loaded from a
// gitignored --variants-file, never baked into this tracked source.
var replayLaplaceCmd = &cobra.Command{
	Use:   "replay-laplace",
	Short: "Replay a captured agent LLM generation from a trace, with optional prompt/model variants",
	Long: `Reconstructs the exact OpenRouter request from a trace's gen span (the
*.CreateChatCompletion child of <agent>.Execute), rehydrates redacted media by
sha256 from --files-dir, applies a named variant transform, and POSTs it to the
LLM endpoint --runs times per variant.

--agent selects which agent's generation to replay (default "laplace"); e.g.
"enricher" or "reranker". --list-agents prints the agent spans present in the
trace and exits. --model overrides the request's model for every variant (handy
for A/B-testing one prompt against two models); a per-variant "model" wins over
it.

Built-in variant "baseline" is a no-op (faithful replay). Other variants are
defined in a gitignored JSON --variants-file:

  {
    "ga":     { "model": "google/gemini-3.1-flash-lite" },
    "anchor": { "insert_before_current_media": "<marker text>" },
    "anchor-protocol": {
      "insert_before_current_media": "<marker text>",
      "system_replace": [{"find": "<substring>", "with": "<replacement>"}]
    },
    "no-raw-audio": { "drop_media_mime_types": ["audio/ogg"] }
  }

Example (compare the captured preview model against GA on the enricher request):
  testbot replay-laplace \
    --trace-file data/replay-enricher/trace.json \
    --files-dir data/replay-enricher \
    --agent enricher \
    --variants-file data/replay-enricher/variants.json \
    --variant baseline --variant ga \
    --runs 5 --out data/replay-laplace/enricher`,
	Annotations: map[string]string{"skip-bot-setup": "true"},
	RunE:        runReplayLaplace,
}

func init() {
	f := replayLaplaceCmd.Flags()
	f.String("trace-file", "", "Path to a trace JSON file (OTLP/Tempo format); fetch it yourself, e.g. curl <tempo>/api/traces/<id>")
	f.String("files-dir", "data/replay-lily", "Directory with <sha256>.* files to rehydrate redacted media")
	f.String("variants-file", "", "JSON file defining named variants (besides built-in 'baseline')")
	f.StringSlice("variant", []string{"baseline"}, "Variant names to run (repeatable)")
	f.String("agent", "laplace", "Agent whose generation to replay: the <agent>.Execute span (e.g. laplace, enricher, reranker)")
	f.String("model", "", "Override the request model for all variants (per-variant 'model' takes precedence)")
	f.Bool("list-agents", false, "List the <agent>.Execute spans present in the trace and exit")
	f.String("gen", "auto", "Which gen turn under <agent>.Execute to replay: 'auto' (prefer media-bearing, else last), 'last', 'first', or a 0-based index (negatives count from end). Use 'last' to hit the post-tool synthesis turn on a media+search trace.")
	f.Int("runs", 1, "Runs per variant")
	f.String("out", "data/replay-laplace/out", "Output directory for replies (gitignored)")
	f.Bool("keep-user", false, "Keep captured user/session/safety/metadata identifiers (off by default for private eval replays)")
	f.Duration("request-timeout", 10*time.Minute, "Timeout for one model generation")
	rootCmd.AddCommand(replayLaplaceCmd)
}

// memoryMarkerPrefix mirrors the marker context.go prepends before each
// reranker-loaded artifact. A file part NOT preceded by it is current-message
// media — the subject we want the model to act on.
const memoryMarkerPrefix = "📄"

var redactedMediaRe = regexp.MustCompile(`^redacted:sha256:([0-9a-f]{64}):([^:]+):(\d+)$`)

type variantSpec struct {
	Model                    string   `json:"model"`
	ReasoningEffort          string   `json:"reasoning_effort,omitempty"`
	ClearReasoning           bool     `json:"clear_reasoning,omitempty"`
	StripMessageReasoning    bool     `json:"strip_message_reasoning_details,omitempty"`
	ProviderOrder            []string `json:"provider_order,omitempty"`
	ProviderOnly             []string `json:"provider_only,omitempty"`
	AllowFallbacks           *bool    `json:"allow_fallbacks,omitempty"`
	DataCollection           string   `json:"data_collection,omitempty"`
	ZeroDataRetention        *bool    `json:"zdr,omitempty"`
	RequireParameters        *bool    `json:"require_parameters,omitempty"`
	ClearProvider            bool     `json:"clear_provider,omitempty"`
	ImageInputFormat         string   `json:"image_input_format,omitempty"`
	DisableTools             bool     `json:"disable_tools,omitempty"`
	ForceToolChoiceNone      bool     `json:"force_tool_choice_none,omitempty"`
	MaxTokens                int      `json:"max_tokens,omitempty"`
	DropMedia                bool     `json:"drop_media"`
	DropMediaMIMETypes       []string `json:"drop_media_mime_types,omitempty"`
	StripSystemTags          []string `json:"strip_system_tags"`
	KeepFactIDs              []string `json:"keep_fact_ids"`
	SetUserText              *string  `json:"set_user_text"`
	InsertBeforeCurrentMedia string   `json:"insert_before_current_media"`
	SystemReplace            []struct {
		Find string `json:"find"`
		With string `json:"with"`
	} `json:"system_replace"`
}

type replayRunResult struct {
	Variant               string           `json:"variant"`
	Run                   int              `json:"run"`
	Agent                 string           `json:"agent"`
	GenSelector           string           `json:"gen_selector"`
	GenIndex              int              `json:"gen_index"`
	GenTurns              int              `json:"gen_turns"`
	RequestedModel        string           `json:"requested_model,omitempty"`
	ResponseModel         string           `json:"response_model,omitempty"`
	Provider              string           `json:"provider,omitempty"`
	GenerationID          string           `json:"generation_id,omitempty"`
	HTTPStatus            int              `json:"http_status,omitempty"`
	DurationMS            int64            `json:"duration_ms"`
	FinishReason          string           `json:"finish_reason"`
	NativeFinish          string           `json:"native_finish_reason,omitempty"`
	PromptTokens          int              `json:"prompt_tokens"`
	CachedTokens          int              `json:"cached_tokens"`
	CacheWriteTokens      int              `json:"cache_write_tokens"`
	PromptAudioTokens     int              `json:"prompt_audio_tokens"`
	PromptVideoTokens     int              `json:"prompt_video_tokens"`
	CompletionTokens      int              `json:"completion_tokens"`
	ReasoningTokens       int              `json:"reasoning_tokens"`
	CompletionImageTokens int              `json:"completion_image_tokens"`
	CompletionAudioTokens int              `json:"completion_audio_tokens"`
	TotalTokens           int              `json:"total_tokens"`
	CostUSD               *float64         `json:"cost_usd,omitempty"`
	UpstreamCostUSD       *float64         `json:"upstream_cost_usd,omitempty"`
	IsBYOK                bool             `json:"is_byok"`
	CacheStatus           string           `json:"cache_status,omitempty"`
	ToolCalls             int              `json:"tool_calls"`
	ToolCallDetails       []replayToolCall `json:"tool_call_details,omitempty"`
	ProtocolFailure       bool             `json:"protocol_failure"`
	RouteMismatch         bool             `json:"route_mismatch"`
	Err                   string           `json:"err,omitempty"`
	ContentFile           string           `json:"content_file"`
	RawFile               string           `json:"raw_file"`
}

type replayToolCall struct {
	ID            string `json:"id,omitempty"`
	Type          string `json:"type,omitempty"`
	Name          string `json:"name,omitempty"`
	Arguments     string `json:"arguments,omitempty"`
	EnvelopeValid bool   `json:"envelope_valid"`
	ParseError    string `json:"parse_error,omitempty"`
}

type toolCallAssessment struct {
	Call                 replayToolCall `json:"call"`
	KnownTool            bool           `json:"known_tool"`
	ArgumentsJSONValid   bool           `json:"arguments_json_valid"`
	ArgumentsSchemaValid bool           `json:"arguments_schema_valid"`
	SchemaSupported      bool           `json:"schema_supported"`
	RuntimeProtocolValid bool           `json:"runtime_protocol_valid"`
	ExactCapturedMatch   bool           `json:"exact_captured_match"`
	Errors               []string       `json:"errors,omitempty"`
	RuntimeErrors        []string       `json:"runtime_errors,omitempty"`
}

type toolTurnAssessment struct {
	ExpectedDecision           string               `json:"expected_decision"`
	ExpectedDecisionAssessable bool                 `json:"expected_decision_assessable"`
	ActualDecision             string               `json:"actual_decision"`
	DecisionMatch              bool                 `json:"decision_match"`
	ExactCallMatch             bool                 `json:"exact_call_match"`
	ExpectedCalls              []replayToolCall     `json:"expected_calls,omitempty"`
	ActualCalls                []toolCallAssessment `json:"actual_calls,omitempty"`
}

const (
	toolDecisionCall      = "call"
	toolDecisionStop      = "stop"
	toolDecisionMalformed = "malformed"
)

func runReplayLaplace(cmd *cobra.Command, _ []string) error {
	traceFile, _ := cmd.Flags().GetString("trace-file")
	filesDir, _ := cmd.Flags().GetString("files-dir")
	variantsFile, _ := cmd.Flags().GetString("variants-file")
	variants, _ := cmd.Flags().GetStringSlice("variant")
	agent, _ := cmd.Flags().GetString("agent")
	modelOverride, _ := cmd.Flags().GetString("model")
	listAgents, _ := cmd.Flags().GetBool("list-agents")
	genSelect, _ := cmd.Flags().GetString("gen")
	runs, _ := cmd.Flags().GetInt("runs")
	outDir, _ := cmd.Flags().GetString("out")
	keepUser, _ := cmd.Flags().GetBool("keep-user")
	requestTimeout, _ := cmd.Flags().GetDuration("request-timeout")

	if traceFile == "" {
		return fmt.Errorf("--trace-file is required")
	}
	traceBytes, err := os.ReadFile(traceFile) // #nosec G304 -- testbot CLI, path supplied by operator
	if err != nil {
		return fmt.Errorf("read trace file: %w", err)
	}

	if listAgents {
		names, err := listAgentSpans(traceBytes)
		if err != nil {
			return err
		}
		fmt.Printf("Agent spans in trace: %s\n", strings.Join(names, ", "))
		return nil
	}

	bodyStr, pickedIdx, nTurns, err := selectGenRequestBody(traceBytes, agent, genSelect)
	if err != nil {
		return fmt.Errorf("locate gen span: %w", err)
	}
	fmt.Printf("gen turn: picked index %d of %d under %s.Execute (--gen=%s)\n", pickedIdx, nTurns, agent, genSelect)

	specs, err := loadVariantSpecs(variantsFile)
	if err != nil {
		return err
	}
	if err := validateReplayVariantNames(variants); err != nil {
		return err
	}

	endpoint, apiKey, err := llmEndpoint()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("mkdir out: %w", err)
	}

	var results []replayRunResult
	for _, v := range variants {
		spec, ok := specs[v]
		if !ok && v != "baseline" {
			return fmt.Errorf("variant %q not found in --variants-file (only 'baseline' is built-in)", v)
		}
		for run := 1; run <= runs; run++ {
			body, err := prepareBody(bodyStr, spec, filesDir, modelOverride, keepUser)
			if err != nil {
				return fmt.Errorf("variant %s: %w", v, err)
			}
			res := postOnce(cmd.Context(), endpoint, apiKey, body, v, run, outDir, requestTimeout)
			res.Agent = agent
			res.GenSelector = genSelect
			res.GenIndex = pickedIdx
			res.GenTurns = nTurns
			results = append(results, res)
			status := res.FinishReason
			if res.Err != "" {
				status = "ERR: " + res.Err
			}
			fmt.Printf("[%s run %d] model=%s provider=%s tokens=%d+%d reasoning=%d cost=%s duration=%s finish=%s -> %s\n",
				v, run, res.ResponseModel, res.Provider, res.PromptTokens, res.CompletionTokens,
				res.ReasoningTokens, formatOptionalCost(res.CostUSD), time.Duration(res.DurationMS)*time.Millisecond,
				status, res.ContentFile)
		}
	}

	summaryPath := filepath.Join(outDir, "summary.json")
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal replay summary: %w", err)
	}
	if err := os.WriteFile(summaryPath, data, 0o600); err != nil {
		return fmt.Errorf("write replay summary: %w", err)
	}
	fmt.Printf("\nSummary: %s (%d runs)\n", summaryPath, len(results))
	return nil
}

// --- OTLP/Tempo trace parsing -------------------------------------------------

type otlpValue struct {
	StringValue *string `json:"stringValue"`
}
type otlpAttr struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}
type otlpEvent struct {
	Name       string     `json:"name"`
	Attributes []otlpAttr `json:"attributes"`
}
type otlpSpan struct {
	Name              string      `json:"name"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	Attributes        []otlpAttr  `json:"attributes"`
	Events            []otlpEvent `json:"events"`
}
type otlpTrace struct {
	Batches []struct {
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"batches"`
}

// flattenSpans collects every span in the trace into a single slice.
func flattenSpans(traceBytes []byte) ([]otlpSpan, error) {
	var tr otlpTrace
	if err := json.Unmarshal(traceBytes, &tr); err != nil {
		return nil, fmt.Errorf("parse trace JSON: %w", err)
	}
	var spans []otlpSpan
	for _, b := range tr.Batches {
		for _, ss := range b.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
	}
	return spans, nil
}

// listAgentSpans returns the agent names ("laplace", "enricher", ...) for which
// an "<agent>.Execute" span exists in the trace, so the operator can pick one.
func listAgentSpans(traceBytes []byte) ([]string, error) {
	spans, err := flattenSpans(traceBytes)
	if err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	for _, s := range spans {
		name, ok := strings.CutSuffix(s.Name, ".Execute")
		if ok && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no <agent>.Execute spans in trace")
	}
	return names, nil
}

type capturedGenTurn struct {
	Index        int
	Start        int64
	RequestBody  string
	ResponseBody string
	HasFiles     bool
}

// collectGenTurns pairs the request and response content events on every
// *.CreateChatCompletion child of "<agent>.Execute". Later requests already
// contain any production tool calls and frozen tool results, so callers can
// replay every turn independently without dispatching a tool.
func collectGenTurns(traceBytes []byte, agent string) ([]capturedGenTurn, error) {
	spans, err := flattenSpans(traceBytes)
	if err != nil {
		return nil, err
	}
	agentSpanName := agent + ".Execute"
	var agentID string
	for _, s := range spans {
		if s.Name == agentSpanName {
			agentID = s.SpanID
			break
		}
	}
	if agentID == "" {
		return nil, fmt.Errorf("no %s span in trace (use --list-agents to see options)", agentSpanName)
	}
	var turns []capturedGenTurn
	for _, s := range spans {
		if !strings.HasSuffix(s.Name, ".CreateChatCompletion") || s.ParentSpanID != agentID {
			continue
		}
		start, _ := strconv.ParseInt(s.StartTimeUnixNano, 10, 64)
		turn := capturedGenTurn{Start: start}
		for _, ev := range s.Events {
			if ev.Name != "llm.request" && ev.Name != "llm.response" {
				continue
			}
			for _, a := range ev.Attributes {
				if a.Key == "body" && a.Value.StringValue != nil {
					if ev.Name == "llm.request" {
						turn.RequestBody = *a.Value.StringValue
					} else {
						turn.ResponseBody = *a.Value.StringValue
					}
				}
			}
		}
		if turn.RequestBody != "" {
			turn.HasFiles = strings.Contains(turn.RequestBody, `"type":"file"`)
			turns = append(turns, turn)
		}
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Start < turns[j].Start })
	for i := range turns {
		turns[i].Index = i
	}
	return turns, nil
}

// collectGenRequestBodies is retained for single-turn replay compatibility.
func collectGenRequestBodies(traceBytes []byte, agent string) (bodies []string, hasFiles []bool, err error) {
	turns, err := collectGenTurns(traceBytes, agent)
	if err != nil {
		return nil, nil, err
	}
	for _, turn := range turns {
		bodies = append(bodies, turn.RequestBody)
		hasFiles = append(hasFiles, turn.HasFiles)
	}
	return bodies, hasFiles, nil
}

// selectGenRequestBody picks one gen request under <agent>.Execute per the
// selector: "auto" (prefer the media-bearing turn, else the last — the historic
// default), "last", "first", or a 0-based index (negatives count from the end).
// Returns the chosen body, its index, and the total turn count.
func selectGenRequestBody(traceBytes []byte, agent, sel string) (string, int, int, error) {
	bodies, hasFiles, err := collectGenRequestBodies(traceBytes, agent)
	if err != nil {
		return "", 0, 0, err
	}
	n := len(bodies)
	if n == 0 {
		return "", 0, 0, fmt.Errorf("no gen llm.request under %s.Execute", agent)
	}
	switch sel {
	case "", "auto":
		for i := n - 1; i >= 0; i-- {
			if hasFiles[i] {
				return bodies[i], i, n, nil
			}
		}
		return bodies[n-1], n - 1, n, nil
	case "last":
		return bodies[n-1], n - 1, n, nil
	case "first":
		return bodies[0], 0, n, nil
	default:
		idx, perr := strconv.Atoi(sel)
		if perr != nil {
			return "", 0, n, fmt.Errorf("invalid --gen %q: want auto|last|first|<index>", sel)
		}
		if idx < 0 {
			idx += n
		}
		if idx < 0 || idx >= n {
			return "", 0, n, fmt.Errorf("--gen index out of range: %s resolves outside [0,%d)", sel, n)
		}
		return bodies[idx], idx, n, nil
	}
}

// --- variant + rehydration ----------------------------------------------------

func loadVariantSpecs(path string) (map[string]variantSpec, error) {
	if path == "" {
		return map[string]variantSpec{}, nil
	}
	b, err := os.ReadFile(path) // #nosec G304 -- testbot CLI, path supplied by operator
	if err != nil {
		return nil, fmt.Errorf("read variants file: %w", err)
	}
	var specs map[string]variantSpec
	if err := decodeStrictJSONDocument(b, &specs); err != nil {
		return nil, fmt.Errorf("parse variants file: %w", err)
	}
	return specs, nil
}

func validateReplayVariantNames(names []string) error {
	components := make(map[string]string, len(names))
	for _, name := range names {
		component := safeFileComponent(name)
		if previous, exists := components[component]; exists {
			return fmt.Errorf("variant names %q and %q collide as output component %q", previous, name, component)
		}
		components[component] = name
	}
	return nil
}

func decodeStrictJSONDocument(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// prepareBody clones the captured request, applies the variant transform, then
// rehydrates redacted media. Returns the marshaled request ready to POST.
// A per-variant model wins over modelOverride, which wins over the captured one.
func prepareBody(bodyStr string, spec variantSpec, filesDir, modelOverride string, keepUser bool) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(bodyStr), &body); err != nil {
		return nil, fmt.Errorf("parse captured body: %w", err)
	}
	originalModel, _ := body["model"].(string)
	delete(body, "trace") // our outbound OTel link; irrelevant to replay
	if !keepUser {
		// These transport identifiers can link a replay back to a production user,
		// session, or tenant. Keep them only behind the explicit operator opt-in.
		for _, key := range []string{"user", "session_id", "safety_identifier", "metadata"} {
			delete(body, key)
		}
	}
	if streaming, _ := body["stream"].(bool); streaming {
		return nil, fmt.Errorf("captured streaming request is not supported by buffered replay")
	}
	body["stream"] = false

	switch {
	case spec.Model != "":
		body["model"] = spec.Model
	case modelOverride != "":
		body["model"] = modelOverride
	}
	if spec.Model != "" || modelOverride != "" {
		delete(body, "models") // a captured fallback chain must not survive a pinned replay
		delete(body, "route")  // captured fallback routing must not override the pinned model
	}
	requestedModel, _ := body["model"].(string)
	modelChanged := requestedModel != "" && originalModel != "" && requestedModel != originalModel
	providerExplicit := spec.ClearProvider || spec.ProviderOrder != nil || spec.ProviderOnly != nil ||
		spec.AllowFallbacks != nil || spec.DataCollection != "" || spec.ZeroDataRetention != nil ||
		spec.RequireParameters != nil
	if modelChanged && !providerExplicit {
		// A captured Gemini request normally pins Google. Carrying that pin to a
		// different model silently invokes OpenRouter fallback and confounds evals.
		delete(body, "provider")
	}

	if spec.ClearReasoning {
		delete(body, "reasoning")
	}
	if spec.ReasoningEffort != "" {
		body["reasoning"] = map[string]any{"effort": spec.ReasoningEffort}
	}
	if spec.StripMessageReasoning {
		stripMessageReasoningDetails(body)
	}
	if spec.ClearProvider {
		delete(body, "provider")
	}
	if spec.ProviderOrder != nil || spec.ProviderOnly != nil || spec.AllowFallbacks != nil ||
		spec.DataCollection != "" || spec.ZeroDataRetention != nil || spec.RequireParameters != nil {
		provider := map[string]any{}
		if spec.ProviderOrder != nil {
			provider["order"] = spec.ProviderOrder
		}
		if spec.ProviderOnly != nil {
			provider["only"] = spec.ProviderOnly
		}
		if spec.AllowFallbacks != nil {
			provider["allow_fallbacks"] = *spec.AllowFallbacks
		}
		if spec.DataCollection != "" {
			provider["data_collection"] = spec.DataCollection
		}
		if spec.ZeroDataRetention != nil {
			provider["zdr"] = *spec.ZeroDataRetention
		}
		if spec.RequireParameters != nil {
			provider["require_parameters"] = *spec.RequireParameters
		}
		body["provider"] = provider
	}
	if spec.DisableTools && spec.ForceToolChoiceNone {
		return nil, fmt.Errorf("disable_tools and force_tool_choice_none are mutually exclusive")
	}
	if spec.DisableTools {
		delete(body, "tools")
		delete(body, "tool_choice")
	}
	if spec.ForceToolChoiceNone {
		if !hasRole(body, "tool") {
			return nil, fmt.Errorf("force_tool_choice_none requires at least one frozen tool result in messages")
		}
		body["tool_choice"] = "none"
	}
	if spec.MaxTokens > 0 {
		body["max_tokens"] = spec.MaxTokens
	}

	if spec.DropMedia {
		dropMediaParts(body)
	}
	if len(spec.DropMediaMIMETypes) > 0 {
		dropMediaPartsByMIME(body, spec.DropMediaMIMETypes)
	}
	for _, tag := range spec.StripSystemTags {
		stripSystemTag(body, tag)
	}
	if spec.KeepFactIDs != nil {
		keepFactIDs(body, spec.KeepFactIDs)
	}
	if spec.SetUserText != nil {
		setUserText(body, *spec.SetUserText)
	}
	if spec.InsertBeforeCurrentMedia != "" {
		insertCurrentMediaMarker(body, spec.InsertBeforeCurrentMedia)
	}
	for _, r := range spec.SystemReplace {
		replaceInSystem(body, r.Find, r.With)
	}
	if err := convertReplayMediaFormat(body, spec.ImageInputFormat); err != nil {
		return nil, err
	}
	if err := rehydrateMedia(body, filesDir); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

// stripMessageReasoningDetails removes provider-specific encrypted thought
// signatures from frozen assistant messages. Captured signatures can be
// non-portable or redacted and cause a deterministic provider rejection even
// though the semantic tool call and result remain valid.
func stripMessageReasoningDetails(body map[string]any) {
	msgs, _ := body["messages"].([]any)
	for _, msg := range msgs {
		if message, ok := msg.(map[string]any); ok {
			delete(message, "reasoning_details")
		}
	}
}

func convertReplayMediaFormat(body map[string]any, format string) error {
	if format == "" || format == "file" {
		return nil
	}
	if format != "openai" {
		return fmt.Errorf("unsupported image_input_format %q", format)
	}
	msgs, _ := body["messages"].([]any)
	for _, msg := range msgs {
		message, _ := msg.(map[string]any)
		content, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for i, part := range content {
			partMap, _ := part.(map[string]any)
			if partMap["type"] != "file" {
				continue
			}
			file, _ := partMap["file"].(map[string]any)
			data, _ := file["file_data"].(string)
			mime := replayDataMIME(data)
			switch {
			case strings.HasPrefix(mime, "image/"):
				content[i] = map[string]any{"type": "image_url", "image_url": map[string]any{"url": data}}
			case strings.HasPrefix(mime, "video/"):
				content[i] = map[string]any{"type": "video_url", "video_url": map[string]any{"url": data}}
			}
		}
	}
	return nil
}

func replayDataMIME(data string) string {
	if match := redactedMediaRe.FindStringSubmatch(data); match != nil {
		return match[2]
	}
	if !strings.HasPrefix(data, "data:") {
		return ""
	}
	rest := strings.TrimPrefix(data, "data:")
	if idx := strings.IndexAny(rest, ";,"); idx >= 0 {
		return rest[:idx]
	}
	return ""
}

func hasRole(body map[string]any, role string) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] == role {
			return true
		}
	}
	return false
}

// insertCurrentMediaMarker prepends a text part before every file part that is
// NOT immediately preceded by the memory-artifact marker — i.e. current-message
// media. Media-type agnostic.
func insertCurrentMediaMarker(body map[string]any, marker string) {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		content, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		var out []any
		for i, p := range content {
			if isFilePart(p) && !precededByMemoryMarker(content, i) {
				out = append(out, map[string]any{"type": "text", "text": marker})
			}
			out = append(out, p)
		}
		mm["content"] = out
	}
}

// dropMediaParts removes every file part from all messages, leaving text only —
// to isolate whether the media (vs the surrounding text) trips a safety filter.
func dropMediaParts(body map[string]any) {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		content, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, p := range content {
			if !isFilePart(p) {
				kept = append(kept, p)
			}
		}
		mm["content"] = kept
	}
}

// dropMediaPartsByMIME removes only media parts whose encoded or redacted MIME
// exactly matches one of the requested values. It runs before rehydration, so a
// dropped blob is never read from disk or sent to the model endpoint.
func dropMediaPartsByMIME(body map[string]any, mimeTypes []string) {
	drop := make(map[string]bool, len(mimeTypes))
	for _, mime := range mimeTypes {
		if mime != "" {
			drop[mime] = true
		}
	}
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		content, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(content))
		for _, part := range content {
			if mime := replayMediaPartMIME(part); mime != "" && drop[mime] {
				continue
			}
			kept = append(kept, part)
		}
		mm["content"] = kept
	}
}

func replayMediaPartMIME(part any) string {
	partMap, _ := part.(map[string]any)
	switch partMap["type"] {
	case "file":
		file, _ := partMap["file"].(map[string]any)
		data, _ := file["file_data"].(string)
		return replayDataMIME(data)
	case "image_url":
		return replayNestedMediaMIME(partMap["image_url"])
	case "video_url":
		return replayNestedMediaMIME(partMap["video_url"])
	default:
		return ""
	}
}

func replayNestedMediaMIME(value any) string {
	switch nested := value.(type) {
	case string:
		return replayDataMIME(nested)
	case map[string]any:
		url, _ := nested["url"].(string)
		return replayDataMIME(url)
	default:
		return ""
	}
}

func isFilePart(p any) bool {
	pm, ok := p.(map[string]any)
	return ok && pm["type"] == "file"
}

func precededByMemoryMarker(content []any, i int) bool {
	if i == 0 {
		return false
	}
	prev, ok := content[i-1].(map[string]any)
	if !ok || prev["type"] != "text" {
		return false
	}
	txt, _ := prev["text"].(string)
	return strings.HasPrefix(txt, memoryMarkerPrefix)
}

// stripSystemTag removes a "<tag>...</tag>" block (including the tags) from the
// system message — to isolate which injected section (e.g. user_profile, history)
// trips a safety filter, without baking that PII-bearing text into a config file.
func stripSystemTag(body map[string]any, tag string) {
	if tag == "" {
		return
	}
	re := regexp.MustCompile(`(?s)\s*<` + regexp.QuoteMeta(tag) + `>.*?</` + regexp.QuoteMeta(tag) + `>`)
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] != "system" {
			continue
		}
		switch c := mm["content"].(type) {
		case string:
			mm["content"] = re.ReplaceAllString(c, "")
		case []any:
			for _, p := range c {
				if pm, ok := p.(map[string]any); ok {
					if txt, ok := pm["text"].(string); ok {
						pm["text"] = re.ReplaceAllString(txt, "")
					}
				}
			}
		}
	}
}

// setUserText replaces the text of the first text part in the user message with
// s and drops any other text parts (file parts are kept) — to test how the
// current query, holding the profile constant, affects a safety refusal.
func setUserText(body map[string]any, s string) {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] != "user" {
			continue
		}
		content, ok := mm["content"].([]any)
		if !ok {
			if _, isStr := mm["content"].(string); isStr {
				mm["content"] = s
			}
			continue
		}
		var out []any
		replaced := false
		for _, p := range content {
			pm, _ := p.(map[string]any)
			if pm["type"] == "text" {
				if !replaced {
					pm["text"] = s
					out = append(out, pm)
					replaced = true
				}
				continue
			}
			out = append(out, p)
		}
		if !replaced {
			out = append([]any{map[string]any{"type": "text", "text": s}}, out...)
		}
		mm["content"] = out
	}
}

var factLineRe = regexp.MustCompile(`\[Fact:(\d+)\]`)

// keepFactIDs drops every "[Fact:N]" line from the system message whose N is not
// in keep — to bisect which profile fact(s) trip a safety filter. Non-fact lines
// are untouched. An empty keep list drops all facts. Fact IDs are not PII, so a
// bisection config carries only numbers, never the fact text.
func keepFactIDs(body map[string]any, keep []string) {
	keepSet := map[string]bool{}
	for _, id := range keep {
		keepSet[id] = true
	}
	filter := func(s string) string {
		lines := strings.Split(s, "\n")
		out := lines[:0]
		for _, ln := range lines {
			if m := factLineRe.FindStringSubmatch(ln); len(m) == 2 && !keepSet[m[1]] {
				continue
			}
			out = append(out, ln)
		}
		return strings.Join(out, "\n")
	}
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] != "system" {
			continue
		}
		switch c := mm["content"].(type) {
		case string:
			mm["content"] = filter(c)
		case []any:
			for _, p := range c {
				if pm, ok := p.(map[string]any); ok {
					if txt, ok := pm["text"].(string); ok {
						pm["text"] = filter(txt)
					}
				}
			}
		}
	}
}

func replaceInSystem(body map[string]any, find, with string) {
	if find == "" {
		return
	}
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] != "system" {
			continue
		}
		switch c := mm["content"].(type) {
		case string:
			mm["content"] = strings.ReplaceAll(c, find, with)
		case []any:
			for _, p := range c {
				if pm, ok := p.(map[string]any); ok {
					if txt, ok := pm["text"].(string); ok {
						pm["text"] = strings.ReplaceAll(txt, find, with)
					}
				}
			}
		}
	}
}

const (
	maxReplayMediaFileBytes  = 25 << 20
	maxReplayMediaTotalBytes = 64 << 20
)

// rehydrateMedia replaces every exact redacted media placeholder anywhere in
// the request tree. Current OpenRouter traces use file.file_data; accepting the
// same placeholder in image_url/video_url keeps older captures replayable too.
// Each staged blob is hash- and size-verified before it can leave the machine.
func rehydrateMedia(body map[string]any, filesDir string) error {
	totalBytes := int64(0)
	cache := map[string][]byte{}
	var walk func(any) (any, error)
	walk = func(value any) (any, error) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				replaced, err := walk(child)
				if err != nil {
					return nil, err
				}
				v[key] = replaced
			}
			return v, nil
		case []any:
			for i, child := range v {
				replaced, err := walk(child)
				if err != nil {
					return nil, err
				}
				v[i] = replaced
			}
			return v, nil
		case string:
			match := redactedMediaRe.FindStringSubmatch(v)
			if match == nil {
				return v, nil
			}
			hash, mime := match[1], match[2]
			expectedSize, err := strconv.ParseInt(match[3], 10, 64)
			if err != nil || expectedSize < 0 {
				return nil, fmt.Errorf("invalid media size for sha256 %s", hash)
			}
			if expectedSize > maxReplayMediaFileBytes {
				return nil, fmt.Errorf("media sha256 %s is too large: %d bytes", hash, expectedSize)
			}
			raw, ok := cache[hash]
			if !ok {
				raw, err = readVerifiedMedia(filesDir, hash, expectedSize)
				if err != nil {
					return nil, err
				}
				cache[hash] = raw
			}
			totalBytes += int64(len(raw))
			if totalBytes > maxReplayMediaTotalBytes {
				return nil, fmt.Errorf("rehydrated media exceeds %d-byte request cap", maxReplayMediaTotalBytes)
			}
			return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
		default:
			return value, nil
		}
	}
	_, err := walk(body)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("verify rehydrated request: %w", err)
	}
	if bytes.Contains(encoded, []byte("redacted:sha256:")) {
		return fmt.Errorf("request still contains an unrecognized redacted media placeholder")
	}
	return nil
}

func readVerifiedMedia(dir, hash string, expectedSize int64) ([]byte, error) {
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve media directory: %w", err)
	}
	matches, err := filepath.Glob(filepath.Join(dirAbs, hash+".*"))
	if err != nil {
		return nil, fmt.Errorf("find media sha256 %s: %w", hash, err)
	}
	var valid [][]byte
	for _, path := range matches {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		rel, relErr := filepath.Rel(dirAbs, path)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if info.Size() != expectedSize || info.Size() > maxReplayMediaFileBytes {
			continue
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 -- constrained to the operator-supplied directory
		if readErr != nil {
			continue
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) == hash {
			valid = append(valid, raw)
		}
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("missing or invalid media sha256 %s in %s", hash, dir)
	}
	if len(valid) > 1 {
		return nil, fmt.Errorf("ambiguous media sha256 %s in %s", hash, dir)
	}
	return valid[0], nil
}

// --- HTTP ---------------------------------------------------------------------

const maxReplayResponseBytes int64 = 32 << 20

type replayAPIResponse struct {
	ID       string `json:"id"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Choices  []struct {
		FinishReason string          `json:"finish_reason"`
		NativeFinish string          `json:"native_finish_reason"`
		Error        json.RawMessage `json:"error"`
		Message      struct {
			Content   any               `json:"content"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int             `json:"prompt_tokens"`
		CompletionTokens int             `json:"completion_tokens"`
		TotalTokens      int             `json:"total_tokens"`
		Cost             json.RawMessage `json:"cost"`
		IsBYOK           bool            `json:"is_byok"`
		PromptDetails    struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
			AudioTokens      int `json:"audio_tokens"`
			VideoTokens      int `json:"video_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
			ImageTokens     int `json:"image_tokens"`
			AudioTokens     int `json:"audio_tokens"`
		} `json:"completion_tokens_details"`
		CostDetails struct {
			UpstreamInferenceCost json.RawMessage `json:"upstream_inference_cost"`
		} `json:"cost_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type replayAssistantOutput struct {
	FinishReason  string
	NativeFinish  string
	Content       any
	ToolCalls     []replayToolCall
	GenerationErr string
}

func parseReplayAPIResponse(raw []byte) (replayAPIResponse, error) {
	var parsed replayAPIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return replayAPIResponse{}, err
	}
	return parsed, nil
}

func firstReplayAssistantOutput(raw []byte) (replayAssistantOutput, error) {
	parsed, err := parseReplayAPIResponse(raw)
	if err != nil {
		return replayAssistantOutput{}, err
	}
	if replayRawErrorPresent(parsed.Error) {
		return replayAssistantOutput{GenerationErr: "top-level API error"}, nil
	}
	if len(parsed.Choices) == 0 {
		return replayAssistantOutput{GenerationErr: "no choices"}, nil
	}
	choice := parsed.Choices[0]
	output := replayAssistantOutput{
		FinishReason: choice.FinishReason,
		NativeFinish: choice.NativeFinish,
		Content:      choice.Message.Content,
		ToolCalls:    parseReplayToolCalls(choice.Message.ToolCalls),
	}
	switch {
	case replayRawErrorPresent(choice.Error):
		output.GenerationErr = "choice-level API error"
	case strings.EqualFold(choice.FinishReason, "error"):
		output.GenerationErr = "generation finished with an error"
	case choice.FinishReason == "":
		output.GenerationErr = "missing finish_reason"
	case !strings.EqualFold(choice.FinishReason, "stop") && !strings.EqualFold(choice.FinishReason, "tool_calls"):
		output.GenerationErr = "incomplete or unsupported finish_reason"
	}
	return output, nil
}

func replayRawErrorPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func llmEndpoint() (string, string, error) {
	key := os.Getenv("LAPLACED_OPENROUTER_API_KEY")
	if key == "" {
		key = os.Getenv("LAPLACED_LLM_API_KEY")
	}
	if key == "" {
		return "", "", fmt.Errorf("no API key (set LAPLACED_OPENROUTER_API_KEY or LAPLACED_LLM_API_KEY)")
	}
	base := os.Getenv("LAPLACED_OPENROUTER_BASE_URL")
	if base == "" {
		base = os.Getenv("LAPLACED_LLM_BASE_URL")
	}
	if base == "" {
		base = "https://openrouter.ai/api/v1"
	}
	return strings.TrimRight(base, "/") + "/chat/completions", key, nil
}

func postOnce(ctx context.Context, endpoint, apiKey string, body []byte, variant string, run int, outDir string, timeout time.Duration) replayRunResult {
	return postOnceAtTurn(ctx, endpoint, apiKey, body, variant, run, -1, outDir, timeout)
}

func postOnceAtTurn(ctx context.Context, endpoint, apiKey string, body []byte, variant string, run, turn int, outDir string, timeout time.Duration) replayRunResult {
	res := replayRunResult{Variant: variant, Run: run, RequestedModel: requestModel(body)}
	fileVariant := safeFileComponent(variant)
	if turn >= 0 {
		fileVariant = fmt.Sprintf("%s_turn%02d", fileVariant, turn)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://laplaced.local")
	req.Header.Set("X-Title", "laplaced-replay-laplace")
	req.Header.Set("X-OpenRouter-Cache", "false")

	started := time.Now()
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		res.DurationMS = time.Since(started).Milliseconds()
		res.Err = err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	res.HTTPStatus = resp.StatusCode
	res.CacheStatus = resp.Header.Get("X-OpenRouter-Cache-Status")
	raw, err := readBoundedReplayResponse(resp.Body, maxReplayResponseBytes)
	if err != nil {
		res.DurationMS = time.Since(started).Milliseconds()
		res.Err = fmt.Sprintf("read response: %v", err)
		return res
	}
	res.DurationMS = time.Since(started).Milliseconds()
	rawPath := filepath.Join(outDir, fmt.Sprintf("%s_run%d.raw.json", fileVariant, run))
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		res.Err = fmt.Sprintf("write raw response: %v", err)
		return res
	}
	res.RawFile = rawPath
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		res.Err = fmt.Sprintf("HTTP %d API error (see raw response)", resp.StatusCode)
		return res
	}

	parsed, err := parseReplayAPIResponse(raw)
	if err != nil {
		res.Err = fmt.Sprintf("decode response: %v", err)
		return res
	}
	res.GenerationID = parsed.ID
	res.ResponseModel = parsed.Model
	res.Provider = parsed.Provider
	res.PromptTokens = parsed.Usage.PromptTokens
	res.CachedTokens = parsed.Usage.PromptDetails.CachedTokens
	res.CacheWriteTokens = parsed.Usage.PromptDetails.CacheWriteTokens
	res.PromptAudioTokens = parsed.Usage.PromptDetails.AudioTokens
	res.PromptVideoTokens = parsed.Usage.PromptDetails.VideoTokens
	res.CompletionTokens = parsed.Usage.CompletionTokens
	res.ReasoningTokens = parsed.Usage.CompletionDetails.ReasoningTokens
	res.CompletionImageTokens = parsed.Usage.CompletionDetails.ImageTokens
	res.CompletionAudioTokens = parsed.Usage.CompletionDetails.AudioTokens
	res.TotalTokens = parsed.Usage.TotalTokens
	res.CostUSD = parseReplayCost(parsed.Usage.Cost)
	res.UpstreamCostUSD = parseReplayCost(parsed.Usage.CostDetails.UpstreamInferenceCost)
	res.IsBYOK = parsed.Usage.IsBYOK
	if len(parsed.Choices) > 0 {
		choice := parsed.Choices[0]
		res.FinishReason = choice.FinishReason
		res.NativeFinish = choice.NativeFinish
		res.ToolCalls = len(choice.Message.ToolCalls)
		res.ToolCallDetails = parseReplayToolCalls(choice.Message.ToolCalls)
		res.ProtocolFailure = requestForbidsTools(body) && res.ToolCalls > 0
	}
	if replayRawErrorPresent(parsed.Error) {
		res.Err = "API error (see raw response)"
		return res
	}
	if len(parsed.Choices) == 0 {
		res.Err = "no choices"
		return res
	}
	// Provider safety refusals can surface as a choice-level error with no
	// top-level error object. JSON null is not an error.
	if replayRawErrorPresent(parsed.Choices[0].Error) {
		res.Err = "choice-level API error (see raw response)"
		return res
	}

	contentPath := filepath.Join(outDir, fmt.Sprintf("%s_run%d.md", fileVariant, run))
	content := replayContentBytes(parsed.Choices[0].Message.Content, parsed.Choices[0].Message.ToolCalls)
	if err := os.WriteFile(contentPath, content, 0o600); err != nil {
		res.Err = fmt.Sprintf("write response content: %v", err)
		return res
	}
	res.ContentFile = contentPath
	switch strings.ToLower(res.FinishReason) {
	case "stop", "tool_calls":
		// Complete OpenAI-compatible generation.
	case "error":
		res.Err = "generation finished with an error"
	default:
		res.Err = "generation finished incompletely"
	}
	if res.Err == "" {
		res.RouteMismatch = !providerAllowed(body, res.Provider) || !responseModelMatches(body, res.ResponseModel)
	}
	return res
}

func readBoundedReplayResponse(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("invalid response limit %d", maxBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d-byte limit", maxBytes)
	}
	return raw, nil
}

func parseReplayToolCalls(rawCalls []json.RawMessage) []replayToolCall {
	calls := make([]replayToolCall, 0, len(rawCalls))
	for _, raw := range rawCalls {
		call := replayToolCall{}
		var envelope struct {
			ID       string          `json:"id"`
			Type     string          `json:"type"`
			Function json.RawMessage `json:"function"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			call.ParseError = fmt.Sprintf("invalid tool call envelope: %v", err)
			calls = append(calls, call)
			continue
		}
		call.ID = envelope.ID
		call.Type = envelope.Type
		var function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		var problems []string
		if len(envelope.Function) == 0 || string(envelope.Function) == "null" {
			problems = append(problems, "missing function")
		} else if err := json.Unmarshal(envelope.Function, &function); err != nil {
			problems = append(problems, fmt.Sprintf("invalid function: %v", err))
		}
		call.Name = function.Name
		if len(function.Arguments) == 0 {
			problems = append(problems, "missing function.arguments")
		} else if err := json.Unmarshal(function.Arguments, &call.Arguments); err != nil {
			problems = append(problems, "function.arguments must be a JSON string")
		}
		if call.ID == "" {
			problems = append(problems, "missing id")
		}
		if call.Type != "function" {
			problems = append(problems, "type must be function")
		}
		if call.Name == "" {
			problems = append(problems, "missing function.name")
		}
		call.EnvelopeValid = len(problems) == 0
		call.ParseError = strings.Join(problems, "; ")
		calls = append(calls, call)
	}
	return calls
}

type replayRequestToolSchema struct {
	Parameters map[string]any
}

func extractRequestToolSchemas(body []byte) (map[string]replayRequestToolSchema, error) {
	var request struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("parse request tools: %w", err)
	}
	schemas := make(map[string]replayRequestToolSchema, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			continue
		}
		schemas[tool.Function.Name] = replayRequestToolSchema{Parameters: tool.Function.Parameters}
	}
	return schemas, nil
}

func decodeToolArguments(arguments string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func validateToolCalls(body []byte, calls []replayToolCall) ([]toolCallAssessment, error) {
	schemas, err := extractRequestToolSchemas(body)
	if err != nil {
		return nil, err
	}
	out := make([]toolCallAssessment, 0, len(calls))
	for _, call := range calls {
		assessment := toolCallAssessment{Call: call}
		if !call.EnvelopeValid {
			assessment.Errors = append(assessment.Errors, call.ParseError)
			out = append(out, assessment)
			continue
		}
		schema, known := schemas[call.Name]
		assessment.KnownTool = known
		if !known {
			assessment.Errors = append(assessment.Errors, "unknown tool")
		}
		value, argsErr := decodeToolArguments(call.Arguments)
		assessment.ArgumentsJSONValid = argsErr == nil
		if argsErr != nil {
			assessment.Errors = append(assessment.Errors, fmt.Sprintf("invalid arguments JSON: %v", argsErr))
		}
		if known && argsErr == nil {
			schemaErrors, supported := validateReplaySchema(value, schema.Parameters, "$")
			assessment.SchemaSupported = supported
			assessment.ArgumentsSchemaValid = supported && len(schemaErrors) == 0
			assessment.Errors = append(assessment.Errors, schemaErrors...)
			assessment.RuntimeErrors = validateReplayRuntimeProtocol(call.Name, value)
			assessment.RuntimeProtocolValid = len(assessment.RuntimeErrors) == 0
		}
		out = append(out, assessment)
	}
	return out, nil
}

func validateReplaySchema(value any, schema map[string]any, path string) ([]string, bool) {
	if schema == nil {
		return nil, true
	}
	// Fail closed on assertion keywords this lightweight validator does not
	// implement. Silently ignoring one would overstate tool-call validity.
	allowedKeywords := map[string]bool{
		"type": true, "enum": true, "required": true, "properties": true,
		"items": true, "additionalProperties": true,
		// Annotation-only keywords do not affect validation.
		"description": true, "title": true, "default": true, "examples": true,
		"deprecated": true, "readOnly": true, "writeOnly": true, "$comment": true,
	}
	for keyword := range schema {
		if !allowedKeywords[keyword] {
			return []string{fmt.Sprintf("%s: unsupported schema keyword %s", path, keyword)}, false
		}
	}
	var problems []string
	schemaType := ""
	if rawType, exists := schema["type"]; exists {
		var ok bool
		schemaType, ok = rawType.(string)
		if !ok {
			return []string{fmt.Sprintf("%s: schema type must be a string", path)}, false
		}
	}
	switch schemaType {
	case "", "object", "array", "string", "number", "integer", "boolean", "null":
	default:
		return []string{fmt.Sprintf("%s: unsupported schema type %q", path, schemaType)}, false
	}
	if schemaType == "" {
		for _, keyword := range []string{"required", "properties", "additionalProperties"} {
			if _, exists := schema[keyword]; exists {
				return []string{fmt.Sprintf("%s: schema keyword %s requires explicit type object", path, keyword)}, false
			}
		}
		if _, exists := schema["items"]; exists {
			return []string{fmt.Sprintf("%s: schema keyword items requires explicit type array", path)}, false
		}
	}
	if schemaType != "" && !replayValueHasType(value, schemaType) {
		return []string{fmt.Sprintf("%s: expected %s", path, schemaType)}, true
	}
	if rawEnum, exists := schema["enum"]; exists {
		enumValues, ok := rawEnum.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: enum must be an array", path)}, false
		}
		matched := false
		actual, _ := json.Marshal(value)
		for _, candidate := range enumValues {
			expected, _ := json.Marshal(candidate)
			if bytes.Equal(actual, expected) {
				matched = true
				break
			}
		}
		if !matched {
			problems = append(problems, fmt.Sprintf("%s: value is not in enum", path))
		}
	}
	if schemaType == "object" {
		object := value.(map[string]any)
		var required []any
		if rawRequired, exists := schema["required"]; exists {
			var ok bool
			required, ok = rawRequired.([]any)
			if !ok {
				return append(problems, fmt.Sprintf("%s: required must be an array", path)), false
			}
		}
		for _, item := range required {
			name, ok := item.(string)
			if !ok || name == "" {
				return append(problems, fmt.Sprintf("%s: required entries must be non-empty strings", path)), false
			}
			if _, exists := object[name]; !exists {
				problems = append(problems, fmt.Sprintf("%s.%s: required property is missing", path, name))
			}
		}
		properties := map[string]any{}
		if rawProperties, exists := schema["properties"]; exists {
			var ok bool
			properties, ok = rawProperties.(map[string]any)
			if !ok {
				return append(problems, fmt.Sprintf("%s: properties must be an object", path)), false
			}
		}
		if rawAdditional, exists := schema["additionalProperties"]; exists {
			if _, ok := rawAdditional.(bool); !ok {
				return append(problems, fmt.Sprintf("%s: schema-valued additionalProperties is unsupported", path)), false
			}
		}
		for name, propertyValue := range object {
			propertySchemaValue, known := properties[name]
			if !known {
				if allow, ok := schema["additionalProperties"].(bool); ok && !allow {
					problems = append(problems, fmt.Sprintf("%s.%s: additional property is not allowed", path, name))
				}
				continue
			}
			propertySchema, ok := propertySchemaValue.(map[string]any)
			if !ok {
				return append(problems, fmt.Sprintf("%s.%s: invalid property schema", path, name)), false
			}
			nested, supported := validateReplaySchema(propertyValue, propertySchema, path+"."+name)
			problems = append(problems, nested...)
			if !supported {
				return problems, false
			}
		}
	}
	if schemaType == "array" {
		itemsSchemaValue, hasItems := schema["items"]
		if hasItems {
			itemsSchema, ok := itemsSchemaValue.(map[string]any)
			if !ok {
				return append(problems, fmt.Sprintf("%s: invalid items schema", path)), false
			}
			for i, item := range value.([]any) {
				nested, supported := validateReplaySchema(item, itemsSchema, fmt.Sprintf("%s[%d]", path, i))
				problems = append(problems, nested...)
				if !supported {
					return problems, false
				}
			}
		}
	}
	return problems, true
}

func replayValueHasType(value any, schemaType string) bool {
	switch schemaType {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		parsed, err := strconv.ParseFloat(number.String(), 64)
		return err == nil && math.Trunc(parsed) == parsed
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

// validateReplayRuntimeProtocol covers constraints enforced only inside the
// executor and therefore absent from the public JSON schema. In particular,
// manage_memory and manage_people currently carry a second JSON document in
// the outer query string. This is validation only: it never dispatches a tool
// or touches production state.
func validateReplayRuntimeProtocol(toolName string, value any) []string {
	args, ok := value.(map[string]any)
	if !ok {
		return []string{"arguments must be an object"}
	}
	switch toolName {
	case "internet_search", "search_history", "search_people":
		if replayString(args["query"]) == "" {
			return []string{"query must be a non-empty string"}
		}
	case "read_url":
		if replayString(args["url"]) == "" {
			return []string{"url must be a non-empty string"}
		}
	case "generate_image":
		if replayString(args["prompt"]) == "" {
			return []string{"prompt must be a non-empty string"}
		}
	case "manage_memory":
		return validateReplayMemoryQuery(args["query"])
	case "manage_people":
		return validateReplayPeopleQuery(args["query"])
	}
	return nil
}

func validateReplayMemoryQuery(raw any) []string {
	root, problems := decodeReplayNestedQuery(raw)
	if len(problems) > 0 {
		return problems
	}
	operations := []map[string]any{root}
	if rawOperations, exists := root["operations"]; exists {
		items, ok := rawOperations.([]any)
		if !ok {
			return []string{"query.operations must be an array"}
		}
		operations = operations[:0]
		for _, item := range items {
			operation, ok := item.(map[string]any)
			if !ok {
				// The production executor silently skips non-object batch items.
				continue
			}
			operations = append(operations, operation)
		}
	}
	for i, operation := range operations {
		prefix := fmt.Sprintf("memory operation %d", i+1)
		action := replayString(operation["action"])
		switch action {
		case "add":
			if replayString(operation["content"]) == "" {
				problems = append(problems, prefix+": add requires non-empty content")
			}
		case "update":
			if !replayValidPrefixedID(operation["fact_id"], "Fact:") {
				problems = append(problems, prefix+": update requires a valid fact_id")
			}
		case "delete":
			if !replayValidPrefixedID(operation["fact_id"], "Fact:") {
				problems = append(problems, prefix+": delete requires a valid fact_id")
			}
		default:
			problems = append(problems, prefix+": action must be add, update, or delete")
		}
	}
	return problems
}

func validateReplayPeopleQuery(raw any) []string {
	params, problems := decodeReplayNestedQuery(raw)
	if len(problems) > 0 {
		return problems
	}
	action := replayString(params["operation"])
	switch action {
	case "create":
		if replayString(params["name"]) == "" {
			problems = append(problems, "people create requires a non-empty name")
		}
	case "update":
		if !replayHasPersonReference(params, "person_id", "name") {
			problems = append(problems, "people update requires person_id or name")
		}
		if _, ok := params["updates"].(map[string]any); !ok {
			problems = append(problems, "people update requires an updates object")
		}
	case "delete":
		if !replayHasPersonReference(params, "person_id", "name") {
			problems = append(problems, "people delete requires person_id or name")
		}
	case "merge":
		if !replayHasPersonReference(params, "target_id", "target") {
			problems = append(problems, "people merge requires target_id or target")
		}
		if !replayHasPersonReference(params, "source_id", "source") {
			problems = append(problems, "people merge requires source_id or source")
		}
	default:
		problems = append(problems, "people operation must be create, update, delete, or merge")
	}
	return problems
}

func decodeReplayNestedQuery(raw any) (map[string]any, []string) {
	query, ok := raw.(string)
	if !ok || strings.TrimSpace(query) == "" {
		return nil, []string{"query must contain a JSON object encoded as a string"}
	}
	decoded, err := decodeToolArguments(query)
	if err != nil {
		return nil, []string{fmt.Sprintf("query contains invalid nested JSON: %v", err)}
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil, []string{"query nested JSON must be an object"}
	}
	return root, nil
}

func replayString(value any) string {
	text, _ := value.(string)
	return text
}

func replayHasPersonReference(params map[string]any, idKey, nameKey string) bool {
	if value, exists := params[idKey]; exists && replayValidPrefixedID(value, "Person:") {
		return true
	}
	return replayString(params[nameKey]) != ""
}

func replayValidPrefixedID(value any, prefix string) bool {
	switch typed := value.(type) {
	case string:
		text := strings.TrimPrefix(typed, prefix)
		parsed, err := strconv.ParseInt(text, 10, 64)
		return err == nil && parsed != 0
	case json.Number:
		parsed, err := strconv.ParseFloat(typed.String(), 64)
		return err == nil && int64(parsed) != 0
	default:
		return false
	}
}

func canonicalToolArguments(arguments string) ([]byte, error) {
	value, err := decodeToolArguments(arguments)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func exactToolCallMatch(expected []replayToolCall, actual []toolCallAssessment) (bool, []toolCallAssessment) {
	if len(expected) != len(actual) {
		return false, actual
	}
	if len(expected) == 0 {
		return false, actual
	}
	used := make([]bool, len(expected))
	for i := range actual {
		actualArgs, err := canonicalToolArguments(actual[i].Call.Arguments)
		if err != nil || !actual[i].Call.EnvelopeValid {
			return false, actual
		}
		for j := range expected {
			if used[j] || expected[j].Name != actual[i].Call.Name || !expected[j].EnvelopeValid {
				continue
			}
			expectedArgs, expectedErr := canonicalToolArguments(expected[j].Arguments)
			if expectedErr == nil && bytes.Equal(expectedArgs, actualArgs) {
				used[j] = true
				actual[i].ExactCapturedMatch = true
				break
			}
		}
		if !actual[i].ExactCapturedMatch {
			return false, actual
		}
	}
	return true, actual
}

func toolDecision(finishReason string, calls []toolCallAssessment, generationErr string) string {
	if generationErr != "" {
		return toolDecisionMalformed
	}
	if len(calls) == 0 {
		if strings.EqualFold(finishReason, "stop") {
			return toolDecisionStop
		}
		return toolDecisionMalformed
	}
	if !strings.EqualFold(finishReason, "tool_calls") {
		return toolDecisionMalformed
	}
	for _, call := range calls {
		if !call.Call.EnvelopeValid || !call.KnownTool || !call.ArgumentsJSONValid ||
			!call.SchemaSupported || !call.ArgumentsSchemaValid || !call.RuntimeProtocolValid {
			return toolDecisionMalformed
		}
	}
	return toolDecisionCall
}

func assessToolTurn(requestBody []byte, expected replayAssistantOutput, actual replayRunResult) (toolTurnAssessment, error) {
	expectedCalls, err := validateToolCalls(requestBody, expected.ToolCalls)
	if err != nil {
		return toolTurnAssessment{}, err
	}
	actualCalls, err := validateToolCalls(requestBody, actual.ToolCallDetails)
	if err != nil {
		return toolTurnAssessment{}, err
	}
	exact, actualCalls := exactToolCallMatch(expected.ToolCalls, actualCalls)
	expectedDecision := toolDecision(expected.FinishReason, expectedCalls, expected.GenerationErr)
	actualDecision := toolDecision(actual.FinishReason, actualCalls, actual.Err)
	// A malformed captured call is production evidence, not a usable oracle.
	// Keep it visible to qualitative judges but exclude it from sensitivity
	// denominators just like a captured provider failure.
	expectedAssessable := expected.GenerationErr == "" && expectedDecision != toolDecisionMalformed
	return toolTurnAssessment{
		ExpectedDecision:           expectedDecision,
		ExpectedDecisionAssessable: expectedAssessable,
		ActualDecision:             actualDecision,
		DecisionMatch:              expectedAssessable && expectedDecision == actualDecision,
		ExactCallMatch:             expectedAssessable && exact,
		ExpectedCalls:              expected.ToolCalls,
		ActualCalls:                actualCalls,
	}, nil
}

var unsafeFileComponent = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func safeFileComponent(value string) string {
	clean := strings.Trim(unsafeFileComponent.ReplaceAllString(value, "_"), "._-")
	if clean == "" {
		return "variant"
	}
	return clean
}

func requestModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &req)
	return req.Model
}

func requestForbidsTools(body []byte) bool {
	var req struct {
		ToolChoice any `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	choice, _ := req.ToolChoice.(string)
	return choice == "none"
}

func providerAllowed(body []byte, actual string) bool {
	var req struct {
		Provider struct {
			Only []string `json:"only"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Provider.Only) == 0 {
		return true
	}
	for _, expected := range req.Provider.Only {
		if strings.EqualFold(expected, actual) {
			return true
		}
	}
	return false
}

func responseModelMatches(body []byte, actual string) bool {
	expected := requestModel(body)
	if expected == "" {
		return true
	}
	return actual != "" && strings.EqualFold(expected, actual)
}

func parseReplayCost(raw json.RawMessage) *float64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return &n
	}
	var obj struct {
		TotalCost float64 `json:"total_cost"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return &obj.TotalCost
	}
	return nil
}

func formatOptionalCost(cost *float64) string {
	if cost == nil {
		return "n/a"
	}
	return fmt.Sprintf("$%.6f", *cost)
}

func replayContentBytes(content any, toolCalls []json.RawMessage) []byte {
	var out []byte
	switch v := content.(type) {
	case string:
		out = []byte(v)
	case nil:
		// Tool-only replies legitimately have no text content.
	default:
		out, _ = json.MarshalIndent(v, "", "  ")
	}
	if len(toolCalls) == 0 {
		return out
	}
	toolJSON, _ := json.MarshalIndent(toolCalls, "", "  ")
	if len(out) > 0 {
		out = append(out, []byte("\n\n")...)
	}
	out = append(out, []byte("```json\n")...)
	out = append(out, toolJSON...)
	out = append(out, []byte("\n```\n")...)
	return out
}
