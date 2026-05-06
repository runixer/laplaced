package main

import (
	"context"
	"errors"

	"github.com/runixer/laplaced/internal/agent/imagegen"
	botTools "github.com/runixer/laplaced/internal/bot/tools"
)

// testbotImageGenAdapter bridges the imagegen agent with the tool-executor's
// narrower ImageGenerator interface (same pattern as cmd/bot/main.go).
type testbotImageGenAdapter struct{ agent *imagegen.Agent }

func (a *testbotImageGenAdapter) Generate(ctx context.Context, req botTools.ImageGenRequest) (*botTools.ImageGenResponse, error) {
	resp, err := a.agent.Generate(ctx, imagegen.Request{
		UserID:      req.UserID,
		Prompt:      req.Prompt,
		InputImages: req.InputImages,
		AspectRatio: req.AspectRatio,
		ImageSize:   req.ImageSize,
	})
	if err != nil {
		return nil, mapImagegenFailureForTestbot(err)
	}
	imgs := make([]botTools.ImageGenImage, len(resp.Images))
	for i, img := range resp.Images {
		imgs[i] = botTools.ImageGenImage{MimeType: img.MimeType, Data: img.Data}
	}
	return &botTools.ImageGenResponse{
		Images:      imgs,
		TextContent: resp.TextContent,
	}, nil
}

// mapImagegenFailureForTestbot mirrors cmd/bot/main.go's mapImagegenFailure.
// Duplicated rather than shared because the two callers live in different
// main packages (no shared internal helper home).
func mapImagegenFailureForTestbot(err error) error {
	var f *imagegen.ImagegenFailure
	if !errors.As(err, &f) {
		return err
	}
	return &botTools.ImageGenFailure{
		Kind:     toToolsImageGenKindForTestbot(f.Kind),
		Text:     f.Text,
		Provider: f.Provider,
		Cause:    f.Cause,
	}
}

func toToolsImageGenKindForTestbot(k imagegen.FailureKind) botTools.ImageGenFailureKind {
	switch k {
	case imagegen.KindTimeout:
		return botTools.ImageGenKindTimeout
	case imagegen.KindUpstreamError:
		return botTools.ImageGenKindUpstreamError
	case imagegen.KindTextRefusal:
		return botTools.ImageGenKindTextRefusal
	case imagegen.KindSilentBlockOAI:
		return botTools.ImageGenKindSilentBlockOAI
	case imagegen.KindUnknownNoImages:
		return botTools.ImageGenKindUnknownNoImages
	default:
		return botTools.ImageGenKindUnknown
	}
}
