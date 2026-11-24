package main

import (
	"os"
	"os/exec"
	"os/signal"

	"github.com/leapmux/leapmux/internal/cli"
)

// execControlCLI runs target with the standard streams of this process, waits
// for it, and returns its exit code. Windows cannot replace a running process
// with another program, so this process stays until target ends.
//
// It returns an error only when target did not start. After target started,
// running the command a second time in this process could repeat what target
// did, so a failure to read how target ended becomes exit code 1.
//
// A Ctrl+C reaches every process of the console. target receives its own and
// decides how to end. This process handles the signal and does nothing, so it
// stays to report the exit code of target; without a handler, Windows would
// end it at once.
func execControlCLI(target string, argv, env []string) (int, error) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	cmd := &exec.Cmd{Path: target, Args: argv, Env: env, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	if err := cmd.Wait(); err != nil && cmd.ProcessState == nil {
		(&cli.Printer{Err: os.Stderr}).Notice("error: cannot read how the Worker's CLI %s ended: %v", target, err)
		return 1, nil
	}
	return cmd.ProcessState.ExitCode(), nil
}
