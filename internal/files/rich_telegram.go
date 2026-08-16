package files

import (
	"context"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

const telegramRichOrigin = "telegram_rich"

// ExtractRichMedia adapts the ordered semantic occurrences produced by the
// Telegram rich projector to the neutral lazy-download pipeline. It preserves
// one IncomingFile for every occurrence, including malformed occurrences that
// lack a downloadable object; ProcessFilesDetailed reports those as failed
// instead of silently losing their marker and position.
func (p *Processor) ExtractRichMedia(media []telegram.RichMediaOccurrence, userID storage.ScopeID) []IncomingFile {
	if len(media) == 0 {
		return nil
	}

	out := make([]IncomingFile, 0, len(media))
	for i := range media {
		occurrence := media[i]
		f := IncomingFile{
			Origin:    telegramRichOrigin,
			Ordinal:   occurrence.Ordinal,
			BlockPath: occurrence.BlockPath,
			Marker:    occurrence.Marker,
		}

		switch occurrence.Kind {
		case telegram.RichMediaPhoto:
			f.Kind = FileTypePhoto
			f.FileName = "photo.jpg"
			f.MIME = "image/jpeg"
			if photo := bestRichPhotoSize(occurrence.Photo); photo != nil {
				f.SourceID = photo.FileID
				f.FileUniqueID = photo.FileUniqueID
				f.Size = photo.FileSize
			}

		case telegram.RichMediaVideo:
			f.Kind = FileTypeVideo
			if video := occurrence.Video; video != nil {
				f.SourceID = video.FileID
				f.FileUniqueID = video.FileUniqueID
				f.FileName = video.FileName
				f.MIME = video.MimeType
				f.Size = video.FileSize
				f.Duration = video.Duration
			}

		case telegram.RichMediaAnimation:
			f.Kind = FileTypeAnimation
			if animation := occurrence.Animation; animation != nil {
				f.SourceID = animation.FileID
				f.FileUniqueID = animation.FileUniqueID
				f.FileName = animation.FileName
				f.MIME = animation.MimeType
				f.Size = animation.FileSize
				f.Duration = animation.Duration
			}

		case telegram.RichMediaAudio:
			f.Kind = FileTypeAudio
			if audio := occurrence.Audio; audio != nil {
				f.SourceID = audio.FileID
				f.FileUniqueID = audio.FileUniqueID
				f.FileName = audioFilename(audio.FileName, audio.Title, audio.Performers)
				f.MIME = audio.MimeType
				f.Size = audio.FileSize
				f.Duration = audio.Duration
			}

		case telegram.RichMediaVoice:
			f.Kind = FileTypeVoice
			f.FileName = "voice.ogg"
			if voice := occurrence.Voice; voice != nil {
				f.SourceID = voice.FileID
				f.FileUniqueID = voice.FileUniqueID
				f.MIME = voice.MimeType
				f.Size = voice.FileSize
				f.Duration = voice.Duration
			}
		}

		f.FetchKey = f.SourceID
		if f.SourceID != "" && f.Kind != "" {
			fileID := f.SourceID
			kind := f.Kind
			f.Fetch = func(ctx context.Context, maxBytes int64) ([]byte, error) {
				data, _, err := p.downloadWithRetry(ctx, fileID, userID, kind, maxBytes, telegramRichOrigin)
				return data, err
			}
		}
		out = append(out, f)
	}
	return out
}

func bestRichPhotoSize(sizes []telegram.PhotoSize) *telegram.PhotoSize {
	if len(sizes) == 0 {
		return nil
	}
	best := 0
	for i := 1; i < len(sizes); i++ {
		bestSize := sizes[best].FileSize
		candidateSize := sizes[i].FileSize
		bestArea := int64(sizes[best].Width) * int64(sizes[best].Height)
		candidateArea := int64(sizes[i].Width) * int64(sizes[i].Height)
		if candidateSize > bestSize || candidateSize == bestSize && candidateArea > bestArea {
			best = i
		}
	}
	return &sizes[best]
}
