package reasonix

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

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
				ModeSetter:           a.setReasonixMode,
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
			} else if opts.ResumeSessionID != "" && !slices.ContainsFunc(handshake.Models, func(info acp.ModelInfo) bool { return info.ModelID == model }) {
				var err error
				handshake, err = a.resumeReasonixModel(handshake, model, opts.WorkingDir)
				if err != nil {
					return err
				}
				// A peer can start work during restore. Apply the snapshot, then the buffered
				// native updates, without a second model write that could cancel a queued change.
				requestedModel = ""
			}
			return a.ApplyPermissionModeStartup(handshake, opts, contracts.ReasonixModeNormal, requestedModel)
		},
	})
}

// resumeReasonixModel lets the native resolver interpret a launch alias after restore.
// The selector catalog omits provider defaults and alias-folding rules.
func (a *Agent) resumeReasonixModel(handshake *acp.SessionResult, model, workingDir string) (*acp.SessionResult, error) {
	updated := handshake
	err := a.WithSessionID(func(sessionID string) error {
		params, err := json.Marshal(map[string]string{"sessionId": sessionID, "modelId": model})
		if err != nil {
			return err
		}
		if _, err := a.SendRequest(acp.MethodSessionSetModel, params, a.APITimeout()); err != nil {
			slog.Warn("requested Reasonix launch model not applied; keeping current model", "requested", model, "error", err)
			return nil
		}
		params, err = json.Marshal(map[string]string{"sessionId": sessionID, "cwd": workingDir})
		if err != nil {
			return err
		}
		raw, err := a.SendRequest(acp.MethodSessionResume, params, a.APITimeout())
		if err != nil {
			return fmt.Errorf("read Reasonix settings after model selection: %w", err)
		}
		updated, err = acp.ParseSessionResult(raw)
		if err != nil {
			return fmt.Errorf("read Reasonix settings after model selection: %w", err)
		}
		if updated.CurrentModelID == "" {
			return fmt.Errorf("the Reasonix settings snapshot has no current model")
		}
		return nil
	})
	return updated, err
}
