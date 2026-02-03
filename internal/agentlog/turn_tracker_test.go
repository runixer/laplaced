package agentlog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewTurnTracker(t *testing.T) {
	tracker := NewTurnTracker()

	assert.NotNil(t, tracker)
	assert.Equal(t, 0, tracker.TurnCount())
	assert.Nil(t, tracker.Build())
}

func TestTurnTracker_SingleTurn(t *testing.T) {
	tracker := NewTurnTracker()

	tracker.StartTurn()
	time.Sleep(10 * time.Millisecond) // Simulate some work
	cost := 0.001
	tracker.EndTurn(`{"model":"test"}`, `{"choices":[]}`, 100, 50, &cost)

	assert.Equal(t, 1, tracker.TurnCount())

	prompt, completion := tracker.TotalTokens()
	assert.Equal(t, 100, prompt)
	assert.Equal(t, 50, completion)

	totalCost := tracker.TotalCost()
	assert.NotNil(t, totalCost)
	assert.Equal(t, 0.001, *totalCost)

	assert.Equal(t, `{"model":"test"}`, tracker.FirstRequest())
	assert.Equal(t, `{"choices":[]}`, tracker.LastResponse())

	result := tracker.Build()
	assert.NotNil(t, result)
	assert.Len(t, result.Turns, 1)
	assert.Equal(t, 1, result.Turns[0].Iteration)
	assert.GreaterOrEqual(t, result.Turns[0].DurationMs, 10)
	assert.Equal(t, 100, result.TotalPromptTokens)
	assert.Equal(t, 50, result.TotalCompletionTokens)
}

func TestTurnTracker_MultipleTurns(t *testing.T) {
	tracker := NewTurnTracker()

	// Turn 1
	tracker.StartTurn()
	cost1 := 0.001
	tracker.EndTurn(`{"turn":1}`, `{"tool_calls":[]}`, 100, 30, &cost1)

	// Turn 2
	tracker.StartTurn()
	cost2 := 0.002
	tracker.EndTurn(`{"turn":2}`, `{"content":"final"}`, 150, 80, &cost2)

	assert.Equal(t, 2, tracker.TurnCount())

	prompt, completion := tracker.TotalTokens()
	assert.Equal(t, 250, prompt)     // 100 + 150
	assert.Equal(t, 110, completion) // 30 + 80

	totalCost := tracker.TotalCost()
	assert.NotNil(t, totalCost)
	assert.InDelta(t, 0.003, *totalCost, 0.0001)

	assert.Equal(t, `{"turn":1}`, tracker.FirstRequest())
	assert.Equal(t, `{"content":"final"}`, tracker.LastResponse())

	result := tracker.Build()
	assert.NotNil(t, result)
	assert.Len(t, result.Turns, 2)
	assert.Equal(t, 1, result.Turns[0].Iteration)
	assert.Equal(t, 2, result.Turns[1].Iteration)
}

func TestTurnTracker_NoCost(t *testing.T) {
	tracker := NewTurnTracker()

	tracker.StartTurn()
	tracker.EndTurn(`{}`, `{}`, 50, 25, nil)

	assert.Nil(t, tracker.TotalCost())

	result := tracker.Build()
	assert.NotNil(t, result)
	assert.Nil(t, result.TotalCost)
}

func TestTurnTracker_PartialCost(t *testing.T) {
	tracker := NewTurnTracker()

	// Turn 1: no cost
	tracker.StartTurn()
	tracker.EndTurn(`{}`, `{}`, 50, 25, nil)

	// Turn 2: has cost
	tracker.StartTurn()
	cost := 0.005
	tracker.EndTurn(`{}`, `{}`, 50, 25, &cost)

	totalCost := tracker.TotalCost()
	assert.NotNil(t, totalCost)
	assert.Equal(t, 0.005, *totalCost)
}

func TestTurnTracker_EmptyBuild(t *testing.T) {
	tracker := NewTurnTracker()

	assert.Nil(t, tracker.Build())
	assert.Equal(t, "", tracker.FirstRequest())
	assert.Equal(t, "", tracker.LastResponse())
}

func TestTurnTracker_TotalDuration(t *testing.T) {
	tracker := NewTurnTracker()

	time.Sleep(50 * time.Millisecond)

	duration := tracker.TotalDuration()
	assert.GreaterOrEqual(t, duration.Milliseconds(), int64(50))
}

func TestTurnTracker_TotalCostValue(t *testing.T) {
	tests := []struct {
		name          string
		setupTurns    func(*TurnTracker)
		expectedValue float64
	}{
		{
			name: "no turns",
			setupTurns: func(t *TurnTracker) {
				// No turns added
			},
			expectedValue: 0.0,
		},
		{
			name: "no cost data",
			setupTurns: func(t *TurnTracker) {
				tracker := t
				tracker.StartTurn()
				tracker.EndTurn(`{}`, `{}`, 100, 50, nil)
			},
			expectedValue: 0.0,
		},
		{
			name: "single turn with cost",
			setupTurns: func(t *TurnTracker) {
				tracker := t
				tracker.StartTurn()
				cost := 0.001
				tracker.EndTurn(`{}`, `{}`, 100, 50, &cost)
			},
			expectedValue: 0.001,
		},
		{
			name: "multiple turns with costs",
			setupTurns: func(t *TurnTracker) {
				tracker := t
				tracker.StartTurn()
				cost1 := 0.001
				tracker.EndTurn(`{}`, `{}`, 100, 50, &cost1)

				tracker.StartTurn()
				cost2 := 0.002
				tracker.EndTurn(`{}`, `{}`, 150, 80, &cost2)

				tracker.StartTurn()
				cost3 := 0.0005
				tracker.EndTurn(`{}`, `{}`, 75, 40, &cost3)
			},
			expectedValue: 0.0035,
		},
		{
			name: "mixed turns (some with cost, some without)",
			setupTurns: func(t *TurnTracker) {
				tracker := t
				tracker.StartTurn()
				tracker.EndTurn(`{}`, `{}`, 100, 50, nil)

				tracker.StartTurn()
				cost := 0.005
				tracker.EndTurn(`{}`, `{}`, 150, 80, &cost)

				tracker.StartTurn()
				tracker.EndTurn(`{}`, `{}`, 75, 40, nil)
			},
			expectedValue: 0.005,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewTurnTracker()
			tt.setupTurns(tracker)
			assert.Equal(t, tt.expectedValue, tracker.TotalCostValue())
		})
	}
}

func TestTurnTracker_FirstRequest_EmptyTracker(t *testing.T) {
	tracker := NewTurnTracker()

	// FirstRequest should return empty string when no turns were recorded
	assert.Equal(t, "", tracker.FirstRequest())

	// Build should return nil when no turns
	assert.Nil(t, tracker.Build())
}

func TestTurnTracker_LastResponse_EmptyTracker(t *testing.T) {
	tracker := NewTurnTracker()

	// LastResponse should return empty string when no turns were recorded
	assert.Equal(t, "", tracker.LastResponse())

	// TurnCount should be 0
	assert.Equal(t, 0, tracker.TurnCount())
}
