package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/umputun/revmux/app/executor"
)

// stagger releases agents onto the runners and caps how many run at once. The agent at index 0 goes
// immediately; every other one waits until that leader produces real activity or stagger_delay
// elapses, whichever comes first.
//
// The gate latches open on either path and never re-arms, which is what lets one instance serve every
// stage: a later stage finds it already open instead of paying another delay to re-prove auth the
// first stage already proved.
//
// It never groups, reorders or filters the roster — releasing is all it does, because roster
// composition is a review-quality decision.
type stagger struct {
	sem  chan struct{}
	gate chan struct{}
	once sync.Once
}

// newStagger builds the gate and arms the release timer. The parameter is named timeout rather than
// cap because cap shadows the builtin; a non-positive one means no stagger at all.
func newStagger(timeout time.Duration, maxParallel int, clk executor.Clock) *stagger {
	s := &stagger{gate: make(chan struct{})}
	if maxParallel > 0 {
		s.sem = make(chan struct{}, maxParallel)
	}
	if timeout <= 0 {
		s.leaderStarted()
		return s
	}
	clk.AfterFunc(timeout, s.leaderStarted)
	return s
}

// acquire takes a slot for the agent at index, blocking until the gate is open and the parallel cap
// allows one more. Only a successful acquire may be paired with release.
func (s *stagger) acquire(ctx context.Context, index int) error {
	if index != 0 && !s.open() {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return fmt.Errorf("wait for stagger release: %w", ctx.Err())
		}
	}
	if s.sem == nil {
		return nil
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for a parallel slot: %w", ctx.Err())
	}
}

// release gives the slot back.
func (s *stagger) release() {
	if s.sem == nil {
		return
	}
	<-s.sem
}

// leaderStarted opens the gate. Every call after the first is a no-op, so the timer firing after real
// activity already released the roster costs nothing.
func (s *stagger) leaderStarted() { s.once.Do(func() { close(s.gate) }) }

// open reports whether the gate has already latched. An agent arriving after the release must not
// lose a select race with its own expiring context and be told it waited for something it did not.
func (s *stagger) open() bool {
	select {
	case <-s.gate:
		return true
	default:
		return false
	}
}
