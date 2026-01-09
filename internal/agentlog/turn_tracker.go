package agentlog

import "time"

// TurnTracker captures LLM conversation turns for any agent.
// It provides a simple API for recording LLM calls and building ConversationTurns.
//
// Usage:
//
//	tracker := agentlog.NewTurnTracker()
//	for {
//	    tracker.StartTurn()
//	    resp, err := client.CreateChatCompletion(ctx, req)
//	    if err != nil { break }
//	    tracker.EndTurn(resp.DebugRequestBody, resp.DebugResponseBody,
//	        resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.Cost)
//	    if noToolCalls { break }
//	}
//	entry.ConversationTurns = tracker.Build()
type TurnTracker struct {
	turns                 []ConversationTurn
	totalPromptTokens     int
	totalCompletionTokens int
	totalCost             float64
	hasCost               bool
	startTime             time.Time
	currentTurnStart      time.Time
}

// NewTurnTracker creates a new TurnTracker.
// Call this at the start of agent processing.
func NewTurnTracker() *TurnTracker {
	return &TurnTracker{
		startTime: time.Now(),
	}
}

// StartTurn marks the beginning of a new LLM call.
// Call this immediately before CreateChatCompletion.
func (t *TurnTracker) StartTurn() {
	t.currentTurnStart = time.Now()
}

// EndTurn records a completed turn with request/response data.
// Call this immediately after CreateChatCompletion succeeds.
//
// Parameters:
//   - request: DebugRequestBody from response (raw JSON string)
//   - response: DebugResponseBody from response (raw JSON string)
//   - promptTokens: from resp.Usage.PromptTokens
//   - completionTokens: from resp.Usage.CompletionTokens
//   - cost: from resp.Usage.Cost (can be nil)
func (t *TurnTracker) EndTurn(request, response string, promptTokens, completionTokens int, cost *float64) {
	turn := ConversationTurn{
		Iteration:  len(t.turns) + 1,
		Request:    request,
		Response:   response,
		DurationMs: int(time.Since(t.currentTurnStart).Milliseconds()),
	}
	t.turns = append(t.turns, turn)
	t.totalPromptTokens += promptTokens
	t.totalCompletionTokens += completionTokens
	if cost != nil {
		t.totalCost += *cost
		t.hasCost = true
	}
}

// Build returns the ConversationTurns struct for logging.
// Returns nil if no turns were recorded.
func (t *TurnTracker) Build() *ConversationTurns {
	if len(t.turns) == 0 {
		return nil
	}
	var cost *float64
	if t.hasCost {
		cost = &t.totalCost
	}
	return &ConversationTurns{
		Turns:                 t.turns,
		TotalDurationMs:       int(time.Since(t.startTime).Milliseconds()),
		TotalPromptTokens:     t.totalPromptTokens,
		TotalCompletionTokens: t.totalCompletionTokens,
		TotalCost:             cost,
	}
}

// TotalTokens returns accumulated token counts across all turns.
func (t *TurnTracker) TotalTokens() (promptTokens, completionTokens int) {
	return t.totalPromptTokens, t.totalCompletionTokens
}

// TotalCost returns accumulated cost across all turns.
// Returns nil if no cost data was recorded.
func (t *TurnTracker) TotalCost() *float64 {
	if !t.hasCost {
		return nil
	}
	return &t.totalCost
}

// TotalCostValue returns accumulated cost as float64.
// Returns 0.0 if no cost data was recorded.
func (t *TurnTracker) TotalCostValue() float64 {
	return t.totalCost
}

// TurnCount returns the number of recorded turns.
func (t *TurnTracker) TurnCount() int {
	return len(t.turns)
}

// FirstRequest returns the first turn's request body.
// Useful for backward compatibility with InputContext field.
func (t *TurnTracker) FirstRequest() string {
	if len(t.turns) == 0 {
		return ""
	}
	if s, ok := t.turns[0].Request.(string); ok {
		return s
	}
	return ""
}

// LastResponse returns the last turn's response body.
// Useful for backward compatibility with OutputContext field.
func (t *TurnTracker) LastResponse() string {
	if len(t.turns) == 0 {
		return ""
	}
	if s, ok := t.turns[len(t.turns)-1].Response.(string); ok {
		return s
	}
	return ""
}

// TotalDuration returns the total duration since tracker was created.
func (t *TurnTracker) TotalDuration() time.Duration {
	return time.Since(t.startTime)
}
