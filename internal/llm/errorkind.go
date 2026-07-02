package llm

// ErrorKind is a coarse, stable classification of an upstream provider error.
// It exists so retry gates and span attributes agree on one vocabulary: a typo
// in a raw string comparison compiles and silently never matches, whereas a
// misspelled ErrorKind constant does not compile. The wire strings (see
// String) are span-attribute values TraceQL queries depend on — treat them
// as a public contract and never change them for existing kinds.
type ErrorKind int

const (
	// KindNone means there is no error envelope to classify.
	KindNone ErrorKind = iota
	// KindUnknown is an envelope whose shape matched no known class; the raw
	// message surfaces on the span for triage.
	KindUnknown
	// KindInvalidArgument is a deterministic rejection of this exact request
	// (malformed/oversized input).
	KindInvalidArgument
	// KindSafety is a content-policy rejection (Gemini PROHIBITED_CONTENT /
	// RECITATION, OpenAI content_filter, ...).
	KindSafety
	// KindRateLimited is a gateway or provider throttle.
	KindRateLimited
	// KindContextLength means the request exceeded the model's context window.
	KindContextLength
	// KindThoughtSignature is Gemini's "Corrupted thought signature" — a
	// transient reasoning-state failure on multi-turn tool calls.
	KindThoughtSignature
	// KindUpstream5xx is a provider-side server error without a finer class.
	KindUpstream5xx
	// KindUpstream4xx is a provider-side client error without a finer class.
	KindUpstream4xx
)

// String returns the stable wire form recorded as the error.upstream_kind
// span attribute. KindNone renders as "" and is never emitted.
func (k ErrorKind) String() string {
	switch k {
	case KindNone:
		return ""
	case KindInvalidArgument:
		return "invalid_argument"
	case KindSafety:
		return "safety"
	case KindRateLimited:
		return "rate_limited"
	case KindContextLength:
		return "context_length"
	case KindThoughtSignature:
		return "thought_signature"
	case KindUpstream5xx:
		return "upstream_5xx"
	case KindUpstream4xx:
		return "upstream_4xx"
	default:
		return "unknown"
	}
}

// IsRetryable reports whether an identical retry can plausibly succeed.
// Deterministic rejections (content policy, malformed/oversized input) are
// permanent: the provider will reject the same request again, so a retry only
// burns latency and tokens. Transient classes (rate limits, 5xx, thought
// signature, unknown) stay retryable. Motivated by an enricher
// PROHIBITED_CONTENT 403 that was retried 3x over ~21s on the user's critical
// path before failing anyway.
func (k ErrorKind) IsRetryable() bool {
	switch k {
	case KindInvalidArgument, KindSafety, KindContextLength:
		return false
	default:
		return true
	}
}
