package yandex

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	stt "github.com/runixer/laplaced/internal/gen/yandex"
)

type mockRecognizerServer struct {
	stt.UnimplementedRecognizerServer
	RecvError   error
	SendError   error
	Responses   []*stt.StreamingResponse
	LastRequest *stt.StreamingRequest
}

func (m *mockRecognizerServer) RecognizeStreaming(stream stt.Recognizer_RecognizeStreamingServer) error {
	// Read the first request to get session options
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	m.LastRequest = req

	// Read audio chunks until the client closes the stream
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}

	// Send responses
	for _, resp := range m.Responses {
		if err := stream.Send(resp); err != nil {
			return err
		}
	}

	return nil
}

func TestSpeechKitClient_Recognize(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	mockServer := &mockRecognizerServer{
		Responses: []*stt.StreamingResponse{
			{
				Event: &stt.StreamingResponse_Final{
					Final: &stt.AlternativeUpdate{
						Alternatives: []*stt.Alternative{
							{Text: "проверка"},
						},
					},
				},
			},
			{
				Event: &stt.StreamingResponse_Final{
					Final: &stt.AlternativeUpdate{
						Alternatives: []*stt.Alternative{
							{Text: "рабства тверь"},
						},
					},
				},
			},
		},
	}
	stt.RegisterRecognizerServer(s, mockServer)

	go func() {
		if err := s.Serve(lis); err != nil {
			t.Logf("Server exited with error: %v", err)
		}
	}()
	defer s.Stop()

	dialer := func(ctx context.Context, s string) (net.Conn, error) {
		return lis.Dial()
	}

	client, err := NewSpeechKitClient(
		context.Background(),
		logger,
		"test-api-key",
		"test-folder-id",
		"ru-RU",
		"",
		"",
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	audioData := []byte("test audio data")
	text, err := client.Recognize(context.Background(), audioData)

	require.NoError(t, err)
	assert.Equal(t, "проверка рабства тверь", text)
}

func TestSpeechKitClient_Recognize_EmptyResponse(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	mockServer := &mockRecognizerServer{
		Responses: []*stt.StreamingResponse{
			{
				Event: &stt.StreamingResponse_Final{
					Final: &stt.AlternativeUpdate{
						Alternatives: []*stt.Alternative{},
					},
				},
			},
		},
	}
	stt.RegisterRecognizerServer(s, mockServer)

	go func() {
		if err := s.Serve(lis); err != nil {
			t.Logf("Server exited with error: %v", err)
		}
	}()
	defer s.Stop()

	dialer := func(ctx context.Context, s string) (net.Conn, error) {
		return lis.Dial()
	}

	client, err := NewSpeechKitClient(
		context.Background(),
		logger,
		"test-api-key",
		"test-folder-id",
		"ru-RU",
		"",
		"",
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	audioData := []byte("test audio data")
	text, err := client.Recognize(context.Background(), audioData)

	require.NoError(t, err)
	assert.Equal(t, "", text)
}

func TestSpeechKitClient_Recognize_Language(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	mockServer := &mockRecognizerServer{
		Responses: []*stt.StreamingResponse{
			{
				Event: &stt.StreamingResponse_Final{
					Final: &stt.AlternativeUpdate{
						Alternatives: []*stt.Alternative{
							{Text: "hello world"},
						},
					},
				},
			},
		},
	}
	stt.RegisterRecognizerServer(s, mockServer)

	go func() {
		if err := s.Serve(lis); err != nil {
			t.Logf("Server exited with error: %v", err)
		}
	}()
	defer s.Stop()

	dialer := func(ctx context.Context, s string) (net.Conn, error) {
		return lis.Dial()
	}

	client, err := NewSpeechKitClient(
		context.Background(),
		logger,
		"test-api-key",
		"test-folder-id",
		"en-US",
		"",
		"",
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	audioData := []byte("test audio data")
	text, err := client.Recognize(context.Background(), audioData)

	require.NoError(t, err)
	assert.Equal(t, "hello world", text)

	// Verify language
	require.NotNil(t, mockServer.LastRequest)
	opts := mockServer.LastRequest.GetSessionOptions()
	require.NotNil(t, opts)
	require.NotNil(t, opts.RecognitionModel)
	require.NotNil(t, opts.RecognitionModel.LanguageRestriction)
	assert.Equal(t, []string{"en-US"}, opts.RecognitionModel.LanguageRestriction.LanguageCode)
}
