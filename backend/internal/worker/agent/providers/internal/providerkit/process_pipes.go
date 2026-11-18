package providerkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/util/procutil"
)

// ProcessPipes constructs one command and its owner together. Callers cannot replace either field.
type ProcessPipes struct {
	prepared         *launch.PreparedLaunch
	cmd              *exec.Cmd
	owner            *procutil.ProcessOwner
	stdin            io.WriteCloser
	stdout           io.ReadCloser
	stderr           io.ReadCloser
	childPipes       []io.Closer
	childCloseOnce   sync.Once
	childCloseErr    error
	stderrCloseOnce  sync.Once
	stderrCloseErr   error
	readersClosed    chan struct{}
	closeReadersOnce sync.Once
	stdoutCloseOnce  sync.Once
	stdoutCloseErr   error
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
	return setupProcessPipes(cmd, cancel, observer, nil)
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
	return setupProcessPipes(cmd, cancel, observer, prepared)
}

func setupProcessPipes(cmd *exec.Cmd, cancel func(), observer procutil.ProcessExitObserver, prepared *launch.PreparedLaunch) (*ProcessPipes, error) {
	if cmd == nil {
		if cancel != nil {
			cancel()
		}
		return nil, errors.Join(errors.New("the process command is absent"), prepared.Close())
	}
	pipes := &ProcessPipes{prepared: prepared, cmd: cmd, owner: procutil.PrepareProcessWithExitObserver(cmd, observer)}
	fail := func(cause error) (*ProcessPipes, error) {
		if closeErr := pipes.Close(); closeErr != nil {
			cause = errors.Join(cause, closeErr)
		}
		if cancel != nil {
			cancel()
		}
		return nil, cause
	}
	var err error
	if pipes.stdin, err = cmd.StdinPipe(); err != nil {
		return fail(fmt.Errorf("stdin pipe: %w", err))
	}
	pipes.childPipes = append(pipes.childPipes, cmd.Stdin.(io.Closer))
	if prepared == nil {
		if pipes.stdout, err = cmd.StdoutPipe(); err != nil {
			return fail(fmt.Errorf("stdout pipe: %w", err))
		}
	} else {
		if cmd.Stdout != nil {
			return fail(errors.New("stdout pipe: stdout is already configured"))
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			return fail(fmt.Errorf("stdout pipe: %w", err))
		}
		pipes.stdout, cmd.Stdout = reader, writer
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
	pipes.childPipes = append(pipes.childPipes, writer)
	return pipes, nil
}

// Start runs the prepared process and releases the parent's copies of the child pipes.
func (p *ProcessPipes) Start(clock quartz.Clock) error {
	startErr := p.owner.Start()
	if err := errors.Join(startErr, p.closeChildPipes()); err != nil {
		return err
	}
	if p.prepared != nil {
		go p.closeReadersAfterNativeExit(clock)
	}
	return nil
}

func (p *ProcessPipes) closeReadersAfterNativeExit(clock quartz.Clock) {
	// Wait does not own these independent read ends. A profile or wrapper
	// can leave a writer open after the native shell exits.
	_ = p.owner.Wait()
	timer := clock.NewTimer(stderrDrainWait, "wrapped-process-drain")
	defer timer.Stop("wrapped-process-drain")
	select {
	case <-p.readersClosed:
	case <-timer.C:
		_ = p.closeStdoutReader()
		_ = p.closeStderrReader()
	}
}

// Wait returns the native status without consuming either reader.
func (p *ProcessPipes) Wait() error { return p.owner.Wait() }

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

func (p *ProcessPipes) closeStderrReader() error {
	if p == nil {
		return nil
	}
	p.stderrCloseOnce.Do(func() { p.stderrCloseErr = closeOwnedPipe(p.stderr) })
	return p.stderrCloseErr
}

func (p *ProcessPipes) closeStdoutReader() error {
	p.stdoutCloseOnce.Do(func() { p.stdoutCloseErr = closeOwnedPipe(p.stdout) })
	return p.stdoutCloseErr
}

func (p *ProcessPipes) Close() error {
	if p == nil {
		return nil
	}
	if p.readersClosed != nil {
		p.closeReadersOnce.Do(func() { close(p.readersClosed) })
	}
	return errors.Join(p.closeChildPipes(), closeOwnedPipe(p.stdin), p.closeStdoutReader(),
		p.closeStderrReader(), p.owner.Close(), p.prepared.Close())
}
