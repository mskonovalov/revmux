package executor_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/executor/mocks"
)

// helperCmd re-executes the test binary as a stand-in for a model CLI. Everything after "--" is ours:
// the testing package stops flag parsing there and leaves the rest in os.Args.
func helperCmd(mode, path string) *exec.Cmd {
	//nolint:gosec // both arguments come from the test that builds this command
	return exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", mode, path)
}

func fakeRunner(mode, path string) *mocks.CommandRunnerMock {
	return &mocks.CommandRunnerMock{
		CommandFunc: func(_ context.Context, _ string, _ ...string) *exec.Cmd { return helperCmd(mode, path) },
	}
}

func discardSink() *mocks.EventSinkMock {
	return &mocks.EventSinkMock{EmitFunc: func(executor.Event) {}}
}

// TestHelperProcess is the fake CLI. It returns immediately during a normal suite run.
func TestHelperProcess(t *testing.T) {
	idx := slices.Index(os.Args, "--")
	if idx < 0 || idx+2 >= len(os.Args) {
		return
	}
	mode, path := os.Args[idx+1], os.Args[idx+2]

	if mode == "env" {
		fmt.Print(strings.Join(os.Environ(), "\n"))
		os.Exit(0)
	}

	data, err := os.ReadFile(path) //nolint:gosec // the path is built by the test that spawned this
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		os.Exit(1)
	}

	switch mode {
	case "stall":
		time.Sleep(time.Minute)
	case "stubborn":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
	case "fail":
		os.Exit(3)
	}
	os.Exit(0)
}

func TestClaude_Run_scrubsChildEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ANTHROPIC_API_KEY", "secret-key")

	t.Run("stripped by default", func(t *testing.T) {
		c := executor.NewClaude(fakeRunner("env", "-"), executor.Opts{})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)
		assert.NotContains(t, res.Raw, "CLAUDECODE=")
		assert.NotContains(t, res.Raw, "ANTHROPIC_API_KEY=")
		assert.Contains(t, res.Raw, "PATH=", "the rest of the environment is passed through")
	})

	t.Run("api key preserved on request", func(t *testing.T) {
		c := executor.NewClaude(fakeRunner("env", "-"), executor.Opts{PreserveAPIKey: true})
		res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
		require.NoError(t, err)
		assert.Contains(t, res.Raw, "ANTHROPIC_API_KEY=secret-key")
		assert.NotContains(t, res.Raw, "CLAUDECODE=", "never passed through")
	})
}

func TestClaude_Run_teesRawOutput(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"clean stream", cleanCapture(t)},
		{"stream that fails to parse", truncatedCapture(t)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, tt.data)
			raw := &bytes.Buffer{}
			c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x", RawOutput: raw}, discardSink())
			require.NoError(t, err)
			assert.Equal(t, tt.data, raw.Bytes(), "archived bytes are what the process produced")
			assert.Equal(t, string(tt.data), res.Raw)
		})
	}
}

func TestClaude_Run_reportsExitCode(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("fail", path), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a non-zero exit is reported on the result, not as an error")
	assert.Equal(t, 3, res.ExitCode)
	assert.NotEmpty(t, res.StructuredOutput, "output produced before the failure is still parsed")
}

func TestClaude_Run_startFailure(t *testing.T) {
	runner := &mocks.CommandRunnerMock{
		CommandFunc: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "revmux-no-such-binary")
		},
	}
	c := executor.NewClaude(runner, executor.Opts{})
	_, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude")
}

func TestClaude_Run_nilClockDefaultsToReal(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{IdleTimeout: time.Hour})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, discardSink())
	require.NoError(t, err, "a nil Opts.Clock must not panic on the first AfterFunc")
	assert.False(t, res.IdleTimedOut)
}

func TestClaude_Run_nilSink(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{})
	res, err := c.Run(context.Background(), executor.Request{Prompt: "x"}, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, res.StructuredOutput)
}

func TestClaude_Run_concurrentOnOneInstance(t *testing.T) {
	path := writeFixture(t, cleanCapture(t))
	c := executor.NewClaude(fakeRunner("emit", path), executor.Opts{IdleTimeout: time.Hour})

	results := make([]executor.Result, 4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			res, err := c.Run(context.Background(), executor.Request{Prompt: "x", Model: "opus"}, discardSink())
			assert.NoError(t, err)
			results[i] = res
		})
	}
	wg.Wait()

	for _, res := range results {
		assert.Equal(t, 0, res.ExitCode)
		assert.NotEmpty(t, res.StructuredOutput, "one executor instance holds no per-run state")
	}
}

func TestClaude_Run_parentCancel(t *testing.T) {
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

	c := executor.NewClaude(fakeRunner("stall", path), executor.Opts{})
	res, err := c.Run(ctx, executor.Request{Prompt: "x"}, sink)
	require.ErrorIs(t, err, context.Canceled, "a canceled parent is an error, not an idle timeout")
	assert.False(t, res.IdleTimedOut)
}
