package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"syscall"
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

// Control response behavior values (shared protocol between frontend and backend).
const (
	ControlBehaviorAllow = "allow"
	ControlBehaviorDeny  = "deny"
)

// ExitHandler is called when an agent process exits.
// agentID identifies the agent, exitCode is the process exit code,
// and err is non-nil if the process exited with an error.
type ExitHandler func(agentID string, exitCode int, err error)

// claudeCodeControlResult holds the outcome of a pending control request.
type claudeCodeControlResult struct {
	Success bool
	Mode    string
	Error   string
}

// ClaudeCodeAgent manages a single Claude Code process.
type ClaudeCodeAgent struct {
	processBase // shared process lifecycle (Stop, Wait, Stderr, etc.)

	model      string
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
}

// Options configures a new ClaudeCodeAgent.
type Options struct {
	AgentID                string
	Model                  string
	Effort                 string // Effort (low, medium, high, max)
	WorkingDir             string
	ResumeSessionID        string                  // If set, uses --resume to resume a previous session
	PermissionMode         string                  // Permission mode to set on startup (default, acceptEdits, plan, bypassPermissions)
	CodexSandboxPolicy     string                  // Codex sandbox policy (e.g. "danger-full-access", "workspace-write")
	CodexNetworkAccess     string                  // Codex network access ("restricted" or "enabled")
	CodexCollaborationMode string                  // Codex collaboration mode ("default" or "plan")
	CodexServiceTier       string                  // Codex service tier ("default" or "fast")
	StartupTimeout         time.Duration           // Timeout for the startup handshake (default: 30s)
	Shell                  string                  // Default shell path (always set when using shell wrapper)
	LoginShell             bool                    // If true, use interactive+login shell flags
	HomeDir                string                  // User's home directory (for reading Claude Code settings)
	AgentProvider          leapmuxv1.AgentProvider // Coding agent provider (default: CLAUDE_CODE)
}

// StartClaudeCode spawns a new Claude Code process and begins reading its output.
// The sink receives parsed output events via the Provider.HandleOutput method.
//
// Claude Code with --input-format stream-json does not produce any output
// (including the init message) until it receives input on stdin. Therefore,
// StartClaudeCode returns immediately without waiting for output. The session ID is
// extracted later from the init message when the first user message triggers
// output from Claude.
func (o Options) startupTimeout() time.Duration {
	if o.StartupTimeout > 0 {
		return o.StartupTimeout
	}
	return 30 * time.Second
}

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
		ctx, opts.Shell, opts.LoginShell, "claude", baseArgs, modelEffortArgs, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "CLAUDE_CODE_ENTRYPOINT=sdk-ts", "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		// Set CLAUDECODE=1 so the user's shell rc files can detect they are
		// being sourced inside Claude Code and skip conflicting aliases.
		// The inner command unsets it before invoking claude.
		cmd.Env = append(cmd.Env, "CLAUDECODE=1")
	}

	// Send SIGTERM (instead of the default SIGKILL) when the context is
	// cancelled, giving Claude Code a chance to persist its session state.
	// If the process doesn't exit within WaitDelay after SIGTERM, Go will
	// send SIGKILL automatically.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Capture stderr via a goroutine that actively drains the pipe. This
	// prevents the process from blocking if stderr output exceeds the OS
	// pipe buffer (~64KB). Buffer is capped at 1MB.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
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
		},
		model:                  opts.Model,
		workingDir:             opts.WorkingDir,
		homeDir:                opts.HomeDir,
		sink:                   sink,
		thirdPartyFromSettings: thirdPartyFromSettings,
		pendingControl:         make(map[string]chan<- claudeCodeControlResult),
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
	if _, err := a.sendControlAndWait(ctx, `{"subtype":"initialize"}`, timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
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

	return a, nil
}

// SendInput writes a user message to the agent's stdin.
func (a *ClaudeCodeAgent) SendInput(content string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return fmt.Errorf("agent is stopped")
	}

	msg := UserInputMessage{
		Type: MessageTypeUser,
		Message: UserInputContent{
			Role:    "user",
			Content: content,
		},
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

// formatStartupError returns a descriptive error including stderr and
// preamble output (if any) for frontend diagnostics.
func (a *ClaudeCodeAgent) formatStartupError(phase string, err error) error {
	return a.processBase.formatStartupError(phase, err, a.PreambleOutput())
}

// CurrentSettings returns the current settings for this agent.
func (a *ClaudeCodeAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	return &leapmuxv1.AgentSettings{
		Model:          a.model,
		PermissionMode: a.confirmedPermissionMode,
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

// UpdateSettings is a no-op for Claude Code — settings changes require a restart.
func (a *ClaudeCodeAgent) UpdateSettings(_ *leapmuxv1.AgentSettings) bool {
	return false
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

// providerRegistration holds the factory function, default model list,
// option groups, and environment variable keys for a provider.
type providerRegistration struct {
	start         startFunc
	defaultModels []*leapmuxv1.AvailableModel
	optionGroups  []*leapmuxv1.AvailableOptionGroup
	envModelKey   string // e.g. "LEAPMUX_CLAUDE_DEFAULT_MODEL"
	envEffortKey  string // e.g. "LEAPMUX_CLAUDE_DEFAULT_EFFORT"
}

// providerRegistry maps each AgentProvider to its registration.
// Providers register at package init time via registerProvider.
var providerRegistry = map[leapmuxv1.AgentProvider]providerRegistration{}

// registerProvider registers a provider's factory function, default model list,
// option groups, and environment variable keys for overriding defaults.
func registerProvider(
	provider leapmuxv1.AgentProvider,
	start startFunc,
	defaultModels []*leapmuxv1.AvailableModel,
	optionGroups []*leapmuxv1.AvailableOptionGroup,
	envModelKey, envEffortKey string,
) {
	providerRegistry[provider] = providerRegistration{
		start:         start,
		defaultModels: defaultModels,
		optionGroups:  optionGroups,
		envModelKey:   envModelKey,
		envEffortKey:  envEffortKey,
	}
}

// DefaultModel returns the default model ID for a provider, checking the
// provider's environment variable first, then falling back to the model
// marked IsDefault in the registered model list.
func DefaultModel(provider leapmuxv1.AgentProvider) string {
	reg, ok := providerRegistry[provider]
	if !ok {
		return ""
	}
	if reg.envModelKey != "" {
		if env := os.Getenv(reg.envModelKey); env != "" {
			return env
		}
	}
	for _, m := range reg.defaultModels {
		if m.IsDefault {
			return m.Id
		}
	}
	if len(reg.defaultModels) > 0 {
		return reg.defaultModels[0].Id
	}
	return ""
}

// DefaultEffort returns the default effort ID for a provider, checking the
// provider's environment variable first, then falling back to the default
// effort of the default model.
func DefaultEffort(provider leapmuxv1.AgentProvider) string {
	reg, ok := providerRegistry[provider]
	if !ok {
		return ""
	}
	if reg.envEffortKey != "" {
		if env := os.Getenv(reg.envEffortKey); env != "" {
			return env
		}
	}
	defaultModelID := DefaultModel(provider)
	for _, m := range reg.defaultModels {
		if m.Id == defaultModelID && m.DefaultEffort != "" {
			return m.DefaultEffort
		}
	}
	return ""
}

func init() {
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
			return StartClaudeCode(ctx, opts, sink)
		},
		claudeCodeAvailableModels,
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:   "permissionMode",
			Label: "Permission Mode",
			Options: []*leapmuxv1.AvailableOption{
				{Id: PermissionModeDefault, Name: "Default"},
				{Id: PermissionModePlan, Name: "Plan Mode"},
				{Id: PermissionModeAcceptEdits, Name: "Accept Edits"},
				{Id: PermissionModeBypassPermissions, Name: "Bypass Permissions"},
			},
		}},
		"LEAPMUX_CLAUDE_DEFAULT_MODEL",
		"LEAPMUX_CLAUDE_DEFAULT_EFFORT",
	)
}

var claudeCodeAvailableModels = []*leapmuxv1.AvailableModel{
	{Id: "opus", DisplayName: "Opus", Description: "Most capable for complex work", IsDefault: true, DefaultEffort: "high", SupportedEfforts: claudeCodeEffortAll, ContextWindow: 200_000},
	{Id: "opus[1m]", DisplayName: "Opus (1M context)", Description: "Most capable for complex work \u00b7 May be billed as extra usage", DefaultEffort: "high", SupportedEfforts: claudeCodeEffortAll, ContextWindow: 1_000_000},
	{Id: "sonnet", DisplayName: "Sonnet", Description: "Best for everyday tasks", DefaultEffort: "high", SupportedEfforts: claudeCodeEffortNoMax, ContextWindow: 200_000},
	{Id: "sonnet[1m]", DisplayName: "Sonnet (1M context)", Description: "Best for everyday tasks \u00b7 May be billed as extra usage", DefaultEffort: "high", SupportedEfforts: claudeCodeEffortNoMax, ContextWindow: 1_000_000},
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

// handlePendingControlResponse checks if a line is a control_response matching
// a pending request. If so, it sends the result to the waiting channel and
// returns true (the line should be consumed, not forwarded).
func (a *ClaudeCodeAgent) handlePendingControlResponse(line []byte) bool {
	// Quick check to avoid parsing non-control_response lines.
	if !bytes.Contains(line, []byte(`"control_response"`)) {
		return false
	}

	var envelope struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Response  struct {
				Mode string `json:"mode"`
			} `json:"response"`
			Error string `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil || envelope.Type != "control_response" {
		return false
	}

	reqID := envelope.Response.RequestID
	a.pendingControlMu.Lock()
	ch, ok := a.pendingControl[reqID]
	a.pendingControlMu.Unlock()

	if !ok {
		return false
	}

	result := claudeCodeControlResult{
		Success: envelope.Response.Subtype == "success",
		Mode:    envelope.Response.Response.Mode,
		Error:   envelope.Response.Error,
	}
	ch <- result
	return true
}

func (a *ClaudeCodeAgent) readOutputLoop(scanner *bufio.Scanner) {
	a.readOutput(scanner, a.handlePendingControlResponse, a.HandleOutput)
}

func generateRequestID() string {
	b := make([]byte, 13)
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}

// filterEnv returns a copy of environ with entries matching any of the
// given key names removed. Keys are matched case-insensitively by the
// portion before the first '='.
func filterEnv(environ []string, keys ...string) []string {
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		skip := false
		for _, k := range keys {
			if strings.EqualFold(name, k) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, entry)
		}
	}
	return filtered
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
