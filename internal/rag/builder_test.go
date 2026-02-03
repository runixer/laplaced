package rag

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceBuilder_RequiredDeps(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*ServiceBuilder)
		wantErr     bool
		errContains string
	}{
		{
			name:        "no deps set",
			setup:       func(b *ServiceBuilder) {},
			wantErr:     true,
			errContains: "logger not set",
		},
		{
			name: "only logger set",
			setup: func(b *ServiceBuilder) {
				b.WithLogger(testutil.TestLogger())
			},
			wantErr:     true,
			errContains: "config not set",
		},
		{
			name: "logger and config set",
			setup: func(b *ServiceBuilder) {
				b.WithLogger(testutil.TestLogger())
				b.WithConfig(testutil.TestConfig())
			},
			wantErr:     true,
			errContains: "openrouter client not set",
		},
		{
			name: "nil logger parameter",
			setup: func(b *ServiceBuilder) {
				b.WithLogger(nil)
			},
			wantErr:     true,
			errContains: "logger required",
		},
		{
			name: "nil config parameter",
			setup: func(b *ServiceBuilder) {
				b.WithLogger(testutil.TestLogger())
				b.WithConfig(nil)
			},
			wantErr:     true,
			errContains: "config required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewServiceBuilder()
			tt.setup(builder)

			svc, err := builder.Build()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, svc)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, svc)
			}
		})
	}
}

func TestServiceBuilder_ValidBuild(t *testing.T) {
	logger := testutil.TestLogger()
	cfg := testutil.TestConfig()
	mockClient := &testutil.MockOpenRouterClient{}
	mockStore := &testutil.MockStorage{}
	mockMemory := &mockMemoryService{}
	translator := testutil.TestTranslator(t)

	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(mockMemory).
		WithTranslator(translator).
		Build()

	require.NoError(t, err)
	assert.NotNil(t, svc)

	// Verify required fields are set
	assert.Same(t, cfg, svc.cfg)
	assert.Same(t, mockClient, svc.client)
	assert.Same(t, mockStore, svc.topicRepo)
	assert.Same(t, mockStore, svc.factRepo)
	assert.Same(t, mockMemory, svc.memoryService)
	assert.Same(t, translator, svc.translator)

	// Verify internal state is initialized
	assert.NotNil(t, svc.topicVectors)
	assert.NotNil(t, svc.factVectors)
	assert.NotNil(t, svc.peopleVectors)
	assert.NotNil(t, svc.artifactVectors)
	assert.NotNil(t, svc.stopChan)
	assert.NotNil(t, svc.consolidationTrigger)
}

func TestServiceBuilder_OptionalDeps(t *testing.T) {
	logger := testutil.TestLogger()
	cfg := testutil.TestConfig()
	mockClient := &testutil.MockOpenRouterClient{}
	mockStore := &testutil.MockStorage{}
	mockMemory := &mockMemoryService{}
	translator := testutil.TestTranslator(t)

	// Create a mock agent
	mockAgent := &mockAgent{}

	t.Run("service works without optional agents", func(t *testing.T) {
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithOpenRouterClient(mockClient).
			WithTopicRepository(mockStore).
			WithFactRepository(mockStore).
			WithFactHistoryRepository(mockStore).
			WithMessageRepository(mockStore).
			WithMaintenanceRepository(mockStore).
			WithMemoryService(mockMemory).
			WithTranslator(translator).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, svc)

		// Optional agents should be nil
		assert.Nil(t, svc.enricherAgent)
		assert.Nil(t, svc.splitterAgent)
		assert.Nil(t, svc.mergerAgent)
		assert.Nil(t, svc.rerankerAgent)
		assert.Nil(t, svc.extractorAgent)
	})

	t.Run("service works with optional agents set", func(t *testing.T) {
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithOpenRouterClient(mockClient).
			WithTopicRepository(mockStore).
			WithFactRepository(mockStore).
			WithFactHistoryRepository(mockStore).
			WithMessageRepository(mockStore).
			WithMaintenanceRepository(mockStore).
			WithMemoryService(mockMemory).
			WithTranslator(translator).
			WithEnricher(mockAgent).
			WithSplitter(mockAgent).
			WithMerger(mockAgent).
			WithReranker(mockAgent).
			WithExtractor(mockAgent).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, svc)

		// Optional agents should be set
		assert.Same(t, mockAgent, svc.enricherAgent)
		assert.Same(t, mockAgent, svc.splitterAgent)
		assert.Same(t, mockAgent, svc.mergerAgent)
		assert.Same(t, mockAgent, svc.rerankerAgent)
		assert.Same(t, mockAgent, svc.extractorAgent)
	})

	t.Run("service works without optional repos", func(t *testing.T) {
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithOpenRouterClient(mockClient).
			WithTopicRepository(mockStore).
			WithFactRepository(mockStore).
			WithFactHistoryRepository(mockStore).
			WithMessageRepository(mockStore).
			WithMaintenanceRepository(mockStore).
			WithMemoryService(mockMemory).
			WithTranslator(translator).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, svc)

		// Optional repos should be nil
		assert.Nil(t, svc.peopleRepo)
		assert.Nil(t, svc.artifactRepo)
		assert.Nil(t, svc.contextService)
	})

	t.Run("service works with optional repos set", func(t *testing.T) {
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithOpenRouterClient(mockClient).
			WithTopicRepository(mockStore).
			WithFactRepository(mockStore).
			WithFactHistoryRepository(mockStore).
			WithMessageRepository(mockStore).
			WithMaintenanceRepository(mockStore).
			WithMemoryService(mockMemory).
			WithTranslator(translator).
			WithPeopleRepository(mockStore).
			WithArtifactRepository(mockStore).
			Build()

		require.NoError(t, err)
		assert.NotNil(t, svc)

		// Optional repos should be set
		assert.Same(t, mockStore, svc.peopleRepo)
		assert.Same(t, mockStore, svc.artifactRepo)
	})
}

func TestServiceBuilder_FluentChaining(t *testing.T) {
	logger := testutil.TestLogger()
	cfg := testutil.TestConfig()
	mockClient := &testutil.MockOpenRouterClient{}
	mockStore := &testutil.MockStorage{}
	mockMemory := &mockMemoryService{}
	translator := testutil.TestTranslator(t)
	mockAgent := &mockAgent{}

	// Test that all With* methods return the builder for chaining
	builder := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(mockMemory).
		WithTranslator(translator).
		WithEnricher(mockAgent).
		WithSplitter(mockAgent).
		WithMerger(mockAgent).
		WithReranker(mockAgent).
		WithExtractor(mockAgent).
		WithPeopleRepository(mockStore).
		WithArtifactRepository(mockStore)

	// The builder should not be nil after chaining
	assert.NotNil(t, builder)

	// And Build() should succeed
	svc, err := builder.Build()
	require.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestServiceBuilder_VectorMapsInitialized(t *testing.T) {
	logger := testutil.TestLogger()
	cfg := testutil.TestConfig()
	mockClient := &testutil.MockOpenRouterClient{}
	mockStore := &testutil.MockStorage{}
	mockMemory := &mockMemoryService{}
	translator := testutil.TestTranslator(t)

	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(mockMemory).
		WithTranslator(translator).
		Build()

	require.NoError(t, err)

	// Verify all vector maps are initialized (not nil)
	assert.NotNil(t, svc.topicVectors, "topicVectors should be initialized")
	assert.NotNil(t, svc.factVectors, "factVectors should be initialized")
	assert.NotNil(t, svc.peopleVectors, "peopleVectors should be initialized")
	assert.NotNil(t, svc.artifactVectors, "artifactVectors should be initialized")

	// Verify they are empty maps
	assert.Equal(t, 0, len(svc.topicVectors), "topicVectors should be empty")
	assert.Equal(t, 0, len(svc.factVectors), "factVectors should be empty")
	assert.Equal(t, 0, len(svc.peopleVectors), "peopleVectors should be empty")
	assert.Equal(t, 0, len(svc.artifactVectors), "artifactVectors should be empty")

	// Verify max IDs are initialized to 0
	assert.Equal(t, int64(0), svc.maxLoadedTopicID)
	assert.Equal(t, int64(0), svc.maxLoadedFactID)
	assert.Equal(t, int64(0), svc.maxLoadedPersonID)
	assert.Equal(t, int64(0), svc.maxLoadedArtifactID)
}

// mockAgent is a minimal mock implementation of agent.Agent for testing.
// LEGITIMATE: Kept inline because this is a simple stub used only for builder tests.
// Using testutil would be overkill for a 2-method interface.
type mockAgent struct{}

func (m *mockAgent) Type() agent.AgentType {
	return "mock"
}

func (m *mockAgent) Execute(_ context.Context, _ *agent.Request) (*agent.Response, error) {
	return &agent.Response{}, nil
}

// mockMemoryService is a minimal mock implementation of MemoryService for testing.
// LEGITIMATE: Kept inline because this is a simple stub used only for builder tests.
// Using testutil would be overkill for a 2-method interface.
type mockMemoryService struct{}

func (m *mockMemoryService) ProcessSession(_ context.Context, _ int64, _ []storage.Message, _ time.Time, _ int64) error {
	return nil
}

func (m *mockMemoryService) ProcessSessionWithStats(_ context.Context, _ int64, _ []storage.Message, _ time.Time, _ int64) (memory.FactStats, error) {
	return memory.FactStats{}, nil
}
