package dr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// defaultHookTimeout bounds a hook script when the plan gives no timeout of its own.
const defaultHookTimeout = 60 * time.Second

// maxHookOutput is how much of a hook's output the run log keeps, so a chatty
// hook cannot bloat the run history.
const maxHookOutput = 64 << 10

// hookKillGrace is how long the runner waits after killing a hook's process
// group before giving up on its output: a child that escaped the group (say,
// a daemon that setsid'd itself away) must not hang the run forever.
const hookKillGrace = 10 * time.Second

// HookRunner runs a recovery plan's hook scripts in the DR host's shell.
type HookRunner interface {
	RunHook(ctx context.Context, script string, env []string, timeout time.Duration) ([]byte, error)
}

// DefaultHookRunner runs a script with `sh -c` (or `bash -c`), waits up to
// timeout, and returns the combined output of stdout and stderr.
type DefaultHookRunner struct {
	Shell []string // program and flags, e.g. ["sh", "-c"]
}

// NewDefaultHookRunner picks the first shell it finds, sh before bash.
func NewDefaultHookRunner() *DefaultHookRunner {
	for _, name := range []string{"sh", "bash"} {
		if _, err := exec.LookPath(name); err == nil {
			return &DefaultHookRunner{Shell: []string{name, "-c"}}
		}
	}
	return &DefaultHookRunner{}
}

// RunHook executes the script in a subshell with env appended to the process
// environment; without a shell it is a configuration error, not a crash.
// The shell runs in its own process group: when the deadline fires the whole
// group — the shell and any children it spawned — is killed, and the same
// kill bounds the output wait when the shell has already exited but a child
// still holds the pipe (`sh -c 'sleep 300 &'` returns while sleep keeps the
// write end open). Either way the run stops within its deadline, and a
// caller's cancellation of ctx is handled the same way.
func (h *DefaultHookRunner) RunHook(ctx context.Context, script string, env []string, timeout time.Duration) ([]byte, error) {
	if len(h.Shell) == 0 {
		return nil, errors.New("no shell found to run hooks (need sh or bash)")
	}
	if timeout <= 0 {
		timeout = defaultHookTimeout
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	args := append(append([]string{}, h.Shell[1:]...), script)
	cmd := exec.CommandContext(cctx, h.Shell[0], args...)
	cmd.Env = append(os.Environ(), env...)
	// A real pipe, not an io.Writer: with a writer, os/exec copies the
	// output itself and Wait blocks on that copy — that is, on every child
	// still holding the write end — past the deadline. With the pipe, Wait
	// ends when the shell itself is gone; the copy below drains our end.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("hook output pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	var closeOnce sync.Once
	closePipe := func() {
		closeOnce.Do(func() {
			_ = pr.Close()
		})
	}
	defer closePipe()

	var out cappedBuffer
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(&out, pr)
	}()

	if err := startHookProc(cmd); err != nil {
		closePipe()
		pw.Close()
		return out.Bytes(), err
	}
	pw.Close()
	pid := cmd.Process.Pid

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()
	deadline := time.After(timeout)
	select {
	case <-deadline:
		// The script is still running: kill the whole group and give the
		// reaper and the output copy a grace period each; a process that
		// refuses to die must not hang the run on top of its timeout.
		killProcessGroup(pid)
		boundedWait(waitErrToDone(waitErr))
		closePipe()
		boundedWait(copied)
		return out.Bytes(), fmt.Errorf("hook timed out after %s", timeout)
	case err := <-waitErr:
		// The shell itself is done, but a child it left behind may still
		// hold the output pipe open: the deadline (or the caller's
		// cancellation) bounds that wait too, killing the group the same
		// way.
		select {
		case <-copied:
			if err != nil && cctx.Err() != nil {
				// The caller's cancellation killed the shell mid-run.
				return out.Bytes(), fmt.Errorf("hook canceled: %w", ctx.Err())
			}
			return out.Bytes(), err
		case <-deadline:
			killProcessGroup(pid)
			closePipe()
			boundedWait(copied)
			return out.Bytes(), fmt.Errorf("hook timed out after %s", timeout)
		case <-cctx.Done():
			killProcessGroup(pid)
			closePipe()
			boundedWait(copied)
			if err != nil {
				return out.Bytes(), fmt.Errorf("hook canceled: %w", ctx.Err())
			}
			return out.Bytes(), nil
		}
	}
}

// boundedWait waits for done, but never past the kill grace period: what
// still has not finished by then (a child that escaped the process group) is
// abandoned rather than allowed to hang the run.
func boundedWait(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(hookKillGrace):
	}
}

// waitErrToDone adapts the reaper's error channel to something boundedWait
// can wait for; the adapter ends when the reaper reports, whenever that is.
func waitErrToDone(waitErr <-chan error) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-waitErr
	}()
	return done
}

// cappedBuffer keeps the first maxHookOutput bytes written to it (the rest is
// dropped) and always reports the full write, so writers see a healthy sink.
// The inner buffer is a named field, not embedded: an embedded bytes.Buffer
// promotes ReadFrom and io.Copy would bypass Write entirely.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer, truncating anything past maxHookOutput.
func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	orig := len(p)
	if n := b.buf.Len(); n >= maxHookOutput {
		return orig, nil
	} else if n+len(p) > maxHookOutput {
		p = p[:maxHookOutput-n]
	}
	_, _ = b.buf.Write(p)
	return orig, nil
}

// Bytes returns the output kept so far.
func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}
