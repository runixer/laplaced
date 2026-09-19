package rag

import (
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/storage"
)

const (
	retryCooldownBase = 5 * time.Minute
	retryCooldownCap  = 6 * time.Hour
)

// retryKey identifies one unit of background work per user: the first
// message id of a chunk for the splitter loop, the topic id for the facts
// (archivist) loop.
type retryKey struct {
	userID storage.ScopeID
	itemID int64
}

type retryFailureState struct {
	count         int
	cooldownUntil time.Time
}

// retryBreaker tracks consecutive failures of a background work item and puts
// persistently-failing items into exponential-backoff cooldown (5m, 10m, …,
// capped at 6h). Without it a persistent failure — an embeddings outage, or a
// topic the provider's safety filter rejects on every attempt — is retried on
// every ticker interval, burning tokens and flooding the error log (one topic
// produced 551 identical errors in a day before the facts loop got this).
type retryBreaker struct {
	mu    sync.Mutex
	state map[retryKey]*retryFailureState
	// now is injected for deterministic testing. Defaults to time.Now.
	now func() time.Time
}

func newRetryBreaker() *retryBreaker {
	return &retryBreaker{
		state: make(map[retryKey]*retryFailureState),
		now:   time.Now,
	}
}

// cooldownRemaining returns the time until this item may be retried, or 0 if
// it is not in cooldown.
func (cb *retryBreaker) cooldownRemaining(userID storage.ScopeID, itemID int64) time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	st, ok := cb.state[retryKey{userID, itemID}]
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
func (cb *retryBreaker) recordFailure(userID storage.ScopeID, itemID int64) time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	key := retryKey{userID, itemID}
	st, ok := cb.state[key]
	if !ok {
		st = &retryFailureState{}
		cb.state[key] = st
	}
	st.count++
	cooldown := retryCooldownBase << (st.count - 1)
	if cooldown <= 0 || cooldown > retryCooldownCap {
		cooldown = retryCooldownCap
	}
	st.cooldownUntil = cb.now().Add(cooldown)
	return cooldown
}

// recordSuccess clears any recorded failures for this item.
func (cb *retryBreaker) recordSuccess(userID storage.ScopeID, itemID int64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	delete(cb.state, retryKey{userID, itemID})
}
