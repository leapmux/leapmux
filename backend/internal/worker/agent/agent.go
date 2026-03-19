package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ExitHandler is called when an agent process exits.
// agentID identifies the agent, exitCode is the process exit code,
// and err is non-nil if the process exited with an error.
type ExitHandler func(agentID string, exitCode int, err error)

// controlResult holds the outcome of a pending control request.
type controlResult struct {
	Success bool
	Mode    string
	Error   string
}

// Agent manages a single Claude Code process.
type Agent struct {
	agentID    string
	model      string
	workingDir string
	homeDir    string
	sink       OutputSink

	// Claude Code-specific state.
	contextUsage    *contextUsageSnapshot
	lastAgentStatus string

	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stderrBuf   *bytes.Buffer
	stderrMu    sync.Mutex    // protects stderrBuf when drained via goroutine
	stderrDone  chan struct{} // closed when the stderr goroutine finishes
	ctx         context.Context
	cancel      context.CancelFunc
	processDone chan struct{} // closed when the process exits
	waitErr     error         // set before processDone is closed

	preambleDelimiter  string            // if set, readOutput skips lines until this delimiter
	preambleMetaPrefix string            // prefix for metadata lines (before delimiter)
	preambleMeta       map[string]string // parsed key=value metadata from preamble
	preambleOutput     []string          // captured preamble lines (before delimiter)

	mu      sync.Mutex
	stopped bool

	pendingControlMu        sync.Mutex
	pendingControl          map[string]chan<- controlResult
	confirmedPermissionMode string
}

// Options configures a new Agent.
type Options struct {
	AgentID         string
	Model           string
	Effort          string // Effort level (low, medium, high, max)
	WorkingDir      string
	ResumeSessionID string                  // If set, uses --resume to resume a previous session
	PermissionMode  string                  // Permission mode to set on startup (default, acceptEdits, plan, bypassPermissions)
	StartupTimeout  time.Duration           // Timeout for the startup handshake (default: 30s)
	Shell           string                  // Default shell path (always set when using shell wrapper)
	LoginShell      bool                    // If true, use interactive+login shell flags
	HomeDir         string                  // User's home directory (for reading Claude Code settings)
	AgentProvider   leapmuxv1.AgentProvider // Coding agent provider (default: CLAUDE_CODE)
}

// Start spawns a new Claude Code process and begins reading its output.
// The sink receives parsed output events via the Provider.HandleOutput method.
//
// Claude Code with --input-format stream-json does not produce any output
// (including the init message) until it receives input on stdin. Therefore,
// Start returns immediately without waiting for output. The session ID is
// extracted later from the init message when the first user message triggers
// output from Claude.
func (o Options) startupTimeout() time.Duration {
	if o.StartupTimeout > 0 {
		return o.StartupTimeout
	}
	return 30 * time.Second
}

func Start(ctx context.Context, opts Options, sink OutputSink) (*Agent, error) {
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
		ctx, opts.Shell, opts.LoginShell, baseArgs, modelEffortArgs, opts.WorkingDir,
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

	var stderrBuf bytes.Buffer
	a := &Agent{
		agentID:            opts.AgentID,
		model:              opts.Model,
		workingDir:         opts.WorkingDir,
		homeDir:            opts.HomeDir,
		sink:               sink,
		cmd:                cmd,
		stdin:              stdin,
		ctx:                ctx,
		cancel:             cancel,
		stderrBuf:          &stderrBuf,
		stderrDone:         make(chan struct{}),
		preambleDelimiter:  preambleDelimiter,
		preambleMetaPrefix: metaPrefix,
		preambleMeta:       make(map[string]string),
		processDone:        make(chan struct{}),
		pendingControl:     make(map[string]chan<- controlResult),
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Drain stderr in a background goroutine.
	const maxStderrSize = 1 << 20 // 1MB
	go func() {
		defer close(a.stderrDone)
		limited := io.LimitReader(stderrPipe, maxStderrSize)
		a.stderrMu.Lock()
		_, _ = io.Copy(&stderrBuf, limited)
		a.stderrMu.Unlock()
		// Discard any remaining stderr beyond the limit.
		_, _ = io.Copy(io.Discard, stderrPipe)
	}()

	// Read stdout in a background goroutine. Output will only arrive after
	// the first message is sent to stdin (Claude Code behavior with
	// --input-format stream-json).
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutput(scanner)

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
	mode := opts.PermissionMode
	if mode == "" {
		mode = "default"
	}
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
func (a *Agent) SendInput(content string) error {
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

// SendRawInput writes raw bytes directly to the agent's stdin without
// wrapping in a UserInputMessage. Used for control_response messages.
func (a *Agent) SendRawInput(data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return fmt.Errorf("agent is stopped")
	}

	// Ensure the data ends with a newline.
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}

	if _, err := a.stdin.Write(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}

	return nil
}

// Stop terminates the agent process gracefully. It closes stdin to signal
// EOF and gives the process a short grace period to persist its session
// state before cancelling the context (which sends SIGTERM, then SIGKILL
// after WaitDelay).
func (a *Agent) Stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.mu.Unlock()

	// Close stdin to signal EOF — Claude Code treats this as a shutdown signal.
	_ = a.stdin.Close()

	// Give the process a moment to save session state and exit on its own.
	select {
	case <-a.processDone:
		// Process exited cleanly after stdin EOF.
		return
	case <-time.After(3 * time.Second):
		// Process didn't exit in time; cancel context to send SIGTERM.
		// Go's exec.CommandContext will then send SIGKILL after WaitDelay
		// if the process still hasn't exited.
		a.cancel()
	}

	// Wait for the process to actually exit after SIGTERM/SIGKILL.
	<-a.processDone
}

// IsStopped returns true if the agent was intentionally stopped via Stop().
func (a *Agent) IsStopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

// Wait blocks until the agent process exits and returns its exit error.
func (a *Agent) Wait() error {
	<-a.processDone
	return a.waitErr
}

// AgentID returns the unique identifier for this agent.
func (a *Agent) AgentID() string {
	return a.agentID
}

// Stderr returns the captured stderr output from the agent process.
// It waits for the stderr goroutine to finish draining the pipe (up to
// 3 seconds) to avoid racing with the goroutine after process exit.
func (a *Agent) Stderr() string {
	select {
	case <-a.stderrDone:
	case <-time.After(3 * time.Second):
	}
	a.stderrMu.Lock()
	defer a.stderrMu.Unlock()
	if a.stderrBuf == nil {
		return ""
	}
	return a.stderrBuf.String()
}

// PreambleOutput returns the captured stdout preamble lines (before the
// delimiter) when running under a login shell wrapper. Returns empty string
// if no preamble was captured.
func (a *Agent) PreambleOutput() string {
	if len(a.preambleOutput) == 0 {
		return ""
	}
	return strings.Join(a.preambleOutput, "\n")
}

// processExitError returns a descriptive error for a process that exited
// unexpectedly. It includes the exit code when available. Callers that
// need stderr and preamble details should read them separately (e.g. via
// a.formatStartupError).
func (a *Agent) processExitError() error {
	if a.waitErr != nil {
		if exitErr, ok := a.waitErr.(*exec.ExitError); ok {
			return fmt.Errorf("agent process exited with code %d", exitErr.ExitCode())
		}
	}
	return fmt.Errorf("agent process exited unexpectedly")
}

// formatStartupError returns a descriptive error including stderr and
// preamble output (if any) for frontend diagnostics.
func (a *Agent) formatStartupError(phase string, err error) error {
	parts := []string{fmt.Sprintf("%s: %s", phase, err)}
	if stderr := strings.TrimSpace(a.Stderr()); stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if preamble := strings.TrimSpace(a.PreambleOutput()); preamble != "" {
		parts = append(parts, "shell preamble: "+preamble)
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// SupportsModelEffort returns whether the agent supports --model/--effort CLI args.
// When a third-party provider was detected from settings files at startup,
// the shell wrapper runs without conditional logic and no metadata is emitted,
// so this returns false. Otherwise, the shell wrapper checks env vars at
// runtime and reports the result via preamble metadata.
func (a *Agent) SupportsModelEffort() bool {
	return a.preambleMeta["supports_model_effort"] == "true"
}

// ConfirmedPermissionMode returns the permission mode confirmed by the agent
// during the startup handshake.
func (a *Agent) ConfirmedPermissionMode() string {
	return a.confirmedPermissionMode
}

// sendControlAndWait sends a control request to the agent and waits for the
// response. The requestBody should be the JSON for the "request" field only
// (e.g. `{"subtype":"initialize"}`). Returns the control result or an error
// on timeout/cancellation/failure.
func (a *Agent) sendControlAndWait(ctx context.Context, requestBody string, timeout time.Duration) (controlResult, error) {
	requestID := generateRequestID()
	ch := make(chan controlResult, 1)
	a.registerPendingControl(requestID, ch)

	msg := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":%s}`, requestID, requestBody)
	if err := a.SendRawInput([]byte(msg)); err != nil {
		a.unregisterPendingControl(requestID)
		return controlResult{}, err
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
		return controlResult{}, a.processExitError()
	case <-ctx.Done():
		a.unregisterPendingControl(requestID)
		return controlResult{}, ctx.Err()
	case <-time.After(timeout):
		a.unregisterPendingControl(requestID)
		return controlResult{}, fmt.Errorf("timeout waiting for agent to respond")
	}
}

func (a *Agent) registerPendingControl(requestID string, ch chan<- controlResult) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	a.pendingControl[requestID] = ch
}

func (a *Agent) unregisterPendingControl(requestID string) {
	a.pendingControlMu.Lock()
	defer a.pendingControlMu.Unlock()
	delete(a.pendingControl, requestID)
}

// handlePendingControlResponse checks if a line is a control_response matching
// a pending request. If so, it sends the result to the waiting channel and
// returns true (the line should be consumed, not forwarded).
func (a *Agent) handlePendingControlResponse(line []byte) bool {
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

	result := controlResult{
		Success: envelope.Response.Subtype == "success",
		Mode:    envelope.Response.Response.Mode,
		Error:   envelope.Response.Error,
	}
	ch <- result
	return true
}

func (a *Agent) readOutput(scanner *bufio.Scanner) {
	// If a preamble delimiter is set, skip lines until the delimiter is found.
	// This handles shell login preamble (motd, .zshrc output, etc.) that
	// appears before claude's NDJSON stream.
	if a.preambleDelimiter != "" {
		delimBytes := []byte(a.preambleDelimiter)
		metaPrefixBytes := []byte(a.preambleMetaPrefix)
		const maxPreambleLines = 50
		for scanner.Scan() {
			line := scanner.Bytes()
			trimmed := bytes.TrimSpace(line)
			if bytes.Equal(trimmed, delimBytes) {
				break
			}
			// Parse metadata lines (e.g. "__LEAPMUX_META_xxx__ key=value").
			if len(metaPrefixBytes) > 0 && bytes.HasPrefix(trimmed, metaPrefixBytes) {
				kv := string(trimmed[len(metaPrefixBytes):])
				if eqIdx := strings.IndexByte(kv, '='); eqIdx >= 0 {
					a.preambleMeta[kv[:eqIdx]] = kv[eqIdx+1:]
				}
				continue
			}
			if len(a.preambleOutput) < maxPreambleLines {
				a.preambleOutput = append(a.preambleOutput, string(line))
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Make a copy since scanner reuses the buffer.
		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		// Check if this is a control_response for a pending request.
		// If so, consume it (don't forward to hub).
		if a.handlePendingControlResponse(lineCopy) {
			continue
		}

		a.HandleOutput(lineCopy)
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("agent stdout read error",
			"agent_id", a.agentID,
			"error", err,
		)
	}

	// Wait for the process to exit and signal completion. This must happen
	// after stdout is fully drained (scanner loop above) to avoid a race
	// where cmd.Wait() closes the stdout pipe while the scanner is reading.
	a.waitErr = a.cmd.Wait()
	close(a.processDone)
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
// Claude Code. Haiku does not support effort levels, and max effort is only
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
