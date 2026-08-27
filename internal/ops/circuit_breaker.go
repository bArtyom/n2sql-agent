package ops

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("circuit breaker is open")

type circuitState struct {
	failures   int
	openedAt   time.Time
	probeInUse bool
}

type CircuitBreaker struct {
	mu           sync.Mutex
	failureLimit int
	resetAfter   time.Duration
	states       map[circuitKey]circuitState
}

type circuitKey struct {
	provider   string
	capability string
}

func NewCircuitBreaker(failureLimit int, resetAfter time.Duration) *CircuitBreaker {
	if failureLimit <= 0 {
		failureLimit = 3
	}
	if resetAfter <= 0 {
		resetAfter = 30 * time.Second
	}
	return &CircuitBreaker{failureLimit: failureLimit, resetAfter: resetAfter, states: make(map[circuitKey]circuitState)}
}

// Allow returns whether a call for the provider/capability pair may start.
// Keeping capability in the key prevents an embedding outage from blocking
// unrelated chat, OCR, or rerank calls to the same provider.
func (b *CircuitBreaker) Allow(provider, capability string) bool {
	if b == nil || provider == "" || capability == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := circuitKey{provider: provider, capability: capability}
	state := b.states[key]
	if state.openedAt.IsZero() {
		return true
	}
	if time.Since(state.openedAt) < b.resetAfter {
		return false
	}
	if state.probeInUse {
		return false
	}
	state.probeInUse = true
	b.states[key] = state
	return true
}

func (b *CircuitBreaker) RecordSuccess(provider, capability string) {
	if b == nil || provider == "" || capability == "" {
		return
	}
	b.mu.Lock()
	delete(b.states, circuitKey{provider: provider, capability: capability})
	b.mu.Unlock()
}

func (b *CircuitBreaker) RecordFailure(provider, capability string) {
	if b == nil || provider == "" || capability == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := circuitKey{provider: provider, capability: capability}
	state := b.states[key]
	state.probeInUse = false
	state.failures++
	if state.failures >= b.failureLimit {
		state.openedAt = time.Now()
	}
	b.states[key] = state
}
