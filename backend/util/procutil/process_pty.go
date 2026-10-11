package procutil

import (
	"context"
	"errors"
	"os"
	"slices"

	pty "github.com/aymanbagabas/go-pty"
)

// PTYLaunch supplies the command configuration before the owner starts it.
type PTYLaunch struct {
	Program string
	Args    []string
	Dir     string
	Env     []string
}

type ptyProcessCommand struct {
	command *pty.Cmd
	ctx     context.Context
}

func (c ptyProcessCommand) start() error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	return c.command.Start()
}
func (c ptyProcessCommand) wait() error             { return c.command.Wait() }
func (c ptyProcessCommand) process() *os.Process    { return c.command.Process }
func (c ptyProcessCommand) state() *os.ProcessState { return c.command.ProcessState }

// PreparePTYProcess creates the command and its owner before cancellation can run.
func PreparePTYProcess(ctx context.Context, terminal pty.Pty, launch PTYLaunch) (*ProcessOwner, error) {
	if ctx == nil {
		return nil, errors.New("the pty process context is absent")
	}
	if terminal == nil || launch.Program == "" {
		return nil, errors.New("the pty process requires a terminal and program")
	}
	command := terminal.CommandContext(ctx, launch.Program, slices.Clone(launch.Args)...)
	command.Dir, command.Env = launch.Dir, slices.Clone(launch.Env)
	owner := &ProcessOwner{cmd: ptyProcessCommand{command: command, ctx: ctx}, children: make(map[ProcessIdentity]struct{})}
	command.Cancel = owner.Cancel
	return owner, nil
}
