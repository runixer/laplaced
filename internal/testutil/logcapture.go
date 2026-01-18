package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// LogCapture captures slog JSON output for testing.
type LogCapture struct {
	entries []LogEntry
	buffer  *bytes.Buffer
	handler *slog.JSONHandler
	logger  *slog.Logger
}

// LogEntry represents a parsed log entry.
type LogEntry struct {
	Level   string                 `json:"level"`
	Message string                 `json:"msg"`
	Time    time.Time              `json:"time"`
	Fields  map[string]interface{} `json:"-"`
}

// NewLogCapture creates a new log capture.
func NewLogCapture() *LogCapture {
	buf := &bytes.Buffer{}
	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	return &LogCapture{
		buffer:  buf,
		handler: handler,
		logger:  slog.New(handler),
		entries: make([]LogEntry, 0),
	}
}

// Logger returns the slog.Logger that writes to this capture.
func (lc *LogCapture) Logger() *slog.Logger {
	return lc.logger
}

// Handler returns the slog.Handler for this capture.
func (lc *LogCapture) Handler() *slog.JSONHandler {
	return lc.handler
}

// ParseJSON parses a JSON log line into a LogEntry.
func (lc *LogCapture) ParseJSON(p []byte) LogEntry {
	var entry map[string]interface{}
	if err := json.Unmarshal(p, &entry); err != nil {
		return LogEntry{}
	}

	result := LogEntry{
		Fields: make(map[string]interface{}),
	}

	// Extract standard fields
	if level, ok := entry["level"].(string); ok {
		result.Level = level
	}
	if msg, ok := entry["msg"].(string); ok {
		result.Message = msg
	}
	if t, ok := entry["time"].(string); ok {
		if parsedTime, err := time.Parse(time.RFC3339Nano, t); err == nil {
			result.Time = parsedTime
		}
	}

	// Copy remaining fields
	for k, v := range entry {
		if k != "level" && k != "msg" && k != "time" {
			result.Fields[k] = v
		}
	}

	return result
}

// Write implements io.Writer for compatibility.
func (lc *LogCapture) Write(p []byte) (n int, err error) {
	entry := lc.ParseJSON(p)
	lc.entries = append(lc.entries, entry)
	return len(p), nil
}

// Sync is a no-op for compatibility.
func (lc *LogCapture) Sync() error {
	return nil
}

// Entries returns all captured log entries.
func (lc *LogCapture) Entries() []LogEntry {
	// Parse any remaining data in buffer
	if lc.buffer.Len() > 0 {
		lines := bytes.Split(lc.buffer.Bytes(), []byte{'\n'})
		for _, line := range lines {
			if len(line) > 0 {
				lc.entries = append(lc.entries, lc.ParseJSON(line))
			}
		}
		lc.buffer.Reset()
	}
	return lc.entries
}

// Find returns entries matching the specified level and message substring.
func (lc *LogCapture) Find(level string, msgSubstring string) []LogEntry {
	var results []LogEntry
	for _, entry := range lc.Entries() {
		if (level == "" || strings.EqualFold(entry.Level, level)) &&
			(msgSubstring == "" || strings.Contains(entry.Message, msgSubstring)) {
			results = append(results, entry)
		}
	}
	return results
}

// FindByField returns entries containing the specified field value.
func (lc *LogCapture) FindByField(key string, value interface{}) []LogEntry {
	var results []LogEntry
	for _, entry := range lc.Entries() {
		if v, ok := entry.Fields[key]; ok && fmt.Sprint(v) == fmt.Sprint(value) {
			results = append(results, entry)
		}
	}
	return results
}

// HasError returns true if any ERROR level entries were captured.
func (lc *LogCapture) HasError() bool {
	for _, entry := range lc.Entries() {
		if strings.EqualFold(entry.Level, "error") {
			return true
		}
	}
	return false
}

// Clear removes all captured entries.
func (lc *LogCapture) Clear() {
	lc.entries = make([]LogEntry, 0)
	lc.buffer.Reset()
}

// MultiWriter creates an io.Writer that writes to both LogCapture and another writer.
// Useful for writing logs to both a capture and stdout/stderr.
func (lc *LogCapture) MultiWriter(w io.Writer) io.Writer {
	return io.MultiWriter(lc, w)
}
