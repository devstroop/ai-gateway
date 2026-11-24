package resilience

import (
	"sync"
	"time"
)

type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

type Breaker struct {
	mu           sync.Mutex
	state        State
	failures     int
	threshold    int
	openUntil    time.Time
	openDuration time.Duration

	// half-open probe tracking
	probeInFlight bool
}

func NewBreaker(threshold int, openDuration time.Duration) *Breaker {
	if threshold <= 0 {
		threshold = 5
	}
	if openDuration <= 0 {
		openDuration = 30 * time.Second
	}
	return &Breaker{threshold: threshold, openDuration: openDuration}
}

func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case StateOpen:
		if time.Now().After(b.openUntil) {
			b.state = StateHalfOpen
			if b.probeInFlight {
				return false
			}
			b.probeInFlight = true
			return true
		}
		return false
	case StateHalfOpen:
		if b.probeInFlight {
			return false
		}
		b.probeInFlight = true
		return true
	case StateClosed:
		return true
	}
	return true
}

func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false
	switch b.state {
	case StateHalfOpen:
		b.state = StateClosed
		b.failures = 0
	case StateClosed:
		b.failures = 0
	case StateOpen:
		// ignore late successes while open
	}
}

func (b *Breaker) ReleaseProbe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false
}

func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false
	switch b.state {
	case StateClosed:
		b.failures++
		if b.failures >= b.threshold {
			b.state = StateOpen
			b.openUntil = time.Now().Add(b.openDuration)
		}
	case StateHalfOpen:
		b.state = StateOpen
		b.openUntil = time.Now().Add(b.openDuration)
	case StateOpen:
		// already open; do not extend openUntil
	}
}
