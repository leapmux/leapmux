package claudetest

import (
	"context"
	"os"
	"os/exec"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude"
)

// StartEcho starts a registered agent over a plain "cat" process.
// The process echoes every frame that the worker writes.
// It does not implement the Claude Code protocol or require native control registration.
//
// Tests outside the provider use it to observe manager state or writes to stdin.
func StartEcho(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, "cat")
	cmd.Env = os.Environ()
	running, err := claude.StartOverCommand(ctx, cancel, cmd, opts, sink)
	if err != nil {
		return nil, err
	}
	return &echoAgent{running}, nil
}

// echoAgent keeps Claude output handling and the process lifecycle for service tests.
// Raw input goes directly to the transport because cat has no native permission state.
type echoAgent struct{ *claude.Agent }

func (a *echoAgent) SendRawInput(data []byte, stop agent.StopContext) error {
	return a.Process.SendRawInput(data, stop)
}

// StartSilent starts a Claude agent over a process that reads stdin
// and writes NOTHING back.
//
// StartEcho's echo is the reason it exists. That mock returns every
// frame the worker writes, so the worker's own interrupt control_request comes
// back as a control_request FROM the agent and lands as a pending permission
// prompt. A test that observes the derived activity state then reads
// WAITING_FOR_USER for a prompt no agent ever asked -- an output file of the
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
