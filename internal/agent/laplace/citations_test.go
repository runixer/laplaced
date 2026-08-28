package laplace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripUnverifiedLinks(t *testing.T) {
	seen := map[string]bool{
		"https://example.com/a": true,
		"https://example.com/b": true,
	}

	tests := []struct {
		name         string
		reply        string
		seen         map[string]bool
		wantReply    string
		wantStripped []string
	}{
		{
			name:      "verified link kept",
			reply:     "see [source](https://example.com/a) for details",
			seen:      seen,
			wantReply: "see [source](https://example.com/a) for details",
		},
		{
			name:         "unverified link unwrapped to text",
			reply:        "see [source](https://evil.com/made-up) here",
			seen:         seen,
			wantReply:    "see source here",
			wantStripped: []string{"https://evil.com/made-up"},
		},
		{
			name:         "mixed: keep verified, strip invented",
			reply:        "[a](https://example.com/a) and [b](https://fake.com/x)",
			seen:         seen,
			wantReply:    "[a](https://example.com/a) and b",
			wantStripped: []string{"https://fake.com/x"},
		},
		{
			name:      "no search this turn — leave everything untouched",
			reply:     "[anything](https://example.com/a)",
			seen:      map[string]bool{},
			wantReply: "[anything](https://example.com/a)",
		},
		{
			name:      "bare [N] markers and plain text untouched",
			reply:     "fact one [1] and fact two [2], no links here",
			seen:      seen,
			wantReply: "fact one [1] and fact two [2], no links here",
		},
		{
			name:      "filename destination is not a fabricated URL",
			reply:     "attached [photo](memory_1053_photo.jpg) from memory",
			seen:      seen,
			wantReply: "attached [photo](memory_1053_photo.jpg) from memory",
		},
		{
			name:      "non-web scheme untouched",
			reply:     "ping me at [chat](tg://resolve?domain=johndoe)",
			seen:      seen,
			wantReply: "ping me at [chat](tg://resolve?domain=johndoe)",
		},
		{
			name:         "uppercase scheme still guarded",
			reply:        "see [source](HTTPS://fake.com/x)",
			seen:         seen,
			wantReply:    "see source",
			wantStripped: []string{"HTTPS://fake.com/x"},
		},
		{
			name:      "canonical marketplace searches are safe to construct",
			reply:     "[Ozon](https://www.ozon.ru/search/?text=Lavazza+Crema) [WB](https://www.wildberries.ru/catalog/0/search.aspx?search=Lavazza+Crema) [Market](https://market.yandex.ru/search?text=Lavazza%20Crema)",
			seen:      seen,
			wantReply: "[Ozon](https://www.ozon.ru/search/?text=Lavazza+Crema) [WB](https://www.wildberries.ru/catalog/0/search.aspx?search=Lavazza+Crema) [Market](https://market.yandex.ru/search?text=Lavazza%20Crema)",
		},
		{
			name:      "new Wildberries search path is accepted",
			reply:     "[WB](https://www.wildberries.ru/catalog?search=Lavazza+Crema)",
			seen:      seen,
			wantReply: "[WB](https://www.wildberries.ru/catalog?search=Lavazza+Crema)",
		},
		{
			name:         "marketplace product page is not exempt",
			reply:        "[product](https://www.ozon.ru/product/invented-123/)",
			seen:         seen,
			wantReply:    "product",
			wantStripped: []string{"https://www.ozon.ru/product/invented-123/"},
		},
		{
			name:         "marketplace search with tracking parameter is not exempt",
			reply:        "[Ozon](https://www.ozon.ru/search/?text=coffee&utm_source=fake)",
			seen:         seen,
			wantReply:    "Ozon",
			wantStripped: []string{"https://www.ozon.ru/search/?text=coffee&utm_source=fake"},
		},
		{
			name:         "lookalike marketplace domain is not exempt",
			reply:        "[Ozon](https://www.ozon.ru.evil.example/search/?text=coffee)",
			seen:         seen,
			wantReply:    "Ozon",
			wantStripped: []string{"https://www.ozon.ru.evil.example/search/?text=coffee"},
		},
		{
			name:         "empty marketplace query is not exempt",
			reply:        "[Market](https://market.yandex.ru/search?text=)",
			seen:         seen,
			wantReply:    "Market",
			wantStripped: []string{"https://market.yandex.ru/search?text="},
		},
		{
			name:      "empty reply",
			reply:     "",
			seen:      seen,
			wantReply: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReply, gotStripped := stripUnverifiedLinks(tt.reply, tt.seen)
			assert.Equal(t, tt.wantReply, gotReply)
			assert.Equal(t, tt.wantStripped, gotStripped)
		})
	}
}
