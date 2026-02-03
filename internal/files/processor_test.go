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
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

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
	mockDownloader := new(testutil.MockFileDownloader)
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

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

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
	mockDownloader := new(testutil.MockFileDownloader)
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

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

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
	mockDownloader := new(testutil.MockFileDownloader)
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

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypePDF, files[0].FileType)
	assert.Equal(t, "doc-789", files[0].FileID)
	assert.Equal(t, "report.pdf", files[0].FileName)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_DocumentImage(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
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

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeImage, files[0].FileType)
	assert.Equal(t, "image/png", files[0].MimeType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_DocumentText(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
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

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeDocument, files[0].FileType)
	assert.Equal(t, "txt-222", files[0].FileID)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_RetryOnFailure(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
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

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeVoice, files[0].FileType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_EmptyMessage(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	msg := &telegram.Message{
		Text: "Hello, no files here!",
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestProcessor_VoiceArtifactDurationThreshold(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Mock file saver
	mockSaver := new(testutil.MockFileSaver)

	voiceData := []byte("fake-ogg-data")
	mockDownloader.On("DownloadFile", mock.Anything, mock.Anything).Return(voiceData, nil)

	t.Run("minVoiceDurationSec=0 saves all voices", func(t *testing.T) {
		processor := NewProcessor(mockDownloader, translator, "en", logger)
		processor.SetFileHandler(mockSaver)
		processor.SetMinVoiceDurationSec(0) // Save all

		mockSaver.ExpectedCalls = nil
		artifactID := int64(1)
		mockSaver.On("SaveFile", mock.Anything, int64(12345), int64(0), "voice", "voice.ogg", "audio/ogg", mock.Anything, "").Return(&artifactID, nil)

		msg := &telegram.Message{
			Voice: &telegram.Voice{
				FileID:   "voice-short",
				Duration: 5, // 5 seconds
				MimeType: "audio/ogg",
			},
		}

		files, err := processor.ProcessMessage(ctx, msg, 12345, "")
		require.NoError(t, err)
		require.Len(t, files, 1)
		mockSaver.AssertExpectations(t)
	})

	t.Run("minVoiceDurationSec=300 skips short voices", func(t *testing.T) {
		processor := NewProcessor(mockDownloader, translator, "en", logger)
		processor.SetFileHandler(mockSaver)
		processor.SetMinVoiceDurationSec(300) // 5 minutes

		mockSaver.ExpectedCalls = nil
		mockSaver.Calls = nil // Clear previous calls
		// Should NOT call SaveFile for short voice

		msg := &telegram.Message{
			Voice: &telegram.Voice{
				FileID:   "voice-short",
				Duration: 30, // 30 seconds - too short
				MimeType: "audio/ogg",
			},
		}

		files, err := processor.ProcessMessage(ctx, msg, 12345, "")
		require.NoError(t, err)
		require.Len(t, files, 1) // Still returns processed file
		assert.Empty(t, mockSaver.Calls, "SaveFile should not be called for short voices")
	})

	t.Run("minVoiceDurationSec=300 saves long voices", func(t *testing.T) {
		processor := NewProcessor(mockDownloader, translator, "en", logger)
		processor.SetFileHandler(mockSaver)
		processor.SetMinVoiceDurationSec(300) // 5 minutes

		mockSaver.ExpectedCalls = nil
		artifactID := int64(2)
		mockSaver.On("SaveFile", mock.Anything, int64(12345), int64(0), "voice", "voice.ogg", "audio/ogg", mock.Anything, "").Return(&artifactID, nil)

		msg := &telegram.Message{
			Voice: &telegram.Voice{
				FileID:   "voice-long",
				Duration: 350, // 5 min 50 sec - should save
				MimeType: "audio/ogg",
			},
		}

		files, err := processor.ProcessMessage(ctx, msg, 12345, "")
		require.NoError(t, err)
		require.Len(t, files, 1)
		mockSaver.AssertExpectations(t)
	})

	t.Run("minVoiceDurationSec=-1 disables voice artifacts", func(t *testing.T) {
		processor := NewProcessor(mockDownloader, translator, "en", logger)
		processor.SetFileHandler(mockSaver)
		processor.SetMinVoiceDurationSec(-1) // Disabled

		mockSaver.ExpectedCalls = nil
		mockSaver.Calls = nil // Clear previous calls
		// Should NOT call SaveFile even for long voice

		msg := &telegram.Message{
			Voice: &telegram.Voice{
				FileID:   "voice-long",
				Duration: 600, // 10 minutes - still not saved
				MimeType: "audio/ogg",
			},
		}

		files, err := processor.ProcessMessage(ctx, msg, 12345, "")
		require.NoError(t, err)
		require.Len(t, files, 1)
		assert.Empty(t, mockSaver.Calls, "SaveFile should not be called when disabled")
	})
}

func TestIsGeminiSupported(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		want     bool
	}{
		// Supported image types
		{"PNG image", "image/png", true},
		{"JPEG image", "image/jpeg", true},
		{"WEBP image", "image/webp", true},
		{"HEIC image", "image/heic", true},
		{"HEIF image", "image/heif", true},
		// Unsupported image types
		{"GIF image", "image/gif", false},
		{"BMP image", "image/bmp", false},
		{"SVG image", "image/svg+xml", false},
		// PDF
		{"PDF document", "application/pdf", true},
		// Videos
		{"MP4 video", "video/mp4", true},
		{"MPEG video", "video/mpeg", true},
		{"MOV video", "video/mov", true},
		{"AVI video", "video/avi", true},
		{"WEBM video", "video/webm", true},
		{"3GPP video", "video/3gpp", true},
		{"QuickTime video", "video/quicktime", true},
		// Unsupported video
		{"MKV video", "video/x-matroska", false},
		// Audio types (all supported)
		{"OGG audio", "audio/ogg", true},
		{"MP3 audio", "audio/mpeg", true},
		{"WAV audio", "audio/wav", true},
		{"M4A audio", "audio/mp4", true},
		{"FLAC audio", "audio/flac", true},
		// Text types (all supported)
		{"Plain text", "text/plain", true},
		{"Markdown", "text/markdown", true},
		{"CSV", "text/csv", true},
		{"JSON", "application/json", true},
		// Unsupported document types
		{"Word document", "application/msword", false},
		{"Word docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
		{"Excel spreadsheet", "application/vnd.ms-excel", false},
		{"PowerPoint", "application/vnd.ms-powerpoint", false},
		{"ZIP archive", "application/zip", false},
		{"Executable", "application/x-executable", false},
		// Edge cases
		{"Empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsGeminiSupported(tt.mimeType))
		})
	}
}

func TestIsFileSizeAllowed(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want bool
	}{
		{"Zero bytes", 0, false},
		{"1 byte", 1, true},
		{"1 MB", 1024 * 1024, true},
		{"10 MB", 10 * 1024 * 1024, true},
		{"20 MB (exact limit)", 20 * 1024 * 1024, true},
		{"20 MB + 1 byte (over limit)", 20*1024*1024 + 1, false},
		{"25 MB", 25 * 1024 * 1024, false},
		{"100 MB", 100 * 1024 * 1024, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsFileSizeAllowed(tt.size))
		})
	}
}

func TestMaxFileSize(t *testing.T) {
	assert.Equal(t, int64(20*1024*1024), MaxFileSize())
}

func TestProcessor_ProcessMessage_UnsupportedFormat(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	msg := &telegram.Message{
		Document: &telegram.Document{
			FileID:   "doc-word",
			FileName: "document.docx",
			MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			FileSize: 1000,
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	// Should return UnsupportedFormatError
	assert.Error(t, err)
	assert.Nil(t, files)
	var unsupportedErr *UnsupportedFormatError
	assert.ErrorAs(t, err, &unsupportedErr)
	assert.Equal(t, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", unsupportedErr.MimeType)
	assert.Equal(t, "document.docx", unsupportedErr.FileName)

	// Verify no download was attempted
	mockDownloader.AssertNotCalled(t, "DownloadFile", mock.Anything, mock.Anything)
}

func TestProcessor_ProcessMessage_FileTooLarge(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	// File larger than 20MB
	msg := &telegram.Message{
		Document: &telegram.Document{
			FileID:   "doc-large",
			FileName: "large.pdf",
			MimeType: "application/pdf",
			FileSize: 25 * 1024 * 1024, // 25 MB
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	// Should return FileTooLargeError
	assert.Error(t, err)
	assert.Nil(t, files)
	var tooLargeErr *FileTooLargeError
	assert.ErrorAs(t, err, &tooLargeErr)
	assert.Equal(t, int64(25*1024*1024), tooLargeErr.Size)
	assert.Equal(t, "large.pdf", tooLargeErr.FileName)

	// Verify no download was attempted
	mockDownloader.AssertNotCalled(t, "DownloadFile", mock.Anything, mock.Anything)
}

func TestProcessor_ProcessMessage_Audio(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	audioData := []byte("fake-mp3-data")
	mockDownloader.On("DownloadFile", mock.Anything, "audio-123").Return(audioData, nil)

	msg := &telegram.Message{
		Audio: &telegram.Audio{
			FileID:   "audio-123",
			Duration: 30,
			MimeType: "audio/mpeg",
			FileName: "song.mp3",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeAudio, files[0].FileType)
	assert.Equal(t, "audio-123", files[0].FileID)
	assert.Equal(t, "audio/mpeg", files[0].MimeType)
	assert.Equal(t, "song.mp3", files[0].FileName)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_VideoNote(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	videoNoteData := []byte("fake-mp4-data")
	mockDownloader.On("DownloadFile", mock.Anything, "video-123").Return(videoNoteData, nil)

	msg := &telegram.Message{
		VideoNote: &telegram.VideoNote{
			FileID:   "video-123",
			Duration: 15,
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeVideoNote, files[0].FileType)
	assert.Equal(t, "video-123", files[0].FileID)
	assert.Equal(t, "video/mp4", files[0].MimeType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_DocumentVideo(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	videoData := []byte("fake-mp4-data")
	mockDownloader.On("DownloadFile", mock.Anything, "video-456").Return(videoData, nil)

	msg := &telegram.Message{
		Document: &telegram.Document{
			FileID:   "video-456",
			FileName: "movie.mp4",
			MimeType: "video/mp4",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeVideo, files[0].FileType)
	assert.Equal(t, "video-456", files[0].FileID)
	assert.Equal(t, "movie.mp4", files[0].FileName)
	assert.Equal(t, "video/mp4", files[0].MimeType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_AllRetriesFailed(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)
	processor.retryDelay = 0 // instant retry for tests

	// All retries fail
	mockDownloader.On("DownloadFile", mock.Anything, "fail-file").
		Return(nil, errors.New("persistent error")).Times(3)

	msg := &telegram.Message{
		Voice: &telegram.Voice{
			FileID: "fail-file",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	// Should return nil, nil (error logged but not propagated)
	assert.NoError(t, err)
	assert.Nil(t, files)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_AudioWithMetadata(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	audioData := []byte("fake-mp3-data")
	mockDownloader.On("DownloadFile", mock.Anything, "audio-meta").Return(audioData, nil)

	msg := &telegram.Message{
		Audio: &telegram.Audio{
			FileID:     "audio-meta",
			Duration:   180,
			MimeType:   "audio/mpeg",
			FileName:   "",
			Title:      "Test Song",
			Performers: "Test Artist",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeAudio, files[0].FileType)
	assert.Equal(t, "audio-meta", files[0].FileID)
	assert.Equal(t, "Test Artist - Test Song.mp3", files[0].FileName)
	assert.Equal(t, "audio/mpeg", files[0].MimeType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_WithTitleOnly(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	audioData := []byte("fake-mp3-data")
	mockDownloader.On("DownloadFile", mock.Anything, "audio-title").Return(audioData, nil)

	msg := &telegram.Message{
		Audio: &telegram.Audio{
			FileID:     "audio-title",
			Duration:   120,
			MimeType:   "audio/mpeg",
			Title:      "Solo Track",
			Performers: "",
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeAudio, files[0].FileType)
	assert.Equal(t, "Solo Track.mp3", files[0].FileName)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_VoiceFileTooLarge(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	// Voice larger than 20MB
	msg := &telegram.Message{
		Voice: &telegram.Voice{
			FileID:   "voice-large",
			Duration: 600,
			FileSize: 25 * 1024 * 1024, // 25 MB
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	// Should return FileTooLargeError
	assert.Error(t, err)
	assert.Nil(t, files)
	var tooLargeErr *FileTooLargeError
	assert.ErrorAs(t, err, &tooLargeErr)
	assert.Equal(t, int64(25*1024*1024), tooLargeErr.Size)

	// Verify no download was attempted
	mockDownloader.AssertNotCalled(t, "DownloadFile", mock.Anything, mock.Anything)
}

func TestProcessor_ProcessMessage_AudioFileTooLarge(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	// Audio larger than 20MB
	msg := &telegram.Message{
		Audio: &telegram.Audio{
			FileID:   "audio-large",
			FileSize: 30 * 1024 * 1024, // 30 MB
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	// Should return FileTooLargeError
	assert.Error(t, err)
	assert.Nil(t, files)
	var tooLargeErr *FileTooLargeError
	assert.ErrorAs(t, err, &tooLargeErr)
	assert.Equal(t, int64(30*1024*1024), tooLargeErr.Size)

	// Verify no download was attempted
	mockDownloader.AssertNotCalled(t, "DownloadFile", mock.Anything, mock.Anything)
}

func TestProcessor_ProcessMessage_VideoNoteFileTooLarge(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	// Video note larger than 20MB
	msg := &telegram.Message{
		VideoNote: &telegram.VideoNote{
			FileID:   "vn-large",
			FileSize: 22 * 1024 * 1024, // 22 MB
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	// Should return FileTooLargeError
	assert.Error(t, err)
	assert.Nil(t, files)
	var tooLargeErr *FileTooLargeError
	assert.ErrorAs(t, err, &tooLargeErr)
	assert.Equal(t, int64(22*1024*1024), tooLargeErr.Size)

	// Verify no download was attempted
	mockDownloader.AssertNotCalled(t, "DownloadFile", mock.Anything, mock.Anything)
}

func TestProcessor_ProcessMessage_WithFileHandler(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	mockSaver := new(testutil.MockFileSaver)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)
	processor.SetFileHandler(mockSaver)

	photoData := []byte("fake-jpeg-data")
	mockDownloader.On("DownloadFile", mock.Anything, "photo-999").Return(photoData, nil)

	artifactID := int64(42)
	mockSaver.On("SaveFile", mock.Anything, int64(12345), int64(0), "image", "photo.jpg", "image/jpeg", mock.Anything, "").
		Return(&artifactID, nil)

	msg := &telegram.Message{
		Photo: []telegram.PhotoSize{
			{FileID: "photo-999", Width: 1920, Height: 1080},
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypePhoto, files[0].FileType)
	require.NotNil(t, files[0].ArtifactID)
	assert.Equal(t, int64(42), *files[0].ArtifactID)
	mockDownloader.AssertExpectations(t)
	mockSaver.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_WithFileHandlerError(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	mockSaver := new(testutil.MockFileSaver)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)
	processor.SetFileHandler(mockSaver)

	photoData := []byte("fake-jpeg-data")
	mockDownloader.On("DownloadFile", mock.Anything, "photo-err").Return(photoData, nil)

	mockSaver.On("SaveFile", mock.Anything, int64(12345), int64(0), "image", "photo.jpg", "image/jpeg", mock.Anything, "").
		Return(nil, errors.New("database error"))

	msg := &telegram.Message{
		Photo: []telegram.PhotoSize{
			{FileID: "photo-err", Width: 1920, Height: 1080},
		},
	}

	// Should still succeed despite artifact save failure
	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Nil(t, files[0].ArtifactID, "artifact ID should be nil on save error")
	mockDownloader.AssertExpectations(t)
	mockSaver.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_VideoNoteWithUniqueID(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	videoNoteData := []byte("fake-mp4-data")
	mockDownloader.On("DownloadFile", mock.Anything, "vn-unique").Return(videoNoteData, nil)

	msg := &telegram.Message{
		VideoNote: &telegram.VideoNote{
			FileID:       "vn-unique",
			FileUniqueID: "unique123abc",
			Length:       300,
			Duration:     30,
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeVideoNote, files[0].FileType)
	assert.Equal(t, "video_note_unique123abc.mp4", files[0].FileName)
	assert.Equal(t, "video/mp4", files[0].MimeType)
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_AudioDefaultMimeType(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	audioData := []byte("fake-audio-data")
	mockDownloader.On("DownloadFile", mock.Anything, "audio-nomime").Return(audioData, nil)

	msg := &telegram.Message{
		Audio: &telegram.Audio{
			FileID:   "audio-nomime",
			Duration: 60,
			MimeType: "", // Empty mime type
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeAudio, files[0].FileType)
	assert.Equal(t, "audio/mpeg", files[0].MimeType, "should default to audio/mpeg")
	mockDownloader.AssertExpectations(t)
}

func TestProcessor_ProcessMessage_VoiceDefaultMimeType(t *testing.T) {
	ctx := context.Background()
	mockDownloader := new(testutil.MockFileDownloader)
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	processor := NewProcessor(mockDownloader, translator, "en", logger)

	voiceData := []byte("fake-voice-data")
	mockDownloader.On("DownloadFile", mock.Anything, "voice-nomime").Return(voiceData, nil)

	msg := &telegram.Message{
		Voice: &telegram.Voice{
			FileID:   "voice-nomime",
			Duration: 15,
			MimeType: "", // Empty mime type
		},
	}

	files, err := processor.ProcessMessage(ctx, msg, 12345, "")

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, FileTypeVoice, files[0].FileType)
	assert.Equal(t, "audio/ogg", files[0].MimeType, "should default to audio/ogg")
	mockDownloader.AssertExpectations(t)
}
