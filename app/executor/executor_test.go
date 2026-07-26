package executor_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/umputun/revmux/app/executor"
)

func TestNewRunner(t *testing.T) {
	cmd := executor.NewRunner().Command(context.Background(), "echo", "hello")
	assert.Equal(t, "echo", filepath.Base(cmd.Path))
	assert.Equal(t, []string{"echo", "hello"}, cmd.Args)
}

func TestNewClock(t *testing.T) {
	clk := executor.NewClock()
	assert.WithinDuration(t, time.Now(), clk.Now(), time.Minute)

	t.Run("fires", func(t *testing.T) {
		fired := make(chan struct{})
		tm := clk.AfterFunc(time.Millisecond, func() { close(fired) })
		<-fired
		assert.False(t, tm.Stop(), "already fired")
	})

	t.Run("resets and stops", func(t *testing.T) {
		tm := clk.AfterFunc(time.Hour, func() { t.Error("must not fire") })
		assert.True(t, tm.Reset(time.Hour))
		assert.True(t, tm.Stop())
	})
}
