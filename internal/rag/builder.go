package rag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// MemoryService defines the interface for memory service operations used by RAG.
// This allows for easier testing and decoupling from the concrete implementation.
type MemoryService interface {
	ProcessSession(ctx context.Context, userID int64, messages []storage.Message, topicDate time.Time, topicID int64) error
	ProcessSessionWithStats(ctx context.Context, userID int64, messages []storage.Message, topicDate time.Time, topicID int64) (memory.FactStats, error)
}

// ServiceBuilder provides a fluent API for constructing RAG Service instances.
// All required dependencies must be set before calling Build().
// Optional dependencies (agents, repos) can be set via With* methods.
type ServiceBuilder struct {
	// Required dependencies
	logger          *slog.Logger
	cfg             *config.Config
	client          openrouter.Client
	topicRepo       storage.TopicRepository
	factRepo        storage.FactRepository
	factHistoryRepo storage.FactHistoryRepository
	msgRepo         storage.MessageRepository
	maintenanceRepo storage.MaintenanceRepository
	memoryService   MemoryService
	translator      *i18n.Translator

	// Optional dependencies
	agentLogger    *agentlog.Logger
	enricherAgent  agent.Agent
	splitterAgent  agent.Agent
	mergerAgent    agent.Agent
	rerankerAgent  agent.Agent
	extractorAgent agent.Agent
	peopleRepo     storage.PeopleRepository
	artifactRepo   storage.ArtifactRepository
	contextService *agent.ContextService

	// Validation errors accumulated during building
	errors []error
}

// NewServiceBuilder creates a new ServiceBuilder for fluent construction.
func NewServiceBuilder() *ServiceBuilder {
	return &ServiceBuilder{
		errors: make([]error, 0),
	}
}

// WithLogger sets the required logger dependency.
func (b *ServiceBuilder) WithLogger(l *slog.Logger) *ServiceBuilder {
	if l == nil {
		b.errors = append(b.errors, errors.New("logger required"))
	}
	b.logger = l
	return b
}

// WithConfig sets the required config dependency.
func (b *ServiceBuilder) WithConfig(c *config.Config) *ServiceBuilder {
	if c == nil {
		b.errors = append(b.errors, errors.New("config required"))
	}
	b.cfg = c
	return b
}

// WithOpenRouterClient sets the required OpenRouter client dependency.
func (b *ServiceBuilder) WithOpenRouterClient(c openrouter.Client) *ServiceBuilder {
	if c == nil {
		b.errors = append(b.errors, errors.New("openrouter client required"))
	}
	b.client = c
	return b
}

// WithTopicRepository sets the required topic repository dependency.
func (b *ServiceBuilder) WithTopicRepository(r storage.TopicRepository) *ServiceBuilder {
	if r == nil {
		b.errors = append(b.errors, errors.New("topic repository required"))
	}
	b.topicRepo = r
	return b
}

// WithFactRepository sets the required fact repository dependency.
func (b *ServiceBuilder) WithFactRepository(r storage.FactRepository) *ServiceBuilder {
	if r == nil {
		b.errors = append(b.errors, errors.New("fact repository required"))
	}
	b.factRepo = r
	return b
}

// WithFactHistoryRepository sets the required fact history repository dependency.
func (b *ServiceBuilder) WithFactHistoryRepository(r storage.FactHistoryRepository) *ServiceBuilder {
	if r == nil {
		b.errors = append(b.errors, errors.New("fact history repository required"))
	}
	b.factHistoryRepo = r
	return b
}

// WithMessageRepository sets the required message repository dependency.
func (b *ServiceBuilder) WithMessageRepository(r storage.MessageRepository) *ServiceBuilder {
	if r == nil {
		b.errors = append(b.errors, errors.New("message repository required"))
	}
	b.msgRepo = r
	return b
}

// WithMaintenanceRepository sets the required maintenance repository dependency.
func (b *ServiceBuilder) WithMaintenanceRepository(r storage.MaintenanceRepository) *ServiceBuilder {
	if r == nil {
		b.errors = append(b.errors, errors.New("maintenance repository required"))
	}
	b.maintenanceRepo = r
	return b
}

// WithMemoryService sets the required memory service dependency.
func (b *ServiceBuilder) WithMemoryService(s MemoryService) *ServiceBuilder {
	if s == nil {
		b.errors = append(b.errors, errors.New("memory service required"))
	}
	b.memoryService = s
	return b
}

// WithTranslator sets the required translator dependency.
func (b *ServiceBuilder) WithTranslator(t *i18n.Translator) *ServiceBuilder {
	if t == nil {
		b.errors = append(b.errors, errors.New("translator required"))
	}
	b.translator = t
	return b
}

// WithAgentLogger sets the optional agent logger dependency.
func (b *ServiceBuilder) WithAgentLogger(l *agentlog.Logger) *ServiceBuilder {
	b.agentLogger = l
	return b
}

// WithEnricher sets the optional enricher agent dependency.
func (b *ServiceBuilder) WithEnricher(a agent.Agent) *ServiceBuilder {
	b.enricherAgent = a
	return b
}

// WithSplitter sets the optional splitter agent dependency.
func (b *ServiceBuilder) WithSplitter(a agent.Agent) *ServiceBuilder {
	b.splitterAgent = a
	return b
}

// WithMerger sets the optional merger agent dependency.
func (b *ServiceBuilder) WithMerger(a agent.Agent) *ServiceBuilder {
	b.mergerAgent = a
	return b
}

// WithReranker sets the optional reranker agent dependency.
func (b *ServiceBuilder) WithReranker(a agent.Agent) *ServiceBuilder {
	b.rerankerAgent = a
	return b
}

// WithExtractor sets the optional extractor agent dependency.
func (b *ServiceBuilder) WithExtractor(a agent.Agent) *ServiceBuilder {
	b.extractorAgent = a
	return b
}

// WithPeopleRepository sets the optional people repository dependency.
func (b *ServiceBuilder) WithPeopleRepository(r storage.PeopleRepository) *ServiceBuilder {
	b.peopleRepo = r
	return b
}

// WithArtifactRepository sets the optional artifact repository dependency.
func (b *ServiceBuilder) WithArtifactRepository(r storage.ArtifactRepository) *ServiceBuilder {
	b.artifactRepo = r
	return b
}

// WithContextService sets the optional context service dependency.
func (b *ServiceBuilder) WithContextService(cs *agent.ContextService) *ServiceBuilder {
	b.contextService = cs
	return b
}

// Build validates all dependencies and creates the RAG Service.
// Returns an error if any required dependency is missing.
func (b *ServiceBuilder) Build() (*Service, error) {
	// Check for accumulated nil parameter errors
	if len(b.errors) > 0 {
		return nil, fmt.Errorf("builder validation failed: %v", b.errors)
	}

	// Validate required dependencies
	if b.logger == nil {
		return nil, errors.New("builder validation failed: logger not set")
	}
	if b.cfg == nil {
		return nil, errors.New("builder validation failed: config not set")
	}
	if b.client == nil {
		return nil, errors.New("builder validation failed: openrouter client not set")
	}
	if b.topicRepo == nil {
		return nil, errors.New("builder validation failed: topic repository not set")
	}
	if b.factRepo == nil {
		return nil, errors.New("builder validation failed: fact repository not set")
	}
	if b.factHistoryRepo == nil {
		return nil, errors.New("builder validation failed: fact history repository not set")
	}
	if b.msgRepo == nil {
		return nil, errors.New("builder validation failed: message repository not set")
	}
	if b.maintenanceRepo == nil {
		return nil, errors.New("builder validation failed: maintenance repository not set")
	}
	if b.memoryService == nil {
		return nil, errors.New("builder validation failed: memory service not set")
	}
	if b.translator == nil {
		return nil, errors.New("builder validation failed: translator not set")
	}

	// Create service instance
	svc := &Service{
		logger:               b.logger.With("component", "rag"),
		cfg:                  b.cfg,
		topicRepo:            b.topicRepo,
		factRepo:             b.factRepo,
		factHistoryRepo:      b.factHistoryRepo,
		msgRepo:              b.msgRepo,
		maintenanceRepo:      b.maintenanceRepo,
		peopleRepo:           b.peopleRepo,
		artifactRepo:         b.artifactRepo,
		client:               b.client,
		memoryService:        b.memoryService,
		translator:           b.translator,
		agentLogger:          b.agentLogger,
		enricherAgent:        b.enricherAgent,
		splitterAgent:        b.splitterAgent,
		mergerAgent:          b.mergerAgent,
		rerankerAgent:        b.rerankerAgent,
		extractorAgent:       b.extractorAgent,
		contextService:       b.contextService,
		topicVectors:         make(map[int64][]TopicVectorItem),
		factVectors:          make(map[int64][]FactVectorItem),
		peopleVectors:        make(map[int64][]PersonVectorItem),
		artifactVectors:      make(map[int64][]ArtifactVectorItem),
		maxLoadedTopicID:     0,
		maxLoadedFactID:      0,
		maxLoadedPersonID:    0,
		maxLoadedArtifactID:  0,
		stopChan:             make(chan struct{}),
		consolidationTrigger: make(chan struct{}, 1),
	}

	return svc, nil
}
