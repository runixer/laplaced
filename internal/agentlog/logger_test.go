package agentlog_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/agentlog"
)

func TestSanitizeInputContext_FileTypedWithNestedFile(t *testing.T) {
	// This is the structure that comes from OpenAI API with files
	// type: "file" at top level, with nested "file" object containing file_data
	inputJSON := `{
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Check this file"
					},
					{
						"type": "file",
						"file": {
							"filename": "MOMENTUM-4_Manual_RU.pdf",
							"file_data": "data:application/pdf;base64,JVBERi0xLjYNJeLjz9MNCjY0OTIgMCBvYmoNPDwvRmlsdGVyL0ZsYXRlRGVjb2RlL0ZpcnN0IDEzOC9MZW5ndGggMTEzNi9OIDE1L1R5cGUvT2JqU3RtPj5zdHJlYW0NCh8OQaMidnj8sHoF3sYeyuTvs2GmC3vT6zKM/5fUArIwrFClaieTnDPm1UjgufzALowB9d2QTWuDR49yrffaX7hty6F2/7cbAPkmT8bkS1FU70B6dH1xEIYb7QZo4PC7o+yt/obitb6lGVIaTPuKTo2AkV3hkojxo+YrMEwVscHtWusa1F7fGC2/St0f3B5Z1t6i3KeApj0KBK4c3Nl+H426s693VG"
						}
					}
				]
			}
		]
	}`

	var input interface{}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatalf("Failed to parse input JSON: %v", err)
	}

	// Get the original file_data length for comparison
	var originalLength int
	if inputMap, ok := input.(map[string]interface{}); ok {
		if messages, ok := inputMap["messages"].([]interface{}); ok && len(messages) > 0 {
			if msg, ok := messages[0].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok && len(content) > 1 {
					if filePart, ok := content[1].(map[string]interface{}); ok {
						if file, ok := filePart["file"].(map[string]interface{}); ok {
							if fileData, ok := file["file_data"].(string); ok {
								originalLength = len(fileData)
							}
						}
					}
				}
			}
		}
	}

	if originalLength == 0 {
		t.Fatal("Could not find original file_data to compare")
	}

	// Apply sanitization
	result := agentlog.SanitizeInputContext(input)

	// Verify that file_data was replaced with a placeholder
	if resultMap, ok := result.(map[string]interface{}); ok {
		if messages, ok := resultMap["messages"].([]interface{}); ok && len(messages) > 0 {
			if msg, ok := messages[0].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok && len(content) > 1 {
					if filePart, ok := content[1].(map[string]interface{}); ok {
						if file, ok := filePart["file"].(map[string]interface{}); ok {
							if fileData, ok := file["file_data"].(string); ok {
								// The sanitized data should be much shorter
								if len(fileData) >= originalLength {
									t.Errorf("file_data was not sanitized: got length %d, expected < %d", len(fileData), originalLength)
								}
								// Should contain placeholder text
								if !strings.Contains(fileData, "FILE:") && !strings.Contains(fileData, "PDF") {
									t.Errorf("file_data placeholder unexpected: %s", fileData)
								}
							} else {
								t.Error("file_data field not found in file object")
							}
						} else {
							t.Error("file object not found in content part")
						}
					}
				}
			}
		}
	}
}

func TestSanitizeConversationTurns(t *testing.T) {
	// Create ConversationTurns with raw base64 data in Request/Response (v0.6.0: FilePart format)
	requestJSON := `{
		"model": "google/gemini-3-flash-preview",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Check this file"},
					{
						"type": "file",
						"file": {
							"filename": "test.pdf",
							"file_data": "data:application/pdf;base64,JVBERi0xLjYNJeLjz9MNCjY0OTIgMCBvYmoNPDwvRmlsdGVyL0ZsYXRlRGVjb2RlL0ZpcnN0IDEzOC9MZW5ndGggMTEzNi9OIDE1L1R5cGUvT2JqU3RtPj5zdHJlYW0NCh8OQaMidnj8sHoF3sYeyuTvs2GmC3vT6zKM/5fUArIwrFClaieTnDPm1UjgufzALowB9d2QTWuDR49yrffaX7hty6F2/7cbAPkmT8bkS1FU70B6dH1xEIYb7QZo4PC7o+yt/obitb6lGVIaTPuKTo2AkV3hkojxo+YrMEwVscHtWusa1F7fGC2/St0f3B5Z1t6i3KeApj0KBK4c3Nl+H426s693VG"
						}
					}
				]
			}
		]
	}`

	responseJSON := `{
		"content": "Response text",
		"usage": {"prompt_tokens": 100, "completion_tokens": 50}
	}`

	turns := &agentlog.ConversationTurns{
		Turns: []agentlog.ConversationTurn{
			{
				Iteration:  1,
				Request:    requestJSON,
				Response:   responseJSON,
				DurationMs: 1000,
			},
		},
		TotalDurationMs:       1000,
		TotalPromptTokens:     100,
		TotalCompletionTokens: 50,
	}

	// Sanitize
	sanitized := agentlog.SanitizeConversationTurns(turns)

	if sanitized == nil {
		t.Fatal("Sanitized turns should not be nil")
	}

	// The Request should be a string (JSON) after sanitization
	sanitizedRequest, ok := sanitized.Turns[0].Request.(string)
	if !ok {
		t.Fatalf("Request should be a string, got %T", sanitized.Turns[0].Request)
	}

	// Check that file_data was sanitized
	if strings.Contains(sanitizedRequest, "data:application/pdf;base64,JVBERi") && !strings.Contains(sanitizedRequest, "FILE:") {
		t.Errorf("Request was not sanitized properly, still contains raw base64")
	}

	if strings.Contains(sanitizedRequest, "FILE:") {
		t.Logf("PASS: Request was sanitized to FILE: placeholder")
	}

	// Also verify it's valid JSON
	var parsedReq map[string]interface{}
	if err := json.Unmarshal([]byte(sanitizedRequest), &parsedReq); err != nil {
		t.Errorf("Request should be valid JSON string: %v", err)
	}

	// Check that Response was preserved
	sanitizedResponseJSON, _ := json.Marshal(sanitized.Turns[0].Response)
	sanitizedResponseStr := string(sanitizedResponseJSON)

	if !strings.Contains(sanitizedResponseStr, "Response text") {
		t.Errorf("Response was modified unexpectedly: %s", sanitizedResponseStr)
	}

	// Check that metadata is preserved
	if sanitized.TotalDurationMs != 1000 {
		t.Errorf("TotalDurationMs should be preserved")
	}
}
