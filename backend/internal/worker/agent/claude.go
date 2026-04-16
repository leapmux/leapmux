package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Claude Code permission mode values.
const (
	PermissionModeDefault           = "default"
	PermissionModePlan              = "plan"
	PermissionModeAcceptEdits       = "acceptEdits"
	PermissionModeBypassPermissions = "bypassPermissions"
)

// claudeCodeControlResult holds the outcome of a pending control request.
type claudeCodeControlResult struct {
	Success               bool
	Mode                  string
	Error                 string
	OutputStyle           string
	AvailableOutputStyles []string
	FastModeState         string // "off", "cooldown", "on", or "" (unavailable)
	RawResponse           json.RawMessage
}

// Extra settings keys for Claude Code option groups.
const (
	ExtraKeyOutputStyle    = "outputStyle"
	ExtraKeyFastMode       = "fastMode"
	ExtraKeyAlwaysThinking = "alwaysThinkingEnabled"
)

// ClaudeCodeAgent manages a single Claude Code process.
type ClaudeCodeAgent struct {
	processBase // shared process lifecycle (Stop, Wait, Stderr, etc.)

	model      string
	effort     string
	workingDir string
	homeDir    string
	sink       OutputSink

	// Claude Code-specific state.
	contextUsage           *contextUsageSnapshot
	lastAgentStatus        string
	thirdPartyFromSettings bool // third-party LLM provider detected from settings at startup

	pendingControlMu        sync.Mutex
	pendingControl          map[string]chan<- claudeCodeControlResult
	confirmedPermissionMode string

	// Settings state from initialize response and runtime updates.
	outputStyle           string
	availableOutputStyles []string
	fastMode              string // "on" / "off"
	alwaysThinking        string // "on" / "off"
}

// StartClaudeCode spawns a new Claude Code process and begins reading its output.
// The sink receives parsed output events via the Provider.HandleOutput method.
//
// Claude Code with --input-format stream-json does not produce any output
// (including the init message) until it receives input on stdin. Therefore,
// StartClaudeCode returns immediately without waiting for output. The session ID is
// extracted later from the init message when the first user message triggers
// output from Claude.
func StartClaudeCode(ctx context.Context, opts Options, sink OutputSink) (*ClaudeCodeAgent, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Check Claude Code settings files for third-party LLM provider env vars.
	// If detected, we omit --model/--effort entirely (simple command).
	// If not detected, we use a conditional shell command that checks env
	// vars at runtime (the user may have them in their shell profile).
	thirdPartyFromSettings := detectThirdPartyProvider(opts.HomeDir, opts.WorkingDir)

	baseArgs := []string{
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--permission-prompt-tool", "stdio",
		"--setting-sources", "user,project,local",
	}

	if opts.ResumeSessionID != "" {
		baseArgs = append(baseArgs, "--resume", opts.ResumeSessionID)
	}

	var modelEffortArgs []string
	if !thirdPartyFromSettings {
		modelEffortArgs = buildModelEffortArgs(opts.Model, opts.Effort)
	}

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "claude", []string{"CLAUDECODE"}, baseArgs, modelEffortArgs, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "CLAUDE_CODE_ENTRYPOINT=sdk-ts", "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		// Set CLAUDECODE=1 so the user's shell rc files can detect they are
		// being sourced inside Claude Code and skip conflicting aliases.
		// The inner command unsets it before invoking claude.
		cmd.Env = append(cmd.Env, "CLAUDECODE=1")
	}

	// setupProcessPipes configures SIGTERM cancel, WaitDelay, and opens
	// stdin/stdout/stderr pipes.
	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &ClaudeCodeAgent{
		processBase: processBase{
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
		},
		model:                  opts.Model,
		effort:                 opts.Effort,
		workingDir:             opts.WorkingDir,
		homeDir:                opts.HomeDir,
		sink:                   sink,
		thirdPartyFromSettings: thirdPartyFromSettings,
		pendingControl:         make(map[string]chan<- claudeCodeControlResult),
		alwaysThinking:         "on", // default
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Drain stderr in a background goroutine.
	a.drainStderr(stderrPipe)

	// Read stdout in a background goroutine. Output will only arrive after
	// the first message is sent to stdin (Claude Code behavior with
	// --input-format stream-json).
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	// cleanup terminates the agent process and waits for it to exit.
	// This ensures no orphaned process or goroutine is left behind.
	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	// Send "initialize" as the first control request, matching the Agent SDK
	// protocol. This triggers Claude Code to emit the init system message
	// (which contains the session_id) and establishes the control protocol.
	initResp, err := a.sendControlAndWait(ctx, `{"subtype":"initialize"}`, timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}

	// Extract settings from the initialize response.
	if initResp.OutputStyle != "" {
		a.outputStyle = initResp.OutputStyle
	}
	a.availableOutputStyles = initResp.AvailableOutputStyles
	if initResp.FastModeState == "on" || initResp.FastModeState == "cooldown" {
		a.fastMode = "on"
	} else {
		a.fastMode = "off"
	}

	// Send set_permission_mode to configure the agent's permission mode.
	// This serves as both a health check and permission mode sync (restores
	// mode after worker restart).
	mode := StringOrDefault(opts.PermissionMode, PermissionModeDefault)
	resp, err := a.sendControlAndWait(ctx,
		fmt.Sprintf(`{"subtype":"set_permission_mode","mode":"%s"}`, mode), timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError("set_permission_mode", err)
	}
	a.confirmedPermissionMode = resp.Mode

	// Apply persisted extra settings that differ from initialized defaults.
	if flagSettings := a.buildStartupFlagSettings(opts.ExtraSettings); len(flagSettings) > 0 {
		body, _ := json.Marshal(map[string]interface{}{
			"subtype":  "apply_flag_settings",
			"settings": flagSettings,
		})
		if _, err := a.sendControlAndWait(ctx, string(body), timeout); err != nil {
			slog.Warn("apply_flag_settings at startup failed", "agent_id", a.agentID, "error", err)
		} else {
			a.refreshSettingsFromAgent(timeout)
		}
	}

	return a, nil
}

// buildStartupFlagSettings builds an apply_flag_settings payload for extra
// settings that differ from the initialized defaults.
func (a *ClaudeCodeAgent) buildStartupFlagSettings(extra map[string]string) map[string]interface{} {
	fs := map[string]interface{}{}
	if v := extra[ExtraKeyOutputStyle]; v != "" && v != a.outputStyle {
		fs[ExtraKeyOutputStyle] = v
	}
	if v := extra[ExtraKeyFastMode]; v != "" && v != a.fastMode {
		fs[ExtraKeyFastMode] = flagSettingOnOff(v)
	}
	if v := extra[ExtraKeyAlwaysThinking]; v != "" && v != a.alwaysThinking {
		fs[ExtraKeyAlwaysThinking] = flagSettingOffOn(v)
	}
	return fs
}

// SendInput writes a user message to the agent's stdin.
func (a *ClaudeCodeAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return fmt.Errorf("agent is stopped")
	}

	msg := UserInputMessage{
		Type: MessageTypeUser,
		Message: UserInputContent{
			Role: "user",
		},
	}

	if len(attachments) == 0 {
		// Plain text — backward compatible string content.
		msg.Message.Content = content
	} else {
		// Multimodal — build a content block array.
		blocks := buildClaudeContentBlocks(content, classifyAttachments(attachments))
		msg.Message.Content = blocks
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal input: %w", err)
	}

	data = append(data, '\n')
	if _, err := a.stdin.Write(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}

	return nil
}

// buildClaudeContentBlocks converts text + classified attachments into Claude
// Code's content block format: text blocks, image blocks (base64), and document
// blocks (PDF).
func buildClaudeContentBlocks(content string, classified []classifiedAttachment) []interface{} {
	var blocks []interface{}
	if content != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": content,
		})
	}
	for _, attachment := range classified {
		switch attachment.kind {
		case attachmentKindText:
			blocks = append(blocks, map[string]interface{}{
				"type": "text",
				"text": buildInlineTextAttachmentBlock(attachment),
			})
		case attachmentKindPDF:
			blocks = append(blocks, map[string]interface{}{
				"type": "document",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": attachment.mimeType,
					"data":       base64.StdEncoding.EncodeToString(attachment.data),
				},
			})
		default:
			blocks = append(blocks, map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": attachment.mimeType,
					"data":       base64.StdEncoding.EncodeToString(attachment.data),
				},
			})
		}
	}
	return blocks
}

// CurrentSettings returns the current settings for this agent.
func (a *ClaudeCodeAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	model, effort, mode := a.model, a.effort, a.confirmedPermissionMode
	outputStyle, fastMode, thinking := a.outputStyle, a.fastMode, a.alwaysThinking
	a.mu.Unlock()

	extra := map[string]string{}
	if outputStyle != "" {
		extra[ExtraKeyOutputStyle] = outputStyle
	}
	if fastMode != "" {
		extra[ExtraKeyFastMode] = fastMode
	}
	if thinking != "" {
		extra[ExtraKeyAlwaysThinking] = thinking
	}
	return &leapmuxv1.AgentSettings{
		Model:          model,
		Effort:         effort,
		PermissionMode: mode,
		ExtraSettings:  extra,
	}
}

// AvailableModels returns the hardcoded Claude Code model/effort list.
// Returns nil when a third-party LLM provider is detected (either from
// settings at startup, or from the shell wrapper's
// can_change_model_and_effort=false metadata), which tells the frontend
// to hide model and effort settings.
func (a *ClaudeCodeAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	if a.thirdPartyFromSettings || a.preambleMeta["can_change_model_and_effort"] == "false" {
		return nil
	}
	return claudeCodeAvailableModels
}

// AvailableOptionGroups returns dynamic option groups including output style,
// fast mode (when available), and extended thinking, in addition to the
// static permission mode group.
func (a *ClaudeCodeAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	outputStyle, fastMode, thinking := a.outputStyle, a.fastMode, a.alwaysThinking
	availStyles := a.availableOutputStyles
	a.mu.Unlock()

	groups := []*leapmuxv1.AvailableOptionGroup{
		AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[0], // permission mode
	}

	if len(availStyles) > 0 {
		opts := make([]*leapmuxv1.AvailableOption, 0, len(availStyles))
		for _, s := range availStyles {
			opts = append(opts, &leapmuxv1.AvailableOption{
				Id:        s,
				Name:      titleCaseID(s, ""),
				IsDefault: s == outputStyle,
			})
		}
		groups = append(groups, &leapmuxv1.AvailableOptionGroup{
			Key:     ExtraKeyOutputStyle,
			Label:   "Output Style",
			Options: opts,
		})
	}

	groups = append(groups, &leapmuxv1.AvailableOptionGroup{
		Key:   ExtraKeyFastMode,
		Label: "Fast Mode",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "on", Name: "On", IsDefault: fastMode == "on"},
			{Id: "off", Name: "Off", IsDefault: fastMode != "on"},
		},
	})

	groups = append(groups, &leapmuxv1.AvailableOptionGroup{
		Key:   ExtraKeyAlwaysThinking,
		Label: "Extended Thinking",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "on", Name: "On", IsDefault: thinking != "off"},
			{Id: "off", Name: "Off", IsDefault: thinking == "off"},
		},
	})

	return groups
}

// UpdateSettings applies settings changes via the apply_flag_settings control
// request, avoiding a process restart. Returns true if the update was handled
// (or nothing changed), false if a restart is needed.
func (a *ClaudeCodeAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	a.mu.Lock()
	curModel, curEffort := a.model, a.effort
	curOutputStyle, curFastMode, curThinking := a.outputStyle, a.fastMode, a.alwaysThinking
	availStyles := a.availableOutputStyles
	a.mu.Unlock()

	flagSettings := map[string]interface{}{}

	if s.Model != "" && s.Model != curModel {
		flagSettings["model"] = s.Model
	}
	if s.Effort != "" && s.Effort != curEffort {
		flagSettings["effortLevel"] = s.Effort
	}

	extra := s.ExtraSettings
	if v := extra[ExtraKeyOutputStyle]; v != "" && v != curOutputStyle {
		if !slices.Contains(availStyles, v) {
			return false
		}
		flagSettings[ExtraKeyOutputStyle] = v
	}
	if v := extra[ExtraKeyFastMode]; v != "" && v != curFastMode {
		flagSettings[ExtraKeyFastMode] = flagSettingOnOff(v)
	}
	if v := extra[ExtraKeyAlwaysThinking]; v != "" && v != curThinking {
		flagSettings[ExtraKeyAlwaysThinking] = flagSettingOffOn(v)
	}

	if len(flagSettings) == 0 {
		return true
	}

	body, _ := json.Marshal(map[string]interface{}{
		"subtype":  "apply_flag_settings",
		"settings": flagSettings,
	})
	if _, err := a.sendControlAndWait(a.ctx, string(body), a.APITimeout()); err != nil {
		slog.Error("apply_flag_settings failed", "agent_id", a.agentID, "error", err)
		return false
	}

	a.refreshSettingsFromAgent(a.APITimeout())
	return true
}

// refreshSettingsFromAgent sends get_settings and updates internal state with
// the actual applied values from Claude Code.
func (a *ClaudeCodeAgent) refreshSettingsFromAgent(timeout time.Duration) {
	resp, err := a.sendControlAndWait(a.ctx, `{"subtype":"get_settings"}`, timeout)
	if err != nil {
		slog.Warn("get_settings failed", "agent_id", a.agentID, "error", err)
		return
	}
	if len(resp.RawResponse) == 0 {
		return
	}

	var settings struct {
		Effective struct {
			OutputStyle           string `json:"outputStyle"`
			FastMode              *bool  `json:"fastMode"`
			AlwaysThinkingEnabled *bool  `json:"alwaysThinkingEnabled"`
		} `json:"effective"`
		Applied struct {
			Model  string  `json:"model"`
			Effort *string `json:"effort"`
		} `json:"applied"`
	}
	if err := json.Unmarshal(resp.RawResponse, &settings); err != nil {
		slog.Warn("get_settings response parse failed", "agent_id", a.agentID, "error", err)
		return
	}

	a.mu.Lock()
	if settings.Applied.Model != "" {
		a.model = settings.Applied.Model
	}
	if settings.Applied.Effort != nil {
		a.effort = *settings.Applied.Effort
	}
	if settings.Effective.OutputStyle != "" {
		a.outputStyle = settings.Effective.OutputStyle
	}
	if settings.Effective.FastMode != nil {
		if *settings.Effective.FastMode {
			a.fastMode = "on"
		} else {
			a.fastMode = "off"
		}
	}
	if settings.Effective.AlwaysThinkingEnabled != nil {
		if *settings.Effective.AlwaysThinkingEnabled {
			a.alwaysThinking = "on"
		} else {
			a.alwaysThinking = "off"
		}
	}
	model, effort, mode := a.model, a.effort, a.confirmedPermissionMode
	outputStyle, fastMode, thinking := a.outputStyle, a.fastMode, a.alwaysThinking
	a.mu.Unlock()

	slog.Info("agent settings refreshed",
		"agent_id", a.agentID,
		"model", model,
		"effort", effort,
		"outputStyle", outputStyle,
		"fastMode", fastMode,
		"alwaysThinking", thinking,
	)

	a.sink.BroadcastSettingsRefreshed(model, effort, mode, map[string]string{
		ExtraKeyOutputStyle:    outputStyle,
		ExtraKeyFastMode:       fastMode,
		ExtraKeyAlwaysThinking: thinking,
	})
}

// flagSettingOnOff maps an "on"/"off" string to a boolean flag setting value
// for apply_flag_settings. "on" → true, anything else → nil (which resets
// the flag to its default).
func flagSettingOnOff(v string) interface{} {
	if v == "on" {
		return true
	}
	return nil
}

// flagSettingOffOn maps an "off"/"on" string to a boolean flag setting value
// for apply_flag_settings. "off" → false, anything else → nil (which resets
// the flag to its default).
func flagSettingOffOn(v string) interface{} {
	if v == "off" {
		return false
	}
	return nil
}

// claudeCodeEfforts shared across models (except haiku gets none, and only opus gets max).
var claudeCodeEffortAll = []*leapmuxv1.AvailableEffort{
	{Id: "auto", Name: "Auto", Description: "Let Claude decide the appropriate effort"},
	{Id: "max", Name: "Max", Description: "Deepest reasoning; uses extended thinking"},
	{Id: "high", Name: "High", Description: "Thorough reasoning for complex tasks"},
	{Id: "medium", Name: "Medium", Description: "Balanced speed and reasoning depth"},
	{Id: "low", Name: "Low", Description: "Faster responses with lighter reasoning"},
}

var claudeCodeEffortNoMax = []*leapmuxv1.AvailableEffort{
	{Id: "auto", Name: "Auto", Description: "Let Claude decide the appropriate effort"},
	{Id: "high", Name: "High", Description: "Thorough reasoning for complex tasks"},
	{Id: "medium", Name: "Medium", Description: "Balanced speed and reasoning depth"},
	{Id: "low", Name: "Low", Description: "Faster responses with lighter reasoning"},
}

var claudeCodeAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: "opus", DisplayName: "Opus", Description: "Most capable for complex work", DefaultEffort: "high", SupportedEfforts: claudeCodeEffortAll, ContextWindow: 200_000},
	{Id: "opus[1m]", DisplayName: "Opus (1M context)", Description: "Most capable for complex work", IsDefault: true, DefaultEffort: "high", SupportedEfforts: claudeCodeEffortAll, ContextWindow: 1_000_000},
	{Id: "sonnet", DisplayName: "Sonnet", Description: "Best for everyday tasks", DefaultEffort: "high", SupportedEfforts: claudeCodeEffortNoMax, ContextWindow: 200_000},
	{Id: "sonnet[1m]", DisplayName: "Sonnet (1M context)", Description: "Best for everyday tasks", DefaultEffort: "high", SupportedEfforts: claudeCodeEffortNoMax, ContextWindow: 1_000_000},
	{Id: "haiku", DisplayName: "Haiku", Description: "Fastest for quick answers", DefaultEffort: "high", ContextWindow: 200_000},
}

// sendControlAndWait sends a control request to the agent and waits for the
// response. The requestBody should be the JSON for the "request" field only
// (e.g. `{"subtype":"initialize"}`). Returns the control result or an error
// on timeout/cancellation/failure.
func (a *ClaudeCodeAgent) sendControlAndWait(ctx context.Context, requestBody string, timeout time.Duration) (claudeCodeControlResult, error) {
	requestID := generateRequestID()
	ch := make(chan claudeCodeControlResult, 1)
	a.registerPendingControl(requestID, ch)

	msg := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":%s}`, requestID, requestBody)
	if err := a.SendRawInput([]byte(msg)); err != nil {
		a.unregisterPendingControl(requestID)
		return claudeCodeControlResult{}, err
	}

	select {
	case resp := <-ch:
		a.unregisterPendingControl(requestID)
		if !resp.Success {
			return resp, fmt.Errorf("%s", resp.Error)
		}
		return resp, nil
	case <-a.processDone:
		a.unregisterPendingControl(requestID)
		return claudeCodeControlResult{}, a.processExitError()
	case <-ctx.Done():
		a.unregisterPendingControl(requestID)
		return claudeCodeControlResult{}, ctx.Err()
	case <-time.After(timeout):
		a.unregisterPendingControl(requestID)
		return claudeCodeControlResult{}, fmt.Errorf("timeout waiting for agent to respond")
	}
}

func (a *ClaudeCodeAgent) registerPendingControl(requestID string, ch chan<- claudeCodeControlResult) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	a.pendingControl[requestID] = ch
}

func (a *ClaudeCodeAgent) unregisterPendingControl(requestID string) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	delete(a.pendingControl, requestID)
}

// handlePendingControlResponse checks if a parsed line is a control_response
// matching a pending request. If so, it sends the result to the waiting
// channel and returns true (the line should be consumed, not forwarded).
func (a *ClaudeCodeAgent) handlePendingControlResponse(line *parsedLine) bool {
	// Quick check using the pre-parsed Type field.
	if line.Type != "control_response" {
		return false
	}

	var envelope struct {
		Response struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line.Raw, &envelope); err != nil {
		return false
	}

	reqID := envelope.Response.RequestID
	a.pendingControlMu.Lock()
	ch, ok := a.pendingControl[reqID]
	a.pendingControlMu.Unlock()

	if !ok {
		return false
	}

	// Parse known fields from the inner response object.
	var innerResponse struct {
		Mode                  string   `json:"mode"`
		OutputStyle           string   `json:"output_style"`
		AvailableOutputStyles []string `json:"available_output_styles"`
		FastModeState         string   `json:"fast_mode_state"`
	}
	if len(envelope.Response.Response) > 0 {
		_ = json.Unmarshal(envelope.Response.Response, &innerResponse)
	}

	result := claudeCodeControlResult{
		Success:               envelope.Response.Subtype == "success",
		Mode:                  innerResponse.Mode,
		Error:                 envelope.Response.Error,
		OutputStyle:           innerResponse.OutputStyle,
		AvailableOutputStyles: innerResponse.AvailableOutputStyles,
		FastModeState:         innerResponse.FastModeState,
		RawResponse:           envelope.Response.Response,
	}
	ch <- result
	return true
}

func (a *ClaudeCodeAgent) readOutputLoop(scanner *bufio.Scanner) {
	a.readOutput(scanner, a.handlePendingControlResponse, a.handleOutput)
}

// handleOutput adapts the parsedLine to the existing HandleOutput method,
// passing the pre-parsed Type to avoid re-parsing the envelope.
func (a *ClaudeCodeAgent) handleOutput(line *parsedLine) {
	a.handleClaudeOutput(line.Raw, line.Type)
}

func generateRequestID() string {
	b := make([]byte, 13)
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}

// buildModelEffortArgs constructs the --model and --effort CLI arguments for
// Claude Code. Haiku does not support effort, and max effort is only
// supported for opus models (falls back to high for others).
func buildModelEffortArgs(model, effort string) []string {
	args := []string{"--model", model}
	if effort != "" && model != "haiku" {
		// Max effort is only supported for opus models; fall back to high.
		if effort == "max" && !strings.HasPrefix(model, "opus") {
			effort = "high"
		}
		args = append(args, "--effort", effort)
	}
	return args
}

func init() {
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
			return StartClaudeCode(ctx, opts, sink)
		},
		claudeCodeAvailableModels,
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:   OptionGroupKeyPermissionMode,
			Label: "Permission Mode",
			Options: []*leapmuxv1.AvailableOption{
				{Id: PermissionModeDefault, Name: "Default", IsDefault: true},
				{Id: PermissionModePlan, Name: "Plan Mode"},
				{Id: PermissionModeAcceptEdits, Name: "Accept Edits"},
				{Id: PermissionModeBypassPermissions, Name: "Bypass Permissions"},
			},
		}},
		"LEAPMUX_CLAUDE_DEFAULT_MODEL",
		"LEAPMUX_CLAUDE_DEFAULT_EFFORT",
		"claude",
	)
}
