package agent

import (
	"context"
	"encoding/json"

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
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "copilot", nil, []string{"--acp", "--stdio"}, nil, opts.WorkingDir,
	)

	cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &CopilotCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "copilot", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
			sink:        sink,
			model:       opts.Model,
		},
	}
	a.modeChannel = modeChannelPermissionMode
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndPermissionMode
	a.refreshFromSession = a.refreshModelAndPermissionModeFromSession

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	initParams, err := acpStandardInitParams()
	if err != nil {
		return nil, err
	}
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams, acpDefaultSessionConfig)
	if err != nil {
		return nil, err
	}

	if err := a.applyPermissionModeStartup(handshake, opts, CopilotCLIModeAgent, opts.Model, a.setModel); err != nil {
		return nil, err
	}

	return a, nil
}

func fallbackCopilotCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CopilotCLIModePlan, Name: "Plan"},
		{Id: CopilotCLIModeAutopilot, Name: "Autopilot"},
	}
}

func (a *CopilotCLIAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
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
