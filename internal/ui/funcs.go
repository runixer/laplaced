package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"

	"github.com/runixer/laplaced/internal/rag"
)

func GetFuncMap() template.FuncMap {
	return template.FuncMap{
		"seq": func(start, end int) []int {
			var s []int
			for i := start; i <= end; i++ {
				s = append(s, i)
			}
			return s
		},
		"add": func(a, b int) int {
			return a + b
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"div": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"add64": func(a, b int64) int64 {
			return a + b
		},
		"sub64": func(a, b int64) int64 {
			return a - b
		},
		"derefInt64": func(i *int64) int64 {
			if i == nil {
				return 0
			}
			return *i
		},
		"derefString": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		"lower": strings.ToLower,
		"derefBool": func(b *bool) bool {
			if b == nil {
				return false
			}
			return *b
		},
		"deref": func(f *float64) float64 {
			if f == nil {
				return 0
			}
			return *f
		},
		"isTopicList": func(v interface{}) bool {
			_, ok := v.([]rag.TopicSearchResult)
			return ok
		},
		"renderContent": func(content interface{}) string {
			if str, ok := content.(string); ok {
				return str
			}
			if parts, ok := content.([]interface{}); ok {
				var sb strings.Builder
				for _, p := range parts {
					if partMap, ok := p.(map[string]interface{}); ok {
						if typeVal, ok := partMap["type"].(string); ok && typeVal == "text" {
							if txt, ok := partMap["text"].(string); ok {
								sb.WriteString(txt)
							}
						} else if typeVal == "image_url" {
							sb.WriteString("[Image]")
						}
					}
				}
				return sb.String()
			}
			return fmt.Sprintf("%v", content)
		},
		"prettyJSON": func(jsonStr string) string {
			var obj interface{}
			if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
				return jsonStr // Return original if not valid JSON
			}
			pretty, err := json.MarshalIndent(obj, "", "  ")
			if err != nil {
				return jsonStr
			}
			return string(pretty)
		},
		// jsString safely encodes a string for use in JavaScript.
		// It wraps the string in JSON encoding which handles all escaping.
		"jsString": func(s string) template.JS {
			// Use JSON encoding to properly escape the string
			encoded, err := json.Marshal(s)
			if err != nil {
				return template.JS(`""`) //nolint:gosec // JSON encoding is safe
			}
			return template.JS(encoded) //nolint:gosec // JSON encoding is safe
		},
	}
}
