package reranker

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/openrouter"
)

func TestFormatMediaParts(t *testing.T) {
	pdf := []byte("PDF-CONTENT")
	pdfHash := sha256.Sum256(pdf)
	pdfDataURL := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf)

	img := []byte{0xff, 0xd8, 0xff, 0xe0}
	imgHash := sha256.Sum256(img)
	imgDataURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img)

	t.Run("empty input returns empty string", func(t *testing.T) {
		assert.Equal(t, "", formatMediaParts(nil))
		assert.Equal(t, "", formatMediaParts([]interface{}{}))
	})

	t.Run("non-FilePart entries are skipped", func(t *testing.T) {
		got := formatMediaParts([]interface{}{
			openrouter.TextPart{Type: "text", Text: "hi"},
			"raw string",
		})
		assert.Equal(t, "", got)
	})

	t.Run("multiple FileParts are recorded with hashes", func(t *testing.T) {
		got := formatMediaParts([]interface{}{
			openrouter.FilePart{
				Type: "file",
				File: openrouter.File{FileName: "doc.pdf", FileData: pdfDataURL},
			},
			openrouter.FilePart{
				Type: "file",
				File: openrouter.File{FileName: "photo.jpg", FileData: imgDataURL},
			},
		})

		var entries []map[string]any
		assert.NoError(t, json.Unmarshal([]byte(got), &entries))
		assert.Len(t, entries, 2)

		assert.Equal(t, "doc.pdf", entries[0]["filename"])
		assert.Equal(t, "application/pdf", entries[0]["mime"])
		assert.EqualValues(t, len(pdf), entries[0]["size_bytes"])
		assert.Equal(t, hex.EncodeToString(pdfHash[:]), entries[0]["sha256"])

		assert.Equal(t, "photo.jpg", entries[1]["filename"])
		assert.Equal(t, "image/jpeg", entries[1]["mime"])
		assert.EqualValues(t, len(img), entries[1]["size_bytes"])
		assert.Equal(t, hex.EncodeToString(imgHash[:]), entries[1]["sha256"])
	})

	t.Run("malformed data URL drops payload but keeps filename", func(t *testing.T) {
		got := formatMediaParts([]interface{}{
			openrouter.FilePart{
				Type: "file",
				File: openrouter.File{FileName: "broken.bin", FileData: "not a data url"},
			},
		})
		var entries []map[string]any
		assert.NoError(t, json.Unmarshal([]byte(got), &entries))
		assert.Len(t, entries, 1)
		assert.Equal(t, "broken.bin", entries[0]["filename"])
		assert.Equal(t, "", entries[0]["mime"])
		assert.EqualValues(t, 0, entries[0]["size_bytes"])
		_, hasHash := entries[0]["sha256"]
		assert.False(t, hasHash, "no sha256 emitted when payload absent")
	})
}

func TestDecodeDataURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMime string
		wantData []byte
	}{
		{
			name:     "valid base64 data URL",
			input:    "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("hi")),
			wantMime: "image/png",
			wantData: []byte("hi"),
		},
		{
			name:     "no data prefix",
			input:    "https://example.com/img.png",
			wantMime: "",
			wantData: nil,
		},
		{
			name:     "missing semicolon",
			input:    "data:image/png",
			wantMime: "",
			wantData: nil,
		},
		{
			name:     "non-base64 encoding",
			input:    "data:text/plain;charset=utf-8,hello",
			wantMime: "text/plain",
			wantData: nil,
		},
		{
			name:     "malformed base64 payload",
			input:    "data:application/pdf;base64,!!!notbase64!!!",
			wantMime: "application/pdf",
			wantData: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mime, data := decodeDataURL(tt.input)
			assert.Equal(t, tt.wantMime, mime)
			assert.Equal(t, tt.wantData, data)
		})
	}
}
