package main

import (
	"context"

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
		return nil, err
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
