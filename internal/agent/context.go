package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
)

// contextKey is the key type for storing SharedContext in context.Context.
type contextKey struct{}

// SharedContext holds user data shared across all agents.
// Loaded once per request to ensure all agents see the same data.
type SharedContext struct {
	UserID int64

	// Profile (always loaded)
	Profile      string         // Formatted <user_profile> for prompts
	ProfileFacts []storage.Fact // Raw facts (for custom formatting)

	// Topics
	RecentTopics string // Formatted <recent_topics>

	// Social Graph (v0.5.1)
	InnerCircle string // Formatted <inner_circle> (Work_Inner + Family)

	// Session Context (v0.5 - nil until implemented)
	// LastSummary *storage.SessionSummary

	// Metadata
	Language string // "en" or "ru"
	LoadedAt time.Time
}

// ContextService loads and manages SharedContext.
type ContextService struct {
	factRepo   storage.FactRepository
	topicRepo  storage.TopicRepository
	peopleRepo storage.PeopleRepository
	cfg        *config.Config
	logger     *slog.Logger
}

// NewContextService creates a new ContextService.
func NewContextService(
	factRepo storage.FactRepository,
	topicRepo storage.TopicRepository,
	cfg *config.Config,
	logger *slog.Logger,
) *ContextService {
	return &ContextService{
		factRepo:  factRepo,
		topicRepo: topicRepo,
		cfg:       cfg,
		logger:    logger.With("component", "context_service"),
	}
}

// SetPeopleRepository sets the people repository for loading inner circle.
func (c *ContextService) SetPeopleRepository(repo storage.PeopleRepository) {
	c.peopleRepo = repo
}

// Load creates SharedContext for a user.
// Call once per request at the beginning of processing.
func (c *ContextService) Load(ctx context.Context, userID int64) *SharedContext {
	shared := &SharedContext{
		UserID:   userID,
		Language: c.cfg.Bot.Language,
		LoadedAt: time.Now(),
	}

	// Load profile facts
	if facts, err := c.factRepo.GetFacts(userID); err == nil {
		shared.ProfileFacts = storage.FilterProfileFacts(facts)
		shared.Profile = storage.FormatUserProfile(shared.ProfileFacts)
	} else {
		c.logger.Warn("failed to load facts", "user_id", userID, "error", err)
		shared.Profile = "<user_profile>\n</user_profile>"
	}

	// Load recent topics
	if c.cfg.RAG.Enabled {
		recentTopicsCount := c.cfg.RAG.GetRecentTopicsInContext()
		if recentTopicsCount > 0 {
			topics, err := c.getRecentTopics(userID, recentTopicsCount)
			if err != nil {
				c.logger.Warn("failed to load recent topics", "user_id", userID, "error", err)
			} else {
				shared.RecentTopics = storage.FormatRecentTopics(topics)
			}
		}
	}

	if shared.RecentTopics == "" {
		shared.RecentTopics = "<recent_topics>\n</recent_topics>"
	}

	// Load inner circle people (v0.5.1)
	if c.peopleRepo != nil {
		people, err := c.getInnerCirclePeople(userID)
		if err != nil {
			c.logger.Warn("failed to load inner circle people", "user_id", userID, "error", err)
		} else if len(people) > 0 {
			shared.InnerCircle = storage.FormatPeople(people, storage.TagInnerCircle)
		}
	}

	if shared.InnerCircle == "" {
		shared.InnerCircle = "<inner_circle>\n</inner_circle>"
	}

	return shared
}

// getInnerCirclePeople returns people from Work_Inner and Family circles.
func (c *ContextService) getInnerCirclePeople(userID int64) ([]storage.Person, error) {
	people, err := c.peopleRepo.GetPeople(userID)
	if err != nil {
		return nil, err
	}

	// Filter to inner circles only
	var innerCircle []storage.Person
	for _, p := range people {
		if p.Circle == "Work_Inner" || p.Circle == "Family" {
			innerCircle = append(innerCircle, p)
		}
	}

	return innerCircle, nil
}

// getRecentTopics returns the N most recent topics for a user with message counts.
func (c *ContextService) getRecentTopics(userID int64, limit int) ([]storage.TopicExtended, error) {
	if limit <= 0 {
		return nil, nil
	}
	filter := storage.TopicFilter{UserID: userID}
	result, err := c.topicRepo.GetTopicsExtended(filter, limit, 0, "created_at", "DESC")
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

// WithContext injects SharedContext into context.Context.
func WithContext(ctx context.Context, shared *SharedContext) context.Context {
	return context.WithValue(ctx, contextKey{}, shared)
}

// FromContext extracts SharedContext from context.Context.
// Returns nil if not found.
func FromContext(ctx context.Context) *SharedContext {
	if shared, ok := ctx.Value(contextKey{}).(*SharedContext); ok {
		return shared
	}
	return nil
}

// MustFromContext extracts SharedContext from context.Context.
// Panics if not found.
func MustFromContext(ctx context.Context) *SharedContext {
	shared := FromContext(ctx)
	if shared == nil {
		panic("agent: SharedContext not found in context")
	}
	return shared
}
