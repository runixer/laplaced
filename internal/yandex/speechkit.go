package yandex

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	stt "github.com/runixer/laplaced/internal/gen/yandex"
)

const (
	speechKitAPIURL = "stt.api.cloud.yandex.net:443"
	chunkSize       = 4096 // 4 KB
)

// Client defines the interface for a Yandex SpeechKit client.
type Client interface {
	Recognize(ctx context.Context, audioData []byte) (string, error)
}

// SpeechKitClient is a client for Yandex SpeechKit API using gRPC.
type SpeechKitClient struct {
	apiKey      string
	folderID    string
	language    string
	audioFormat string
	sampleRate  int
	logger      *slog.Logger
	conn        *grpc.ClientConn
}

// NewSpeechKitClient creates a new SpeechKitClient.
func NewSpeechKitClient(ctx context.Context, logger *slog.Logger, apiKey, folderID, language, audioFormat, sampleRateStr, target string, opts ...grpc.DialOption) (Client, error) {
	if len(opts) == 0 {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}

	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc connection: %w", err)
	}

	if language == "" {
		language = "ru-RU"
	}
	if audioFormat == "" {
		audioFormat = "ogg_opus"
	}

	sampleRate := 48000
	if sampleRateStr != "" {
		if s, err := strconv.Atoi(sampleRateStr); err == nil {
			sampleRate = s
		}
	}

	return &SpeechKitClient{
		apiKey:      apiKey,
		folderID:    folderID,
		language:    language,
		audioFormat: audioFormat,
		sampleRate:  sampleRate,
		logger:      logger.With("component", "speechkit_client"),
		conn:        conn,
	}, nil
}

// Recognize sends audio data to SpeechKit API for recognition using streaming gRPC.
func (c *SpeechKitClient) Recognize(ctx context.Context, audioData []byte) (string, error) {
	md := metadata.New(map[string]string{
		"authorization": "Api-Key " + c.apiKey,
		"x-folder-id":   c.folderID,
	})
	ctx = metadata.NewOutgoingContext(ctx, md)

	client := stt.NewRecognizerClient(c.conn)
	stream, err := client.RecognizeStreaming(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to open stream: %w", err)
	}

	// Configure Audio Format
	var audioFormatOption *stt.AudioFormatOptions
	switch c.audioFormat {
	case "lpcm":
		audioFormatOption = &stt.AudioFormatOptions{
			AudioFormat: &stt.AudioFormatOptions_RawAudio{
				RawAudio: &stt.RawAudio{
					AudioEncoding:     stt.RawAudio_LINEAR16_PCM,
					SampleRateHertz:   int64(c.sampleRate),
					AudioChannelCount: 1,
				},
			},
		}
	case "ogg_opus":
		fallthrough
	default:
		audioFormatOption = &stt.AudioFormatOptions{
			AudioFormat: &stt.AudioFormatOptions_ContainerAudio{
				ContainerAudio: &stt.ContainerAudio{
					ContainerAudioType: stt.ContainerAudio_OGG_OPUS,
				},
			},
		}
	}

	// Send recognition options
	err = stream.Send(&stt.StreamingRequest{
		Event: &stt.StreamingRequest_SessionOptions{
			SessionOptions: &stt.StreamingOptions{
				RecognitionModel: &stt.RecognitionModelOptions{
					AudioFormat: audioFormatOption,
					TextNormalization: &stt.TextNormalizationOptions{
						TextNormalization: stt.TextNormalizationOptions_TEXT_NORMALIZATION_ENABLED,
						ProfanityFilter:   true,
					},
					LanguageRestriction: &stt.LanguageRestrictionOptions{
						RestrictionType: stt.LanguageRestrictionOptions_WHITELIST,
						LanguageCode:    []string{c.language},
					},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to send session options: %w", err)
	}

	// Stream audio data
	for i := 0; i < len(audioData); i += chunkSize {
		end := i + chunkSize
		if end > len(audioData) {
			end = len(audioData)
		}
		chunk := audioData[i:end]
		err = stream.Send(&stt.StreamingRequest{
			Event: &stt.StreamingRequest_Chunk{
				Chunk: &stt.AudioChunk{Data: chunk},
			},
		})
		if err != nil {
			return "", fmt.Errorf("failed to send audio chunk: %w", err)
		}
		// To avoid sending too fast
		time.Sleep(100 * time.Millisecond)
	}

	if err := stream.CloseSend(); err != nil {
		return "", fmt.Errorf("failed to close send stream: %w", err)
	}

	var finalTexts []string
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to receive from stream: %w", err)
		}

		if final := resp.GetFinal(); final != nil {
			if len(final.Alternatives) > 0 {
				finalTexts = append(finalTexts, final.Alternatives[0].Text)
			}
		} else if partial := resp.GetPartial(); partial != nil {
			if len(partial.Alternatives) > 0 {
				// You can log partial results if needed
				c.logger.Debug("Partial recognition result", "text", partial.Alternatives[0].Text)
			}
		}
	}

	fullResponse := strings.Join(finalTexts, " ")
	c.logger.Info("Successfully recognized speech", "recognized_text", fullResponse)
	return fullResponse, nil
}

// Close closes the gRPC connection.
func (c *SpeechKitClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
