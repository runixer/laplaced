package imagegen

import (
	"context"
	"errors"
	"strings"

	"github.com/runixer/laplaced/internal/openrouter"
)

// classifyFailure determines why an image-generation call did not yield an
// image. The classifier intentionally does NOT consult finish_reason or
// native_finish_reason: empirically (curl-verified 2026-05-06) Google image
// models return stop/STOP for both success and refusal, and OpenAI image
// models leave both fields null on a content-policy block — neither is a
// usable signal.
//
// Inputs:
//
//	genErr   — error returned by the OpenRouter client (nil on a successful
//	           HTTP exchange that nonetheless produced no images).
//	msg      — the assistant message from choices[0]; consulted only when
//	           genErr is nil.
//	provider — value of ChatCompletionResponse.Provider (e.g. "OpenAI",
//	           "Google"). Disambiguates silent empty responses.
//
// Returns KindUnknown only as a defensive default when called with images
// already present, which the caller should not do.
func classifyFailure(genErr error, msg openrouter.ResponseMessage, provider string) FailureKind {
	if genErr != nil {
		if errors.Is(genErr, context.DeadlineExceeded) {
			return KindTimeout
		}
		return KindUpstreamError
	}
	if len(msg.Images) == 0 {
		if strings.TrimSpace(msg.Content) != "" {
			return KindTextRefusal
		}
		if provider == "OpenAI" {
			return KindSilentBlockOAI
		}
		return KindUnknownNoImages
	}
	return KindUnknown
}
