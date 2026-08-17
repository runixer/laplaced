package tools

import (
	"context"
	"testing"

	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newArtifactSendTestExecutor(store *testutil.MockStorage) *ToolExecutor {
	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "generate_image"}}
	exec := NewToolExecutor(nil, store, store, cfg, testutil.TestLogger())
	exec.SetArtifactRepository(store)
	return exec
}

func TestPerformSendArtifactsStagesTrustedSelectionInOrder(t *testing.T) {
	store := new(testutil.MockStorage)
	exec := newArtifactSendTestExecutor(store)
	userID := storage.ScopeID("scope")
	store.On("GetArtifact", userID, int64(22)).Return(&storage.Artifact{ID: 22, UserID: userID, MimeType: "image/png"}, nil).Once()
	store.On("GetArtifact", userID, int64(11)).Return(&storage.Artifact{ID: 11, UserID: userID, MimeType: "application/pdf"}, nil).Once()

	result, err := exec.ExecuteToolCall(context.Background(), CallContext{
		UserID: userID, ArtifactDeliveryEnabled: true, TrustedArtifactIDs: []int64{11, 22},
	}, "send_artifacts", `{"items":[{"artifact_id":22,"mode":"preview_and_original"},{"artifact_id":11,"mode":"original"}]}`)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []artifactdelivery.Selected{
		{ArtifactID: 22, Mode: artifactdelivery.ModePreviewAndOriginal},
		{ArtifactID: 11, Mode: artifactdelivery.ModeOriginal},
	}, result.SelectedArtifacts)
	assert.NotContains(t, result.Content, "22")
	assert.NotContains(t, result.Content, "11")
	store.AssertExpectations(t)
}

func TestPerformSendArtifactsFailsClosedOutsidePrivateCapability(t *testing.T) {
	store := new(testutil.MockStorage)
	exec := newArtifactSendTestExecutor(store)
	result, err := exec.ExecuteToolCall(context.Background(), CallContext{
		UserID: "scope", TrustedArtifactIDs: []int64{11},
	}, "send_artifacts", `{"items":[{"artifact_id":11,"mode":"original"}]}`)
	require.NoError(t, err)
	assert.Empty(t, result.SelectedArtifacts)
	assert.Contains(t, result.Content, "UNAVAILABLE")
	store.AssertNotCalled(t, "GetArtifact", mock.Anything, mock.Anything)
}

func TestPerformSendArtifactsRejectsForgedAndNonImagePreview(t *testing.T) {
	t.Run("forged inventory id", func(t *testing.T) {
		store := new(testutil.MockStorage)
		exec := newArtifactSendTestExecutor(store)
		result, err := exec.ExecuteToolCall(context.Background(), CallContext{
			UserID: "scope", ArtifactDeliveryEnabled: true, TrustedArtifactIDs: []int64{11},
		}, "send_artifacts", `{"items":[{"artifact_id":99,"mode":"original"}]}`)
		require.NoError(t, err)
		assert.Empty(t, result.SelectedArtifacts)
		assert.Contains(t, result.Content, "REJECTED")
		store.AssertNotCalled(t, "GetArtifact", mock.Anything, mock.Anything)
	})

	t.Run("pdf preview", func(t *testing.T) {
		store := new(testutil.MockStorage)
		exec := newArtifactSendTestExecutor(store)
		store.On("GetArtifact", storage.ScopeID("scope"), int64(11)).Return(&storage.Artifact{
			ID: 11, UserID: "scope", MimeType: "application/pdf",
		}, nil)
		_, err := exec.ExecuteToolCall(context.Background(), CallContext{
			UserID: "scope", ArtifactDeliveryEnabled: true, TrustedArtifactIDs: []int64{11},
		}, "send_artifacts", `{"items":[{"artifact_id":11,"mode":"preview"}]}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not an image")
		assert.NotContains(t, err.Error(), "11")
	})
}

func TestPerformSendArtifactsRejectsDuplicateAndUnknownFields(t *testing.T) {
	store := new(testutil.MockStorage)
	exec := newArtifactSendTestExecutor(store)
	store.On("GetArtifact", storage.ScopeID("scope"), int64(11)).Return(&storage.Artifact{
		ID: 11, UserID: "scope", MimeType: "image/jpeg",
	}, nil).Maybe()
	ctx := CallContext{UserID: "scope", ArtifactDeliveryEnabled: true, TrustedArtifactIDs: []int64{11}}
	_, err := exec.ExecuteToolCall(context.Background(), ctx, "send_artifacts",
		`{"items":[{"artifact_id":11,"mode":"preview"},{"artifact_id":11,"mode":"original"}]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicates")
	assert.NotContains(t, err.Error(), "11")

	_, err = exec.ExecuteToolCall(context.Background(), ctx, "send_artifacts",
		`{"items":[{"artifact_id":11,"mode":"preview","path":"/tmp/x"}]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}
