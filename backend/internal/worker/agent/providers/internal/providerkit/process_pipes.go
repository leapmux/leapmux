package providerkit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/leapmux/leapmux/util/procutil"
)

// ProcessPipes constructs one command and its owner together. Callers cannot replace either field.
type ProcessPipes struct {
	cmd             *exec.Cmd
	owner           *procutil.ProcessOwner
	stdin           io.WriteCloser
	stdout          io.ReadCloser
	stderr          io.ReadCloser
	childPipes      []io.Closer
	childCloseOnce  sync.Once
	childCloseErr   error
	stderrCloseOnce sync.Once
	stderrCloseErr  error
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
	if cmd == nil {
		if cancel != nil {
			cancel()
		}
		return nil, errors.New("the process command is absent")
	}
	pipes := &ProcessPipes{cmd: cmd, owner: procutil.PrepareProcessWithExitObserver(cmd, observer)}
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
	if pipes.stdout, err = cmd.StdoutPipe(); err != nil {
		return fail(fmt.Errorf("stdout pipe: %w", err))
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

func (p *ProcessPipes) Close() error {
	if p == nil {
		return nil
	}
	failure := p.closeChildPipes()
	for _, pipe := range []io.Closer{p.stdin, p.stdout} {
		failure = errors.Join(failure, closeOwnedPipe(pipe))
	}
	return errors.Join(failure, p.closeStderrReader(), p.owner.Close())
}
