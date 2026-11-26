package partner

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitUnavailable = errors.New("partner circuit unavailable")

type state string

const (
	closed   state = "closed"
	open     state = "open"
	halfOpen state = "half_open"
)

type outcome uint8

const (
	ignored outcome = iota
	succeeded
	failed
)

type breaker struct {
	mu           sync.Mutex
	state        state
	generation   uint64
	failures     int
	firstFailure time.Time
	retryAt      time.Time
	probing      bool
	threshold    int
	window       time.Duration
	cooldown     time.Duration
}

func newBreaker(threshold int, window, cooldown time.Duration) *breaker {
	return &breaker{state: closed, threshold: threshold, window: window, cooldown: cooldown}
}

func (b *breaker) admit(now time.Time, onChange func(state, state)) (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == open {
		if now.Before(b.retryAt) {
			return 0, false
		}
		b.transition(halfOpen, now, onChange)
	}
	if b.state == halfOpen {
		if b.probing {
			return 0, false
		}
		b.probing = true
	}
	return b.generation, true
}

func (b *breaker) complete(generation uint64, result outcome, now time.Time, onChange func(state, state)) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if generation != b.generation {
		return
	}
	if b.state == halfOpen {
		b.probing = false
		switch result {
		case succeeded:
			b.transition(closed, now, onChange)
		case failed:
			b.transition(open, now, onChange)
		}
		return
	}
	switch result {
	case succeeded:
		b.failures = 0
	case failed:
		if b.failures == 0 || now.Sub(b.firstFailure) > b.window {
			b.firstFailure = now
			b.failures = 0
		}
		b.failures++
		if b.failures >= b.threshold {
			b.transition(open, now, onChange)
		}
	}
}

func (b *breaker) transition(next state, now time.Time, onChange func(state, state)) {
	previous := b.state
	b.state = next
	b.generation++
	b.failures = 0
	b.probing = false
	if next == open {
		b.retryAt = now.Add(b.cooldown)
	}
	onChange(previous, next)
}
