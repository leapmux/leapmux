package agent

import (
	"context"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	reasonixSteerNamespace = "reasonix.io"
	reasonixSteerMethod    = "_reasonix.io/session/steer"
)

// ReasonixAgent manages a Reasonix Agent Client Protocol (ACP) process.
type ReasonixAgent struct {
	acpBase
}

func (a *ReasonixAgent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.steerAdvertised(content, attachments)
}

// StartReasonix starts a Reasonix ACP agent process and performs the handshake.
//
// The launch flag selects the initial model. The session response supplies
// the live model catalog and mutable settings.
func StartReasonix(ctx context.Context, opts Options, sink ProviderServices) (Agent, error) {
	model := opts.Model()
	if model == "" {
		model = DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX)
	}
	return acpStart(ctx, opts, sink, acpStartSpec[ReasonixAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		providerName: "reasonix",
		binaryName:   "reasonix",
		baseArgs:     []string{"acp", "--model", model},
		newAgent:     func() *ReasonixAgent { return &ReasonixAgent{} },
		base:         func(a *ReasonixAgent) *acpBase { return &a.acpBase },
		configure: func(a *ReasonixAgent) {
			transcript := newReasonixToolTranscript(ctx, a.sink, a.currentSessionID, opts.WorkingDir)
			a.sink = transcript
			a.clearProviderState = transcript.reset
			a.advertisedSteerMethod = func(response []byte) string {
				return parseACPAdvertisedMethod(response, reasonixSteerNamespace, reasonixSteerMethod)
			}
			// Keep the launch selection until the session reports its current model.
			a.model = model
			a.modeChannel = modeChannelPermissionMode
			a.clientCapabilityMeta = map[string]any{"reasonix.io": map[string]any{"mcpInteraction": map[string]any{"supported": true, "schemaVersion": 1}}}
			// Task launches open no span. Final updates close the task registry row.
			a.subagentFromToolCall = reasonixSubagentFromToolCall
			a.subagentFromToolCallUpdate = reasonixSubagentFromToolCallUpdate
			// Reasonix sends goal state outside standard ACP session updates.
			a.extraMethod = a.handleExtraMethod
		},
		afterHandshake: func(a *ReasonixAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, contracts.ReasonixModeNormal, model)
		},
	})
}

// reasonixAvailableModels is the static catalog of Reasonix's built-in provider
// entries (reasonix/internal/config/config.go). The id is the provider-entry
// name Reasonix's `--model` flag accepts (cfg.ResolveModel resolves it to the
// concrete model). deepseek-flash is Reasonix's own default_model.
var reasonixAvailableModels = []*ModelInfo{
	{Id: "deepseek-flash", DisplayName: "DeepSeek Flash", Description: "Fast, economical DeepSeek model", IsDefault: true, ContextWindow: 1_000_000},
	{Id: "deepseek-pro", DisplayName: "DeepSeek Pro", Description: "Most capable DeepSeek model for complex work", ContextWindow: 1_000_000},
	{Id: "mimo-pro", DisplayName: "MiMo Pro", Description: "Xiaomi MiMo, most capable (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
	{Id: "mimo-flash", DisplayName: "MiMo Flash", Description: "Xiaomi MiMo, fast and economical (requires MIMO_API_KEY)", ContextWindow: 1_000_000},
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
		StartReasonix,
		reasonixAvailableModels,
		nil, // The session supplies available modes and config options.
		"LEAPMUX_REASONIX_DEFAULT_MODEL",
		"",
		"reasonix",
	)
	setAdditionalOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, OptionIDPermissionMode, OptionIDEffort, contracts.ReasonixConfigToolApproval)
	setPermissionDefaults(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, PermissionDefaults{Fallback: contracts.ReasonixModeNormal})
}
