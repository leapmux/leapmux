package reasonix

import (
	"context"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const (
	reasonixSteerNamespace = "reasonix.io"
	reasonixSteerMethod    = "_reasonix.io/session/steer"
)

// Agent manages a Reasonix Agent Client Protocol (ACP) process.
type Agent struct {
	acp.Base
	goalStatusMu       sync.Mutex
	goalStatusRevision uint64
	// lastStatusPhase is the phase the last status update reported. Reasonix
	// restates its WHOLE status on every change, so the same phase arrives many
	// times per turn and only a move to a new one says anything.
	//
	// No mutex guards it. The stdout reader goroutine owns it: handleOutput
	// dispatches every notification, reportReasonixPhase is the one writer, and
	// handleReasonixStatusUpdate is its one caller. The phase report runs outside
	// goalStatusMu on purpose, because it writes a row and the lock protects the
	// goal revision alone.
	lastStatusPhase string
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.SteerAdvertised(content, attachments)
}

// Start starts a Reasonix ACP agent process and performs the handshake.
//
// The launch flag selects the initial model. The session response supplies
// the live model catalog and mutable settings.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	model := opts.Model()
	if model == "" {
		model = Registration().DefaultModel()
	}
	return acp.Start(ctx, opts, sink, acp.StartSpec[Agent]{
		Provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		Locator:      reasonixLocator,
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

// reasonixAvailableModels is the static catalog of Reasonix's built-in provider
// entries (reasonix/internal/config/config.go). The id is the provider-entry
// name Reasonix's `--model` flag accepts (cfg.ResolveModel resolves it to the
// concrete model). deepseek-flash is Reasonix's own default_model.
var reasonixAvailableModels = []*agent.ModelInfo{
	{Id: "deepseek-flash", DisplayName: "DeepSeek Flash", Description: "Fast, economical DeepSeek model", IsDefault: true, ContextWindow: 1_000_000},
	{Id: "deepseek-pro", DisplayName: "DeepSeek Pro", Description: "Most capable DeepSeek model for complex work", ContextWindow: 1_000_000},
	{Id: "mimo-pro", DisplayName: "MiMo Pro", Description: "Xiaomi MiMo, most capable (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
	{Id: "mimo-flash", DisplayName: "MiMo Flash", Description: "Xiaomi MiMo, fast and economical (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
}

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// reasonixLocator finds the Reasonix CLI on the user's PATH.
var reasonixLocator = launch.Binaries("reasonix")

// Registration states everything the worker knows about Reasonix before
// any of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		Plugin:        reasonixProvider{},
		Start:         Start,
		Locator:       reasonixLocator,
		DefaultModels: reasonixAvailableModels,
		// The session supplies available modes and config options.
		OptionGroups:        nil,
		AdditionalOptionIDs: []string{agent.OptionIDPermissionMode, agent.OptionIDEffort, contracts.ReasonixConfigToolApproval},
		PermissionDefaults:  agent.PermissionDefaults{Fallback: contracts.ReasonixModeNormal},
		EnvModelKey:         "LEAPMUX_REASONIX_DEFAULT_MODEL",
	}
}
