package snapshot

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SnapshotDir is the on-disk layout of a snapshot.
const (
	tracesSubdir  = "traces"
	manifestFile  = "manifest.yaml"
	defaultDBName = "laplaced.db"
)

// WriteTrace persists a single trace JSON to <outDir>/traces/<traceID>.json.
// Returns the path written, relative to outDir, for inclusion in the manifest.
func WriteTrace(outDir, traceID string, body []byte) (string, error) {
	tracesDir := filepath.Join(outDir, tracesSubdir)
	if err := os.MkdirAll(tracesDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir traces: %w", err)
	}
	rel := filepath.Join(tracesSubdir, traceID+".json")
	abs := filepath.Join(outDir, rel)
	if err := os.WriteFile(abs, body, 0o600); err != nil {
		return "", fmt.Errorf("write trace %s: %w", traceID, err)
	}
	return rel, nil
}

// CopyDB copies srcDB into <outDir>/<defaultDBName>. SQLite files are stable
// snapshots when the source isn't actively being written to — we don't take
// extra locks. Caller is responsible for ensuring the source isn't being
// modified concurrently (typically true for data/prod/laplaced.db which is
// already a copy).
func CopyDB(outDir, srcDB string) (string, error) {
	if srcDB == "" {
		return "", nil
	}
	src, err := os.Open(srcDB) // #nosec G304 -- testbot CLI, srcDB supplied by operator
	if err != nil {
		return "", fmt.Errorf("open source DB %s: %w", srcDB, err)
	}
	defer func() { _ = src.Close() }()

	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir outDir: %w", err)
	}
	dstPath := filepath.Join(outDir, defaultDBName)
	dst, err := os.Create(dstPath) // #nosec G304 -- testbot CLI, outDir supplied by operator
	if err != nil {
		return "", fmt.Errorf("create dst DB %s: %w", dstPath, err)
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("copy DB: %w", err)
	}
	return defaultDBName, nil
}

// WriteManifestFile is a convenience wrapper over WriteManifest pinning the
// canonical filename inside the snapshot directory.
func WriteManifestFile(outDir string, m *Manifest) error {
	return WriteManifest(filepath.Join(outDir, manifestFile), m)
}

// ExtractServiceVersion pulls service.version from the resource attributes
// of a Tempo trace JSON if present. Returns "" on any decode issue — this
// is best-effort metadata for the manifest.
func ExtractServiceVersion(traceJSON []byte) string {
	var doc struct {
		Batches []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(traceJSON, &doc); err != nil {
		return ""
	}
	for _, b := range doc.Batches {
		for _, a := range b.Resource.Attributes {
			if a.Key == "service.version" {
				return a.Value.StringValue
			}
		}
	}
	return ""
}
