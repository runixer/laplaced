package files

import (
	"context"
	"testing"

	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestExtractRichMedia_MapsEverySupportedOccurrence(t *testing.T) {
	downloader := new(testutil.MockFileDownloader)
	p := newDetailedTestProcessor(t)
	p.downloader = downloader

	media := []telegram.RichMediaOccurrence{
		{
			Ordinal: 4, Marker: "[rich-media:4]", BlockPath: "blocks[2].photo", Kind: telegram.RichMediaPhoto,
			Photo: []telegram.PhotoSize{
				{FileID: "photo-wide", FileUniqueID: "photo-u-wide", Width: 1600, Height: 900, FileSize: 900},
				{FileID: "photo-large", FileUniqueID: "photo-u-large", Width: 1200, Height: 1200, FileSize: 1200},
			},
		},
		{
			Ordinal: 5, Marker: "[rich-media:5]", BlockPath: "blocks[3].video", Kind: telegram.RichMediaVideo,
			Video: &telegram.Video{
				FileID: "video", FileUniqueID: "video-u", FileName: "movie.mp4", MimeType: "video/mp4", FileSize: 2000, Duration: 12,
			},
		},
		{
			Ordinal: 6, Marker: "[rich-media:6]", BlockPath: "blocks[4].animation", Kind: telegram.RichMediaAnimation,
			Animation: &telegram.Animation{
				FileID: "animation", FileUniqueID: "animation-u", FileName: "motion.mp4", MimeType: "video/mp4", FileSize: 3000, Duration: 4,
			},
		},
		{
			Ordinal: 7, Marker: "[rich-media:7]", BlockPath: "blocks[5].audio", Kind: telegram.RichMediaAudio,
			Audio: &telegram.Audio{
				FileID: "audio", FileUniqueID: "audio-u", MimeType: "audio/mpeg", FileSize: 4000, Duration: 90,
				Title: "Song", Performers: "Artist",
			},
		},
		{
			Ordinal: 8, Marker: "[rich-media:8]", BlockPath: "blocks[6].voice", Kind: telegram.RichMediaVoice,
			Voice: &telegram.Voice{
				FileID: "voice", FileUniqueID: "voice-u", MimeType: "audio/ogg", FileSize: 5000, Duration: 20,
			},
		},
	}

	incoming := p.ExtractRichMedia(media, "user")

	require.Len(t, incoming, 5)
	assert.Equal(t, FileTypePhoto, incoming[0].Kind)
	assert.Equal(t, "photo-large", incoming[0].SourceID)
	assert.Equal(t, "photo-u-large", incoming[0].FileUniqueID)
	assert.Equal(t, int64(1200), incoming[0].Size)
	assert.Equal(t, 4, incoming[0].Ordinal)
	assert.Equal(t, "blocks[2].photo", incoming[0].BlockPath)
	assert.Equal(t, "[rich-media:4]", incoming[0].Marker)
	assert.Equal(t, telegramRichOrigin, incoming[0].Origin)
	assert.Equal(t, "photo-large", incoming[0].FetchKey)

	assert.Equal(t, FileTypeVideo, incoming[1].Kind)
	assert.Equal(t, "movie.mp4", incoming[1].FileName)
	assert.Equal(t, "video/mp4", incoming[1].MIME)
	assert.Equal(t, 12, incoming[1].Duration)
	assert.Equal(t, FileTypeAnimation, incoming[2].Kind)
	assert.Equal(t, "motion.mp4", incoming[2].FileName)
	assert.Equal(t, FileTypeAudio, incoming[3].Kind)
	assert.Equal(t, "Artist - Song.mp3", incoming[3].FileName)
	assert.Equal(t, FileTypeVoice, incoming[4].Kind)
	assert.Equal(t, "voice.ogg", incoming[4].FileName)

	downloader.On("DownloadFile", mock.Anything, "photo-large").Return([]byte("photo"), nil).Once()
	data, err := incoming[0].Fetch(context.Background(), incoming[0].Size)
	require.NoError(t, err)
	assert.Equal(t, []byte("photo"), data)
	downloader.AssertExpectations(t)
}

func TestExtractRichMedia_PreservesMalformedOccurrenceForDetailedFailure(t *testing.T) {
	p := newDetailedTestProcessor(t)
	incoming := p.ExtractRichMedia([]telegram.RichMediaOccurrence{{
		Ordinal: 2, Marker: "[rich-media:2]", BlockPath: "blocks[1].video", Kind: telegram.RichMediaVideo,
	}}, "user")

	require.Len(t, incoming, 1)
	assert.Equal(t, FileTypeVideo, incoming[0].Kind)
	assert.Empty(t, incoming[0].SourceID)
	assert.Nil(t, incoming[0].Fetch)

	results := p.ProcessFilesDetailed(context.Background(), incoming, "user", "")
	require.Len(t, results, 1)
	assert.Equal(t, ProcessFileFailed, results[0].Status)
	assert.Equal(t, "[rich-media:2]", results[0].Incoming.Marker)
}
