package i18n

import (
	"embed"
	"io/fs"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

//go:embed locales/*.yaml
var testLocalesEmbed embed.FS

// testLocales is the sub-filesystem rooted at locales/
var testLocales, _ = fs.Sub(testLocalesEmbed, "locales")

func TestLocaleKeysSynchronized(t *testing.T) {
	// Load en.yaml
	enData, err := fs.ReadFile(testLocalesEmbed, "locales/en.yaml")
	require.NoError(t, err, "failed to read en.yaml")

	var enMap map[string]interface{}
	err = yaml.Unmarshal(enData, &enMap)
	require.NoError(t, err, "failed to parse en.yaml")

	// Load ru.yaml
	ruData, err := fs.ReadFile(testLocalesEmbed, "locales/ru.yaml")
	require.NoError(t, err, "failed to read ru.yaml")

	var ruMap map[string]interface{}
	err = yaml.Unmarshal(ruData, &ruMap)
	require.NoError(t, err, "failed to parse ru.yaml")

	// Extract all keys recursively
	enKeys := extractKeys(enMap, "")
	ruKeys := extractKeys(ruMap, "")

	sort.Strings(enKeys)
	sort.Strings(ruKeys)

	// Find missing keys
	enSet := make(map[string]bool)
	for _, k := range enKeys {
		enSet[k] = true
	}

	ruSet := make(map[string]bool)
	for _, k := range ruKeys {
		ruSet[k] = true
	}

	var missingInRu []string
	for _, k := range enKeys {
		if !ruSet[k] {
			missingInRu = append(missingInRu, k)
		}
	}

	var missingInEn []string
	for _, k := range ruKeys {
		if !enSet[k] {
			missingInEn = append(missingInEn, k)
		}
	}

	if len(missingInRu) > 0 {
		t.Errorf("Keys in en.yaml but missing in ru.yaml:\n%v", missingInRu)
	}

	if len(missingInEn) > 0 {
		t.Errorf("Keys in ru.yaml but missing in en.yaml:\n%v", missingInEn)
	}

	assert.Equal(t, enKeys, ruKeys, "Locale files should have identical key structure")
}

func TestGet(t *testing.T) {
	tr, err := NewTranslatorFromFS(testLocales, "en")
	require.NoError(t, err)

	tests := []struct {
		name     string
		lang     string
		key      string
		args     []interface{}
		expected string
	}{
		{
			name:     "existing key in en",
			lang:     "en",
			key:      "bot.api_error",
			expected: "Sorry, I can't answer right now. Please try asking something else a bit later.",
		},
		{
			name:     "existing key in ru",
			lang:     "ru",
			key:      "bot.api_error",
			expected: "Извините, не могу сейчас ответить. Попробуйте спросить что-нибудь еще чуть позже.",
		},
		{
			name:     "fallback to default lang",
			lang:     "fr", // non-existent language
			key:      "bot.api_error",
			expected: "Sorry, I can't answer right now. Please try asking something else a bit later.",
		},
		{
			name:     "missing key returns key",
			lang:     "en",
			key:      "nonexistent.key",
			expected: "nonexistent.key",
		},
		{
			name:     "empty lang uses default",
			lang:     "",
			key:      "bot.api_error",
			expected: "Sorry, I can't answer right now. Please try asking something else a bit later.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tr.Get(tt.lang, tt.key, tt.args...)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// extractKeys recursively extracts all keys from a nested map, using dot notation.
func extractKeys(m map[string]interface{}, prefix string) []string {
	var keys []string
	for k, v := range m {
		fullKey := k
		if prefix != "" {
			fullKey = prefix + "." + k
		}

		switch val := v.(type) {
		case map[string]interface{}:
			// Recurse into nested maps
			keys = append(keys, extractKeys(val, fullKey)...)
		default:
			// Leaf node - add the key
			keys = append(keys, fullKey)
		}
	}
	return keys
}
