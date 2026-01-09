// Package prompts provides typed parameter structs for agent prompt templates.
// These structs are used with i18n.Translator.GetTemplate() to provide
// compile-time safety and IDE autocomplete for prompt parameters.
//
// Migration guide: Replace fmt.Sprintf positional args with struct fields.
// Example:
//
//	// Before: translator.Get(lang, "rag.enrichment_system_prompt", date, profile, topics)
//	// After:  translator.GetTemplate(lang, "rag.enrichment_system_prompt", prompts.EnricherParams{...})
package prompts

// EnricherParams for rag.enrichment_system_prompt template.
// The Enricher agent analyzes user queries and formulates search queries
// for vector retrieval of relevant conversation history.
type EnricherParams struct {
	Date         string // Current date for time-aware queries
	Profile      string // Formatted <user_profile> block
	RecentTopics string // Formatted <recent_topics> block
}

// EnricherUserParams for rag.enrichment_user_prompt template.
type EnricherUserParams struct {
	History string // Recent conversation history
	Query   string // Current user query
}

// SplitterParams for rag.topic_extraction_prompt template.
// The Splitter agent breaks down conversation logs into logical topics.
type SplitterParams struct {
	Profile      string // Formatted <user_profile> block
	RecentTopics string // Formatted <recent_topics> block
	Goal         string // Optional goal section (e.g., topic_extraction_goal_split for large topics)
}

// MergerParams for rag.topic_consolidation_system_prompt template.
// The Merger agent determines whether consecutive topics should be merged.
type MergerParams struct {
	Profile      string // Formatted <user_profile> block
	RecentTopics string // Formatted <recent_topics> block
}

// MergerUserParams for rag.topic_consolidation_user_prompt template.
type MergerUserParams struct {
	Topic1Summary string // First topic summary
	Topic2Summary string // Second topic summary
}

// RerankerParams for rag.reranker_system_prompt template.
// The Reranker agent selects relevant topics from memory candidates.
type RerankerParams struct {
	Profile       string // Formatted <user_profile> block
	RecentTopics  string // Formatted <recent_topics> block
	MaxTopics     int    // Maximum topics in final selection
	TargetCharsK  int    // Target budget in thousands (e.g., 30 for ~30K chars)
	MinCandidates int    // Minimum candidates to consider before filtering
	MaxCandidates int    // Maximum candidates to load
	LargeBudgetK  int    // Budget for excerpts from large topics (K chars)
}

// RerankerSimpleParams for rag.reranker_system_prompt_simple template.
// Used when ignore_excerpts is true - no excerpt requirements.
type RerankerSimpleParams struct {
	Profile       string // Formatted <user_profile> block
	RecentTopics  string // Formatted <recent_topics> block
	MaxTopics     int    // Maximum topics in final selection
	MinCandidates int    // Minimum candidates to consider
	MaxCandidates int    // Maximum candidates to load
}

// RerankerUserParams for rag.reranker_user_prompt template.
type RerankerUserParams struct {
	Date            string // Current date
	Query           string // Original user query
	EnrichedQuery   string // Extended search context from Enricher
	CurrentMessages string // Recent conversation messages
	Candidates      string // Formatted candidate list (ID | Date | Size | Topic)
}

// ArchivistParams for memory.system_prompt template.
// The Archivist agent extracts and manages facts from conversations.
type ArchivistParams struct {
	Date            string // Current date
	UserFactsLimit  int    // Maximum facts allowed for user
	UserFactsCount  int    // Current count of user facts
	OtherFactsCount int    // Current count of facts about others
	UserFacts       string // Formatted existing facts about user
	OtherFacts      string // Formatted existing facts about others
	Conversation    string // Conversation to analyze
}

// DeduplicatorParams for memory.consolidation_prompt template.
// The Deduplicator agent identifies and handles duplicate facts.
type DeduplicatorParams struct {
	NewFact    string // The new fact to evaluate
	Candidates string // Existing similar facts for comparison
}

// LaplaceParams for bot.system_prompt template.
// The main chat agent system prompt.
type LaplaceParams struct {
	BotName string // Bot's name (e.g., "Laplaced")
}
