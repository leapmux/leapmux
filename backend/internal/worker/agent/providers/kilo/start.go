package kilo

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

// kiloQuestionToolEnv is Kilo's spelling of openCodeQuestionToolEnv: the flag
// that turns ON the daemon's own question tool, which the question bridge in
// opencode/questions.go answers. Kilo keeps OpenCode's gate, so the reason to pin
// it is the same.
const kiloQuestionToolEnv = "KILO_ENABLE_QUESTION_TOOL"

// kiloDisableAutoUpdateEnv is Kilo's spelling of openCodeDisableAutoUpdateEnv.
// Kilo's TUI installs a patch release through npm, yarn, pnpm or bun after the
// same one-second delay, and `kilo acp` never starts the check.
const kiloDisableAutoUpdateEnv = "KILO_DISABLE_AUTOUPDATE"

var _ agent.StartFunc = Start

// Start starts a Kilo ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	// kiloLaunchEnv below refuses the same invalid value, before the process starts.
	swarm, err := kiloSwarmFromOptions(opts.Options)
	if err != nil {
		return nil, fmt.Errorf("kilo swarm: %w", err)
	}
	var started *Agent
	create := func() *Agent {
		started = newAgent(swarm)
		return started
	}
	ag, err := opencode.StartFamily(ctx, opts, sink, Registration(), opencode.FamilySpec{
		ProviderName:   "kilo",
		RCMarkerEnvKey: "KILO_CLIENT",
		// Kilo forks OpenCode and requires its own spelling of the flag for the same
		// tool; see kiloQuestionToolEnv. Its allowed client list adds `vscode`, and
		// its own `kilo acp` handler assigns `KILO_CLIENT="acp"` the same way, so the
		// flag is the only route here too.
		QuestionToolEnv:      kiloQuestionToolEnv,
		DisableAutoUpdateEnv: kiloDisableAutoUpdateEnv,
		DefaultPrimaryAgent:  PrimaryAgentCode,
		LaunchEnv:            kiloLaunchEnv,
	}, create, func(a *Agent) *opencode.FamilyBase { return &a.FamilyBase })
	if err != nil {
		return nil, err
	}
	// The loop ends with the process. A request that the daemon address made
	// before the loop started waits in the channel.
	go started.refreshUsageLoop()
	return ag, nil
}
