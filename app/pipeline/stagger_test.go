package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	emocks "github.com/umputun/revmux/app/executor/mocks"
)

func TestStagger_acquire(t *testing.T) {
	t.Run("index 0 goes immediately while the rest wait on the gate", func(t *testing.T) {
		s, _ := newHeldStagger(time.Minute, 4)
		require.NoError(t, s.acquire(context.Background(), 0), "the leader never waits")

		// a follower can only leave the gate through its own context, so canceling proves it waited
		ctx, cancel := context.WithCancel(context.Background())
		blocked := make(chan error, 1)
		go func() { blocked <- s.acquire(ctx, 1) }()
		cancel()

		err := <-blocked
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), "stagger release")
	})

	t.Run("the leader's first activity releases the rest", func(t *testing.T) {
		s, _ := newHeldStagger(time.Minute, 4)
		require.NoError(t, s.acquire(context.Background(), 0))

		s.leaderStarted()
		require.NoError(t, s.acquire(context.Background(), 1))
		require.NoError(t, s.acquire(context.Background(), 2))
	})

	t.Run("the delay releases the rest when the leader never emits", func(t *testing.T) {
		s, fire := newHeldStagger(time.Minute, 4)
		require.NoError(t, s.acquire(context.Background(), 0))

		fire()
		require.NoError(t, s.acquire(context.Background(), 1), "the delay is the other release path")
	})

	t.Run("the gate latches open and never re-arms", func(t *testing.T) {
		s, fire := newHeldStagger(time.Minute, 0)
		fire()
		require.NoError(t, s.acquire(context.Background(), 1))

		// a second stage runs through the same instance and must not pay the delay again
		s.leaderStarted()
		fire()
		require.NoError(t, s.acquire(context.Background(), 3), "a later stage finds the gate already open")
	})

	t.Run("a non-positive delay means no stagger at all", func(t *testing.T) {
		clk := &emocks.ClockMock{AfterFuncFunc: func(time.Duration, func()) executor.Timer {
			t.Fatal("a non-positive delay must not arm a timer")
			return nil
		}}
		s := newStagger(0, 0, clk)
		require.NoError(t, s.acquire(context.Background(), 1))
	})

	t.Run("max_parallel caps concurrency", func(t *testing.T) {
		s, fire := newHeldStagger(time.Minute, 2)
		fire()
		require.NoError(t, s.acquire(context.Background(), 0))
		require.NoError(t, s.acquire(context.Background(), 1))

		ctx, cancel := context.WithCancel(context.Background())
		blocked := make(chan error, 1)
		go func() { blocked <- s.acquire(ctx, 2) }()
		cancel()

		err := <-blocked
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parallel slot")

		s.release()
		require.NoError(t, s.acquire(context.Background(), 2), "a released slot is taken by the next agent")
	})

	t.Run("no cap means no semaphore", func(t *testing.T) {
		s, fire := newHeldStagger(time.Minute, 0)
		fire()
		for i := range 32 {
			require.NoError(t, s.acquire(context.Background(), i))
		}
		s.release()
	})

	t.Run("the leader still waits for a slot", func(t *testing.T) {
		s, _ := newHeldStagger(time.Minute, 1)
		require.NoError(t, s.acquire(context.Background(), 0))

		ctx, cancel := context.WithCancel(context.Background())
		blocked := make(chan error, 1)
		go func() { blocked <- s.acquire(ctx, 0) }()
		cancel()
		assert.Contains(t, (<-blocked).Error(), "parallel slot")
	})
}

func TestStagger_leaderStarted(t *testing.T) {
	t.Run("is idempotent under concurrent callers", func(t *testing.T) {
		s, _ := newHeldStagger(time.Minute, 0)

		var wg sync.WaitGroup
		for range 16 {
			wg.Go(s.leaderStarted)
		}
		wg.Wait()
		require.NoError(t, s.acquire(context.Background(), 1))
	})
}

// newHeldStagger builds a stagger over a clock that hands its timer callback back instead of running
// it, so a test fires the stagger delay itself rather than waiting one out.
func newHeldStagger(timeout time.Duration, maxParallel int) (s *stagger, fire func()) {
	var mu sync.Mutex
	var cb func()
	clk := &emocks.ClockMock{
		NowFunc: func() time.Time { return time.Time{} },
		AfterFuncFunc: func(_ time.Duration, f func()) executor.Timer {
			mu.Lock()
			defer mu.Unlock()
			cb = f
			return &emocks.TimerMock{
				StopFunc:  func() bool { return true },
				ResetFunc: func(time.Duration) bool { return true },
			}
		},
	}
	s = newStagger(timeout, maxParallel, clk)
	return s, func() {
		mu.Lock()
		defer mu.Unlock()
		if cb != nil {
			cb()
		}
	}
}
