package agent

import (
	"context"
	"encoding/json"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
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
func StartCopilotCLI(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "copilot", nil, []string{"--acp", "--stdio"}, nil, opts.WorkingDir,
	)

	cmd.Env = append(cmd.Environ(), "LEAPMUX_WORKER=1")

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &CopilotCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:            opts.AgentID,
				providerName:       "copilot",
				cmd:                cmd,
				stdin:              stdin,
				ctx:                ctx,
				cancel:             cancel,
				stderrDone:         make(chan struct{}),
				processDone:        make(chan struct{}),
				preambleDelimiter:  preambleDelimiter,
				preambleMetaPrefix: metaPrefix,
				preambleMeta:       make(map[string]string),
				apiTimeout:         opts.apiTimeout(),
			}},
			sink:  sink,
			model: opts.Model,
		},
	}
	a.extraSessionUpdate = configOptionSessionUpdateHandler(a.handleConfigOptionUpdate)
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndPermissionMode
	a.refreshFromSession = a.refreshModelAndPermissionModeFromSession

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	initParams, err := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities":    map[string]interface{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams, acpDefaultSessionConfig)
	if err != nil {
		return nil, err
	}

	a.availableModels = buildACPModels(handshake.Models, handshake.CurrentModelID, nil)
	if a.model == "" && handshake.CurrentModelID != "" {
		a.model = handshake.CurrentModelID
	}

	a.availableModes = buildACPModes(handshake.Modes, handshake.CurrentModeID, nil)
	a.permissionMode = handshake.CurrentModeID
	if a.permissionMode == "" {
		a.permissionMode = CopilotCLIModeAgent
	}

	// Parse Copilot-specific configOptions from the raw session response.
	a.syncConfigOptions(handshake.ConfigOptions)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	if requested := StringOrDefault(opts.PermissionMode, ""); requested != "" && requested != a.permissionMode {
		if err := a.setPermissionMode(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError(acpMethodSessionSetMode, err)
		}
	}
	if requested := StringOrDefault(opts.Model, ""); requested != "" && requested != a.model {
		if err := a.setModel(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError(acpMethodSessionSetModel, err)
		}
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
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
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
