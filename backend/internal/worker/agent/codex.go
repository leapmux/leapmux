package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// CodexAgent manages a single Codex app-server process.
type CodexAgent struct {
	agentID    string
	model      string
	workingDir string
	sink       OutputSink

	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stderrBuf   *bytes.Buffer
	stderrMu    sync.Mutex
	stderrDone  chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	processDone chan struct{}
	waitErr     error

	mu      sync.Mutex
	stopped bool

	// Codex-specific state.
	threadID     string       // from thread/start response
	turnID       string       // currently active turn ID
	nextReqID    atomic.Int64 // JSON-RPC request ID counter
	pendingReqs  sync.Map     // reqID (int64) -> chan json.RawMessage
	approvalMode string       // mapped permission mode ("never", "onRequest", "unlessTrusted")
}

// StartCodex starts a Codex agent process and performs the JSON-RPC handshake.
func StartCodex(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, "codex", "app-server")
	cmd.Dir = opts.WorkingDir
	cmd.Env = filterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")

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

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	a := &CodexAgent{
		agentID:     opts.AgentID,
		model:       opts.Model,
		workingDir:  opts.WorkingDir,
		sink:        sink,
		cmd:         cmd,
		stdin:       stdin,
		ctx:         ctx,
		cancel:      cancel,
		stderrBuf:   &stderrBuf,
		stderrDone:  make(chan struct{}),
		processDone: make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start codex: %w", err)
	}

	// Drain stderr in background.
	const maxStderrSize = 1 << 20
	go func() {
		defer close(a.stderrDone)
		limited := io.LimitReader(stderrPipe, maxStderrSize)
		a.stderrMu.Lock()
		_, _ = io.Copy(&stderrBuf, limited)
		a.stderrMu.Unlock()
		_, _ = io.Copy(io.Discard, stderrPipe)
	}()

	// Read stdout JSONL in background.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutput(scanner)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	// 1. Send "initialize" request.
	initResp, err := a.sendRequest("initialize", json.RawMessage(`{
		"clientInfo": {"name": "leapmux", "version": "1.0.0"},
		"capabilities": {"experimentalApi": true}
	}`), timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}
	_ = initResp

	// 2. Send "initialized" notification.
	if err := a.sendNotification("initialized", nil); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialized notification", err)
	}

	// 3. Map permission mode to Codex approval policy.
	approvalPolicy := mapPermissionModeToApprovalPolicy(opts.PermissionMode)
	a.approvalMode = mapApprovalPolicyToPermissionMode(approvalPolicy)

	// 4. Send "thread/start" or "thread/resume" request.
	var threadMethod string
	var threadParams map[string]interface{}
	if opts.ResumeSessionID != "" {
		threadMethod = "thread/resume"
		threadParams = map[string]interface{}{
			"threadId":       opts.ResumeSessionID,
			"model":          opts.Model,
			"cwd":            opts.WorkingDir,
			"approvalPolicy": approvalPolicy,
			"sandbox":        "dangerFullAccess",
		}
	} else {
		threadMethod = "thread/start"
		threadParams = map[string]interface{}{
			"model":          opts.Model,
			"cwd":            opts.WorkingDir,
			"approvalPolicy": approvalPolicy,
			"sandbox":        "dangerFullAccess",
		}
	}
	threadParamsJSON, _ := json.Marshal(threadParams)
	threadResp, err := a.sendRequest(threadMethod, threadParamsJSON, timeout)
	if err != nil {
		// If thread/resume fails, fall back to thread/start.
		if threadMethod == "thread/resume" {
			slog.Warn("codex thread/resume failed, falling back to thread/start",
				"agent_id", opts.AgentID, "error", err)
			threadParams = map[string]interface{}{
				"model":          opts.Model,
				"cwd":            opts.WorkingDir,
				"approvalPolicy": approvalPolicy,
				"sandbox":        "dangerFullAccess",
			}
			threadParamsJSON, _ = json.Marshal(threadParams)
			threadResp, err = a.sendRequest("thread/start", threadParamsJSON, timeout)
		}
		if err != nil {
			cleanup()
			return nil, a.formatStartupError(threadMethod, err)
		}
	}

	// Extract thread ID from response.
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(threadResp, &threadResult); err == nil && threadResult.Thread.ID != "" {
		a.threadID = threadResult.Thread.ID
		sink.UpdateSessionID(a.threadID)
		sink.BroadcastStatusActive(a.threadID)
	}

	return a, nil
}

// mapPermissionModeToApprovalPolicy maps LeapMux permission modes to Codex approval policies.
func mapPermissionModeToApprovalPolicy(mode string) string {
	switch mode {
	case "bypassPermissions", "":
		return "never"
	case "default":
		return "onRequest"
	case "acceptEdits":
		return "unlessTrusted"
	default:
		return "never"
	}
}

// mapApprovalPolicyToPermissionMode maps Codex approval policies back to LeapMux permission modes.
func mapApprovalPolicyToPermissionMode(policy string) string {
	switch policy {
	case "never":
		return "bypassPermissions"
	case "onRequest":
		return "default"
	case "unlessTrusted":
		return "acceptEdits"
	default:
		return "bypassPermissions"
	}
}

// AgentID returns the unique identifier for this agent.
func (a *CodexAgent) AgentID() string {
	return a.agentID
}

// SendInput writes a user message to the agent via turn/start.
func (a *CodexAgent) SendInput(content string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return fmt.Errorf("agent is stopped")
	}

	if a.threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}

	params := map[string]interface{}{
		"threadId": a.threadID,
		"input": []map[string]interface{}{
			{"type": "text", "text": content},
		},
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}

	// Send turn/start as a request (don't wait for the full turn to complete
	// since we get streaming notifications). Use a generous timeout.
	resp, err := a.sendRequest("turn/start", paramsJSON, 30*time.Second)
	if err != nil {
		return fmt.Errorf("turn/start: %w", err)
	}

	// Extract turn ID from response.
	var turnResult struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(resp, &turnResult); err == nil && turnResult.Turn.ID != "" {
		a.turnID = turnResult.Turn.ID
	}

	return nil
}

// SendRawInput writes raw bytes directly to the agent's stdin.
// The frontend is responsible for sending provider-specific protocol messages
// (e.g., JSON-RPC for Codex, control_request for Claude Code).
func (a *CodexAgent) SendRawInput(data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.stopped {
		return fmt.Errorf("agent is stopped")
	}

	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}

	if _, err := a.stdin.Write(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}

	return nil
}

// Stop terminates the agent process gracefully.
func (a *CodexAgent) Stop() {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.mu.Unlock()

	_ = a.stdin.Close()

	select {
	case <-a.processDone:
		return
	case <-time.After(3 * time.Second):
		a.cancel()
	}

	<-a.processDone
}

// IsStopped returns true if the agent was intentionally stopped.
func (a *CodexAgent) IsStopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

// Wait blocks until the agent process exits.
func (a *CodexAgent) Wait() error {
	<-a.processDone
	return a.waitErr
}

// Stderr returns the captured stderr output.
func (a *CodexAgent) Stderr() string {
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

// SupportsModelEffort returns true — Codex supports model/effort via turn/start params.
func (a *CodexAgent) SupportsModelEffort() bool {
	return true
}

// ConfirmedPermissionMode returns the mapped permission mode.
func (a *CodexAgent) ConfirmedPermissionMode() string {
	return a.approvalMode
}

// HandleOutput processes a single JSONL notification from Codex.
func (a *CodexAgent) HandleOutput(content []byte) {
	handleCodexOutput(a, content)
}

// --- JSON-RPC helpers ---

// sendRequest sends a JSON-RPC request and waits for the response.
func (a *CodexAgent) sendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	reqID := a.nextReqID.Add(1)

	ch := make(chan json.RawMessage, 1)
	a.pendingReqs.Store(reqID, ch)
	defer a.pendingReqs.Delete(reqID)

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  method,
	}
	if params != nil {
		msg["params"] = json.RawMessage(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := a.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-a.processDone:
		return nil, a.processExitError()
	case <-a.ctx.Done():
		return nil, a.ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for %s response", method)
	}
}

// sendNotification sends a JSON-RPC notification (no id, no response expected).
func (a *CodexAgent) sendNotification(method string, params json.RawMessage) error {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		msg["params"] = json.RawMessage(params)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	data = append(data, '\n')

	if _, err := a.stdin.Write(data); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}

	return nil
}

// readOutput reads JSONL lines from stdout and dispatches them.
func (a *CodexAgent) readOutput(scanner *bufio.Scanner) {
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		// Check if this is a JSON-RPC response (has "id" + "result"/"error" but no "method").
		if a.handleJSONRPCResponse(lineCopy) {
			continue
		}

		// Otherwise it's a notification — forward to HandleOutput.
		a.HandleOutput(lineCopy)
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("codex agent stdout read error",
			"agent_id", a.agentID,
			"error", err,
		)
	}

	a.waitErr = a.cmd.Wait()
	close(a.processDone)
}

// handleJSONRPCResponse checks if a line is a JSON-RPC response and routes it.
func (a *CodexAgent) handleJSONRPCResponse(line []byte) bool {
	// Quick check: responses have "result" or "error" at the top level.
	if !bytes.Contains(line, []byte(`"result"`)) && !bytes.Contains(line, []byte(`"error"`)) {
		return false
	}

	var envelope struct {
		ID     *json.Number    `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return false
	}

	// If it has a "method" field, it's a notification or request, not a response.
	if envelope.Method != "" {
		return false
	}

	if envelope.ID == nil {
		return false
	}

	reqID, err := envelope.ID.Int64()
	if err != nil {
		return false
	}

	val, ok := a.pendingReqs.Load(reqID)
	if !ok {
		return false
	}

	ch := val.(chan json.RawMessage)
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		// Send error as the response — caller can inspect it.
		ch <- envelope.Error
	} else {
		ch <- envelope.Result
	}

	return true
}

// processExitError returns a descriptive error for unexpected process exit.
func (a *CodexAgent) processExitError() error {
	if a.waitErr != nil {
		if exitErr, ok := a.waitErr.(*exec.ExitError); ok {
			return fmt.Errorf("codex process exited with code %d", exitErr.ExitCode())
		}
	}
	return fmt.Errorf("codex process exited unexpectedly")
}

// formatStartupError returns a descriptive error including stderr.
func (a *CodexAgent) formatStartupError(phase string, err error) error {
	parts := []string{fmt.Sprintf("%s: %s", phase, err)}
	if stderr := a.Stderr(); stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	msg := parts[0]
	for i := 1; i < len(parts); i++ {
		msg += "; " + parts[i]
	}
	return fmt.Errorf("%s", msg)
}
