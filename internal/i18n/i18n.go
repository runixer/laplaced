package i18n

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed locales/*.yaml
var defaultLocalesFS embed.FS

type Translator struct {
	translations map[string]map[string]string // lang -> key -> value
	defaultLang  string
	mu           sync.RWMutex
}

// NewTranslator creates a new Translator using the embedded locales.
func NewTranslator(defaultLang string) (*Translator, error) {
	subFS, err := fs.Sub(defaultLocalesFS, "locales")
	if err != nil {
		return nil, fmt.Errorf("failed to access embedded locales: %w", err)
	}
	return NewTranslatorFromFS(subFS, defaultLang)
}

// NewTranslatorFromFS creates a new Translator from a given filesystem.
// This is useful for testing or loading locales from a custom location.
func NewTranslatorFromFS(localesFS fs.FS, defaultLang string) (*Translator, error) {
	t := &Translator{
		translations: make(map[string]map[string]string),
		defaultLang:  defaultLang,
	}

	entries, err := fs.ReadDir(localesFS, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read locales directory: %w", err)
	}

	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		ext := filepath.Ext(f.Name())
		if ext == ".yaml" || ext == ".yml" {
			lang := f.Name()[:len(f.Name())-len(ext)]
			content, err := fs.ReadFile(localesFS, f.Name())
			if err != nil {
				return nil, fmt.Errorf("failed to read locale file %s: %w", f.Name(), err)
			}

			// We use a flat map for simplicity.
			// If we want nested structure in YAML, we need to flatten it or use a recursive walker.
			// For now, let's assume the YAML is a flat key-value pair list,
			// OR we can implement a simple flattener.
			var data map[string]interface{}
			if err := yaml.Unmarshal(content, &data); err != nil {
				return nil, fmt.Errorf("failed to parse locale file %s: %w", f.Name(), err)
			}

			flatData := make(map[string]string)
			flatten("", data, flatData)
			t.translations[lang] = flatData
		}
	}

	return t, nil
}

func flatten(prefix string, src map[string]interface{}, dest map[string]string) {
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch child := v.(type) {
		case map[string]interface{}:
			flatten(key, child, dest)
		case string:
			dest[key] = child
		default:
			dest[key] = fmt.Sprintf("%v", v)
		}
	}
}

func (t *Translator) Get(lang, key string, args ...interface{}) string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if lang == "" {
		lang = t.defaultLang
	}

	val := key // Default to key if not found

	// Try requested language
	if tr, ok := t.translations[lang]; ok {
		if v, ok := tr[key]; ok {
			val = v
		}
	}

	// Fallback to default language if key not found
	if val == key && lang != t.defaultLang {
		if tr, ok := t.translations[t.defaultLang]; ok {
			if v, ok := tr[key]; ok {
				val = v
			}
		}
	}

	if len(args) > 0 {
		return fmt.Sprintf(val, args...)
	}
	return val
}
