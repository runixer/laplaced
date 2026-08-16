package files

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/telegram"
)

func newDetailedTestProcessor(t *testing.T) *Processor {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewProcessor(nil, createTestTranslator(t), "en", logger)
}

func TestProcessFilesDetailed_PreservesOrderAndContinuesAfterFailures(t *testing.T) {
	p := newDetailedTestProcessor(t)
	var unsupportedFetched atomic.Bool
	var laterFetched atomic.Bool
	downloadErr := errors.New("network unavailable")

	incoming := []IncomingFile{
		{
			Kind: FileTypePhoto, SourceID: "photo", Size: 5,
			Fetch: func(context.Context, int64) ([]byte, error) { return []byte("photo"), nil },
		},
		{
			Kind: FileTypeAnimation, SourceID: "gif", FileName: "clip.gif", MIME: "image/gif", Size: 3,
			Fetch: func(context.Context, int64) ([]byte, error) {
				unsupportedFetched.Store(true)
				return []byte("gif"), nil
			},
		},
		{
			Kind: FileTypeVoice, SourceID: "voice", Size: 1,
			Fetch: func(context.Context, int64) ([]byte, error) { return nil, downloadErr },
		},
		{
			Kind: FileTypeAudio, SourceID: "audio", MIME: "audio/mpeg", Size: 5,
			Fetch: func(context.Context, int64) ([]byte, error) {
				laterFetched.Store(true)
				return []byte("audio"), nil
			},
		},
	}

	results := p.ProcessFilesDetailed(context.Background(), incoming, "user", "")

	require.Len(t, results, 4)
	assert.Equal(t, "photo", results[0].Incoming.SourceID)
	assert.Equal(t, ProcessFileAvailable, results[0].Status)
	assert.Equal(t, "gif", results[1].Incoming.SourceID)
	assert.Equal(t, ProcessFileUnsupported, results[1].Status)
	assert.False(t, unsupportedFetched.Load(), "unsupported animation must fail before fetch")
	assert.Equal(t, "voice", results[2].Incoming.SourceID)
	assert.Equal(t, ProcessFileFailed, results[2].Status)
	assert.ErrorIs(t, results[2].Err, downloadErr)
	assert.Equal(t, "audio", results[3].Incoming.SourceID)
	assert.Equal(t, ProcessFileAvailable, results[3].Status)
	assert.True(t, laterFetched.Load(), "a failed sibling must not abort later files")
}

func TestProcessFilesDetailed_AnimationMIME(t *testing.T) {
	p := newDetailedTestProcessor(t)
	results := p.ProcessFilesDetailed(context.Background(), []IncomingFile{
		{
			Kind: FileTypeAnimation, SourceID: "mp4", FileName: "clip.mp4", MIME: "video/mp4", Size: 3,
			Fetch: func(context.Context, int64) ([]byte, error) { return []byte("mp4"), nil },
		},
		{
			Kind: FileTypeAnimation, SourceID: "gif", FileName: "clip.gif", MIME: "image/gif", Size: 3,
			Fetch: func(context.Context, int64) ([]byte, error) { return []byte("gif"), nil },
		},
	}, "user", "")

	require.Len(t, results, 2)
	assert.Equal(t, ProcessFileAvailable, results[0].Status)
	require.NotNil(t, results[0].Processed)
	assert.Equal(t, FileTypeAnimation, results[0].Processed.FileType)
	assert.Equal(t, "video/mp4", results[0].Processed.MimeType)
	assert.Equal(t, ProcessFileUnsupported, results[1].Status)
	var unsupported *UnsupportedFormatError
	assert.ErrorAs(t, results[1].Err, &unsupported)
}

func TestProcessFilesDetailed_CoalescesNonEmptyUniqueID(t *testing.T) {
	p := newDetailedTestProcessor(t)
	var fetches atomic.Int32
	newOccurrence := func(sourceID string) IncomingFile {
		return IncomingFile{
			Kind: FileTypePhoto, SourceID: sourceID, FileUniqueID: "stable-photo", Origin: "telegram_rich",
			Ordinal: 3, BlockPath: "blocks[1].photo", Size: 4,
			Fetch: func(context.Context, int64) ([]byte, error) {
				fetches.Add(1)
				return []byte("jpeg"), nil
			},
		}
	}

	results := p.ProcessFilesDetailed(context.Background(), []IncomingFile{
		newOccurrence("first-file-id"),
		newOccurrence("second-file-id"),
	}, "user", "")

	require.Len(t, results, 2)
	assert.Equal(t, int32(1), fetches.Load())
	assert.Equal(t, ProcessFileAvailable, results[0].Status)
	require.NotNil(t, results[0].Processed)
	assert.Equal(t, 3, results[0].Processed.Ordinal)
	assert.Equal(t, "blocks[1].photo", results[0].Processed.BlockPath)
	assert.Equal(t, "telegram_rich", results[0].Processed.Origin)
	assert.Equal(t, "stable-photo", results[0].Processed.FileUniqueID)
	assert.Equal(t, ProcessFileDuplicateRef, results[1].Status)
	assert.Equal(t, 0, results[1].DuplicateOf)
	assert.Nil(t, results[1].Processed)
	assert.Equal(t, "second-file-id", results[1].Incoming.SourceID, "duplicate occurrence metadata is preserved")
}

func TestProcessFilesDetailed_EmptyUniqueIDNeverDeduplicates(t *testing.T) {
	p := newDetailedTestProcessor(t)
	var fetches atomic.Int32
	newOccurrence := func(sourceID string) IncomingFile {
		return IncomingFile{
			Kind: FileTypePhoto, SourceID: sourceID, Size: 1,
			Fetch: func(context.Context, int64) ([]byte, error) {
				fetches.Add(1)
				return []byte{'x'}, nil
			},
		}
	}

	results := p.ProcessFilesDetailed(context.Background(), []IncomingFile{
		newOccurrence("first"),
		newOccurrence("second"),
	}, "user", "")

	require.Len(t, results, 2)
	assert.Equal(t, int32(2), fetches.Load())
	assert.Equal(t, ProcessFileAvailable, results[0].Status)
	assert.Equal(t, ProcessFileAvailable, results[1].Status)
}

func TestProcessFilesDetailed_AggregateBudget(t *testing.T) {
	p := newDetailedTestProcessor(t)
	firstSize := int64(12 * 1024 * 1024)
	secondSize := int64(8 * 1024 * 1024)
	var secondFetched atomic.Bool
	var thirdFetched atomic.Bool

	results := p.ProcessFilesDetailed(context.Background(), []IncomingFile{
		{
			Kind: FileTypePhoto, SourceID: "first", Size: firstSize,
			Fetch: func(context.Context, int64) ([]byte, error) { return make([]byte, firstSize), nil },
		},
		{
			Kind: FileTypePhoto, SourceID: "second", Size: secondSize,
			Fetch: func(context.Context, int64) ([]byte, error) {
				secondFetched.Store(true)
				return make([]byte, secondSize), nil
			},
		},
		{
			Kind: FileTypePhoto, SourceID: "third", Size: 1,
			Fetch: func(context.Context, int64) ([]byte, error) {
				thirdFetched.Store(true)
				return []byte{'x'}, nil
			},
		},
	}, "user", "")

	require.Len(t, results, 3)
	assert.Equal(t, ProcessFileAvailable, results[0].Status)
	assert.Equal(t, ProcessFileAvailable, results[1].Status, "exactly 20 MiB aggregate must be accepted")
	assert.True(t, secondFetched.Load())
	assert.Equal(t, ProcessFileOmittedLimit, results[2].Status)
	assert.False(t, thirdFetched.Load(), "the first byte over the aggregate limit must not be downloaded")
	var aggregate *AggregateBudgetExceededError
	assert.ErrorAs(t, results[2].Err, &aggregate)
}

func TestProcessFilesDetailed_FailedReservationDoesNotDropLaterSibling(t *testing.T) {
	p := newDetailedTestProcessor(t)
	var laterFetched atomic.Bool
	results := p.ProcessFilesDetailed(context.Background(), []IncomingFile{
		{
			Kind: FileTypePhoto, SourceID: "failed-large", Size: 15 * 1024 * 1024,
			Fetch: func(context.Context, int64) ([]byte, error) { return nil, errors.New("network error") },
		},
		{
			Kind: FileTypePhoto, SourceID: "later", Size: 10,
			Fetch: func(context.Context, int64) ([]byte, error) {
				laterFetched.Store(true)
				return []byte("0123456789"), nil
			},
		},
	}, "user", "")

	require.Len(t, results, 2)
	assert.Equal(t, ProcessFileFailed, results[0].Status)
	assert.Equal(t, ProcessFileAvailable, results[1].Status)
	assert.True(t, laterFetched.Load())
}

func TestProcessFilesDetailed_DiscardsBytesBeyondReservation(t *testing.T) {
	p := newDetailedTestProcessor(t)
	results := p.ProcessFilesDetailed(context.Background(), []IncomingFile{{
		Kind: FileTypePhoto, SourceID: "mismatch", Size: 2,
		Fetch: func(context.Context, int64) ([]byte, error) { return []byte("three"), nil },
	}}, "user", "")

	require.Len(t, results, 1)
	assert.Equal(t, ProcessFileOmittedLimit, results[0].Status)
	assert.Nil(t, results[0].Processed)
	var mismatch *DeclaredFileSizeExceededError
	assert.ErrorAs(t, results[0].Err, &mismatch)
	assert.Equal(t, int64(2), mismatch.Declared)
	assert.Equal(t, int64(5), mismatch.Actual)
}

func TestProcessFilesDetailed_FalselySmallDeclarationsCannotEscapeFetchReservations(t *testing.T) {
	p := newDetailedTestProcessor(t)
	const count = 3
	const declaredSize int64 = 1
	const actualSourceSize int64 = 20 * 1024 * 1024

	var activeReserved atomic.Int64
	var maxActiveReserved atomic.Int64
	started := make(chan int64, count)
	release := make(chan struct{})
	incoming := make([]IncomingFile, 0, count)
	for i := 0; i < count; i++ {
		incoming = append(incoming, IncomingFile{
			Kind: FileTypePhoto, SourceID: string(rune('a' + i)), Origin: telegramRichOrigin, Size: declaredSize,
			Fetch: func(_ context.Context, maxBytes int64) ([]byte, error) {
				current := activeReserved.Add(maxBytes)
				for {
					previous := maxActiveReserved.Load()
					if current <= previous || maxActiveReserved.CompareAndSwap(previous, current) {
						break
					}
				}
				started <- maxBytes
				<-release
				activeReserved.Add(-maxBytes)
				if actualSourceSize > maxBytes {
					return nil, telegram.ErrFileDownloadTooLarge
				}
				return make([]byte, maxBytes), nil
			},
		})
	}

	done := make(chan []ProcessFileResult, 1)
	go func() {
		done <- p.ProcessFilesDetailed(context.Background(), incoming, "user", "")
	}()

	for i := 0; i < count; i++ {
		select {
		case limit := <-started:
			assert.Equal(t, declaredSize, limit, "each hostile source must receive only its reservation")
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for bounded rich fetch")
		}
	}
	assert.LessOrEqual(t, maxActiveReserved.Load(), MaxFileSize())
	close(release)

	select {
	case results := <-done:
		require.Len(t, results, count)
		for _, result := range results {
			assert.Equal(t, ProcessFileOmittedLimit, result.Status)
			assert.Zero(t, result.DownloadedSize)
			var mismatch *DeclaredFileSizeExceededError
			require.ErrorAs(t, result.Err, &mismatch)
			assert.Equal(t, declaredSize, mismatch.Declared)
			assert.Equal(t, declaredSize+1, mismatch.Actual)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded rich results")
	}
}

func TestProcessFilesDetailed_BoundsFetchConcurrency(t *testing.T) {
	p := newDetailedTestProcessor(t)
	const count = 7
	var active atomic.Int32
	var maxActive atomic.Int32
	started := make(chan struct{}, count)
	release := make(chan struct{})
	incoming := make([]IncomingFile, 0, count)
	for i := 0; i < count; i++ {
		incoming = append(incoming, IncomingFile{
			Kind: FileTypePhoto, SourceID: string(rune('a' + i)), Size: 1,
			Fetch: func(context.Context, int64) ([]byte, error) {
				current := active.Add(1)
				for {
					previous := maxActive.Load()
					if current <= previous || maxActive.CompareAndSwap(previous, current) {
						break
					}
				}
				started <- struct{}{}
				<-release
				active.Add(-1)
				return []byte{'x'}, nil
			},
		})
	}

	done := make(chan []ProcessFileResult, 1)
	go func() {
		done <- p.ProcessFilesDetailed(context.Background(), incoming, "user", "")
	}()

	for i := 0; i < maxConcurrentFileFetches; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for initial fetch workers")
		}
	}
	select {
	case <-started:
		t.Fatal("more than three fetches started before a worker was released")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)

	select {
	case results := <-done:
		require.Len(t, results, count)
		for _, result := range results {
			assert.Equal(t, ProcessFileAvailable, result.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for detailed processing")
	}
	assert.LessOrEqual(t, maxActive.Load(), int32(maxConcurrentFileFetches))
}

func TestProcessFiles_LegacyWrapperStillAbortsOnValidation(t *testing.T) {
	p := newDetailedTestProcessor(t)
	var fetched atomic.Bool
	processed, err := p.ProcessFiles(context.Background(), []IncomingFile{
		{
			Kind: FileTypeAnimation, SourceID: "gif", FileName: "clip.gif", MIME: "image/gif", Size: 3,
			Fetch: func(context.Context, int64) ([]byte, error) { return []byte("gif"), nil },
		},
		{
			Kind: FileTypePhoto, SourceID: "later", Size: 1,
			Fetch: func(context.Context, int64) ([]byte, error) {
				fetched.Store(true)
				return []byte{'x'}, nil
			},
		},
	}, "user", "")

	assert.Error(t, err)
	assert.Nil(t, processed)
	assert.False(t, fetched.Load())
}

func TestProcessFiles_LegacyWrapperSkipsDownloadFailureAndContinues(t *testing.T) {
	p := newDetailedTestProcessor(t)
	processed, err := p.ProcessFiles(context.Background(), []IncomingFile{
		{
			Kind: FileTypePhoto, SourceID: "failed", Size: 1,
			Fetch: func(context.Context, int64) ([]byte, error) { return nil, errors.New("download failed") },
		},
		{
			Kind: FileTypePhoto, SourceID: "later", Size: 1,
			Fetch: func(context.Context, int64) ([]byte, error) { return []byte{'x'}, nil },
		},
	}, "user", "")

	require.NoError(t, err)
	require.Len(t, processed, 1)
	assert.Equal(t, "later", processed[0].FileID)
}
