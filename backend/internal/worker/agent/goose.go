package agent

import (
	"context"
	"encoding/json"

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
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		BinaryName: "goose",
		BaseArgs:   []string{"acp"},
		WorkingDir: opts.WorkingDir,
	})

	cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &GooseCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "goose", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
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

	if err := a.applyPermissionModeStartup(handshake, opts, GooseCLIModeAuto, opts.Model, a.setModel); err != nil {
		return nil, err
	}

	return a, nil
}

func fallbackGooseCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: GooseCLIModeAuto, Name: "Auto", IsDefault: true},
		{Id: GooseCLIModeApprove, Name: "Approve"},
		{Id: GooseCLIModeSmartApprove, Name: "Smart Approve"},
		{Id: GooseCLIModeChat, Name: "Chat"},
	}
}

func (a *GooseCLIAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
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
