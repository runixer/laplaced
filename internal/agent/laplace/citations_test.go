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
