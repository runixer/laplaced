package rag

import (
	"sync"
	"time"
)

const (
	chunkCooldownBase = 5 * time.Minute
	chunkCooldownCap  = 6 * time.Hour
)

type chunkKey struct {
	userID     int64
	startMsgID int64
}

type chunkFailureState struct {
	count         int
	cooldownUntil time.Time
}

// chunkCircuitBreaker tracks consecutive failures for chunk processing and puts
// persistently-failing chunks into exponential-backoff cooldown. This prevents
// a persistent upstream outage (e.g. embeddings provider down) from draining
// tokens via splitter retries on the same chunks every ticker interval.
type chunkCircuitBreaker struct {
	mu    sync.Mutex
	state map[chunkKey]*chunkFailureState
	// now is injected for deterministic testing. Defaults to time.Now.
	now func() time.Time
}

func newChunkCircuitBreaker() *chunkCircuitBreaker {
	return &chunkCircuitBreaker{
		state: make(map[chunkKey]*chunkFailureState),
		now:   time.Now,
	}
}

// cooldownRemaining returns the time until this chunk may be retried, or 0 if
// it is not in cooldown.
func (cb *chunkCircuitBreaker) cooldownRemaining(userID, startMsgID int64) time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	st, ok := cb.state[chunkKey{userID, startMsgID}]
	if !ok {
		return 0
	}
	remaining := st.cooldownUntil.Sub(cb.now())
	if remaining < 0 {
		return 0
	}
	return remaining
}

// recordFailure increments the failure count and sets a new cooldown. Returns
// the cooldown duration applied (for logging).
func (cb *chunkCircuitBreaker) recordFailure(userID, startMsgID int64) time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	key := chunkKey{userID, startMsgID}
	st, ok := cb.state[key]
	if !ok {
		st = &chunkFailureState{}
		cb.state[key] = st
	}
	st.count++
	cooldown := chunkCooldownBase << (st.count - 1)
	if cooldown <= 0 || cooldown > chunkCooldownCap {
		cooldown = chunkCooldownCap
	}
	st.cooldownUntil = cb.now().Add(cooldown)
	return cooldown
}

// recordSuccess clears any recorded failures for this chunk.
func (cb *chunkCircuitBreaker) recordSuccess(userID, startMsgID int64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	delete(cb.state, chunkKey{userID, startMsgID})
}
