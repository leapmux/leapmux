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

// CopilotCLIAgent manages a single Copilot CLI ACP process.
type CopilotCLIAgent struct {
	acpBase
}

// StartCopilotCLI starts a Copilot CLI ACP agent process and performs the handshake.
func StartCopilotCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[CopilotCLIAgent]{
		providerName: "copilot",
		binaryName:   "copilot",
		baseArgs:     []string{"--acp", "--stdio"},
		newAgent:     func() *CopilotCLIAgent { return &CopilotCLIAgent{} },
		base:         func(a *CopilotCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *CopilotCLIAgent) {
			a.modeChannel = modeChannelPermissionMode
			a.reapplySettings = a.reapplyModelAndPermissionMode
			a.refreshFromSession = a.refreshModelAndPermissionModeFromSession
		},
		afterHandshake: func(a *CopilotCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, CopilotCLIModeAgent, opts.Model, a.setModel)
		},
	})
}

func fallbackCopilotCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CopilotCLIModePlan, Name: "Plan"},
		{Id: CopilotCLIModeAutopilot, Name: "Autopilot"},
	}
}

func (a *CopilotCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.permissionModeOptionGroups("Mode", fallbackCopilotCLIModes())
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartCopilotCLI(ctx, opts, sink)
		},
		nil, // models discovered dynamically from session/new
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:     OptionGroupKeyPermissionMode,
			Label:   "Mode",
			Options: fallbackCopilotCLIModes(),
		}},
		"LEAPMUX_COPILOT_DEFAULT_MODEL",
		"",
		"copilot",
	)
}
