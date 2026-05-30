package agent

import (
	"context"
	"log/slog"
	"sort"
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
	InnerCircle string // Formatted <inner_circle> (Work_Inner + Family), or <channel_participants> for channels

	// IsChannel marks the scope as a multi-participant channel (Phase 6). Agents
	// use it to select channel-framed prompts; false for DMs.
	IsChannel bool

	// Session Context (v0.5 - nil until implemented)
	// LastSummary *storage.SessionSummary

	// Metadata
	Language string // "en" or "ru"
	LoadedAt time.Time
}

// maxChannelParticipantsInContext caps how many channel members are injected
// into the system prompt (most recently active first), keeping the context
// bounded on busy channels.
const maxChannelParticipantsInContext = 20

// ContextService loads and manages SharedContext.
type ContextService struct {
	factRepo   storage.FactRepository
	topicRepo  storage.TopicRepository
	peopleRepo storage.PeopleRepository
	scopeRepo  storage.ScopeRepository // channel-scope detection (Phase 6); nil on home
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

// SetScopeRepository sets the scope repository used to detect channel scopes
// (Phase 6). When nil, every scope is treated as a DM.
func (c *ContextService) SetScopeRepository(repo storage.ScopeRepository) {
	c.scopeRepo = repo
}

// isChannelScope reports whether userID is a channel scope, defaulting to false
// (DM) when the repo is absent or the lookup fails.
func (c *ContextService) isChannelScope(userID int64) bool {
	if c.scopeRepo == nil {
		return false
	}
	isCh, err := c.scopeRepo.IsChannelScope(userID)
	if err != nil {
		c.logger.Warn("channel-scope lookup failed", "user_id", userID, "error", err)
		return false
	}
	return isCh
}

// Load creates SharedContext for a user.
// Call once per request at the beginning of processing.
func (c *ContextService) Load(ctx context.Context, userID int64) *SharedContext {
	shared := &SharedContext{
		UserID:   userID,
		Language: c.cfg.Bot.Language,
		LoadedAt: time.Now(),
	}

	// A channel scope frames its profile/participants around the channel rather
	// than a single person (Phase 6). DMs (and the Telegram home path) keep the
	// user-centric framing unchanged.
	isChannel := c.isChannelScope(userID)
	shared.IsChannel = isChannel

	// Load profile facts
	if facts, err := c.factRepo.GetFacts(userID); err == nil {
		shared.ProfileFacts = storage.FilterProfileFacts(facts)
		if isChannel {
			shared.Profile = storage.FormatChannelProfile(shared.ProfileFacts)
		} else {
			shared.Profile = storage.FormatUserProfile(shared.ProfileFacts)
		}
	} else {
		c.logger.Warn("failed to load facts", "user_id", userID, "error", err)
		if isChannel {
			shared.Profile = "<channel_profile>\n</channel_profile>"
		} else {
			shared.Profile = "<user_profile>\n</user_profile>"
		}
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

	// Load people for the system prompt. In a channel the relevant set is the
	// active participants (the channel's members); in a DM it's the user's inner
	// circle (Work_Inner + Family). Both reuse the InnerCircle slot, tagged
	// distinctly so the model can tell them apart.
	if c.peopleRepo != nil {
		if isChannel {
			if people, err := c.getChannelParticipants(userID); err != nil {
				c.logger.Warn("failed to load channel participants", "user_id", userID, "error", err)
			} else if len(people) > 0 {
				shared.InnerCircle = storage.FormatPeople(people, storage.TagChannelParticipants)
			}
		} else {
			if people, err := c.getInnerCirclePeople(userID); err != nil {
				c.logger.Warn("failed to load inner circle people", "user_id", userID, "error", err)
			} else if len(people) > 0 {
				shared.InnerCircle = storage.FormatPeople(people, storage.TagInnerCircle)
			}
		}
	}

	if shared.InnerCircle == "" {
		if isChannel {
			shared.InnerCircle = "<channel_participants>\n</channel_participants>"
		} else {
			shared.InnerCircle = "<inner_circle>\n</inner_circle>"
		}
	}

	return shared
}

// getChannelParticipants returns the channel's most recently active members
// (People in the scope), newest first, capped at maxChannelParticipantsInContext.
func (c *ContextService) getChannelParticipants(userID int64) ([]storage.Person, error) {
	people, err := c.peopleRepo.GetPeople(userID)
	if err != nil {
		return nil, err
	}
	sort.Slice(people, func(i, j int) bool {
		return people[i].LastSeen.After(people[j].LastSeen)
	})
	if len(people) > maxChannelParticipantsInContext {
		people = people[:maxChannelParticipantsInContext]
	}
	return people, nil
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

// GetSharedContext extracts profile, recent topics, and inner circle from
// SharedContext (request or context.Context).
//
// Returns empty XML-tagged defaults if SharedContext is not available.
// Callers that need DB fallback should implement it themselves.
//
// This helper eliminates duplicated fallback logic across agents.
func GetSharedContext(ctx context.Context, req *Request) (profile, recentTopics, innerCircle string) {
	// Try request.Shared first
	if req != nil && req.Shared != nil {
		return req.Shared.Profile, req.Shared.RecentTopics, req.Shared.InnerCircle
	}
	// Try context.Context
	if shared := FromContext(ctx); shared != nil {
		return shared.Profile, shared.RecentTopics, shared.InnerCircle
	}
	// Empty defaults for tests/background jobs
	return "<user_profile>\n</user_profile>",
		"<recent_topics>\n</recent_topics>",
		"<inner_circle>\n</inner_circle>"
}

// GetUserID extracts user ID from request SharedContext or params.
// Returns 0 if not found.
func GetUserID(req *Request) int64 {
	if req != nil && req.Shared != nil {
		return req.Shared.UserID
	}
	if req != nil && req.Params != nil {
		if userID, ok := req.Params["user_id"].(int64); ok {
			return userID
		}
	}
	return 0
}
