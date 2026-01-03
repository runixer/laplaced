package files

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"testing"
	"testing/fstest"

	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockFileDownloader is a mock implementation of telegram.FileDownloader.
type MockFileDownloader struct {
	mock.Mock
}

func (m *MockFileDownloader) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	args := m.Called(ctx, fileID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockFileDownloader) DownloadFileAsBase64(ctx context.Context, fileID string) (string, error) {
	args := m.Called(ctx, fileID)
	return args.String(0), args.Error(1)
}

func createTestTranslator(t *testing.T) *i18n.Translator {
	localesFS := fstest.MapFS{
		"en.yaml": &fstest.MapFile{
			Data: []byte(`
bot:
  voice_instruction: "Listen to the voice message and respond."
  voice_message_marker: "[Voice message]"
`),
		},
	}

	translator, err := i18n.NewTranslatorFromFS(fs.FS(localesFS), "en")
	require.NoError(t, err)
	return translator
}

func TestProcessor_ProcessMessage_Photo(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	photoData := []byte("fake-jpeg-data")
	mockDownloader.On("DownloadFile", mock.Anything, "photo-123").Return(photoData, nil)

	msg := &telegram.Message{
		Photo: []telegram.PhotoSize{
			{FileID: "photo-small", Width: 100, Height: 100},
			{FileID: "photo-123", Width: 1920, Height: 1080}, // best quality
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345)

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypePhoto, files[0].FileType)
	assert.Equal(t, "photo-123", files[0].FileID)
	assert.Equal(t, "image/jpeg", files[0].MimeType)
	assert.Equal(t, int64(len(photoData)), files[0].Size)
	assert.Len(t, files[0].LLMParts, 1)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_Voice(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	voiceData := []byte("fake-ogg-data")
	mockDownloader.On("DownloadFile", mock.Anything, "voice-456").Return(voiceData, nil)

	msg := &telegram.Message{
		Voice: &telegram.Voice{
			FileID:   "voice-456",
			Duration: 10,
			MimeType: "audio/ogg",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345)

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeVoice, files[0].FileType)
	assert.Equal(t, "voice-456", files[0].FileID)
	assert.Equal(t, "audio/ogg", files[0].MimeType)
	assert.Equal(t, "Listen to the voice message and respond.", files[0].Instruction)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_DocumentPDF(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	pdfData := []byte("fake-pdf-data")
	mockDownloader.On("DownloadFile", mock.Anything, "doc-789").Return(pdfData, nil)

	msg := &telegram.Message{
		Document: &telegram.Document{
			FileID:   "doc-789",
			FileName: "report.pdf",
			MimeType: "application/pdf",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345)

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypePDF, files[0].FileType)
	assert.Equal(t, "doc-789", files[0].FileID)
	assert.Equal(t, "report.pdf", files[0].FileName)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_DocumentImage(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	imageData := []byte("fake-png-data")
	mockDownloader.On("DownloadFile", mock.Anything, "img-111").Return(imageData, nil)

	msg := &telegram.Message{
		Document: &telegram.Document{
			FileID:   "img-111",
			FileName: "screenshot.png",
			MimeType: "image/png",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345)

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeImage, files[0].FileType)
	assert.Equal(t, "image/png", files[0].MimeType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_DocumentText(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	textData := []byte("Hello, World!")
	mockDownloader.On("DownloadFile", mock.Anything, "txt-222").Return(textData, nil)

	msg := &telegram.Message{
		Document: &telegram.Document{
			FileID:   "txt-222",
			FileName: "hello.txt",
			MimeType: "text/plain",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345)

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeDocument, files[0].FileType)
	assert.Equal(t, "txt-222", files[0].FileID)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_RetryOnFailure(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)
	processor.retryDelay = 0 // instant retry for tests

	// First call fails, second succeeds
	mockDownloader.On("DownloadFile", mock.Anything, "retry-file").
		Return(nil, errors.New("network error")).Once()
	mockDownloader.On("DownloadFile", mock.Anything, "retry-file").
		Return([]byte("success-data"), nil).Once()

	msg := &telegram.Message{
		Voice: &telegram.Voice{
			FileID: "retry-file",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345)

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeVoice, files[0].FileType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_EmptyMessage(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	msg := &telegram.Message{
		Text: "Hello, no files here!",
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345)

	require.NoError(t, err)
	assert.Empty(t, files)
}
