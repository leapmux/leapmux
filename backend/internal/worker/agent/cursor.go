package agent

import (
	"context"
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	CursorCLIModeAgent = "agent"
	CursorCLIModePlan  = "plan"
	CursorCLIModeAsk   = "ask"

	cursorCLIModelAuto     = "auto"
	cursorCLIModelAutoWire = "default[]"
)

// CursorCLIAgent manages a single Cursor CLI ACP process.
type CursorCLIAgent struct {
	acpBase
}

// StartCursorCLI starts a Cursor CLI ACP agent process and performs the handshake.
func StartCursorCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "cursor-agent", nil, []string{"acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &CursorCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "cursor", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
			sink:        sink,
			model:       normalizeCursorModelID(opts.Model),
		},
	}
	a.modelIDNormalizer = normalizeCursorModelID
	a.modeChannel = modeChannelPermissionMode
	a.extraMethod = a.handleExtraMethod
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndMode
	a.refreshFromSession = a.refreshCursorFromSession

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

	if err := a.applyPermissionModeStartup(handshake, opts, CursorCLIModeAgent, normalizeCursorModelID(opts.Model), a.setCursorModel); err != nil {
		return nil, err
	}

	return a, nil
}

func normalizeCursorModelID(model string) string {
	if model == cursorCLIModelAutoWire {
		return cursorCLIModelAuto
	}
	return model
}

func cursorModelIDForWire(model string) string {
	if model == cursorCLIModelAuto {
		return cursorCLIModelAutoWire
	}
	return model
}

func fallbackCursorCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: CursorCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CursorCLIModePlan, Name: "Plan"},
		{Id: CursorCLIModeAsk, Name: "Ask"},
	}
}

func (a *CursorCLIAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
}

func (a *CursorCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.permissionModeOptionGroups("Mode", fallbackCursorCLIModes())
}

func (a *CursorCLIAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	ok := acpApplySetting(a.providerName, a.agentID, "model", normalizeCursorModelID(s.GetModel()), a.setCursorModel)
	ok = acpApplySetting(a.providerName, a.agentID, "mode", s.GetPermissionMode(), a.setPermissionMode) && ok
	return ok
}

func (a *CursorCLIAgent) setCursorModel(model string) error {
	// Send the wire id but store the normalized (display) id, so b.model never
	// transiently holds the wire form "default[]" (see setModelRPC).
	if err := a.setModelRPC(cursorModelIDForWire(model)); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	return nil
}

// reapplyModelAndMode re-applies the current model and permission mode
// after a session/new. Uses setCursorModel for the model-ID wire format.
func (a *CursorCLIAgent) reapplyModelAndMode() {
	a.reapplyModelAndSecondary(&a.permissionMode, "mode", a.setCursorModel, a.setPermissionMode)
}

// refreshCursorFromSession refreshes model (with Cursor-specific
// normalization) and permission mode.
func (a *CursorCLIAgent) refreshCursorFromSession(resp json.RawMessage) {
	a.applySessionRefresh(resp, normalizeCursorModelID, &a.permissionMode, "mode")
}

var cursorCLIAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: cursorCLIModelAuto, DisplayName: "Auto", Description: "Automatically selects the best available Cursor model", IsDefault: true},
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartCursorCLI(ctx, opts, sink)
		},
		cursorCLIAvailableModels,
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:     OptionGroupKeyPermissionMode,
			Label:   "Mode",
			Options: fallbackCursorCLIModes(),
		}},
		"LEAPMUX_CURSOR_DEFAULT_MODEL",
		"",
		"cursor-agent",
	)
}
