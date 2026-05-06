package tools

// This file mirrors imagegen.FailureKind / imagegen.ImagegenFailure inside
// the tools package so performImageGeneration can errors.As against a typed
// error without dragging the imagegen agent package into every consumer of
// bot/tools. The mirror is justified by the same rationale as the
// ImageGenerator interface in executor.go (see "Kept internal here so tests
// can substitute without pulling the agent package"). The adapters in
// cmd/bot and cmd/testbot translate between the agent's own type and this
// mirror.

// ImageGenFailureKind classifies why an image-generation call did not
// produce an image. Mirror of imagegen.FailureKind.
type ImageGenFailureKind int

const (
	// ImageGenKindUnknown is the zero value — never set by a real failure.
	ImageGenKindUnknown ImageGenFailureKind = iota
	ImageGenKindTimeout
	ImageGenKindUpstreamError
	ImageGenKindTextRefusal
	ImageGenKindSilentBlockOAI
	ImageGenKindUnknownNoImages
)

// String returns the same lower_snake identifiers as imagegen.FailureKind
// so logs and dashboards line up across the layer boundary.
func (k ImageGenFailureKind) String() string {
	switch k {
	case ImageGenKindTimeout:
		return "timeout"
	case ImageGenKindUpstreamError:
		return "upstream_error"
	case ImageGenKindTextRefusal:
		return "text_refusal"
	case ImageGenKindSilentBlockOAI:
		return "silent_block_oai"
	case ImageGenKindUnknownNoImages:
		return "unknown_no_images"
	default:
		return "unknown"
	}
}

// ImageGenFailure is the typed error performImageGeneration recovers via
// errors.As. The agent-side adapter populates Kind/Text/Provider; Cause is
// the underlying network or context error if any.
type ImageGenFailure struct {
	Kind     ImageGenFailureKind
	Text     string
	Provider string
	Cause    error
}

func (f *ImageGenFailure) Error() string {
	if f == nil {
		return "imagegen: <nil failure>"
	}
	if f.Cause != nil {
		return "imagegen: " + f.Kind.String() + ": " + f.Cause.Error()
	}
	if f.Text != "" {
		return "imagegen: " + f.Kind.String() + ": " + f.Text
	}
	return "imagegen: " + f.Kind.String()
}

func (f *ImageGenFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}
