package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
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
}

// StartGeminiCLI starts a Gemini CLI ACP agent process and performs the handshake.
func StartGeminiCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:        opts.Shell,
		LoginShell:   opts.LoginShell,
		BinaryName:   "gemini",
		StripEnvKeys: []string{"GEMINI_CLI"},
		BaseArgs:     []string{"--acp"},
		WorkingDir:   opts.WorkingDir,
	})

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "GEMINI_CLI", "GEMINI_CLI_NO_RELAUNCH")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "GEMINI_CLI=1")
	}
	cmd.Env = FinalizeAgentEnv(cmd.Env, opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &GeminiCLIAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "gemini", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
			sink:        sink,
			model:       opts.Model,
		},
	}
	a.extraSessionUpdate = a.handleExtraSessionUpdate
	a.modelsDecorator = geminiEnsureAuto
	// modeChannel stays modeChannelUnmapped (the zero value): Gemini tracks a permission
	// mode but drives it through the native current_mode_update channel, not the
	// configOptions `mode` select, which is surfaced read-only instead.
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndPermissionMode
	a.refreshFromSession = a.refreshModelAndPermissionModeFromSession

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	initParams, err := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs": map[string]bool{
				"readTextFile":  false,
				"writeTextFile": false,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams, acpDefaultSessionConfig)
	if err != nil {
		return nil, err
	}

	if err := a.applyPermissionModeStartup(handshake, opts, GeminiCLIModeDefault, opts.Model, a.setModel); err != nil {
		return nil, err
	}

	return a, nil
}

// geminiEnsureAuto prepends a synthetic "auto" model when the server's list
// omits it. Wired as the acpBase modelsDecorator so it runs on both the
// handshake and runtime model channels, keeping the two consistent.
func geminiEnsureAuto(models []*leapmuxv1.AvailableModel, currentModelID string) []*leapmuxv1.AvailableModel {
	for _, m := range models {
		if m.GetId() == "auto" {
			return models
		}
	}
	auto := &leapmuxv1.AvailableModel{
		Id:          "auto",
		DisplayName: "Auto",
		Description: "Automatically selects the best Gemini model",
		IsDefault:   currentModelID == "" || currentModelID == "auto",
	}
	return append([]*leapmuxv1.AvailableModel{auto}, models...)
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
	if err := json.Unmarshal(resp, &result); err != nil {
		slog.Warn("gemini quota session info unmarshal failed", "error", err)
		return
	}

	inputTokens := result.Meta.Quota.TokenCount.InputTokens
	outputTokens := result.Meta.Quota.TokenCount.OutputTokens
	if inputTokens == 0 && outputTokens == 0 {
		return
	}

	sink.BroadcastSessionInfo(map[string]interface{}{
		"context_usage": map[string]interface{}{
			"input_tokens":                inputTokens,
			"cache_creation_input_tokens": int64(0),
			"cache_read_input_tokens":     int64(0),
			"output_tokens":               outputTokens,
		},
	})
}

func (a *GeminiCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.permissionModeOptionGroups("Permission Mode", fallbackGeminiCLIModes())
}

var geminiCLIAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: "auto", DisplayName: "Auto", Description: "Let Gemini CLI choose the best model for the task", IsDefault: true},
	{Id: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Description: "Most capable for complex reasoning and coding"},
	{Id: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", Description: "Fast, balanced model for most tasks"},
	{Id: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash Lite", Description: "Fastest option for lightweight tasks"},
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartGeminiCLI(ctx, opts, sink)
		},
		geminiCLIAvailableModels,
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:     OptionGroupKeyPermissionMode,
			Label:   "Permission Mode",
			Options: fallbackGeminiCLIModes(),
		}},
		"LEAPMUX_GEMINI_DEFAULT_MODEL",
		"",
		"gemini",
	)
}
