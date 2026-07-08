package bot

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// fakeFileStorage is a minimal in-memory files.Storage for delivery tests.
type fakeFileStorage struct {
	blobs map[string][]byte
}

func (f *fakeFileStorage) SaveFile(context.Context, storage.ScopeID, io.Reader, string) (*files.SavedFile, error) {
	return nil, nil
}
func (f *fakeFileStorage) ReadFile(_ context.Context, key string) ([]byte, error) {
	return f.blobs[key], nil
}
func (f *fakeFileStorage) DeleteFile(context.Context, string) error { return nil }

// recordingTransport captures SendMedia calls on top of the stub transport.
type recordingTransport struct {
	stubTransport
	media []OutgoingMedia
}

func (r *recordingTransport) SendMedia(_ context.Context, m OutgoingMedia) (string, error) {
	r.media = append(r.media, m)
	return "", nil
}

// TestDeliverGeneratedOnError verifies the imagegen-lost-on-error fix at the
// bot layer: a failed laplace turn that still produced images delivers them
// with the canned caption; turns without images fall through to the normal
// error reply (return false).
func TestDeliverGeneratedOnError(t *testing.T) {
	userID := storage.ScopeID("123")

	setup := func(t *testing.T) (*Bot, *testutil.MockStorage, *recordingTransport) {
		t.Helper()
		mockStore := new(testutil.MockStorage)
		transport := &recordingTransport{}
		b := &Bot{
			cfg:          testutil.TestConfig(),
			logger:       testutil.TestLogger(),
			translator:   testutil.TestTranslator(t),
			msgRepo:      mockStore,
			artifactRepo: mockStore,
			fileStorage:  &fakeFileStorage{blobs: map[string][]byte{"gen/cat.png": []byte("png-bytes")}},
			transport:    transport,
			renderer:     NewTelegramRenderer(testutil.TestLogger()),
		}
		return b, mockStore, transport
	}

	newPath := func(b *Bot) *responsePath {
		return &responsePath{bot: b, logger: b.logger, userID: userID, convID: "123"}
	}

	t.Run("nil response — nothing to deliver", func(t *testing.T) {
		b, _, transport := setup(t)
		delivered := b.deliverGeneratedOnError(context.Background(), newPath(b), userID, "123", "", "1", nil, b.logger)
		assert.False(t, delivered)
		assert.Empty(t, transport.media)
	})

	t.Run("no artifacts — nothing to deliver", func(t *testing.T) {
		b, _, transport := setup(t)
		delivered := b.deliverGeneratedOnError(context.Background(), newPath(b), userID, "123", "", "1",
			&laplace.Response{}, b.logger)
		assert.False(t, delivered)
		assert.Empty(t, transport.media)
	})

	t.Run("artifacts present — delivered with canned caption", func(t *testing.T) {
		b, mockStore, transport := setup(t)
		mockStore.On("GetArtifact", userID, int64(42)).Return(&storage.Artifact{
			ID: 42, UserID: userID, FilePath: "gen/cat.png",
			OriginalName: "cat.png", MimeType: "image/png",
		}, nil)
		mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
		mockStore.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 7}}, nil)
		mockStore.On("UpdateMessageID", userID, int64(42), int64(7)).Return(nil)

		delivered := b.deliverGeneratedOnError(context.Background(), newPath(b), userID, "123", "", "1",
			&laplace.Response{GeneratedArtifactIDs: []int64{42}}, b.logger)
		assert.True(t, delivered)

		require.Len(t, transport.media, 1)
		sent := transport.media[0]
		require.Len(t, sent.Items, 1)
		assert.Equal(t, []byte("png-bytes"), sent.Items[0].Data)
		assert.Equal(t, b.translator.Get(b.cfg.Bot.Language, "bot.image_delivered_text_failed"), sent.Caption)

		mockStore.AssertExpectations(t)
	})
}
