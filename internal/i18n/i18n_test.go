package i18n

import (
	"embed"
	"io"
	"io/fs"
	"sort"
	"testing"
	"time"

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

func TestGetTemplate(t *testing.T) {
	// Create a test filesystem with template strings
	testFS := &testMapFS{
		files: map[string]string{
			"en.yaml": `
test:
  greeting: "Hello, {{.Name}}!"
  multi: "User {{.Name}} has {{.Count}} items"
  nested: "Project: {{.Project.Name}}"
  invalid_template: "Bad syntax {{.Name"
`,
			"ru.yaml": `
test:
  greeting: "Привет, {{.Name}}!"
  multi: "У пользователя {{.Name}} {{.Count}} элементов"
  nested: "Проект: {{.Project.Name}}"
  invalid_template: "Плохой синтаксис {{.Name"
`,
		},
	}

	tr, err := NewTranslatorFromFS(testFS, "en")
	require.NoError(t, err)

	t.Run("simple template en", func(t *testing.T) {
		result, err := tr.GetTemplate("en", "test.greeting", map[string]string{"Name": "World"})
		require.NoError(t, err)
		assert.Equal(t, "Hello, World!", result)
	})

	t.Run("simple template ru", func(t *testing.T) {
		result, err := tr.GetTemplate("ru", "test.greeting", map[string]string{"Name": "Мир"})
		require.NoError(t, err)
		assert.Equal(t, "Привет, Мир!", result)
	})

	t.Run("multiple placeholders", func(t *testing.T) {
		data := struct {
			Name  string
			Count int
		}{Name: "Alice", Count: 5}
		result, err := tr.GetTemplate("en", "test.multi", data)
		require.NoError(t, err)
		assert.Equal(t, "User Alice has 5 items", result)
	})

	t.Run("nested struct", func(t *testing.T) {
		data := struct {
			Project struct {
				Name string
			}
		}{Project: struct{ Name string }{Name: "Laplaced"}}
		result, err := tr.GetTemplate("en", "test.nested", data)
		require.NoError(t, err)
		assert.Equal(t, "Project: Laplaced", result)
	})

	t.Run("missing key returns error", func(t *testing.T) {
		_, err := tr.GetTemplate("en", "nonexistent.key", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "translation key not found")
	})

	t.Run("invalid template syntax returns error", func(t *testing.T) {
		_, err := tr.GetTemplate("en", "test.invalid_template", map[string]string{"Name": "test"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse template")
	})

	t.Run("missing field in struct returns error", func(t *testing.T) {
		// For maps, missing keys return empty string (Go template behavior)
		// For structs, missing fields cause an error
		data := struct{ WrongField string }{WrongField: "value"}
		_, err := tr.GetTemplate("en", "test.greeting", data)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "execute template")
	})

	t.Run("fallback to default lang", func(t *testing.T) {
		result, err := tr.GetTemplate("fr", "test.greeting", map[string]string{"Name": "World"})
		require.NoError(t, err)
		assert.Equal(t, "Hello, World!", result)
	})
}

// testMapFS implements fs.FS for testing with in-memory YAML files
type testMapFS struct {
	files map[string]string
}

func (f *testMapFS) Open(name string) (fs.File, error) {
	content, ok := f.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &testFile{name: name, content: content}, nil
}

func (f *testMapFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, fs.ErrNotExist
	}
	var entries []fs.DirEntry
	for name := range f.files {
		entries = append(entries, &testDirEntry{name: name})
	}
	return entries, nil
}

type testFile struct {
	name    string
	content string
	offset  int
}

func (f *testFile) Stat() (fs.FileInfo, error) {
	return &testFileInfo{name: f.name, size: int64(len(f.content))}, nil
}

func (f *testFile) Read(b []byte) (int, error) {
	if f.offset >= len(f.content) {
		return 0, io.EOF
	}
	n := copy(b, f.content[f.offset:])
	f.offset += n
	return n, nil
}

func (f *testFile) Close() error { return nil }

type testFileInfo struct {
	name string
	size int64
}

func (fi *testFileInfo) Name() string       { return fi.name }
func (fi *testFileInfo) Size() int64        { return fi.size }
func (fi *testFileInfo) Mode() fs.FileMode  { return 0444 }
func (fi *testFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *testFileInfo) IsDir() bool        { return false }
func (fi *testFileInfo) Sys() any           { return nil }

type testDirEntry struct {
	name string
}

func (de *testDirEntry) Name() string               { return de.name }
func (de *testDirEntry) IsDir() bool                { return false }
func (de *testDirEntry) Type() fs.FileMode          { return 0 }
func (de *testDirEntry) Info() (fs.FileInfo, error) { return &testFileInfo{name: de.name}, nil }

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
