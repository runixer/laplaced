package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadMatrix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.yaml")
	content := `variants:
  - name: cloud
  - name: local
    chat_base_url: https://example.com
    chat_model: qwen
    chat_thinking: true
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	variants, err := loadMatrix(path)
	require.NoError(t, err)
	require.Len(t, variants, 2)
	require.Equal(t, "local", variants[1].Name)
	require.True(t, variants[1].ChatThinking)
}

func TestLoadMatrixRejectsDuplicateNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.yaml")
	require.NoError(t, os.WriteFile(path, []byte("variants:\n  - name: same\n  - name: same\n"), 0o600))

	_, err := loadMatrix(path)
	require.ErrorContains(t, err, "duplicate")
}
