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

func TestContextService_SetPeopleRepository(t *testing.T) {
	mockStore := &testutil.MockStorage{}
	cfg := testutil.TestConfig()
	svc := NewContextService(mockStore, mockStore, cfg, testutil.TestLogger())

	// Initially nil - inner circle won't be loaded
	mockStore.On("GetFacts", testutil.TestUserID).Return([]storage.Fact{}, nil)
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{}, nil)
	shared := svc.Load(context.Background(), testutil.TestUserID)
	assert.Equal(t, "<inner_circle>\n</inner_circle>", shared.InnerCircle)

	// Set repository
	mockStore.On("GetPeople", testutil.TestUserID).Return([]storage.Person{
		{ID: 1, UserID: testutil.TestUserID, DisplayName: "Alice", Circle: "Work_Inner"},
		{ID: 2, UserID: testutil.TestUserID, DisplayName: "Bob", Circle: "Work_Outer"},
		{ID: 3, UserID: testutil.TestUserID, DisplayName: "Charlie", Circle: "Family"},
	}, nil)
	mockStore.On("GetFacts", testutil.TestUserID).Return([]storage.Fact{}, nil)
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{}, nil)

	svc.SetPeopleRepository(mockStore)

	// Now inner circle should be loaded
	shared = svc.Load(context.Background(), testutil.TestUserID)
	assert.Contains(t, shared.InnerCircle, "Alice")
	assert.Contains(t, shared.InnerCircle, "Charlie")
	assert.NotContains(t, shared.InnerCircle, "Bob") // Not inner circle

	mockStore.AssertExpectations(t)
}

func TestContextService_getInnerCirclePeople(t *testing.T) {
	tests := []struct {
		name          string
		people        []storage.Person
		expectedCount int
		expectedNames []string
	}{
		{
			name: "filters Work_Inner and Family",
			people: []storage.Person{
				{ID: 1, DisplayName: "Alice", Circle: "Work_Inner"},
				{ID: 2, DisplayName: "Bob", Circle: "Work_Outer"},
				{ID: 3, DisplayName: "Charlie", Circle: "Family"},
				{ID: 4, DisplayName: "David", Circle: "Family_Outer"},
			},
			expectedCount: 2,
			expectedNames: []string{"Alice", "Charlie"},
		},
		{
			name:          "empty list",
			people:        []storage.Person{},
			expectedCount: 0,
			expectedNames: nil,
		},
		{
			name: "all inner circle",
			people: []storage.Person{
				{ID: 1, DisplayName: "Alice", Circle: "Work_Inner"},
				{ID: 2, DisplayName: "Bob", Circle: "Family"},
			},
			expectedCount: 2,
			expectedNames: []string{"Alice", "Bob"},
		},
		{
			name: "no inner circle people",
			people: []storage.Person{
				{ID: 1, DisplayName: "Alice", Circle: "Work_Outer"},
				{ID: 2, DisplayName: "Bob", Circle: "Family_Outer"},
			},
			expectedCount: 0,
			expectedNames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := &testutil.MockStorage{}
			mockStore.On("GetPeople", testutil.TestUserID).Return(tt.people, nil)

			cfg := testutil.TestConfig()
			svc := NewContextService(mockStore, mockStore, cfg, testutil.TestLogger())
			svc.SetPeopleRepository(mockStore)

			people, err := svc.getInnerCirclePeople(testutil.TestUserID)
			assert.NoError(t, err)
			assert.Len(t, people, tt.expectedCount)

			if tt.expectedNames != nil {
				names := make([]string, len(people))
				for i, p := range people {
					names[i] = p.DisplayName
				}
				assert.Equal(t, tt.expectedNames, names)
			}

			mockStore.AssertExpectations(t)
		})
	}
}

func TestContextService_getInnerCirclePeople_Error(t *testing.T) {
	mockStore := &testutil.MockStorage{}
	mockStore.On("GetPeople", testutil.TestUserID).Return([]storage.Person(nil), assert.AnError)

	cfg := testutil.TestConfig()
	svc := NewContextService(mockStore, mockStore, cfg, testutil.TestLogger())
	svc.SetPeopleRepository(mockStore)

	_, err := svc.getInnerCirclePeople(testutil.TestUserID)
	assert.Error(t, err)

	mockStore.AssertExpectations(t)
}

func TestGetSharedContext(t *testing.T) {
	tests := []struct {
		name             string
		req              *Request
		ctx              context.Context
		wantProfile      string
		wantRecentTopics string
		wantInnerCircle  string
	}{
		{
			name: "from request.Shared",
			req: &Request{
				Shared: &SharedContext{
					Profile:      "<profile>user</profile>",
					RecentTopics: "<topics>recent</topics>",
					InnerCircle:  "<circle>inner</circle>",
				},
			},
			ctx:              context.Background(),
			wantProfile:      "<profile>user</profile>",
			wantRecentTopics: "<topics>recent</topics>",
			wantInnerCircle:  "<circle>inner</circle>",
		},
		{
			name: "from context.Context",
			req:  nil,
			ctx: WithContext(context.Background(), &SharedContext{
				Profile:      "<profile>ctx</profile>",
				RecentTopics: "<topics>ctx</topics>",
				InnerCircle:  "<circle>ctx</circle>",
			}),
			wantProfile:      "<profile>ctx</profile>",
			wantRecentTopics: "<topics>ctx</topics>",
			wantInnerCircle:  "<circle>ctx</circle>",
		},
		{
			name: "request.Shared takes priority over context",
			req: &Request{
				Shared: &SharedContext{
					Profile:      "<profile>req</profile>",
					RecentTopics: "",
					InnerCircle:  "",
				},
			},
			ctx: WithContext(context.Background(), &SharedContext{
				Profile:      "<profile>ctx</profile>",
				RecentTopics: "<topics>ctx</topics>",
				InnerCircle:  "<circle>ctx</circle>",
			}),
			wantProfile:      "<profile>req</profile>",
			wantRecentTopics: "",
			wantInnerCircle:  "",
		},
		{
			name:             "empty defaults when no context",
			req:              nil,
			ctx:              context.Background(),
			wantProfile:      "<user_profile>\n</user_profile>",
			wantRecentTopics: "<recent_topics>\n</recent_topics>",
			wantInnerCircle:  "<inner_circle>\n</inner_circle>",
		},
		{
			name: "nil request with context",
			req:  nil,
			ctx: WithContext(context.Background(), &SharedContext{
				Profile:      "<profile>ctx</profile>",
				RecentTopics: "<topics>ctx</topics>",
				InnerCircle:  "<circle>ctx</circle>",
			}),
			wantProfile:      "<profile>ctx</profile>",
			wantRecentTopics: "<topics>ctx</topics>",
			wantInnerCircle:  "<circle>ctx</circle>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, topics, circle := GetSharedContext(tt.ctx, tt.req)

			assert.Equal(t, tt.wantProfile, profile)
			assert.Equal(t, tt.wantRecentTopics, topics)
			assert.Equal(t, tt.wantInnerCircle, circle)
		})
	}
}

func TestGetUserID(t *testing.T) {
	tests := []struct {
		name   string
		req    *Request
		wantID int64
	}{
		{
			name: "from request.Shared.UserID",
			req: &Request{
				Shared: &SharedContext{UserID: 123},
			},
			wantID: 123,
		},
		{
			name: "from request.Params user_id",
			req: &Request{
				Params: map[string]any{"user_id": int64(456)},
			},
			wantID: 456,
		},
		{
			name: "request.Shared takes priority over params",
			req: &Request{
				Shared: &SharedContext{UserID: 123},
				Params: map[string]any{"user_id": int64(456)},
			},
			wantID: 123,
		},
		{
			name:   "nil request returns 0",
			req:    nil,
			wantID: 0,
		},
		{
			name: "request with nil Shared and no user_id param",
			req: &Request{
				Shared: nil,
				Params: map[string]any{"other": "value"},
			},
			wantID: 0,
		},
		{
			name: "user_id param with wrong type returns 0",
			req: &Request{
				Params: map[string]any{"user_id": "not an int64"},
			},
			wantID: 0,
		},
		{
			name: "user_id param with int returns 0 (type mismatch)",
			req: &Request{
				Params: map[string]any{"user_id": 123},
			},
			wantID: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID := GetUserID(tt.req)
			assert.Equal(t, tt.wantID, gotID)
		})
	}
}

func TestContextService_getRecentTopics_LimitZero(t *testing.T) {
	mockStore := &testutil.MockStorage{}
	cfg := testutil.TestConfig()
	svc := NewContextService(mockStore, mockStore, cfg, testutil.TestLogger())

	// limit <= 0 should return nil without calling repo
	topics, err := svc.getRecentTopics(testutil.TestUserID, 0)
	assert.NoError(t, err)
	assert.Nil(t, topics)

	topics, err = svc.getRecentTopics(testutil.TestUserID, -1)
	assert.NoError(t, err)
	assert.Nil(t, topics)
}
