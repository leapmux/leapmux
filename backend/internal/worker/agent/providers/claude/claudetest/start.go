package claudetest

import (
	"context"
	"os"
	"os/exec"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude"
)

// StartEcho starts a Claude agent over a plain "cat" process: a
// running agent the Manager registers, which echoes every frame the worker
// writes. It does not implement the Claude Code protocol.
//
// It is for tests outside the provider that need a running agent registered in
// the manager (e.g. to make HasAgent return true) or that observe what the
// worker wrote to stdin.
func StartEcho(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, "cat")
	cmd.Env = os.Environ()
	return claude.StartOverCommand(ctx, cancel, cmd, opts, sink)
}

// StartSilent starts a Claude agent over a process that reads stdin
// and writes NOTHING back.
//
// StartEcho's echo is the reason it exists. That mock returns every
// frame the worker writes, so the worker's own interrupt control_request comes
// back as a control_request FROM the agent and lands as a pending permission
// prompt. A test that observes the derived activity state then reads
// WAITING_FOR_USER for a prompt no agent ever asked -- an artifact of the
// harness, and one that arrives asynchronously, so it cannot even be waited out.
//
// Use this one to observe state, and StartEcho to observe what the
// worker wrote to stdin. The process still drains stdin, so every write the
// worker makes succeeds.
func StartSilent(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, "sh", "-c", "cat > /dev/null")
	cmd.Env = os.Environ()
	return claude.StartOverCommand(ctx, cancel, cmd, opts, sink)
}
