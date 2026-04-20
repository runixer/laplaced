package rag

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestChunkCircuitBreaker_NoFailures_NoCooldown(t *testing.T) {
	cb := newChunkCircuitBreaker()
	assert.Equal(t, time.Duration(0), cb.cooldownRemaining(1, 100))
}

func TestChunkCircuitBreaker_ExponentialBackoff(t *testing.T) {
	now := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	cb := newChunkCircuitBreaker()
	cb.now = func() time.Time { return now }

	// 1st failure → 5m cooldown.
	cooldown := cb.recordFailure(1, 100)
	assert.Equal(t, 5*time.Minute, cooldown)
	assert.Equal(t, 5*time.Minute, cb.cooldownRemaining(1, 100))

	// 2nd failure → 10m.
	cooldown = cb.recordFailure(1, 100)
	assert.Equal(t, 10*time.Minute, cooldown)

	// 3rd → 20m, 4th → 40m, 5th → 80m.
	_ = cb.recordFailure(1, 100)
	_ = cb.recordFailure(1, 100)
	cooldown = cb.recordFailure(1, 100)
	assert.Equal(t, 80*time.Minute, cooldown)
}

func TestChunkCircuitBreaker_CooldownCap(t *testing.T) {
	cb := newChunkCircuitBreaker()
	// 20+ failures must not overflow.
	var last time.Duration
	for range 20 {
		last = cb.recordFailure(1, 100)
	}
	assert.Equal(t, chunkCooldownCap, last)
}

func TestChunkCircuitBreaker_RemainingDecreasesOverTime(t *testing.T) {
	now := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	cb := newChunkCircuitBreaker()
	cb.now = func() time.Time { return now }

	cb.recordFailure(1, 100)
	assert.Equal(t, 5*time.Minute, cb.cooldownRemaining(1, 100))

	// Advance 3 minutes → 2 left.
	cb.now = func() time.Time { return now.Add(3 * time.Minute) }
	assert.Equal(t, 2*time.Minute, cb.cooldownRemaining(1, 100))

	// Advance past cooldown.
	cb.now = func() time.Time { return now.Add(10 * time.Minute) }
	assert.Equal(t, time.Duration(0), cb.cooldownRemaining(1, 100))
}

func TestChunkCircuitBreaker_SuccessClearsState(t *testing.T) {
	cb := newChunkCircuitBreaker()
	cb.recordFailure(1, 100)
	cb.recordFailure(1, 100)
	cb.recordSuccess(1, 100)
	assert.Equal(t, time.Duration(0), cb.cooldownRemaining(1, 100))

	// After clear, next failure starts at base again, not at count=3.
	cooldown := cb.recordFailure(1, 100)
	assert.Equal(t, 5*time.Minute, cooldown)
}

func TestChunkCircuitBreaker_PerChunkIsolation(t *testing.T) {
	cb := newChunkCircuitBreaker()
	cb.recordFailure(1, 100)
	cb.recordFailure(1, 100)

	// Different user or different chunk → no cooldown.
	assert.Equal(t, time.Duration(0), cb.cooldownRemaining(2, 100))
	assert.Equal(t, time.Duration(0), cb.cooldownRemaining(1, 200))
}
