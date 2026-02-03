package files

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedFormatError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *UnsupportedFormatError
		expected string
	}{
		{
			name: "with filename and mime type",
			err: &UnsupportedFormatError{
				MimeType: "application/msword",
				FileName: "document.doc",
			},
			expected: "unsupported file format: .doc (MIME: application/msword)",
		},
		{
			name: "with filename only (no extension)",
			err: &UnsupportedFormatError{
				MimeType: "application/zip",
				FileName: "archive",
			},
			expected: "unsupported file format: (application/zip) (MIME: application/zip)",
		},
		{
			name: "with mime type only (no filename)",
			err: &UnsupportedFormatError{
				MimeType: "application/vnd.ms-excel",
				FileName: "",
			},
			expected: "unsupported file format:  (MIME: application/vnd.ms-excel)",
		},
		{
			name: "with double extension",
			err: &UnsupportedFormatError{
				MimeType: "application/x-tar",
				FileName: "archive.tar.gz",
			},
			expected: "unsupported file format: .gz (MIME: application/x-tar)",
		},
		{
			name: "with no extension and no mime type",
			err: &UnsupportedFormatError{
				MimeType: "",
				FileName: "unknown",
			},
			expected: "unsupported file format:  (MIME: )",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.err.Error()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFileTooLargeError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *FileTooLargeError
		expected string
	}{
		{
			name: "with filename and size",
			err: &FileTooLargeError{
				Size:     25 * 1024 * 1024,
				FileName: "large.pdf",
			},
			expected: "file too large: large.pdf (26214400 bytes, max 20971520 bytes)",
		},
		{
			name: "exactly at limit",
			err: &FileTooLargeError{
				Size:     20 * 1024 * 1024,
				FileName: "exact.pdf",
			},
			expected: "file too large: exact.pdf (20971520 bytes, max 20971520 bytes)",
		},
		{
			name: "very large file",
			err: &FileTooLargeError{
				Size:     100 * 1024 * 1024,
				FileName: "huge.mp4",
			},
			expected: "file too large: huge.mp4 (104857600 bytes, max 20971520 bytes)",
		},
		{
			name: "with empty filename",
			err: &FileTooLargeError{
				Size:     30 * 1024 * 1024,
				FileName: "",
			},
			expected: "file too large:  (31457280 bytes, max 20971520 bytes)",
		},
		{
			name: "small but over limit",
			err: &FileTooLargeError{
				Size:     20 * 1024 * 1024,
				FileName: "voice.ogg",
			},
			expected: "file too large: voice.ogg (20971520 bytes, max 20971520 bytes)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.err.Error()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFileType_StringValues(t *testing.T) {
	tests := []struct {
		ft   FileType
		want string
	}{
		{FileTypePhoto, "photo"},
		{FileTypeImage, "image"},
		{FileTypePDF, "pdf"},
		{FileTypeVoice, "voice"},
		{FileTypeAudio, "audio"},
		{FileTypeVideo, "video"},
		{FileTypeVideoNote, "video_note"},
		{FileTypeDocument, "document"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, string(tt.ft))
		})
	}
}

func TestProcessedFile_Structure(t *testing.T) {
	t.Run("creates valid ProcessedFile with all fields", func(t *testing.T) {
		pf := &ProcessedFile{
			LLMParts:    []interface{}{"part1", "part2"},
			Instruction: "test instruction",
			FileType:    FileTypePhoto,
			FileID:      "file123",
			FileName:    "photo.jpg",
			MimeType:    "image/jpeg",
			Size:        1024,
			Duration:    100,
		}

		assert.Equal(t, FileTypePhoto, pf.FileType)
		assert.Equal(t, "file123", pf.FileID)
		assert.Equal(t, "photo.jpg", pf.FileName)
		assert.Equal(t, "image/jpeg", pf.MimeType)
		assert.Equal(t, int64(1024), pf.Size)
		assert.Equal(t, time.Duration(100), pf.Duration)
		assert.Len(t, pf.LLMParts, 2)
		assert.Equal(t, "test instruction", pf.Instruction)
	})

	t.Run("with nil ArtifactID", func(t *testing.T) {
		pf := &ProcessedFile{
			FileType:   FileTypeVoice,
			ArtifactID: nil,
		}

		assert.Nil(t, pf.ArtifactID)
	})

	t.Run("with set ArtifactID", func(t *testing.T) {
		aid := int64(123)
		pf := &ProcessedFile{
			FileType:   FileTypeVoice,
			ArtifactID: &aid,
		}

		require.NotNil(t, pf.ArtifactID)
		assert.Equal(t, int64(123), *pf.ArtifactID)
	})
}
