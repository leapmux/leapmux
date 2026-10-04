package worker

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bootstrap"
)

// RunAgentHelper runs a provider helper when a provider's CLI started this
// process as one, and reports whether it did. Every executable that can run the
// worker calls it first in main, before it sets up logging or reads its
// arguments: the helper's stderr reaches the provider's CLI as a message, so a
// log line there would reach it too.
//
// A process is a helper when it starts with NO argument and
// contracts.EnvAgentHelper is set. The provider's CLI starts the helper with no
// argument. The leapmux CLI needs an argument for everything else it does. The
// desktop sidecar starts with no argument too, and the desktop shell removes an
// inherited copy of the variable when it spawns the sidecar, so the executable
// never mistakes either run for a helper.
//
// A helper ends when the process receives an interrupt or a termination
// signal, which is what a CLI sends a helper it no longer waits for.
//
// A run that is not a helper removes the variable from its own environment.
// Every process that the run starts then inherits no spec path of another
// agent: a terminal, a nested worker, and any other child alike.
func RunAgentHelper(args []string, stdin io.Reader, stdout, stderr io.Writer) (exitCode int, handled bool) {
	exitCode, handled = runAgentHelper(args, os.Getenv, stdin, stdout, stderr, bootstrap.RunAgentHelper)
	if !handled {
		_ = os.Unsetenv(contracts.EnvAgentHelper)
	}
	return exitCode, handled
}

// runAgentHelper is RunAgentHelper with the environment and the dispatch
// supplied, so a test can drive the decision without the real registry.
func runAgentHelper(
	args []string,
	getenv func(string) string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	run func(ctx context.Context, specPath string, invocation agent.HelperInvocation) int,
) (int, bool) {
	specPath := getenv(contracts.EnvAgentHelper)
	if len(args) != 0 || specPath == "" {
		return 0, false
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, specPath, agent.HelperInvocation{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Getenv: getenv,
	}), true
}
