package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
    }
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
	rootCmd.AddCommand(replayLaplaceCmd)
}

// memoryMarkerPrefix mirrors the marker context.go prepends before each
// reranker-loaded artifact. A file part NOT preceded by it is current-message
// media — the subject we want the model to act on.
const memoryMarkerPrefix = "📄"

var redactedMediaRe = regexp.MustCompile(`^redacted:sha256:([0-9a-f]+):([^:]+):(\d+)$`)

type variantSpec struct {
	Model                    string   `json:"model"`
	DropMedia                bool     `json:"drop_media"`
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
	Variant      string `json:"variant"`
	Run          int    `json:"run"`
	FinishReason string `json:"finish_reason"`
	OutputTokens int    `json:"output_tokens"`
	Err          string `json:"err,omitempty"`
	ContentFile  string `json:"content_file"`
}

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

	endpoint, apiKey, err := llmEndpoint()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("mkdir out: %w", err)
	}

	var results []replayRunResult
	for _, v := range variants {
		spec, ok := specs[v]
		if !ok && v != "baseline" {
			return fmt.Errorf("variant %q not found in --variants-file (only 'baseline' is built-in)", v)
		}
		for run := 1; run <= runs; run++ {
			body, err := prepareBody(bodyStr, spec, filesDir, modelOverride)
			if err != nil {
				return fmt.Errorf("variant %s: %w", v, err)
			}
			res := postOnce(cmd.Context(), endpoint, apiKey, body, v, run, outDir)
			results = append(results, res)
			status := res.FinishReason
			if res.Err != "" {
				status = "ERR: " + res.Err
			}
			fmt.Printf("[%s run %d] tokens_out=%d finish=%s -> %s\n", v, run, res.OutputTokens, status, res.ContentFile)
		}
	}

	summaryPath := filepath.Join(outDir, "summary.json")
	if data, err := json.MarshalIndent(results, "", "  "); err == nil {
		_ = os.WriteFile(summaryPath, data, 0o600)
		fmt.Printf("\nSummary: %s (%d runs)\n", summaryPath, len(results))
	}
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

// collectGenRequestBodies returns the verbatim request JSON of every
// *.CreateChatCompletion span that is a child of "<agent>.Execute", ordered by
// span start time (so index 0 is the first turn, the last is the synthesis turn
// after a tool loop). Each entry keeps whether the request carried multimodal
// file parts, used by the "auto" selector.
func collectGenRequestBodies(traceBytes []byte, agent string) (bodies []string, hasFiles []bool, err error) {
	spans, err := flattenSpans(traceBytes)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, fmt.Errorf("no %s span in trace (use --list-agents to see options)", agentSpanName)
	}
	type genTurn struct {
		start int64
		body  string
	}
	var turns []genTurn
	for _, s := range spans {
		if !strings.HasSuffix(s.Name, ".CreateChatCompletion") || s.ParentSpanID != agentID {
			continue
		}
		start, _ := strconv.ParseInt(s.StartTimeUnixNano, 10, 64)
		for _, ev := range s.Events {
			if ev.Name != "llm.request" {
				continue
			}
			for _, a := range ev.Attributes {
				if a.Key == "body" && a.Value.StringValue != nil {
					turns = append(turns, genTurn{start: start, body: *a.Value.StringValue})
				}
			}
		}
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].start < turns[j].start })
	for _, t := range turns {
		bodies = append(bodies, t.body)
		hasFiles = append(hasFiles, strings.Contains(t.body, `"type":"file"`))
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
	if err := json.Unmarshal(b, &specs); err != nil {
		return nil, fmt.Errorf("parse variants file: %w", err)
	}
	return specs, nil
}

// prepareBody clones the captured request, applies the variant transform, then
// rehydrates redacted media. Returns the marshaled request ready to POST.
// A per-variant model wins over modelOverride, which wins over the captured one.
func prepareBody(bodyStr string, spec variantSpec, filesDir, modelOverride string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(bodyStr), &body); err != nil {
		return nil, fmt.Errorf("parse captured body: %w", err)
	}
	delete(body, "trace") // our outbound OTel link; irrelevant to replay

	switch {
	case spec.Model != "":
		body["model"] = spec.Model
	case modelOverride != "":
		body["model"] = modelOverride
	}

	if spec.DropMedia {
		dropMediaParts(body)
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
	if err := rehydrateMedia(body, filesDir); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func messageContents(body map[string]any) []([]any) {
	msgs, _ := body["messages"].([]any)
	var out [][]any
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if c, ok := mm["content"].([]any); ok {
			out = append(out, c)
		}
	}
	return out
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

// rehydrateMedia replaces every "redacted:sha256:<hash>:<mime>:<size>" file_data
// with a real data URL loaded from <filesDir>/<hash>.*. Missing files are
// reported together so the operator can copy them once.
func rehydrateMedia(body map[string]any, filesDir string) error {
	var missing []string
	for _, content := range messageContents(body) {
		for _, p := range content {
			pm, ok := p.(map[string]any)
			if !ok || pm["type"] != "file" {
				continue
			}
			fm, _ := pm["file"].(map[string]any)
			fd, _ := fm["file_data"].(string)
			match := redactedMediaRe.FindStringSubmatch(fd)
			if match == nil {
				continue // already a real data URL or unexpected shape
			}
			hash, mime := match[1], match[2]
			path, err := findByHash(filesDir, hash)
			if err != nil {
				missing = append(missing, hash)
				continue
			}
			raw, err := os.ReadFile(path) // #nosec G304 -- resolved within operator-supplied filesDir
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			fm["file_data"] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing media files in %s for sha256: %s", filesDir, strings.Join(missing, ", "))
	}
	return nil
}

func findByHash(dir, hash string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, hash+".*"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("not found")
	}
	return matches[0], nil
}

// --- HTTP ---------------------------------------------------------------------

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

func postOnce(ctx context.Context, endpoint, apiKey string, body []byte, variant string, run int, outDir string) replayRunResult {
	res := replayRunResult{Variant: variant, Run: run}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://laplaced.local")
	req.Header.Set("X-Title", "laplaced-replay-laplace")

	resp, err := (&http.Client{Timeout: 180 * time.Second}).Do(req)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("%s_run%d.raw.json", variant, run)), raw, 0o600)

	var parsed struct {
		Choices []struct {
			FinishReason string          `json:"finish_reason"`
			NativeFinish string          `json:"native_finish_reason"`
			Error        json.RawMessage `json:"error"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		res.Err = fmt.Sprintf("decode response: %v", err)
		return res
	}
	// Gemini safety refusals can surface as a choice-level error with
	// finish_reason "error" and no top-level error object.
	if parsed.Error == nil && len(parsed.Choices) > 0 && len(parsed.Choices[0].Error) > 0 {
		res.Err = string(parsed.Choices[0].Error)
	}
	if parsed.Error != nil {
		res.Err = fmt.Sprintf("%v", parsed.Error)
		return res
	}
	if len(parsed.Choices) == 0 {
		res.Err = "no choices"
		return res
	}
	res.FinishReason = parsed.Choices[0].FinishReason
	res.OutputTokens = parsed.Usage.CompletionTokens

	contentPath := filepath.Join(outDir, fmt.Sprintf("%s_run%d.md", variant, run))
	_ = os.WriteFile(contentPath, []byte(parsed.Choices[0].Message.Content), 0o600)
	res.ContentFile = contentPath
	return res
}
