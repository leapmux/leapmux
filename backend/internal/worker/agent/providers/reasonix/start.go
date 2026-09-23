package reasonix

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

var _ agent.StartFunc = Start

// Start starts a Reasonix ACP agent process and performs the handshake.
//
// The launch flag selects the initial model. The session response supplies
// the live model catalog and mutable settings.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	registration := Registration()
	model := opts.Model()
	if model == "" {
		model = registration.DefaultModel()
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Registration: registration,
		ProviderName: "reasonix",
		BaseArgs:     []string{"acp", "--model", model},
		NewAgent:     func() *Agent { return &Agent{} },
		Base:         func(a *Agent) *acp.Base { return &a.Base },
		Configure: func(a *Agent, sink agent.ProviderServices) acp.Hooks {
			storeQuery := agent.StoredSessionQuery{HomeDir: opts.HomeDir, WorkingDir: opts.WorkingDir}
			transcript := newReasonixToolTranscript(ctx, sink, a.CurrentSessionID, storeQuery, opts.WorkingDir)
			return acp.Hooks{
				Sink:               transcript,
				ClearProviderState: transcript.Reset,
				AdvertisedSteerMethod: func(response []byte) string {
					return acp.ParseAdvertisedMethod(response, reasonixSteerNamespace, reasonixSteerMethod)
				},
				// Keep the launch selection until the session reports its current model.
				InitialModel:         model,
				ModeChannel:          acp.ModeChannelPermissionMode,
				ClientCapabilityMeta: map[string]any{"reasonix.io": map[string]any{"mcpInteraction": map[string]any{"supported": true, "schemaVersion": 1}}},
				// Task launches open no span. Final updates close the task registry row.
				SubagentFromToolCall:       reasonixSubagentFromToolCall,
				SubagentFromToolCallUpdate: reasonixSubagentFromToolCallUpdate,
				// Reasonix sends goal state outside standard ACP session updates.
				ExtraMethod: a.handleExtraMethod,
			}
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.ReasonixModeNormal, model)
		},
	})
}
