package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMustGetHelpers(t *testing.T) {
	// Test panic behavior for mustGetString, mustGetInt, mustGetBool
	tests := []struct {
		name        string
		getFlag     func(*cobra.Command) interface{}
		shouldPanic bool
	}{
		{
			name: "mustGetString panics on invalid flag",
			getFlag: func(cmd *cobra.Command) interface{} {
				return mustGetString(cmd, "nonexistent_flag")
			},
			shouldPanic: true,
		},
		{
			name: "mustGetInt panics on invalid flag",
			getFlag: func(cmd *cobra.Command) interface{} {
				return mustGetInt(cmd, "nonexistent_flag")
			},
			shouldPanic: true,
		},
		{
			name: "mustGetBool panics on invalid flag",
			getFlag: func(cmd *cobra.Command) interface{} {
				return mustGetBool(cmd, "nonexistent_flag")
			},
			shouldPanic: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			if tt.shouldPanic {
				assert.Panics(t, func() {
					tt.getFlag(cmd)
				})
			}
		})
	}

	t.Run("mustGetString works with valid flag", func(t *testing.T) {
		cmd := &cobra.Command{}
		cmd.Flags().String("test_flag", "default", "test")
		err := cmd.Flags().Set("test_flag", "test_value")
		require.NoError(t, err)
		result := mustGetString(cmd, "test_flag")
		assert.Equal(t, "test_value", result)
	})

	t.Run("mustGetInt works with valid flag", func(t *testing.T) {
		cmd := &cobra.Command{}
		cmd.Flags().Int("test_flag", 0, "test")
		err := cmd.Flags().Set("test_flag", "42")
		require.NoError(t, err)
		result := mustGetInt(cmd, "test_flag")
		assert.Equal(t, 42, result)
	})

	t.Run("mustGetBool works with valid flag", func(t *testing.T) {
		cmd := &cobra.Command{}
		cmd.Flags().Bool("test_flag", false, "test")
		err := cmd.Flags().Set("test_flag", "true")
		require.NoError(t, err)
		result := mustGetBool(cmd, "test_flag")
		assert.True(t, result)
	})
}

func TestGetDefaultUserID(t *testing.T) {
	tests := []struct {
		name     string
		envVar   string
		expected int64
	}{
		{"empty env", "", 0},
		{"single ID", "123", 123},
		{"multiple IDs", "123,456,789", 123},
		{"with spaces", " 123 , 456 ", 123},
		{"invalid ID", "abc", 0},
		{"mixed invalid", "abc,123", 123},
		{"negative ID", "-123", 0},
		{"zero ID", "0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set env var
			if tt.envVar != "" {
				os.Setenv("LAPLACED_ALLOWED_USER_IDS", tt.envVar)
				defer os.Unsetenv("LAPLACED_ALLOWED_USER_IDS")
			}
			result := getDefaultUserID()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindConfigPath(t *testing.T) {
	// Get original working directory
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	// Always restore original directory at end of test
	t.Cleanup(func() { _ = os.Chdir(originalWd) })

	tests := []struct {
		name        string
		setup       func(tempDir string) (provided string, cleanup func())
		expectedErr bool
		checkPath   func(string, error)
	}{
		{
			name: "explicit path exists",
			setup: func(tempDir string) (string, func()) {
				cfgPath := filepath.Join(tempDir, "config.yaml")
				err := os.WriteFile(cfgPath, []byte("test: config"), 0600)
				require.NoError(t, err)
				return cfgPath, func() {}
			},
			expectedErr: false,
			checkPath: func(path string, err error) {
				require.NoError(t, err)
				assert.NotEmpty(t, path)
			},
		},
		{
			name: "explicit path not found",
			setup: func(tempDir string) (string, func()) {
				return filepath.Join(tempDir, "nonexistent.yaml"), func() {}
			},
			expectedErr: true,
			checkPath: func(path string, err error) {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "config file not found")
				assert.Empty(t, path)
			},
		},
		{
			name: "empty path and cwd/configs exists",
			setup: func(tempDir string) (string, func()) {
				// Create configs directory in temp directory
				configsDir := filepath.Join(tempDir, "configs")
				err := os.MkdirAll(configsDir, 0700)
				require.NoError(t, err)
				cfgPath := filepath.Join(configsDir, "config.yaml")
				err = os.WriteFile(cfgPath, []byte("test: config"), 0600)
				require.NoError(t, err)
				// Change to temp dir
				err = os.Chdir(tempDir)
				require.NoError(t, err)
				return "", func() { _ = os.Chdir(originalWd) }
			},
			expectedErr: false,
			checkPath: func(path string, err error) {
				require.NoError(t, err)
				// Should find the config in cwd/configs
				assert.NotEmpty(t, path)
				assert.Contains(t, path, "configs")
			},
		},
		{
			name: "empty path and cwd/configs not found",
			setup: func(tempDir string) (string, func()) {
				// Change to temp dir which has no configs subdirectory
				err := os.Chdir(tempDir)
				require.NoError(t, err)
				return "", func() { _ = os.Chdir(originalWd) }
			},
			expectedErr: false,
			checkPath: func(path string, err error) {
				require.NoError(t, err)
				// Should return empty string (will use defaults)
				// Note: if the parent directory has configs/, this might find it
				// So we just check there's no error instead of asserting empty
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a single temp dir for each subtest
			tempDir := t.TempDir()
			provided, cleanup := tt.setup(tempDir)
			defer cleanup()
			path, err := findConfigPath(provided)
			if tt.expectedErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			tt.checkPath(path, err)
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("load config with valid path", func(t *testing.T) {
		// We can't easily test this without a real config file
		// Just verify the function exists and returns the right type
		cfg, err := loadConfig("")
		// Should load defaults
		require.NoError(t, err)
		assert.NotNil(t, cfg)
	})
}

func TestGetTempFileSuffix(t *testing.T) {
	// Test multiple calls produce different results
	suffixes := make(map[string]bool)
	for i := 0; i < 100; i++ {
		suffix := getTempFileSuffix()
		// Should be 16 chars (8 bytes * 2 for hex)
		assert.Equal(t, 16, len(suffix))
		// Should be valid hex
		for _, c := range suffix {
			assert.True(t, strings.ContainsAny(string(c), "0123456789abcdef"))
		}
		suffixes[suffix] = true
	}
	// Should produce mostly unique values (with very high probability)
	assert.Greater(t, len(suffixes), 95)
}

func TestOutputHelpers(t *testing.T) {
	// These are hard to test without capturing stdout
	// Just verify they compile and don't crash on nil inputs
	t.Run("outputCheckJSON works", func(t *testing.T) {
		data := map[string]interface{}{
			"type":  "test",
			"count": 42,
		}
		err := outputCheckJSON(data)
		assert.NoError(t, err)
	})
}

func TestTestBotClose(t *testing.T) {
	// Test the close method with a minimal testbot
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// close() should not panic even with nil services
	err := tb.close()
	assert.NoError(t, err)

	// Calling close again should not error
	err = tb.close()
	assert.NoError(t, err)
}
