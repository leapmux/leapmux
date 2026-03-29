package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	GeminiCLIModeDefault  = "default"
	GeminiCLIModeAutoEdit = "autoEdit"
	GeminiCLIModeYolo     = "yolo"
	GeminiCLIModePlan     = "plan"
)

// GeminiCLIAgent manages a single Gemini CLI ACP process.
type GeminiCLIAgent struct {
	acpBase

	model          string
	permissionMode string

	availableModels []*leapmuxv1.AvailableModel
	availableModes  []*leapmuxv1.AvailableOption
}

// StartGeminiCLI starts a Gemini CLI ACP agent process and performs the handshake.
func StartGeminiCLI(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "gemini", []string{"GEMINI_CLI"}, []string{"--acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "GEMINI_CLI", "GEMINI_CLI_NO_RELAUNCH")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "GEMINI_CLI=1")
	}

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &GeminiCLIAgent{
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
			providerName: "gemini",
		},
		model: opts.Model,
	}
	a.extraSessionUpdate = a.handleExtraSessionUpdate
	a.promptFunc = a.doSendPrompt

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start gemini: %w", err)
	}

	initParams, _ := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs": map[string]bool{
				"readTextFile":  false,
				"writeTextFile": false,
			},
		},
	})
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams,
		acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: acpMethodSessionLoad})
	if err != nil {
		return nil, err
	}

	a.availableModels = buildGeminiCLIModels(handshake.Models, handshake.CurrentModelID)
	if a.model == "" && handshake.CurrentModelID != "" {
		a.model = handshake.CurrentModelID
	}

	a.availableModes = buildACPModes(handshake.Modes, handshake.CurrentModeID, nil)
	a.permissionMode = handshake.CurrentModeID
	if a.permissionMode == "" {
		a.permissionMode = GeminiCLIModeDefault
	}

	if requested := StringOrDefault(opts.PermissionMode, ""); requested != "" && requested != a.permissionMode {
		if err := a.setPermissionMode(requested); err != nil {
			a.Stop()
			_ = a.Wait()
			return nil, a.formatStartupError("session/set_mode", err)
		}
	}

	return a, nil
}

func buildGeminiCLIModels(models []acpModelInfo, currentModelID string) []*leapmuxv1.AvailableModel {
	hasAuto := false
	for _, m := range models {
		if m.ModelID == "auto" {
			hasAuto = true
			break
		}
	}
	result := buildACPModels(models, currentModelID)
	if !hasAuto {
		result = append([]*leapmuxv1.AvailableModel{{
			Id:          "auto",
			DisplayName: "Auto",
			Description: "Automatically selects the best Gemini model",
			IsDefault:   currentModelID == "" || currentModelID == "auto",
		}}, result...)
	}
	return result
}

func fallbackGeminiCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: GeminiCLIModeDefault, Name: "Default", IsDefault: true},
		{Id: GeminiCLIModeAutoEdit, Name: "Auto Edit"},
		{Id: GeminiCLIModeYolo, Name: "YOLO"},
		{Id: GeminiCLIModePlan, Name: "Plan"},
	}
}

func (a *GeminiCLIAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, func(r json.RawMessage) {
			broadcastGeminiQuotaSessionInfo(a.sink, r)
		})
	})
}

func broadcastGeminiQuotaSessionInfo(sink OutputSink, resp json.RawMessage) {
	var result struct {
		Meta struct {
			Quota struct {
				TokenCount struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"token_count"`
			} `json:"quota"`
		} `json:"_meta"`
	}
	if json.Unmarshal(resp, &result) != nil {
		return
	}

	inputTokens := result.Meta.Quota.TokenCount.InputTokens
	outputTokens := result.Meta.Quota.TokenCount.OutputTokens
	if inputTokens == 0 && outputTokens == 0 {
		return
	}

	sink.BroadcastSessionInfo(map[string]interface{}{
		"contextUsage": map[string]interface{}{
			"inputTokens":              inputTokens,
			"cacheCreationInputTokens": int64(0),
			"cacheReadInputTokens":     int64(0),
			"outputTokens":             outputTokens,
		},
	})
}

func (a *GeminiCLIAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:          a.model,
		PermissionMode: a.permissionMode,
	}
}

func (a *GeminiCLIAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.availableModels
}

func (a *GeminiCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	options := a.availableModes
	if len(options) == 0 {
		options = fallbackGeminiCLIModes()
	}
	return []*leapmuxv1.AvailableOptionGroup{{
		Key:     "permissionMode",
		Label:   "Permission Mode",
		Options: options,
	}}
}

func (a *GeminiCLIAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	if model := s.GetModel(); model != "" {
		if err := a.setModel(model); err != nil {
			slog.Warn("gemini unstable_setSessionModel failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	if mode := s.GetPermissionMode(); mode != "" {
		if err := a.setPermissionMode(mode); err != nil {
			slog.Warn("gemini setSessionMode failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	return true
}

func (a *GeminiCLIAgent) setModel(model string) error {
	if err := a.acpSetModel(model); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	return nil
}

func (a *GeminiCLIAgent) setPermissionMode(mode string) error {
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

