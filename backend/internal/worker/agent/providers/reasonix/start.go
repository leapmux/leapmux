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
				WithBypassPermission: func(claim func()) {
					a.WithOptionValue(contracts.ReasonixConfigToolApproval, contracts.ReasonixApprovalYolo, claim)
				},
				PermissionAllowsBypass: permissionAllowsBypass,
				Sink:                   transcript,
				ClearProviderState: func() {
					transcript.Reset()
					a.resetPlanDraft()
				},
				AdvertisedSteerMethod: func(response []byte) string {
					return acp.ParseAdvertisedMethod(response, reasonixSteerNamespace, reasonixSteerMethod)
				},
				// Keep the launch selection until the session reports its current model.
				InitialModel:         model,
				ModeChannel:          acp.ModeChannelPermissionMode,
				InitializeResponse:   a.observeReasonixCapabilities,
				ClientCapabilityMeta: map[string]any{"reasonix.io": map[string]any{"statusUpdates": map[string]any{"supported": true}}},
				// Task launches open no span. Final updates close the task registry row.
				SubagentFromToolCall:       reasonixSubagentFromToolCall,
				SubagentFromToolCallUpdate: reasonixSubagentFromToolCallUpdate,
				// Reasonix sends goal state outside standard ACP session updates.
				ExtraMethod:            a.handleExtraMethod,
				ControlRequestObserver: a.observeControlRequest,
				// A notice chunk splits from the model's own stream by its
				// prefix.
				SessionMetadataHandler: a.observeSessionMetadata,
			}
		},
		AfterHandshake: func(a *Agent, handshake *acp.SessionResult, opts agent.Options) error {
			requestedModel := model
			// The CLI already selects a new session's model. Its reply gives the canonical
			// selector value, which can differ from the launch alias. A loaded session
			// still needs the requested model applied after it restores its saved selection.
			if opts.ResumeSessionID == "" && handshake.CurrentModelID != "" {
				requestedModel = handshake.CurrentModelID
			}
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.ReasonixModeNormal, requestedModel)
		},
	})
}
