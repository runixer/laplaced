// Package jobtype provides job type classification for observability.
//
// Job types allow distinguishing between interactive user requests
// and background maintenance tasks in metrics and logs.
//
// Usage:
//
//	// In background jobs (archiver, enrichment):
//	ctx = jobtype.WithJobType(ctx, jobtype.Background)
//
//	// In metrics/logging code:
//	jt := jobtype.FromContext(ctx) // Returns Interactive if not set
package jobtype

import "context"

// JobType classifies the type of operation for observability purposes.
type JobType string

const (
	// Interactive represents user-facing operations: chat, tool calls, etc.
	// This is the default when no job type is explicitly set.
	Interactive JobType = "interactive"

	// Background represents maintenance tasks: archiver, enrichment, consolidation.
	// These operations are expected to be slower and more expensive.
	Background JobType = "background"
)

// String returns the string representation of the job type.
func (jt JobType) String() string {
	return string(jt)
}

// contextKey is an unexported type for context keys to prevent collisions.
type contextKey struct{}

// WithJobType returns a new context with the specified job type.
func WithJobType(ctx context.Context, jt JobType) context.Context {
	return context.WithValue(ctx, contextKey{}, jt)
}

// FromContext extracts the job type from context.
// Returns Interactive as a safe default if no job type is set.
func FromContext(ctx context.Context) JobType {
	if jt, ok := ctx.Value(contextKey{}).(JobType); ok {
		return jt
	}
	return Interactive
}
