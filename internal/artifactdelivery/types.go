// Package artifactdelivery defines the transport-neutral intent carried from
// model tool calls to the persistent outbound planner. It contains no storage
// paths, transport identifiers, or network behavior.
package artifactdelivery

import (
	"fmt"
	"strings"
)

// Mode describes how an artifact should be presented to the user.
type Mode string

const (
	// ModeAuto lets the application choose preview for compatible images and
	// original for every other file. It is valid only for stored artifacts.
	ModeAuto Mode = "auto"
	// ModePreview requests Telegram's native Photo presentation.
	ModePreview Mode = "preview"
	// ModeOriginal requests the stored bytes as a Document.
	ModeOriginal Mode = "original"
	// ModePreviewAndOriginal requests both the Photo presentation and the
	// byte-exact Document. The planner decides their surrounding composition.
	ModePreviewAndOriginal Mode = "preview_and_original"
)

// Generated is one newly-created artifact and its delivery intent. Entries
// are ordered exactly like GeneratedArtifactIDs and remain a side channel;
// they are never rendered as model-authored text.
type Generated struct {
	ArtifactID int64
	Mode       Mode
}

// Selected is one user-owned stored artifact staged by send_artifacts.
type Selected struct {
	ArtifactID int64
	Mode       Mode
}

// ParseGeneratedMode validates a generate_image delivery_mode. The empty
// value deliberately defaults to preview for backward compatibility with old
// traces and models; the current tool schema still requires the field.
func ParseGeneratedMode(value string) (Mode, error) {
	mode := Mode(strings.TrimSpace(value))
	if mode == "" {
		return ModePreview, nil
	}
	switch mode {
	case ModePreview, ModeOriginal, ModePreviewAndOriginal:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported generated artifact delivery mode %q", value)
	}
}

// ParseStoredMode validates a send_artifacts item mode. Empty means auto so a
// plain "send this file" request remains useful without guessing a MIME type
// in the model prompt.
func ParseStoredMode(value string) (Mode, error) {
	mode := Mode(strings.TrimSpace(value))
	if mode == "" {
		return ModeAuto, nil
	}
	switch mode {
	case ModeAuto, ModePreview, ModeOriginal, ModePreviewAndOriginal:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported stored artifact delivery mode %q", value)
	}
}
