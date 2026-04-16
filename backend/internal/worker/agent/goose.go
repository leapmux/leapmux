package agent

import (
	"context"
	"encoding/json"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
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
func StartGooseCLI(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "goose", nil, []string{"acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = append(cmd.Environ(), "LEAPMUX_WORKER=1")

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &GooseCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
				agentID:            opts.AgentID,
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
			sink:         sink,
			providerName: "goose",
			model:        opts.Model,
		},
	}
	a.extraSessionUpdate = configOptionSessionUpdateHandler(a.handleConfigOptionUpdate)
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndPermissionMode
	a.refreshFromSession = a.refreshModelAndPermissionModeFromSession

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start goose: %w", err)
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
		a.permissionMode = GooseCLIModeAuto
	}

	// Parse Goose-specific configOptions from the raw session response.
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
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
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
