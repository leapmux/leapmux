package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
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

// AvailableOptionGroups returns the static Claude Code option groups.
func (a *ClaudeCodeAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
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
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Response  struct {
				Mode string `json:"mode"`
			} `json:"response"`
			Error string `json:"error"`
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

	result := claudeCodeControlResult{
		Success: envelope.Response.Subtype == "success",
		Mode:    envelope.Response.Response.Mode,
		Error:   envelope.Response.Error,
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
