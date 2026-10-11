package procutil

import (
	"os"
	"os/exec"
)

// processCommand keeps every operation on the same private command instance.
type processCommand interface {
	start() error
	wait() error
	process() *os.Process
	state() *os.ProcessState
}

type execProcessCommand struct{ command *exec.Cmd }

func (c execProcessCommand) start() error            { return c.command.Start() }
func (c execProcessCommand) wait() error             { return c.command.Wait() }
func (c execProcessCommand) process() *os.Process    { return c.command.Process }
func (c execProcessCommand) state() *os.ProcessState { return c.command.ProcessState }
