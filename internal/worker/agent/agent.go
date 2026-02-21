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
)

// OutputHandler is called for each NDJSON line produced by the Claude Code process.
// The line is passed verbatim (not parsed).
type OutputHandler func(line []byte)

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

	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stderrBuf   *bytes.Buffer
	ctx         context.Context
	cancel      context.CancelFunc
	processDone chan struct{} // closed when the process exits
	waitErr     error         // set before processDone is closed

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
	Effort          string // Effort level (low, medium, high)
	WorkingDir      string
	ResumeSessionID string        // If set, uses --resume to resume a previous session
	PermissionMode  string        // Permission mode to set on startup (default, acceptEdits, plan, bypassPermissions)
	StartupTimeout  time.Duration // Timeout for the startup handshake (default: 30s)
}

// Start spawns a new Claude Code process and begins reading its output.
// The outputFn callback is called for each NDJSON line.
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

func Start(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	args := []string{
		"--model", opts.Model,
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--permission-prompt-tool", "stdio",
		"--setting-sources", "user,project,local",
	}

	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}

	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = opts.WorkingDir
	cmd.Env = filterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "CLAUDE_CODE_ENTRYPOINT=sdk-ts")

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

	// Capture stderr for debugging. If the process crashes, we want to know why.
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	a := &Agent{
		agentID:        opts.AgentID,
		model:          opts.Model,
		workingDir:     opts.WorkingDir,
		cmd:            cmd,
		stdin:          stdin,
		ctx:            ctx,
		cancel:         cancel,
		stderrBuf:      &stderrBuf,
		processDone:    make(chan struct{}),
		pendingControl: make(map[string]chan<- controlResult),
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Read stdout in a background goroutine. Output will only arrive after
	// the first message is sent to stdin (Claude Code behavior with
	// --input-format stream-json).
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutput(scanner, outputFn)

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
		return nil, fmt.Errorf("initialize: %w", err)
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
		return nil, fmt.Errorf("set_permission_mode: %w", err)
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
func (a *Agent) Stderr() string {
	if a.stderrBuf == nil {
		return ""
	}
	return a.stderrBuf.String()
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
		stderr := strings.TrimSpace(a.Stderr())
		if stderr != "" {
			return controlResult{}, fmt.Errorf("agent process exited: %s", stderr)
		}
		return controlResult{}, fmt.Errorf("agent process exited unexpectedly")
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

func (a *Agent) readOutput(scanner *bufio.Scanner, outputFn OutputHandler) {
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

		outputFn(lineCopy)
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
