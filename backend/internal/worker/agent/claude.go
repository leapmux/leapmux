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
	PermissionModeDontAsk           = "dontAsk"
	PermissionModeAuto              = "auto"
)

// autoModeUnavailableErrorPrefix is the prefix of the error message Claude
// Code returns when set_permission_mode:auto is rejected (regardless of
// reason: admin settings, plan circuit-breaker, or unsupported model).
const autoModeUnavailableErrorPrefix = "Cannot set permission mode to auto"

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

// Extended Thinking option IDs. Claude Code only exposes a single
// alwaysThinkingEnabled boolean — it picks thinking.type:"adaptive" vs
// "enabled" per-model — so there is nothing to store beyond on/off. The
// UI label for the "on" option is set per-model in AvailableOptionGroups
// ("Adaptive" for Opus/Sonnet, "On" for Haiku).
const (
	AlwaysThinkingOn  = "on"
	AlwaysThinkingOff = "off"
)

// Fast Mode option IDs.
const (
	FastModeOn  = "on"
	FastModeOff = "off"
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
	autoModeAvailable     bool
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
	TraceStartupPhase(opts.AgentID, "claude_begin")
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
		// Emit summarized thinking text in thinking blocks; without this,
		// thinking blocks arrive with an empty `thinking` field.
		"--thinking-display", "summarized",
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
			providerName:       "claude",
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
		alwaysThinking:         AlwaysThinkingOn,
	}

	TraceStartupPhase(opts.AgentID, "before_exec_start")
	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}
	TraceStartupPhase(opts.AgentID, "after_exec_start")

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
	TraceStartupPhase(opts.AgentID, "before_initialize")
	initResp, err := a.sendControlAndWait(ctx, `{"subtype":"initialize"}`, timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}
	TraceStartupPhase(opts.AgentID, "after_initialize")

	// Extract settings from the initialize response.
	if initResp.OutputStyle != "" {
		a.outputStyle = initResp.OutputStyle
	}
	a.availableOutputStyles = initResp.AvailableOutputStyles
	if initResp.FastModeState == FastModeOn || initResp.FastModeState == "cooldown" {
		a.fastMode = FastModeOn
	} else {
		a.fastMode = FastModeOff
	}

	TraceStartupPhase(opts.AgentID, "before_permission_mode")
	resp, err := a.applyStartupPermissionMode(ctx, StringOrDefault(opts.PermissionMode, PermissionModeDefault), timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError("set_permission_mode", err)
	}
	a.confirmedPermissionMode = resp.Mode
	TraceStartupPhase(opts.AgentID, "after_permission_mode")

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
		fs[ExtraKeyAlwaysThinking] = flagSettingThinking(v)
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
	if a.thirdPartyFromSettings || a.preambleMetaValue("can_change_model_and_effort") == "false" {
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
	autoAvail := a.autoModeAvailable
	model := a.model
	a.mu.Unlock()

	var groups []*leapmuxv1.AvailableOptionGroup
	for _, g := range AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE) {
		if g.GetKey() == OptionGroupKeyPermissionMode {
			groups = append(groups, filterPermissionModeGroup(g, autoAvail))
			continue
		}
		groups = append(groups, g)
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
			{Id: FastModeOn, Name: "On", IsDefault: fastMode == FastModeOn},
			{Id: FastModeOff, Name: "Off", IsDefault: fastMode != FastModeOn},
		},
	})

	enabledName := "On"
	if modelSupportsAdaptiveThinking(model) {
		enabledName = "Adaptive"
	}
	groups = append(groups, &leapmuxv1.AvailableOptionGroup{
		Key:   ExtraKeyAlwaysThinking,
		Label: "Extended Thinking",
		Options: []*leapmuxv1.AvailableOption{
			{Id: AlwaysThinkingOn, Name: enabledName, IsDefault: thinking != AlwaysThinkingOff},
			{Id: AlwaysThinkingOff, Name: "Off", IsDefault: thinking == AlwaysThinkingOff},
		},
	})

	return groups
}

// filterPermissionModeGroup hides "auto" when the startup probe rejected it,
// so the UI can't offer a mode this Claude Code instance can't enter.
func filterPermissionModeGroup(group *leapmuxv1.AvailableOptionGroup, autoAvail bool) *leapmuxv1.AvailableOptionGroup {
	if autoAvail {
		return group
	}
	opts := make([]*leapmuxv1.AvailableOption, 0, len(group.GetOptions()))
	for _, o := range group.GetOptions() {
		if o.GetId() == PermissionModeAuto {
			continue
		}
		opts = append(opts, o)
	}
	return &leapmuxv1.AvailableOptionGroup{
		Key:     group.GetKey(),
		Label:   group.GetLabel(),
		Options: opts,
	}
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
		flagSettings[ExtraKeyAlwaysThinking] = flagSettingThinking(v)
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
		a.model = normalizeClaudeCodeModel(settings.Applied.Model)
	}
	if settings.Applied.Effort != nil {
		a.effort = *settings.Applied.Effort
	}
	if settings.Effective.OutputStyle != "" {
		a.outputStyle = settings.Effective.OutputStyle
	}
	if settings.Effective.FastMode != nil {
		if *settings.Effective.FastMode {
			a.fastMode = FastModeOn
		} else {
			a.fastMode = FastModeOff
		}
	}
	if settings.Effective.AlwaysThinkingEnabled != nil {
		if *settings.Effective.AlwaysThinkingEnabled {
			a.alwaysThinking = AlwaysThinkingOn
		} else {
			a.alwaysThinking = AlwaysThinkingOff
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
	if v == FastModeOn {
		return true
	}
	return nil
}

// flagSettingThinking maps an "on"/"off" string to the alwaysThinkingEnabled
// flag value for apply_flag_settings. "off" → false (thinking disabled).
// Anything else returns nil, which removes the key from flagSettings and
// lets Claude Code fall back to its default-on behavior — internally picking
// type:"adaptive" or type:"enabled" per its own model gate.
func flagSettingThinking(v string) interface{} {
	if v == AlwaysThinkingOff {
		return false
	}
	return nil
}

// normalizeClaudeCodeModel collapses the fully-qualified model ID that
// Claude Code's get_settings "applied.model" field returns (e.g.
// "claude-opus-4-7", "claude-haiku-4-5-20251001", "claude-sonnet-4-6[1m]")
// back to the short alias leapmux uses (opus, opus[1m], sonnet,
// sonnet[1m], haiku). Short aliases pass through unchanged.
//
// Rules:
//   - Strip an optional "claude-" prefix.
//   - Preserve a trailing "[...]" bracket suffix (the 1M-context marker).
//   - Keep only the leading alphabetic token (opus/sonnet/haiku), dropping
//     version numbers (e.g. "-4-7") and date suffixes (e.g. "-20251001").
func normalizeClaudeCodeModel(model string) string {
	if model == "" {
		return ""
	}
	core := strings.TrimPrefix(model, "claude-")
	var suffix string
	if i := strings.IndexByte(core, '['); i >= 0 {
		suffix = core[i:]
		core = core[:i]
	}
	end := 0
	for end < len(core) {
		c := core[end]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			break
		}
		end++
	}
	alias := core[:end]
	if alias == "" {
		// Unrecognized shape — return the original input unchanged so the
		// caller can still display it.
		return model
	}
	return alias + suffix
}

// modelSupportsAdaptiveThinking reports whether Claude Code emits
// thinking.type:"adaptive" for this model (Opus and Sonnet) versus
// thinking.type:"enabled" with a budget (Haiku). Unknown model strings
// default to true to match Claude Code's first-party fallback. This is
// used purely to pick the "Adaptive" vs "On" display label in
// AvailableOptionGroups — the wire payload (alwaysThinkingEnabled) is
// identical either way.
//
// Expects the short alias (e.g. "opus", "haiku"). `a.model` is kept
// normalized after every refreshSettingsFromAgent call, so internal
// callers can pass it directly without re-normalizing.
func modelSupportsAdaptiveThinking(model string) bool {
	return !strings.HasPrefix(model, "haiku")
}

// Claude Code effort levels are model-dependent. Keep each slice ordered
// strongest → weakest so the RadioGroup renders in the same order.

// claudeEffortXHighMax is used by models that support both xhigh and max
// (currently Opus 4.7).
var claudeEffortXHighMax = []*leapmuxv1.AvailableEffort{
	{Id: "auto", Name: "Auto", Description: "Let Claude decide the appropriate effort"},
	{Id: "max", Name: "Max", Description: "Deepest reasoning; no constraints on token spend"},
	{Id: "xhigh", Name: "X-High", Description: "Extended capability for long-horizon agentic tasks"},
	{Id: "high", Name: "High", Description: "Thorough reasoning for complex tasks"},
	{Id: "medium", Name: "Medium", Description: "Balanced speed and reasoning depth"},
	{Id: "low", Name: "Low", Description: "Faster responses with lighter reasoning"},
}

// claudeEffortMax is used by models that support max but not xhigh
// (currently Sonnet 4.6, older Opus).
var claudeEffortMax = []*leapmuxv1.AvailableEffort{
	{Id: "auto", Name: "Auto", Description: "Let Claude decide the appropriate effort"},
	{Id: "max", Name: "Max", Description: "Deepest reasoning; no constraints on token spend"},
	{Id: "high", Name: "High", Description: "Thorough reasoning for complex tasks"},
	{Id: "medium", Name: "Medium", Description: "Balanced speed and reasoning depth"},
	{Id: "low", Name: "Low", Description: "Faster responses with lighter reasoning"},
}

var claudeCodeAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: "opus", DisplayName: "Opus", Description: "Most capable for complex work", DefaultEffort: "xhigh", SupportedEfforts: claudeEffortXHighMax, ContextWindow: 200_000},
	{Id: "opus[1m]", DisplayName: "Opus (1M context)", Description: "Most capable for complex work", IsDefault: true, DefaultEffort: "xhigh", SupportedEfforts: claudeEffortXHighMax, ContextWindow: 1_000_000},
	{Id: "sonnet", DisplayName: "Sonnet", Description: "Best for everyday tasks", DefaultEffort: "high", SupportedEfforts: claudeEffortMax, ContextWindow: 200_000},
	{Id: "sonnet[1m]", DisplayName: "Sonnet (1M context)", Description: "Best for everyday tasks", DefaultEffort: "high", SupportedEfforts: claudeEffortMax, ContextWindow: 1_000_000},
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
	TraceStartupPhase(a.agentID, "control_stdin_write")

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

// applyStartupPermissionMode sets the agent's permission mode during startup
// while also detecting whether auto mode is available for this session.
// a.autoModeAvailable is updated as a side effect; the returned result
// reflects the mode actually applied (which may be default if the requested
// auto mode was rejected with autoModeUnavailableErrorPrefix).
//
// When requested == auto a single set_permission_mode call serves both
// purposes; otherwise auto is probed first (leaving the session briefly in
// auto on success) before the requested mode is applied to restore the
// intended state. Transient probe errors are treated as unavailable so the
// UI does not offer a mode the agent cannot enter.
func (a *ClaudeCodeAgent) applyStartupPermissionMode(ctx context.Context, requested string, timeout time.Duration) (claudeCodeControlResult, error) {
	setMode := func(m string) (claudeCodeControlResult, error) {
		return a.sendControlAndWait(ctx,
			fmt.Sprintf(`{"subtype":"set_permission_mode","mode":"%s"}`, m), timeout)
	}

	if requested == PermissionModeAuto {
		resp, err := setMode(PermissionModeAuto)
		switch {
		case err == nil:
			a.setAutoModeAvailable(true)
			return resp, nil
		case isAutoModeUnavailableError(err):
			slog.Warn("requested auto permission mode is unavailable; falling back to default",
				"agent_id", a.agentID)
			a.setAutoModeAvailable(false)
			return setMode(PermissionModeDefault)
		default:
			return claudeCodeControlResult{}, err
		}
	}

	if _, err := setMode(PermissionModeAuto); err != nil {
		if !isAutoModeUnavailableError(err) {
			slog.Warn("auto-mode probe failed (transient); treating as unavailable",
				"agent_id", a.agentID, "error", err)
		}
		a.setAutoModeAvailable(false)
	} else {
		a.setAutoModeAvailable(true)
	}
	return setMode(requested)
}

// setAutoModeAvailable stores the auto-mode probe result under a.mu so
// concurrent readers (e.g. AvailableOptionGroups) observe a consistent value.
func (a *ClaudeCodeAgent) setAutoModeAvailable(v bool) {
	a.mu.Lock()
	a.autoModeAvailable = v
	a.mu.Unlock()
}

// isAutoModeUnavailableError reports whether err is the Claude Code
// control_response rejection for set_permission_mode:auto (admin-disabled,
// plan-gated, or unsupported-model).
func isAutoModeUnavailableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), autoModeUnavailableErrorPrefix)
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
// Claude Code. Haiku does not support --effort at all. Other models each
// expose a subset of effort levels via claudeCodeAvailableModels; any effort
// not in the subset is downgraded to "high" as a universal safe fallback.
func buildModelEffortArgs(model, effort string) []string {
	args := []string{"--model", model}
	if effort == "" || model == "haiku" {
		return args
	}
	if !effortSupported(model, effort) {
		effort = "high"
	}
	return append(args, "--effort", effort)
}

// effortSupported reports whether the given effort ID is in the known
// SupportedEfforts list for the given model. Unknown models are trusted
// (returns true) so new aliases work without a code change.
func effortSupported(modelID, effort string) bool {
	for _, m := range claudeCodeAvailableModels {
		if m.Id != modelID {
			continue
		}
		for _, e := range m.SupportedEfforts {
			if e.Id == effort {
				return true
			}
		}
		return false
	}
	return true
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
				{
					Id:          PermissionModeDefault,
					Name:        "Default",
					Description: "Standard behavior, prompts for dangerous operations.",
					IsDefault:   true,
				},
				{
					Id:          PermissionModePlan,
					Name:        "Plan Mode",
					Description: "Planning mode, no actual tool execution.",
				},
				{
					Id:          PermissionModeAcceptEdits,
					Name:        "Accept Edits",
					Description: "Auto-accept file edit operations.",
				},
				{
					Id:          PermissionModeBypassPermissions,
					Name:        "Bypass Permissions",
					Description: "Bypass all permission checks (requires allowDangerouslySkipPermissions).",
				},
				{
					Id:          PermissionModeDontAsk,
					Name:        "Don't Ask",
					Description: "Don't prompt for permissions, deny if not pre-approved.",
				},
				{
					Id:          PermissionModeAuto,
					Name:        "Auto Mode",
					Description: "Uses an AI classifier to auto-approve safe tool calls and falls back to prompting for risky ones.",
				},
			},
		}},
		"LEAPMUX_CLAUDE_DEFAULT_MODEL",
		"LEAPMUX_CLAUDE_DEFAULT_EFFORT",
		"claude",
	)
}
