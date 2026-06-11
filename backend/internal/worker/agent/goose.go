package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	GooseCLIModeAuto         = "auto"
	GooseCLIModeApprove      = "approve"
	GooseCLIModeSmartApprove = "smart_approve"
	GooseCLIModeChat         = "chat"
)

// Goose's server-driven ACP config-option ids (surfaced as mutable option groups,
// not static templates). Declared in KnownOptionIDs so a not-running agent validates
// them, matching the ids the live `session/set_config_option` channel reports. Goose
// has no well-known "effort" axis -- its reasoning axis is the config option "thinking_effort".
const (
	GooseConfigThinkingEffort = "thinking_effort"
	GooseConfigProvider       = "provider"
)

// GooseCLIAgent manages a single Goose CLI ACP process.
type GooseCLIAgent struct {
	acpBase
}

// StartGooseCLI starts a Goose CLI ACP agent process and performs the handshake.
func StartGooseCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[GooseCLIAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		providerName: "goose",
		binaryName:   "goose",
		baseArgs:     []string{"acp"},
		newAgent:     func() *GooseCLIAgent { return &GooseCLIAgent{} },
		base:         func(a *GooseCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *GooseCLIAgent) {
			a.modeChannel = modeChannelPermissionMode
			// Goose's reasoning-effort axis is the convention id "thinking_effort", not the
			// well-known "effort" -- declare it so the env-effort override maps onto it.
			a.effortConfigID = GooseConfigThinkingEffort
		},
		afterHandshake: func(a *GooseCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, GooseCLIModeAuto, opts.Model())
		},
	})
}

func fallbackGooseCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: GooseCLIModeAuto, Name: "Auto"},
		{Id: GooseCLIModeApprove, Name: "Approve"},
		{Id: GooseCLIModeSmartApprove, Name: "Smart Approve"},
		{Id: GooseCLIModeChat, Name: "Chat"},
	}
}

func init() {
	// model + permissionMode (static group) + Goose's server-driven config options.
	registerPermissionModeConfigProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		StartGooseCLI,
		fallbackGooseCLIModes(),
		"LEAPMUX_GOOSE_DEFAULT_MODEL", "goose",
		GooseConfigThinkingEffort, GooseConfigProvider,
	)
}
