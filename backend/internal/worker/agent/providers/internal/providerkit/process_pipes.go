package providerkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/util/procutil"
)

// WrappedProcessDrainTimerTag identifies the Quartz timer for the wrapped output drain.
const WrappedProcessDrainTimerTag = "wrapped-process-drain"

// ProcessPipes constructs one command and its owner together. Callers cannot replace either field.
type ProcessPipes struct {
	prepared           *launch.PreparedLaunch
	cmd                *exec.Cmd
	owner              *procutil.ProcessOwner
	stdin              io.WriteCloser
	stdout             io.ReadCloser
	stderr             io.ReadCloser
	childPipes         []io.Closer
	childCloseOnce     sync.Once
	childCloseErr      error
	closeStderr        func() error
	nativeExitObserved chan struct{}
	readersClosed      chan struct{}
	closeReadersOnce   sync.Once
	closeStdout        func() error
}

func (p *ProcessPipes) Stdin() io.WriteCloser { return p.stdin }
func (p *ProcessPipes) Stdout() io.ReadCloser { return p.stdout }
func (p *ProcessPipes) Stderr() io.ReadCloser { return p.stderr }

// SetupProcessPipes closes opened pipes if a later setup step fails.
func SetupProcessPipes(cmd *exec.Cmd, cancel func()) (*ProcessPipes, error) {
	return SetupProcessPipesWithExitObserver(cmd, cancel, nil)
}

// SetupProcessPipesWithExitObserver fixes the observer when it constructs the command and owner.
func SetupProcessPipesWithExitObserver(cmd *exec.Cmd, cancel func(), observer procutil.ProcessExitObserver) (*ProcessPipes, error) {
	return setupProcessPipes(cmd, cancel, observer, nil, true)
}

// SetupLaunchPipes takes ownership of prepared even when pipe creation fails.
func SetupLaunchPipes(prepared *launch.PreparedLaunch, cancel func()) (*ProcessPipes, error) {
	return SetupLaunchPipesWithExitObserver(prepared, cancel, nil)
}

// SetupLaunchPipesWithExitObserver fixes the observer before starting the command.
func SetupLaunchPipesWithExitObserver(prepared *launch.PreparedLaunch, cancel func(), observer procutil.ProcessExitObserver) (*ProcessPipes, error) {
	var cmd *exec.Cmd
	if prepared != nil {
		cmd = prepared.Cmd
	}
	return setupProcessPipes(cmd, cancel, observer, prepared, true)
}

func setupProcessPipes(cmd *exec.Cmd, cancel func(), observer procutil.ProcessExitObserver, prepared *launch.PreparedLaunch, pipeStdin bool) (*ProcessPipes, error) {
	if cmd == nil {
		if cancel != nil {
			cancel()
		}
		return nil, errors.Join(errors.New("the process command is absent"), prepared.Close())
	}
	pipes := &ProcessPipes{prepared: prepared, cmd: cmd, owner: procutil.PrepareProcessWithExitObserver(cmd, observer)}
	pipes.closeStdout = sync.OnceValue(func() error { return closeOwnedPipe(pipes.stdout) })
	pipes.closeStderr = sync.OnceValue(func() error { return closeOwnedPipe(pipes.stderr) })
	fail := func(cause error) (*ProcessPipes, error) {
		if closeErr := pipes.Close(); closeErr != nil {
			cause = errors.Join(cause, closeErr)
		}
		if cancel != nil {
			cancel()
		}
		return nil, cause
	}
	if pipeStdin {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fail(fmt.Errorf("stdin pipe: %w", err))
		}
		pipes.stdin = stdin
		pipes.childPipes = append(pipes.childPipes, cmd.Stdin.(io.Closer))
	}
	if prepared == nil {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fail(fmt.Errorf("stdout pipe: %w", err))
		}
		pipes.stdout = stdout
	} else {
		if cmd.Stdout != nil {
			return fail(errors.New("stdout pipe: stdout is already configured"))
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			return fail(fmt.Errorf("stdout pipe: %w", err))
		}
		pipes.stdout, cmd.Stdout = newProcessOutputPipe(reader), writer
		pipes.nativeExitObserved = make(chan struct{})
		pipes.readersClosed = make(chan struct{})
	}
	pipes.childPipes = append(pipes.childPipes, cmd.Stdout.(io.Closer))
	if cmd.Stderr != nil {
		return fail(errors.New("stderr pipe: stderr is already configured"))
	}
	// exec.Cmd.Wait closes a StderrPipe reader. An independently owned pipe
	// preserves unread diagnostics after the native wait records the exit.
	reader, writer, err := os.Pipe()
	if err != nil {
		return fail(fmt.Errorf("stderr pipe: %w", err))
	}
	pipes.stderr, cmd.Stderr = reader, writer
	if prepared != nil {
		pipes.stderr = newProcessOutputPipe(reader)
	}
	pipes.childPipes = append(pipes.childPipes, writer)
	return pipes, nil
}

// Start runs the prepared process and releases the parent's copies of the child pipes.
func (p *ProcessPipes) Start(clock quartz.Clock) error {
	return p.start(clock, nil)
}

func (p *ProcessPipes) start(clock quartz.Clock, recordExit func(error)) error {
	return p.startWithDrainLimit(clock, stderrDrainWait, recordExit)
}

func (p *ProcessPipes) startWithDrainLimit(clock quartz.Clock, drainLimit time.Duration, recordExit func(error)) error {
	startErr := p.owner.Start()
	if err := errors.Join(startErr, p.closeChildPipes()); err != nil {
		return err
	}
	if p.prepared != nil {
		go p.waitAndLimitDrain(clock, drainLimit, recordExit)
	}
	return nil
}

func (p *ProcessPipes) waitAndLimitDrain(clock quartz.Clock, drainLimit time.Duration, recordExit func(error)) {
	// Wait does not own these independent read ends. A profile or wrapper
	// can leave a writer open after the native shell exits.
	err := p.Wait()
	if recordExit != nil {
		recordExit(err)
	}
	close(p.nativeExitObserved)

	timer := clock.NewTimer(drainLimit, WrappedProcessDrainTimerTag)
	defer timer.Stop(WrappedProcessDrainTimerTag)
	select {
	case <-p.readersClosed:
	case <-timer.C:
		finishPipeDrain(p.stdout, p.closeStdout)
		finishPipeDrain(p.stderr, p.closeStderr)
	}
}

// copyOutput drains both owned streams concurrently. A surviving inherited
// writer can hold a stream open, so the native waiter limits the drain.
func (p *ProcessPipes) copyOutput(stdout, stderr io.Writer) error {
	results := make(chan error, 2)
	copyStream := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		if DrainLimitReached(err) {
			err = nil
		}
		results <- err
	}
	go copyStream(stdout, p.stdout)
	go copyStream(stderr, p.stderr)
	first, second := <-results, <-results
	if first != nil {
		return first
	}
	return second
}

// Wait returns the native status. For raw commands, drain stdout first
// because exec.Cmd.Wait closes that reader.
func (p *ProcessPipes) Wait() error { return p.owner.Wait() }

// NativeExitObserved reports whether the owner has recorded the process exit.
func (p *ProcessPipes) NativeExitObserved() bool { return p.owner.ProcessState() != nil }

// waitForRecordedExit waits for the Process to record its native exit when a
// wrapped command starts a native waiter independently of its readers.
func (p *ProcessPipes) waitForRecordedExit() error {
	err := p.Wait()
	if p.nativeExitObserved != nil {
		<-p.nativeExitObserved
	}
	return err
}

// Capture records descendants while the shell is still alive.
func (p *ProcessPipes) Capture(ctx context.Context) error { return p.owner.Capture(ctx) }

func closeOwnedPipe(pipe io.Closer) error {
	if pipe == nil {
		return nil
	}
	err := pipe.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

// closeChildPipes releases the parent's copies after a start attempt, or all
// copies when setup fails before exec.Cmd can close its own child descriptors.
func (p *ProcessPipes) closeChildPipes() error {
	if p == nil {
		return nil
	}
	p.childCloseOnce.Do(func() {
		for _, pipe := range p.childPipes {
			p.childCloseErr = errors.Join(p.childCloseErr, closeOwnedPipe(pipe))
		}
	})
	return p.childCloseErr
}

func finishPipeDrain(reader io.ReadCloser, closeReader func() error) {
	if pipe, ok := reader.(*processOutputPipe); ok {
		pipe.stopDraining()
	} else {
		_ = closeReader()
	}
}

func (p *ProcessPipes) Close() error {
	if p == nil {
		return nil
	}
	return errors.Join(p.closeProcess(), p.prepared.Close())
}

func (p *ProcessPipes) closeProcess() error {
	if p.readersClosed != nil {
		p.closeReadersOnce.Do(func() { close(p.readersClosed) })
	}
	// Closing a pipe can make the root exit and reparent a child before capture.
	return errors.Join(p.owner.Close(), p.closeChildPipes(), closeOwnedPipe(p.stdin), p.closeStdout(), p.closeStderr())
}
