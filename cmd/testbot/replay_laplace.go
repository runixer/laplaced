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
	Short: "Replay a captured laplace LLM generation from a trace, with optional prompt variants",
	Long: `Reconstructs the exact OpenRouter request from a trace's gen span (the
llm.CreateChatCompletion child of laplace.Execute), rehydrates redacted media by
sha256 from --files-dir, applies a named variant transform, and POSTs it to the
LLM endpoint --runs times per variant.

Built-in variant "baseline" is a no-op (faithful replay). Other variants are
defined in a gitignored JSON --variants-file:

  {
    "anchor": { "insert_before_current_media": "<marker text>" },
    "anchor-protocol": {
      "insert_before_current_media": "<marker text>",
      "system_replace": [{"find": "<substring>", "with": "<replacement>"}]
    }
  }

Example:
  testbot replay-laplace \
    --trace-file data/replay-lily/trace.json \
    --files-dir data/replay-lily \
    --variants-file data/replay-lily/variants.json \
    --variant baseline --variant anchor --variant anchor-protocol \
    --runs 5 --out data/replay-laplace/lily`,
	Annotations: map[string]string{"skip-bot-setup": "true"},
	RunE:        runReplayLaplace,
}

func init() {
	f := replayLaplaceCmd.Flags()
	f.String("trace-file", "", "Path to a trace JSON file (OTLP/Tempo format); fetch it yourself, e.g. curl <tempo>/api/traces/<id>")
	f.String("files-dir", "data/replay-lily", "Directory with <sha256>.* files to rehydrate redacted media")
	f.String("variants-file", "", "JSON file defining named variants (besides built-in 'baseline')")
	f.StringSlice("variant", []string{"baseline"}, "Variant names to run (repeatable)")
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
	InsertBeforeCurrentMedia string `json:"insert_before_current_media"`
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
	runs, _ := cmd.Flags().GetInt("runs")
	outDir, _ := cmd.Flags().GetString("out")

	if traceFile == "" {
		return fmt.Errorf("--trace-file is required")
	}
	traceBytes, err := os.ReadFile(traceFile) // #nosec G304 -- testbot CLI, path supplied by operator
	if err != nil {
		return fmt.Errorf("read trace file: %w", err)
	}

	bodyStr, err := extractGenRequestBody(traceBytes)
	if err != nil {
		return fmt.Errorf("locate gen span: %w", err)
	}

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
			body, err := prepareBody(bodyStr, spec, filesDir)
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
	Name         string      `json:"name"`
	SpanID       string      `json:"spanId"`
	ParentSpanID string      `json:"parentSpanId"`
	Attributes   []otlpAttr  `json:"attributes"`
	Events       []otlpEvent `json:"events"`
}
type otlpTrace struct {
	Batches []struct {
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"batches"`
}

// extractGenRequestBody finds the laplace generation request: the
// llm.CreateChatCompletion span that is a child of laplace.Execute and whose
// llm.request body carries multimodal file parts. Returns the verbatim request
// JSON string. If a tool loop produced several, the last is used.
func extractGenRequestBody(traceBytes []byte) (string, error) {
	var tr otlpTrace
	if err := json.Unmarshal(traceBytes, &tr); err != nil {
		return "", fmt.Errorf("parse trace JSON: %w", err)
	}
	var spans []otlpSpan
	for _, b := range tr.Batches {
		for _, ss := range b.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
	}
	var laplaceID string
	for _, s := range spans {
		if s.Name == "laplace.Execute" {
			laplaceID = s.SpanID
			break
		}
	}
	if laplaceID == "" {
		return "", fmt.Errorf("no laplace.Execute span in trace")
	}
	var best string
	for _, s := range spans {
		if s.Name != "llm.CreateChatCompletion" || s.ParentSpanID != laplaceID {
			continue
		}
		for _, ev := range s.Events {
			if ev.Name != "llm.request" {
				continue
			}
			for _, a := range ev.Attributes {
				if a.Key == "body" && a.Value.StringValue != nil && strings.Contains(*a.Value.StringValue, `"type":"file"`) {
					best = *a.Value.StringValue
				}
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("no gen llm.request with file parts under laplace.Execute")
	}
	return best, nil
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
func prepareBody(bodyStr string, spec variantSpec, filesDir string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal([]byte(bodyStr), &body); err != nil {
		return nil, fmt.Errorf("parse captured body: %w", err)
	}
	delete(body, "trace") // our outbound OTel link; irrelevant to replay

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

	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
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
