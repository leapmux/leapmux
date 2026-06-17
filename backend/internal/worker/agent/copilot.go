package agent

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	CopilotCLIModeAgent     = "https://agentclientprotocol.com/protocol/session-modes#agent"
	CopilotCLIModePlan      = "https://agentclientprotocol.com/protocol/session-modes#plan"
	CopilotCLIModeAutopilot = "https://agentclientprotocol.com/protocol/session-modes#autopilot"
)

// Copilot's server-driven ACP config-option ids (surfaced as mutable option groups,
// not static templates). Declared in KnownOptionIDs so a not-running agent validates
// them, matching the ids the live `session/set_config_option` channel reports.
const (
	CopilotConfigReasoningEffort = "reasoning_effort"
	CopilotConfigAllowAll        = "allow_all"
)

// CopilotCLIAgent manages a single Copilot CLI ACP process.
type CopilotCLIAgent struct {
	acpBase
}

// StartCopilotCLI starts a Copilot CLI ACP agent process and performs the handshake.
func StartCopilotCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[CopilotCLIAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		providerName: "copilot",
		binaryName:   "copilot",
		baseArgs:     []string{"--acp", "--stdio"},
		newAgent:     func() *CopilotCLIAgent { return &CopilotCLIAgent{} },
		base:         func(a *CopilotCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *CopilotCLIAgent) {
			a.modeChannel = modeChannelPermissionMode
			// Copilot's reasoning-effort axis is the convention id "reasoning_effort", not the
			// well-known "effort" -- declare it so the env-effort override maps onto it.
			a.effortConfigID = CopilotConfigReasoningEffort
		},
		afterHandshake: func(a *CopilotCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, CopilotCLIModeAgent, opts.Model())
		},
	})
}

func fallbackCopilotCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent"},
		{Id: CopilotCLIModePlan, Name: "Plan"},
		{Id: CopilotCLIModeAutopilot, Name: "Autopilot"},
	}
}

func init() {
	// model + permissionMode (static group) + Copilot's server-driven config options. Copilot
	// has no well-known "effort" axis -- its reasoning axis is the config option
	// "reasoning_effort" -- so `--effort` against Copilot is correctly treated as foreign.
	registerPermissionModeConfigProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		StartCopilotCLI,
		fallbackCopilotCLIModes(),
		"LEAPMUX_COPILOT_DEFAULT_MODEL", "copilot",
		CopilotConfigReasoningEffort, CopilotConfigAllowAll,
	)
}
