package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// maxLineBytes bounds one stream-json line. A result event carrying structured output runs well past
// bufio.Scanner's 64k default, and a line over the cap would look like a truncated stream.
const maxLineBytes = 8 << 20

// parser turns a process's stdout into a Result. Each executor supplies its own.
type parser func(ctx context.Context, r io.Reader) Result

// runSpec is the per-run wiring proc.run needs beyond Request.
type runSpec struct {
	argv  []string
	parse parser
	sink  EventSink
}

// proc is the machinery Claude and Codex share: start, idle watchdog, process-group teardown, line
// reading. It holds immutable configuration only — one instance serves every roster entry concurrently,
// so any per-run state would be a data race.
type proc struct {
	bin    string
	runner CommandRunner
	opts   Opts
}

func newProc(bin string, runner CommandRunner, opts Opts) proc {
	if opts.Clock == nil {
		opts.Clock = NewClock()
	}
	return proc{bin: bin, runner: runner, opts: opts}
}

// procRun is one live process. start returns it rather than stashing it on proc.
type procRun struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	cleanup *processGroupCleanup
}

func (p *proc) run(ctx context.Context, req Request, spec runSpec) (Result, error) {
	if p.opts.HardTimeout > 0 {
		var cancelHard context.CancelFunc
		ctx, cancelHard = context.WithTimeout(ctx, p.opts.HardTimeout)
		defer cancelHard()
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	var idle Timer
	if p.opts.IdleTimeout > 0 {
		idle = p.opts.Clock.AfterFunc(p.opts.IdleTimeout, cancelRun)
		defer idle.Stop()
	}

	run, err := p.start(runCtx, spec.argv, req.Prompt)
	if err != nil {
		return Result{}, err
	}
	p.emit(spec.sink, Event{Kind: EventStarted, Text: p.bin})

	var raw strings.Builder
	tee := &teeReader{src: run.stdout, dst: p.rawSink(&raw, req.RawOutput), touch: func() {
		if idle != nil {
			idle.Reset(p.opts.IdleTimeout)
		}
	}}

	res := spec.parse(runCtx, tee)
	res.Raw = raw.String()
	res.ExitCode = run.finish()
	p.emit(spec.sink, Event{Kind: EventFinished, Text: fmt.Sprintf("exit %d", res.ExitCode)})

	if ctx.Err() != nil {
		return res, fmt.Errorf("%s run stopped: %w", p.bin, ctx.Err())
	}
	if runCtx.Err() != nil {
		res.IdleTimedOut = true
	}
	if tee.err != nil {
		return res, tee.err
	}
	return res, nil
}

// rawSink fans the verbatim stream out to the always-on buffer and to the caller's archive writer.
func (p *proc) rawSink(buf *strings.Builder, extra io.Writer) io.Writer {
	if extra == nil {
		return buf
	}
	return io.MultiWriter(buf, extra)
}

func (p *proc) start(ctx context.Context, argv []string, prompt string) (*procRun, error) {
	cmd := p.runner.Command(ctx, p.bin, argv...)
	cmd.Env = p.childEnv()
	cmd.Dir = p.opts.WorkDir
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe for %s: %w", p.bin, err)
	}

	p.setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", p.bin, err)
	}
	return &procRun{cmd: cmd, stdout: stdout, cleanup: newProcessGroupCleanup(cmd, ctx.Done())}, nil
}

func (p *proc) readLines(ctx context.Context, r io.Reader, handler func(string)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLineBytes)
	for sc.Scan() {
		if ctx.Err() != nil {
			return fmt.Errorf("%s output stopped: %w", p.bin, ctx.Err())
		}
		handler(sc.Text())
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s output stopped: %w", p.bin, ctx.Err())
		}
		return fmt.Errorf("read %s output: %w", p.bin, err)
	}
	return nil
}

// childEnv drops the variables that break or misroute a child model CLI. CLAUDECODE always goes:
// revmux normally runs inside an AI coding session and the child refuses to start as a nested one.
func (p *proc) childEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		switch name, _, _ := strings.Cut(kv, "="); {
		case name == "CLAUDECODE":
		case name == "ANTHROPIC_API_KEY" && !p.opts.PreserveAPIKey:
		default:
			out = append(out, kv)
		}
	}
	return out
}

func (p *proc) emit(sink EventSink, ev Event) {
	if sink == nil {
		return
	}
	sink.Emit(ev)
}

// finish waits for the process, then kills its group even on a normal exit — that is what reaps the
// subagents and MCP servers a model CLI leaves behind.
func (r *procRun) finish() int {
	_ = r.cleanup.wait() // the exit status is read off ProcessState below
	r.cleanup.killProcessGroup()
	if r.cmd.ProcessState == nil {
		return -1
	}
	return r.cmd.ProcessState.ExitCode()
}

// teeReader copies every byte to the archive before it is parsed and touches the idle watchdog on each
// read. A stream that fails to parse is exactly the one worth having on disk, so the copy happens first.
type teeReader struct {
	src   io.Reader
	dst   io.Writer
	touch func()
	err   error
}

func (t *teeReader) Read(p []byte) (int, error) {
	n, err := t.src.Read(p)
	if n > 0 {
		if _, werr := t.dst.Write(p[:n]); werr != nil && t.err == nil {
			t.err = fmt.Errorf("tee raw output: %w", werr)
		}
		if t.touch != nil {
			t.touch()
		}
	}
	return n, err //nolint:wrapcheck // io.Reader contract: pass EOF and read errors through unchanged
}
