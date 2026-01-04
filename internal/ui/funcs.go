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
		"derefBool": func(b *bool) bool {
			if b == nil {
				return false
			}
			return *b
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
	}
}
