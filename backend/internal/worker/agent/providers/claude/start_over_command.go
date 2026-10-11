package claude

import (
	"context"
	"os/exec"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// StartOverCommand builds an Agent around a command the caller
// prepared instead of the Claude CLI: it wires stdin/stdout, starts the process,
// and spawns the output reader loop. cmd must be bound to ctx through
// exec.CommandContext, and cancel must be ctx's own, so a start failure can
// release the context.
//
// It exists for test support that stands a mock process in for the CLI -- a
// process that echoes, or one that answers the Claude protocol from a script.
// It performs none of the launch handshake Start performs, so the
// agent it returns has negotiated nothing with the process.
func StartOverCommand(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, opts agent.Options, sink agent.ProviderServices) (*Agent, error) {
	cmd.Dir = opts.WorkingDir

	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	stdout := pipes.Stdout()

	a := &Agent{
		Process: providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "claude", ShutdownGrace: Registration().ShutdownGrace}, pipes, ctx, cancel),
		// Mirror Start: a.model is initialized from the normalized launch
		// model, not the raw stored value, so the mock keeps the same "a.model lives
		// in the normalized alias space" invariant (e.g. a stored "opus" becomes
		// "opus[1m]") that the real launch path establishes.
		model:          normalizeClaudeCodeModel(opts.Model()),
		sessionID:      opts.ResumeSessionID,
		workingDir:     opts.WorkingDir,
		homeDir:        opts.HomeDir,
		sink:           sink,
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	a.DrainStderr(pipes.Stderr())

	if err := a.StartCmd(); err != nil {
		cancel()
		return nil, err
	}

	scanner := agent.NewStdoutScanner(stdout)
	go a.readOutputLoop(scanner)

	return a, nil
}
