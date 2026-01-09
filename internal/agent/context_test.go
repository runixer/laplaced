package agent

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestContextService_Load(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(*testutil.MockStorage)
		ragEnabled     bool
		expectedFacts  int
		expectedTopics bool
	}{
		{
			name: "loads profile facts",
			setupMocks: func(ms *testutil.MockStorage) {
				ms.On("GetFacts", testutil.TestUserID).Return(testutil.TestFacts(), nil)
				ms.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
					Return(storage.TopicResult{}, nil)
			},
			ragEnabled:     true,
			expectedFacts:  1, // Only identity facts pass filter
			expectedTopics: false,
		},
		{
			name: "loads recent topics when RAG enabled",
			setupMocks: func(ms *testutil.MockStorage) {
				ms.On("GetFacts", testutil.TestUserID).Return([]storage.Fact{}, nil)
				ms.On("GetTopicsExtended", mock.Anything, 3, 0, "created_at", "DESC").
					Return(storage.TopicResult{
						Data: []storage.TopicExtended{
							{
								Topic: storage.Topic{
									ID:        1,
									UserID:    testutil.TestUserID,
									Summary:   "Test topic",
									SizeChars: 1500,
									CreatedAt: time.Now(),
								},
								MessageCount: 5,
							},
						},
						TotalCount: 1,
					}, nil)
			},
			ragEnabled:     true,
			expectedFacts:  0,
			expectedTopics: true,
		},
		{
			name: "skips topics when RAG disabled",
			setupMocks: func(ms *testutil.MockStorage) {
				ms.On("GetFacts", testutil.TestUserID).Return([]storage.Fact{}, nil)
				// GetTopicsExtended should NOT be called
			},
			ragEnabled:     false,
			expectedFacts:  0,
			expectedTopics: false,
		},
		{
			name: "handles fact loading error gracefully",
			setupMocks: func(ms *testutil.MockStorage) {
				ms.On("GetFacts", testutil.TestUserID).Return([]storage.Fact(nil), assert.AnError)
				ms.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
					Return(storage.TopicResult{}, nil)
			},
			ragEnabled:     true,
			expectedFacts:  0,
			expectedTopics: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &testutil.MockStorage{}
			tt.setupMocks(mockStore)

			cfg := testutil.TestConfig()
			cfg.RAG.Enabled = tt.ragEnabled
			cfg.RAG.RecentTopicsInContext = 3

			svc := NewContextService(mockStore, mockStore, cfg, testutil.TestLogger())

			shared := svc.Load(context.Background(), testutil.TestUserID)

			assert.Equal(t, testutil.TestUserID, shared.UserID)
			assert.Equal(t, "en", shared.Language)
			assert.NotZero(t, shared.LoadedAt)
			assert.Len(t, shared.ProfileFacts, tt.expectedFacts)
			assert.Contains(t, shared.Profile, "<user_profile>")

			if tt.expectedTopics {
				assert.Contains(t, shared.RecentTopics, "Test topic")
			}

			mockStore.AssertExpectations(t)
		})
	}
}

func TestWithContext_FromContext(t *testing.T) {
	shared := &SharedContext{
		UserID:   123,
		Profile:  "test profile",
		Language: "ru",
		LoadedAt: time.Now(),
	}

	ctx := WithContext(context.Background(), shared)
	retrieved := FromContext(ctx)

	assert.Equal(t, shared, retrieved)
	assert.Equal(t, int64(123), retrieved.UserID)
	assert.Equal(t, "test profile", retrieved.Profile)
}

func TestFromContext_NotFound(t *testing.T) {
	ctx := context.Background()
	retrieved := FromContext(ctx)

	assert.Nil(t, retrieved)
}

func TestMustFromContext_Panics(t *testing.T) {
	ctx := context.Background()

	assert.Panics(t, func() {
		MustFromContext(ctx)
	})
}

func TestMustFromContext_Success(t *testing.T) {
	shared := &SharedContext{UserID: 123}
	ctx := WithContext(context.Background(), shared)

	assert.NotPanics(t, func() {
		retrieved := MustFromContext(ctx)
		assert.Equal(t, shared, retrieved)
	})
}
