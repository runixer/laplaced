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

func TestLoadMatrixRoleRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.yaml")
	content := `variants:
  - name: mixed
    agents:
      splitter:
        base_url: http://localhost:8081
        model: same-model
      archivist:
        base_url: http://localhost:8082
        model: same-model
        chat_template_thinking: true
      answerer:
        base_url: http://localhost:8083
        model: answer-model
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	variants, err := loadMatrix(path)
	require.NoError(t, err)
	require.Len(t, variants, 1)
	opts := variants[0].apply(options{})
	require.Equal(t, "http://localhost:8081", opts.roleRoutes["splitter"].BaseURL)
	require.True(t, opts.roleRoutes["archivist"].ChatThinking)
	require.Equal(t, "answer-model", opts.roleRoutes["laplace"].Model)
}

func TestLoadMatrixRejectsMixedRouting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.yaml")
	content := `variants:
  - name: invalid
    chat_base_url: http://localhost:8080
    chat_model: local
    agents:
      splitter:
        base_url: http://localhost:8081
        model: splitter
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	_, err := loadMatrix(path)
	require.ErrorContains(t, err, "cannot combine")
}

func TestLoadMatrixRejectsDuplicateNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.yaml")
	require.NoError(t, os.WriteFile(path, []byte("variants:\n  - name: same\n  - name: same\n"), 0o600))

	_, err := loadMatrix(path)
	require.ErrorContains(t, err, "duplicate")
}
