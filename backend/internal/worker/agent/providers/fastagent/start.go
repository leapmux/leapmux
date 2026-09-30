package fastagent

import (
	"context"
	"encoding/json"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// Start starts a fast-agent ACP process and performs the handshake.
//
// The launch takes `acp --model <model> -x`: the ACP server entry, the model
// string the session runs, and the local shell runtime that exposes coding
// tools. Fast Agent's ACP permission handler asks before tool execution. The
// model is fixed at session creation; fast-agent offers no live model switch.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	registration := Registration()
	model := opts.Model()
	if model == "" {
		model = registration.DefaultModel()
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: registration,
		ProviderName: "fastagent",
		BaseArgs:     []string{"--no-update-check", "acp", "--model", model, "-x", "--subagents"},
		NewAgent: func() *Agent {
			return &Agent{home: fastagentHome(agent.StoredSessionQuery{WorkingDir: opts.WorkingDir, HomeDir: opts.HomeDir}), clock: quartz.NewReal()}
		},
		Base: func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			return a.hooks(model, sink)
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

func (a *Agent) hooks(model string, sink agent.ProviderServices) acp.Hooks {
	// Fast Agent raises no steer method. Its mode list arrives in the session
	// response, and its usage status arrives on the outer notification metadata.
	return acp.Hooks{
		InitialModel:               model,
		ModeChannel:                acp.ModeChannelUnmapped,
		SubagentFromToolCall:       a.subagentFromToolCall,
		SubagentFromToolCallUpdate: a.subagentFromToolCallUpdate,
		ChildUpdateRoute:           a.childUpdateRoute,
		ChildUserMessages:          true,
		ClearProviderState:         a.clearChildState,
		BeforeWaitCleanup:          a.archiveWG.Wait,
		PromptEnded:                a.retryChildArchives,
		SessionNotificationMetadata: func(metadata map[string]json.RawMessage) {
			broadcastFastagentStatusLine(sink, metadata)
		},
	}
}
