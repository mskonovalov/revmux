package executor_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

// a child that ignores SIGTERM must still be reaped, or a stalled agent would hang the whole review.
func TestClaude_Run_escalatesToKill(t *testing.T) {
	path := writeFixture(t, stallCapture(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seen := make(chan struct{})
	var once sync.Once
	sink := &mocks.EventSinkMock{EmitFunc: func(ev executor.Event) {
		if ev.Kind == executor.EventActivity {
			once.Do(func() { close(seen) })
		}
	}}

	go func() {
		<-seen
		cancel()
	}()

	c := executor.NewClaude(fakeRunner("stubborn", path), executor.Opts{})
	res, err := c.Run(ctx, executor.Request{Prompt: "x"}, sink)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotEqual(t, 0, res.ExitCode, "the process was signaled, not exited cleanly")
}
