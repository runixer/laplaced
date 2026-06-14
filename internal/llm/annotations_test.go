package llm

import (
	"encoding/json"
	"testing"
)

// TestResponseMessage_AnnotationsParsed verifies the source citations returned
// by web-search models (perplexity/sonar) are decoded off the assistant
// message. Regression guard for the dropped-URLs incident: the API delivers
// real URLs in message.annotations[].url_citation.url; if this field stops
// parsing, internet_search silently loses every link again.
func TestResponseMessage_AnnotationsParsed(t *testing.T) {
	body := `{"id":"x","model":"perplexity/sonar-pro","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"RDA <= 250 is safe.[1][2]","annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/a","title":"Source A"}},{"type":"url_citation","url_citation":{"url":"https://example.com/b","title":"Source B"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

	var resp ChatCompletionResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	ann := resp.Choices[0].Message.Annotations
	if len(ann) != 2 {
		t.Fatalf("annotations = %d, want 2", len(ann))
	}
	if ann[0].Type != "url_citation" {
		t.Errorf("type = %q, want url_citation", ann[0].Type)
	}
	if ann[0].URLCitation.URL != "https://example.com/a" || ann[0].URLCitation.Title != "Source A" {
		t.Errorf("annotation[0] = %+v", ann[0].URLCitation)
	}
	if ann[1].URLCitation.URL != "https://example.com/b" {
		t.Errorf("annotation[1].url = %q", ann[1].URLCitation.URL)
	}
}

// TestResponseMessage_NoAnnotations verifies absence is harmless: a normal
// completion without web search decodes with a nil Annotations slice.
func TestResponseMessage_NoAnnotations(t *testing.T) {
	body := `{"id":"x","model":"google/gemini-3.5-flash","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	var resp ChatCompletionResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.Choices[0].Message.Annotations != nil {
		t.Errorf("annotations = %v, want nil", resp.Choices[0].Message.Annotations)
	}
}
