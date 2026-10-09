package providerkit

import (
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/leapmux/leapmux/util/procutil"
)

// ProcessPipes constructs one command and its owner together. Callers cannot replace either field.
type ProcessPipes struct {
	cmd    *exec.Cmd
	owner  *procutil.ProcessOwner
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
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
	if pipes.stdout, err = cmd.StdoutPipe(); err != nil {
		return fail(fmt.Errorf("stdout pipe: %w", err))
	}
	if pipes.stderr, err = cmd.StderrPipe(); err != nil {
		return fail(fmt.Errorf("stderr pipe: %w", err))
	}
	return pipes, nil
}

func (p *ProcessPipes) Close() error {
	if p == nil {
		return nil
	}
	var failure error
	for _, pipe := range []io.Closer{p.stdin, p.stdout, p.stderr} {
		if pipe != nil {
			failure = errors.Join(failure, pipe.Close())
		}
	}
	return errors.Join(failure, p.owner.Close())
}
