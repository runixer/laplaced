package imagegen

import (
	"context"
	"errors"
	"testing"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/stretchr/testify/assert"
)

func TestClassifyFailure(t *testing.T) {
	imgPresent := []openrouter.ImageOutput{{Type: "image_url"}}

	tests := []struct {
		name     string
		genErr   error
		msg      openrouter.ResponseMessage
		provider string
		want     FailureKind
	}{
		{
			name:   "context deadline → timeout",
			genErr: context.DeadlineExceeded,
			want:   KindTimeout,
		},
		{
			name:   "wrapped context deadline → timeout (errors.Is)",
			genErr: errors.Join(errors.New("wrap"), context.DeadlineExceeded),
			want:   KindTimeout,
		},
		{
			name:   "generic upstream error → upstream_error",
			genErr: errors.New("openrouter API error: 502 Bad Gateway"),
			want:   KindUpstreamError,
		},
		{
			name:     "google text refusal → text_refusal",
			msg:      openrouter.ResponseMessage{Content: "I cannot fulfill this request."},
			provider: "Google",
			want:     KindTextRefusal,
		},
		{
			name:     "openai silent empty → silent_block_oai",
			msg:      openrouter.ResponseMessage{Content: ""},
			provider: "OpenAI",
			want:     KindSilentBlockOAI,
		},
		{
			name:     "openai whitespace-only content treated as empty → silent_block_oai",
			msg:      openrouter.ResponseMessage{Content: "   \n\t  "},
			provider: "OpenAI",
			want:     KindSilentBlockOAI,
		},
		{
			name:     "google empty (no text) → unknown_no_images",
			msg:      openrouter.ResponseMessage{Content: ""},
			provider: "Google",
			want:     KindUnknownNoImages,
		},
		{
			name:     "unknown provider empty → unknown_no_images",
			msg:      openrouter.ResponseMessage{Content: ""},
			provider: "",
			want:     KindUnknownNoImages,
		},
		{
			// Defensive: caller shouldn't reach the classifier with images
			// already present, but if they do, return KindUnknown rather
			// than silently miscategorizing.
			name:     "images present (caller bug) → unknown",
			msg:      openrouter.ResponseMessage{Images: imgPresent},
			provider: "Google",
			want:     KindUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFailure(tt.genErr, tt.msg, tt.provider)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFailureKind_String(t *testing.T) {
	cases := map[FailureKind]string{
		KindUnknown:         "unknown",
		KindTimeout:         "timeout",
		KindUpstreamError:   "upstream_error",
		KindTextRefusal:     "text_refusal",
		KindSilentBlockOAI:  "silent_block_oai",
		KindUnknownNoImages: "unknown_no_images",
		FailureKind(99):     "unknown",
	}
	for k, want := range cases {
		assert.Equal(t, want, k.String(), "kind=%d", k)
	}
}
