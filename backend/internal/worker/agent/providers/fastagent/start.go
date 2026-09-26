package fastagent

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Start starts a fast-agent ACP process and performs the handshake.
//
// The launch takes `acp --model <model> -x --no-permissions`: the ACP server
// entry, the model string the session runs, the local shell runtime that
// exposes the coding tools, and an auto-allow for every permission request the
// tools that DO gate raise. The model is fixed at session creation; fast-agent
// offers no per-session model switch over ACP.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	registration := Registration()
	model := opts.Model()
	if model == "" {
		model = registration.DefaultModel()
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: registration,
		ProviderName: "fastagent",
		BaseArgs:     []string{"--no-update-check", "acp", "--model", model, "-x", "--no-permissions"},
		NewAgent:     func() *Agent { return &Agent{} },
		Base:         func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, _ agent.ProviderServices) acp.Hooks {
			// fast-agent raises no extension method and advertises no steer
			// route. The modes arrive in the session response; the embedded
			// base reads them.
			return acp.Hooks{
				InitialModel: model,
				ModeChannel:  acp.ModeChannelUnmapped,
			}
		},
		// The session's `modes` channel carries the one configured agent mode;
		// applyHandshakeMode stores it on the permission-mode axis, and the
		// requested model is pushed last and best-effort (fast-agent fixes its
		// model at session creation and rejects a live write).
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.FastagentDefaultMode, opts.Model())
		},
	})
}
