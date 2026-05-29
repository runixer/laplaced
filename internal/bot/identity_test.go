package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/testutil"
)

func TestResolveScopeID_TelegramPassthrough(t *testing.T) {
	mockStore := new(testutil.MockStorage)

	id, err := resolveScopeID(mockStore, transportTelegram, "123456")
	require.NoError(t, err)
	assert.Equal(t, int64(123456), id)

	// Passthrough must not touch the scopes table on the home path.
	mockStore.AssertNotCalled(t, "ResolveScope")
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_TelegramInvalid(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	_, err := resolveScopeID(mockStore, transportTelegram, "not-an-int")
	assert.Error(t, err)
}

func TestResolveScopeID_TimeMintsSurrogate(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockStore.On("ResolveScope", transportTime, "user", "u26charstring").Return(int64(7), nil)

	id, err := resolveScopeID(mockStore, transportTime, "u26charstring")
	require.NoError(t, err)
	assert.Equal(t, int64(7), id)
	mockStore.AssertExpectations(t)
}
