package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
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

	permissionMode string
	availableModes []*leapmuxv1.AvailableOption
}

// StartCursorCLI starts a Cursor CLI ACP agent process and performs the handshake.
func StartCursorCLI(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "cursor-agent", nil, []string{"acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = append(cmd.Environ(), "LEAPMUX_WORKER=1")

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &CursorCLIAgent{
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
			}},
			sink:         sink,
			providerName: "cursor",
			model:        normalizeCursorModelID(opts.Model),
		},
	}
	a.extraSessionUpdate = configOptionSessionUpdateHandler(a.handleConfigOptionUpdate)
	a.extraMethod = a.handleExtraMethod
	a.promptFunc = a.doSendPrompt

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start cursor-agent: %w", err)
	}

	initParams, _ := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities":    map[string]interface{}{},
	})
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams, acpDefaultSessionConfig)
	if err != nil {
		return nil, err
	}

	a.availableModels = buildCursorCLIModels(handshake.Models, handshake.CurrentModelID)
	if a.model == "" && handshake.CurrentModelID != "" {
		a.model = normalizeCursorModelID(handshake.CurrentModelID)
	}

	a.availableModes = buildACPModes(handshake.Modes, handshake.CurrentModeID, nil)
	a.permissionMode = handshake.CurrentModeID
	if a.permissionMode == "" {
		a.permissionMode = CursorCLIModeAgent
	}

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	if requested := normalizeCursorModelID(StringOrDefault(opts.Model, "")); requested != "" && requested != a.model {
		if err := a.setCursorModel(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError(acpMethodSessionSetModel, err)
		}
	}
	if requested := StringOrDefault(opts.PermissionMode, ""); requested != "" && requested != a.permissionMode {
		if err := a.setPermissionMode(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError(acpMethodSessionSetMode, err)
		}
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

func buildCursorCLIModels(models []acpModelInfo, currentModelID string) []*leapmuxv1.AvailableModel {
	currentModelID = normalizeCursorModelID(currentModelID)
	result := make([]*leapmuxv1.AvailableModel, 0, len(models))
	for _, model := range models {
		id := normalizeCursorModelID(model.ModelID)
		if id == "" {
			continue
		}
		name := model.Name
		if name == "" {
			name = id
		}
		result = append(result, &leapmuxv1.AvailableModel{
			Id:          id,
			DisplayName: name,
			Description: model.Description,
			IsDefault:   id == currentModelID,
		})
	}
	return result
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

func (a *CursorCLIAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:          a.model,
		PermissionMode: a.permissionMode,
	}
}

func (a *CursorCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	options := a.availableModes
	if len(options) == 0 {
		options = fallbackCursorCLIModes()
	}
	return []*leapmuxv1.AvailableOptionGroup{{
		Key:     "permissionMode",
		Label:   "Mode",
		Options: options,
	}}
}

func (a *CursorCLIAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	if model := normalizeCursorModelID(s.GetModel()); model != "" {
		if err := a.setCursorModel(model); err != nil {
			slog.Warn("cursor session/set_model failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	if mode := s.GetPermissionMode(); mode != "" {
		if err := a.setPermissionMode(mode); err != nil {
			slog.Warn("cursor session/set_mode failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	return true
}

func (a *CursorCLIAgent) setCursorModel(model string) error {
	wireModel := cursorModelIDForWire(model)
	if err := a.setModel(wireModel); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	return nil
}

func (a *CursorCLIAgent) setPermissionMode(mode string) error {
	a.mu.Lock()
	available := a.availableModes
	a.mu.Unlock()

	if err := a.acpSetMode(mode, available); err != nil {
		return err
	}
	a.mu.Lock()
	a.permissionMode = mode
	a.mu.Unlock()
	return nil
}

var cursorCLIAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: cursorCLIModelAuto, DisplayName: "Auto", Description: "Automatically selects the best available Cursor model", IsDefault: true},
}

func init() {
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR_CLI,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
			return StartCursorCLI(ctx, opts, sink)
		},
		cursorCLIAvailableModels,
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:     "permissionMode",
			Label:   "Mode",
			Options: fallbackCursorCLIModes(),
		}},
		"LEAPMUX_CURSOR_DEFAULT_MODEL",
		"",
		"cursor-agent",
	)
}
