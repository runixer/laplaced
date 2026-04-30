package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/runixer/laplaced/internal/agent/imagegen"
	"github.com/spf13/cobra"
)

var (
	generateImageAspectRatio string
	generateImageSize        string
	generateImageOutputDir   string
)

var generateImageCmd = &cobra.Command{
	Use:   "generate-image <prompt>",
	Short: "Generate an image via the imagegen agent (nano banana) and save to disk",
	Long: `Runs the imagegen agent directly without the full bot pipeline.
Useful for testing prompt quality, aspect ratios, and model availability
without involving Telegram or the artifacts lifecycle.

Examples:
  testbot generate-image "a cat samurai in rainy Tokyo, cinematic"
  testbot generate-image "a banana logo" --aspect-ratio 16:9 --size 2K
  testbot generate-image "sunset" --output /tmp/test_output/
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tb := getTestBot(cmd)
		if tb == nil {
			return fmt.Errorf("testbot not initialized")
		}

		userID := getUserID(cmd)
		prompt := args[0]

		if tb.cfg.Agents.ImageGenerator.Model == "" {
			return fmt.Errorf("agents.image_generator.model is not configured")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		agent := imagegen.New(tb.services.OpenRouterClient, &tb.cfg.Agents.ImageGenerator, tb.logger)

		fmt.Printf("Generating with model %s\n", tb.cfg.Agents.ImageGenerator.Model)
		fmt.Printf("  Prompt: %s\n", prompt)
		if generateImageAspectRatio != "" {
			fmt.Printf("  Aspect ratio: %s\n", generateImageAspectRatio)
		}
		if generateImageSize != "" {
			fmt.Printf("  Image size: %s\n", generateImageSize)
		}

		start := time.Now()
		resp, err := agent.Generate(ctx, imagegen.Request{
			UserID:      userID,
			Prompt:      prompt,
			AspectRatio: generateImageAspectRatio,
			ImageSize:   generateImageSize,
		})
		elapsed := time.Since(start)
		if err != nil {
			return fmt.Errorf("generation failed: %w", err)
		}

		outDir := generateImageOutputDir
		if outDir == "" {
			outDir = filepath.Join(os.TempDir(), "testbot-images")
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}

		ts := time.Now().UTC().Format("20060102T150405Z")
		for i, img := range resp.Images {
			ext := ".png"
			switch img.MimeType {
			case "image/jpeg":
				ext = ".jpg"
			case "image/webp":
				ext = ".webp"
			}
			filename := fmt.Sprintf("gen_%s_%d%s", ts, i+1, ext)
			path := filepath.Join(outDir, filename)
			if err := os.WriteFile(path, img.Data, 0o600); err != nil {
				return fmt.Errorf("save image %d: %w", i, err)
			}
			fmt.Printf("  ✓ wrote %s (%d bytes, %s)\n", path, len(img.Data), img.MimeType)
		}

		fmt.Printf("\nDone: %d image(s) in %s.\n", len(resp.Images), elapsed.Round(time.Millisecond))
		if resp.TextContent != "" {
			fmt.Printf("Model note: %s\n", resp.TextContent)
		}
		fmt.Printf("Tokens: prompt=%d completion=%d", resp.PromptTokens, resp.CompletionTokens)
		if resp.Cost != nil {
			fmt.Printf(" cost=$%.4f", *resp.Cost)
		}
		fmt.Println()

		return nil
	},
}

func init() {
	generateImageCmd.Flags().StringVar(&generateImageAspectRatio, "aspect-ratio", "", "Aspect ratio: 1:1, 16:9, 21:9, 1:4, 8:1, etc.")
	generateImageCmd.Flags().StringVar(&generateImageSize, "size", "", "Image size: 512, 1K, 2K, 4K")
	generateImageCmd.Flags().StringVar(&generateImageOutputDir, "output", "", "Output directory (default: $TMPDIR/testbot-images)")
	rootCmd.AddCommand(generateImageCmd)
}
