package snapshot

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ManifestVersion is the on-disk schema version for snapshot manifests.
// Bump when changing the YAML structure in a non-additive way.
const ManifestVersion = "1"

// Manifest describes a single snapshot directory: which traces it contains,
// where they came from, and which DB snapshot ships alongside.
type Manifest struct {
	Version     string               `yaml:"version"`
	Agent       string               `yaml:"agent"`     // free-form label, e.g. "reranker"
	SpanName    string               `yaml:"span_name"` // OTel span name driving the search
	TempoURL    string               `yaml:"tempo_url"`
	Filter      string               `yaml:"filter"` // TraceQL query used
	WindowStart time.Time            `yaml:"window_start"`
	WindowEnd   time.Time            `yaml:"window_end"`
	CapturedAt  time.Time            `yaml:"captured_at"`
	TraceCount  int                  `yaml:"trace_count"`
	DBPath      string               `yaml:"db_path"` // relative to snapshot dir
	Traces      []TraceManifestEntry `yaml:"traces"`
}

// TraceManifestEntry is one row in the manifest's trace listing.
// service_version is captured opportunistically from resource attrs so a
// human reading the manifest can spot mixed-deploy snapshots at a glance.
type TraceManifestEntry struct {
	TraceID        string `yaml:"trace_id"`
	File           string `yaml:"file"`
	DurationMs     int64  `yaml:"duration_ms"`
	StartTimeUnix  int64  `yaml:"start_time_unix"`
	ServiceVersion string `yaml:"service_version,omitempty"`
}

// WriteManifest serialises to <path> with restrictive permissions.
func WriteManifest(path string, m *Manifest) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write manifest %s: %w", path, err)
	}
	return nil
}

// ReadManifest loads a manifest from disk.
func ReadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- testbot CLI, path supplied by operator
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	return &m, nil
}
