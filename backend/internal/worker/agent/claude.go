package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
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
// The sink receives parsed output events via the Agent.HandleOutput method.
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

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "CLAUDE_CODE_ENTRYPOINT=cli")
	if opts.LoginShell {
		// Set CLAUDECODE=1 so the user's shell rc files can detect they are
		// being sourced inside Claude Code and skip conflicting aliases.
		// The inner command unsets it before invoking claude.
		cmd.Env = append(cmd.Env, "CLAUDECODE=1")
	}
	cmd.Env = FinalizeAgentEnv(cmd.Env, opts)

	// setupProcessPipes configures SIGTERM cancel, WaitDelay, and opens
	// stdin/stdout/stderr pipes.
	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &ClaudeCodeAgent{
		processBase:            newProcessBase(opts, "claude", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
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
		if err := a.sendApplyFlagSettings(ctx, flagSettings, timeout); err != nil {
			slog.Warn("apply_flag_settings at startup failed", "agent_id", a.agentID, "error", err)
		}
	}
	// Refresh from the CLI once startup completes so the persisted effort
	// reflects the value the CLI actually picked (e.g. when we launched
	// with --effort omitted because Leapmux resolved Options.Effort to
	// "auto"). Run even if apply_flag_settings failed so the DB mirrors
	// the CLI's actual state rather than what we tried to set.
	a.refreshSettingsFromAgent(timeout)

	return a, nil
}

// buildStartupFlagSettings builds an apply_flag_settings payload for extra
// settings that differ from the initialized defaults.
//
// Reads a.effort/a.model without holding a.mu: this runs only from
// StartClaudeCode, before the agent is registered with the manager and thus
// before any concurrent UpdateSettings/refreshSettingsFromAgent can touch those
// fields, so the lock-free read is safe. Do not call it post-registration.
func (a *ClaudeCodeAgent) buildStartupFlagSettings(extra map[string]string) map[string]interface{} {
	fs := map[string]interface{}{}
	// We launched with --effort xhigh as the ultracode base (see
	// buildModelEffortArgs), so the CLI's current effort is already xhigh.
	// Enable the ultracode boolean on top to complete the combo -- but only when
	// the model actually supports it. buildModelEffortArgs downgrades an
	// unsupported ultracode launch to --effort high, so for a Sonnet/Haiku/unknown
	// agent whose stored effort was somehow "ultracode" we must NOT re-enable it
	// here, or startup would contradict the launch flag and force xhigh+ultracode
	// onto a model that can't run it.
	if a.effort == EffortUltracode && modelSupportsUltracode(a.model) {
		maps.Copy(fs, ultracodeFlagSettings())
	}
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

// Interrupt aborts the current turn by sending the Claude Code
// interrupt control_request. This matches the wire format the
// frontend's buildInterruptRequest produced before the dedicated RPC,
// and the receiving claudeProvider.IsInterrupt detects:
//
//	{"type":"control_request","request_id":"...",
//	 "request":{"subtype":"interrupt"}}
//
// The request is best-effort: if the agent has already exited or is
// mid-stop the call returns the underlying error so the caller can
// surface it, but a no-active-turn agent won't fail — Claude Code
// silently ack's interrupts received outside a turn.
func (a *ClaudeCodeAgent) Interrupt() error {
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	// Use the agent's own context so a process exit unblocks the
	// wait. APITimeout caps how long we hold the caller; the control
	// protocol itself is fast (single round-trip).
	_, err := a.sendControlAndWait(a.ctx, `{"subtype":"interrupt"}`, a.APITimeout())
	return err
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
	if err := a.writeStdin(data); err != nil {
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
//
// The returned slice and every AvailableModel/AvailableEffort it points at are
// shared, immutable catalog data: the same effortTier* pointers back multiple
// model slices (see the var block below), so a mutation through any returned
// entry would corrupt every model that shares it. Callers MUST treat the result
// as read-only; copy before mutating.
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

// ultracodeFlagSettings is the apply_flag_settings payload that enables the
// xhigh+ultracode combo. "ultracode" is not a real --effort value: the CLI
// models it as the boolean settings key `ultracode` layered on top of
// effortLevel:"xhigh". Shared by the startup path (buildStartupFlagSettings)
// and the live path (claudeEffortFlagSettings) so the wire encoding -- in
// particular the "xhigh base" -- is defined in exactly one place. Returns a
// fresh map each call so callers can mutate it freely.
func ultracodeFlagSettings() map[string]interface{} {
	return map[string]interface{}{"effortLevel": EffortXHigh, "ultracode": true}
}

// claudeEffortFlagSettings returns the effortLevel/ultracode keys to merge into an
// apply_flag_settings payload to move curEffort -> newEffort. nil = no change.
//
// Selecting ultracode sends {effortLevel:"xhigh", ultracode:true} (see
// ultracodeFlagSettings); switching away from it sends the new level plus an
// explicit ultracode:false so the boolean is cleared.
func claudeEffortFlagSettings(newEffort, curEffort string) map[string]interface{} {
	if newEffort == "" || newEffort == EffortAuto || newEffort == curEffort {
		return nil
	}
	if newEffort == EffortUltracode {
		return ultracodeFlagSettings()
	}
	fs := map[string]interface{}{"effortLevel": newEffort}
	if curEffort == EffortUltracode {
		fs["ultracode"] = false // explicitly clear when leaving ultracode
	}
	return fs
}

// claudeEffortUpdateFlagSettings builds the effort/ultracode portion of a live
// apply_flag_settings payload for moving curEffort -> the requested newEffort on
// targetModel (the model the change lands on). It resolves newEffort against the
// model first, so an unsupported combo can't be pushed to the CLI.
//
// The extra clause beyond claudeEffortFlagSettings handles the model-only change:
// when newEffort is empty there is no effort delta, so claudeEffortFlagSettings
// returns nil and the CLI's per-session `ultracode` boolean would persist even
// after switching onto a model that can't run it (e.g. opus+ultracode ->
// sonnet). We force ultracode:false whenever the session is leaving ultracode for
// a model whose catalog doesn't offer it, so the boolean never outlives the model
// that supports it. (The UI resets effort to auto on a model change and restarts,
// so this only matters for non-UI/raw callers.)
func claudeEffortUpdateFlagSettings(targetModel, newEffort, curEffort string) map[string]interface{} {
	fs := claudeEffortFlagSettings(resolveClaudeEffortForModel(targetModel, newEffort), curEffort)
	if curEffort == EffortUltracode && !modelSupportsUltracode(targetModel) {
		if fs == nil {
			fs = map[string]interface{}{}
		}
		fs["ultracode"] = false
	}
	return fs
}

// claudeEffortFromApplied decodes the effort/ultracode pair that get_settings
// reports back into LeapMux's internal effort value -- the decode-side inverse of
// claudeEffortFlagSettings. The CLI reports an active ultracode session as
// effortLevel:"xhigh" plus ultracode:true, so applied.ultracode==true maps to the
// internal "ultracode" -- but only when the model's catalog confirms it supports
// ultracode (modelSupportsUltracode), so we never mislabel a Sonnet/Haiku/unknown
// session as ultracode even if the CLI were to report it. An unentitled session
// reports ultracode:false and we keep the reported level (e.g. "xhigh"). When
// ultracode is explicitly turned off but applied.effort
// is omitted, we fall back to ultracode's "xhigh" launch base instead of leaving a
// stale "ultracode". curEffort is retained when applied.effort is omitted or empty.
//
// applied.effort is reported as a concrete effort enum or null (get_settings
// sends `typeof effort === "string" ? effort : null`), never an empty string,
// so the `!= ""` guard below is purely defensive: a malformed/empty report
// retains curEffort rather than blanking the stored effort to "".
//
// The final guard catches the remaining mislabel path the switch can't: when
// the CLI omits the ultracode field entirely (ultracode == nil) a stale
// curEffort=="ultracode" would otherwise survive onto a model that can't run it
// (e.g. a model switch that didn't touch effort), so we clear it to the xhigh
// base unless the model's catalog confirms ultracode support.
func claudeEffortFromApplied(appliedEffort *string, ultracode *bool, curEffort, model string) string {
	effort := curEffort
	if appliedEffort != nil && *appliedEffort != "" {
		effort = *appliedEffort
	}
	if ultracode != nil {
		switch {
		case *ultracode && modelSupportsUltracode(model):
			effort = EffortUltracode // overrides the "xhigh" reported in applied.effort
		case !*ultracode && effort == EffortUltracode:
			effort = EffortXHigh // ultracode cleared; fall back to its xhigh launch base
		}
	}
	if effort == EffortUltracode && !modelSupportsUltracode(model) {
		effort = EffortXHigh
	}
	return effort
}

// UpdateSettings applies settings changes via the apply_flag_settings control
// request, avoiding a process restart. Returns true if the update was handled
// (or nothing changed), false if a restart is needed.
func (a *ClaudeCodeAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	a.mu.Lock()
	curModel, curEffort := a.model, a.effort
	curPermissionMode := a.confirmedPermissionMode
	curOutputStyle, curFastMode, curThinking := a.outputStyle, a.fastMode, a.alwaysThinking
	availStyles := a.availableOutputStyles
	a.mu.Unlock()

	// Switching to EffortAuto can't be done live: the CLI doesn't accept
	// effortLevel="auto" for apply_flag_settings, and the only way to go
	// back to "let Claude pick" is to re-launch without --effort. Signal
	// the caller to restart instead.
	if IsEffortAutoTransition(s.Effort, curEffort) {
		return false
	}

	flagSettings := map[string]interface{}{}

	if s.Model != "" && s.Model != curModel {
		flagSettings["model"] = s.Model
	}
	// Resolve the requested effort against the model it will run under (the new model
	// when this update also switches model) so a combined model+effort change can't
	// push an unsupported effort -- e.g. {model:"sonnet", ultracode:true} -- to the
	// CLI. The UI sends single-field updates, so this only bites non-UI/raw callers,
	// but it keeps the live path consistent with buildModelEffortArgs's launch-time
	// downgrade.
	targetModel := curModel
	if s.Model != "" {
		targetModel = s.Model
	}
	maps.Copy(flagSettings, claudeEffortUpdateFlagSettings(targetModel, s.Effort, curEffort))

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

	if len(flagSettings) > 0 {
		if err := a.sendApplyFlagSettings(a.ctx, flagSettings, a.APITimeout()); err != nil {
			slog.Error("apply_flag_settings failed", "agent_id", a.agentID, "error", err)
			return false
		}
		a.refreshSettingsFromAgent(a.APITimeout())
	}

	if mode := s.GetPermissionMode(); mode != "" && mode != curPermissionMode {
		resp, err := a.sendSetPermissionMode(a.ctx, mode, a.APITimeout())
		if err != nil {
			slog.Error("set_permission_mode failed", "agent_id", a.agentID, "mode", mode, "error", err)
			return false
		}
		a.mu.Lock()
		a.confirmedPermissionMode = resp.Mode
		a.mu.Unlock()
	}
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
			Model     string  `json:"model"`
			Effort    *string `json:"effort"`
			Ultracode *bool   `json:"ultracode"`
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
	// a.model is updated just above from applied.model, so the ultracode/model
	// gate inside claudeEffortFromApplied sees the model the CLI actually applied.
	a.effort = claudeEffortFromApplied(settings.Applied.Effort, settings.Applied.Ultracode, a.effort, a.model)
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
	model, effort := a.model, a.effort
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

	// get_settings does not report permission mode. Keep that field empty so
	// the service preserves the DB value, including startup-time raw
	// set_permission_mode changes that are applied again after startup.
	a.sink.PersistSettingsRefresh(model, effort, "", map[string]string{
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
// strongest → weakest so the RadioGroup renders in the same order. Descriptions
// mirror Claude Code's own effort copy (binary MP5()/ultracode strings) so the
// LeapMux selector reads identically to the CLI's /effort menu.

// Individual effort tiers, defined once and composed into the per-model slices
// below. Sharing one definition per tier keeps the descriptions single-sourced
// so a copy edit (like the one this list just received) can't drift between the
// Opus and Sonnet menus. The entries are immutable catalog data; the slices
// reference the same pointers (as opus and opus[1m] already share a whole slice).
//
// Because the pointers are shared, they MUST be treated as read-only after init:
// mutating one tier (e.g. its Description) would change it for every model slice
// that references it. AvailableModels() hands these out without copying, so the
// read-only contract extends to its callers (see its doc).
var (
	effortTierAuto      = &leapmuxv1.AvailableEffort{Id: "auto", Name: "Auto", Description: "Let Claude decide the appropriate effort"}
	effortTierUltracode = &leapmuxv1.AvailableEffort{Id: "ultracode", Name: "Ultracode", Description: "xhigh effort plus standing dynamic-workflow orchestration"}
	effortTierMax       = &leapmuxv1.AvailableEffort{Id: "max", Name: "Max", Description: "Maximum capability with deepest reasoning"}
	effortTierXHigh     = &leapmuxv1.AvailableEffort{Id: EffortXHigh, Name: "X-High", Description: "Deeper reasoning than high, just below maximum"}
	effortTierHigh      = &leapmuxv1.AvailableEffort{Id: "high", Name: "High", Description: "Comprehensive implementation with extensive testing and documentation"}
	effortTierMedium    = &leapmuxv1.AvailableEffort{Id: "medium", Name: "Medium", Description: "Balanced approach with standard implementation and testing"}
	effortTierLow       = &leapmuxv1.AvailableEffort{Id: "low", Name: "Low", Description: "Quick, straightforward implementation with minimal overhead"}
)

// claudeEffortXHighMax is used by models that support both xhigh and max,
// plus the xhigh+ultracode combo (currently Opus).
var claudeEffortXHighMax = []*leapmuxv1.AvailableEffort{
	effortTierAuto, effortTierUltracode, effortTierMax, effortTierXHigh, effortTierHigh, effortTierMedium, effortTierLow,
}

// claudeEffortMax is used by models that support max but not xhigh
// (currently Sonnet 4.6, older Opus).
var claudeEffortMax = []*leapmuxv1.AvailableEffort{
	effortTierAuto, effortTierMax, effortTierHigh, effortTierMedium, effortTierLow,
}

var claudeCodeAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: "opus", DisplayName: "Opus", Description: "Most capable for complex work", DefaultEffort: EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: 200_000},
	{Id: "opus[1m]", DisplayName: "Opus (1M context)", Description: "Most capable for complex work", IsDefault: true, DefaultEffort: EffortXHigh, SupportedEfforts: claudeEffortXHighMax, ContextWindow: 1_000_000},
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
		// A write failure almost always means the child closed its stdin —
		// i.e. it exited before we could hand off the request. Wait briefly
		// for the wait goroutine to finalize so callers see "agent process
		// exited with code N" (with captured stderr) instead of a raw
		// "broken pipe" symptom. This also removes a race in
		// TestAgent_EarlyExitDetected where, on fast Linux runners, the
		// subprocess exits before the initialize write reaches the pipe.
		select {
		case <-a.processDone:
			return claudeCodeControlResult{}, a.processExitError()
		case <-time.After(1 * time.Second):
			return claudeCodeControlResult{}, err
		}
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

// sendApplyFlagSettings marshals flagSettings into an apply_flag_settings
// control request and sends it, returning the control error (if any). The
// envelope shape lives here so the startup path (StartClaudeCode) and the live
// path (UpdateSettings) don't each hand-roll the same JSON literal.
func (a *ClaudeCodeAgent) sendApplyFlagSettings(ctx context.Context, flagSettings map[string]interface{}, timeout time.Duration) error {
	body, _ := json.Marshal(map[string]interface{}{
		"subtype":  "apply_flag_settings",
		"settings": flagSettings,
	})
	_, err := a.sendControlAndWait(ctx, string(body), timeout)
	return err
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
	if requested == PermissionModeAuto {
		resp, err := a.sendSetPermissionMode(ctx, PermissionModeAuto, timeout)
		switch {
		case err == nil:
			a.setAutoModeAvailable(true)
			return resp, nil
		case isAutoModeUnavailableError(err):
			slog.Warn("requested auto permission mode is unavailable; falling back to default",
				"agent_id", a.agentID)
			a.setAutoModeAvailable(false)
			return a.sendSetPermissionMode(ctx, PermissionModeDefault, timeout)
		default:
			return claudeCodeControlResult{}, err
		}
	}

	if _, err := a.sendSetPermissionMode(ctx, PermissionModeAuto, timeout); err != nil {
		if !isAutoModeUnavailableError(err) {
			slog.Warn("auto-mode probe failed (transient); treating as unavailable",
				"agent_id", a.agentID, "error", err)
		}
		a.setAutoModeAvailable(false)
	} else {
		a.setAutoModeAvailable(true)
	}
	return a.sendSetPermissionMode(ctx, requested, timeout)
}

// sendSetPermissionMode issues set_permission_mode and falls back to the
// requested mode when the response omits the applied mode field, so callers
// always receive a non-empty resp.Mode on success.
func (a *ClaudeCodeAgent) sendSetPermissionMode(ctx context.Context, mode string, timeout time.Duration) (claudeCodeControlResult, error) {
	body, err := json.Marshal(map[string]string{
		"subtype": "set_permission_mode",
		"mode":    mode,
	})
	if err != nil {
		return claudeCodeControlResult{}, err
	}
	resp, err := a.sendControlAndWait(ctx, string(body), timeout)
	if err == nil && resp.Mode == "" {
		resp.Mode = mode
	}
	return resp, err
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
	if line.Type != claudeMsgTypeControlResponse {
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
// Claude Code. Haiku does not support --effort at all. EffortAuto also omits
// --effort so the CLI picks its own default. Other models each expose a
// subset of effort levels via claudeCodeAvailableModels; any effort not in
// the subset is downgraded to "high" as a universal safe fallback.
func buildModelEffortArgs(model, effort string) []string {
	args := []string{"--model", model}
	if effort == "" || effort == EffortAuto || model == "haiku" {
		return args
	}
	effort = resolveClaudeEffortForModel(model, effort)
	// The --effort launch flag accepts only low|medium|high|xhigh|max. "ultracode"
	// is enabled post-init via apply_flag_settings (it forces xhigh), so launch with
	// xhigh as the base. resolveClaudeEffortForModel only leaves effort == "ultracode"
	// for a model that supports it, so this substitution is reached only then.
	if effort == EffortUltracode {
		effort = EffortXHigh
	}
	return append(args, "--effort", effort)
}

// modelDefinedEfforts returns the catalog-defined effort list for modelID and
// whether the model is known to the catalog at all. The single lookup is shared
// by effortSupported and modelSupportsUltracode, which differ only in how they
// treat an unknown model.
func modelDefinedEfforts(modelID string) (efforts []*leapmuxv1.AvailableEffort, known bool) {
	for _, m := range claudeCodeAvailableModels {
		if m.Id == modelID {
			return m.SupportedEfforts, true
		}
	}
	return nil, false
}

// effortListContains reports whether efforts holds an entry with the given ID.
func effortListContains(efforts []*leapmuxv1.AvailableEffort, id string) bool {
	return slices.ContainsFunc(efforts, func(e *leapmuxv1.AvailableEffort) bool { return e.Id == id })
}

// effortSupported reports whether the given effort ID is in the known
// SupportedEfforts list for the given model. Unknown models are trusted
// (returns true) so new aliases work without a code change.
func effortSupported(modelID, effort string) bool {
	efforts, known := modelDefinedEfforts(modelID)
	if !known {
		return true
	}
	return effortListContains(efforts, effort)
}

// modelSupportsUltracode reports whether the model's catalog explicitly offers the
// ultracode tier. Unlike effortSupported, it does NOT trust unknown models: ultracode
// is a narrow Opus-only combo, so launching or enabling it on a model we can't confirm
// supports it would risk forcing an --effort xhigh the model may reject. A genuinely
// new ultracode-capable model must be added to claudeCodeAvailableModels to be
// selectable at all, so requiring an explicit catalog entry costs no real forward
// compatibility.
func modelSupportsUltracode(modelID string) bool {
	efforts, known := modelDefinedEfforts(modelID)
	return known && effortListContains(efforts, EffortUltracode)
}

// resolveClaudeEffortForModel resolves a requested effort against the model it will
// run under: an effort the model's catalog doesn't support is downgraded to the
// universal-safe "high", and "ultracode" stays "ultracode" only for a model that
// actually supports it (otherwise it too becomes "high" -- this catches unknown
// models, which effortSupported trusts but which we can't confirm are xhigh-capable).
// EffortAuto and "" pass through for the caller to handle. Shared by the --effort
// launch flag (buildModelEffortArgs) and the live apply_flag_settings path
// (UpdateSettings) so neither can push an unsupported effort to the CLI.
func resolveClaudeEffortForModel(model, effort string) string {
	if effort == "" || effort == EffortAuto {
		return effort
	}
	if !effortSupported(model, effort) {
		return "high"
	}
	if effort == EffortUltracode && !modelSupportsUltracode(model) {
		return "high"
	}
	return effort
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
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
