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

// GooseCLIAgent manages a single Goose CLI ACP process.
type GooseCLIAgent struct {
	acpBase
}

// StartGooseCLI starts a Goose CLI ACP agent process and performs the handshake.
func StartGooseCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[GooseCLIAgent]{
		providerName: "goose",
		binaryName:   "goose",
		baseArgs:     []string{"acp"},
		newAgent:     func() *GooseCLIAgent { return &GooseCLIAgent{} },
		base:         func(a *GooseCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *GooseCLIAgent) {
			a.modeChannel = modeChannelPermissionMode
			a.reapplySettings = a.reapplyModelAndPermissionMode
			a.refreshFromSession = a.refreshModelAndPermissionModeFromSession
		},
		afterHandshake: func(a *GooseCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, GooseCLIModeAuto, opts.Model, a.setModel)
		},
	})
}

func fallbackGooseCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: GooseCLIModeAuto, Name: "Auto", IsDefault: true},
		{Id: GooseCLIModeApprove, Name: "Approve"},
		{Id: GooseCLIModeSmartApprove, Name: "Smart Approve"},
		{Id: GooseCLIModeChat, Name: "Chat"},
	}
}

func (a *GooseCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.permissionModeOptionGroups("Mode", fallbackGooseCLIModes())
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartGooseCLI(ctx, opts, sink)
		},
		nil, // models discovered dynamically from session/new
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:     OptionGroupKeyPermissionMode,
			Label:   "Mode",
			Options: fallbackGooseCLIModes(),
		}},
		"LEAPMUX_GOOSE_DEFAULT_MODEL",
		"",
		"goose",
	)
}
