package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// CodexAgent manages a single Codex app-server process.
type CodexAgent struct {
	processBase // shared process lifecycle (Stop, Wait, Stderr, etc.)

	model      string
	workingDir string
	sink       OutputSink

	// Codex-specific state.
	threadID       string       // from thread/start response
	turnID         string       // currently active turn ID
	nextReqID      atomic.Int64 // JSON-RPC request ID counter
	pendingReqs    sync.Map     // reqID (int64) -> chan json.RawMessage
	approvalPolicy string       // Codex approval policy (stored as-is from DB)
}

// StartCodex starts a Codex agent process and performs the JSON-RPC handshake.
func StartCodex(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Codex doesn't have third-party provider detection or model/effort
	// conditional args, so we pass empty modelEffortArgs for a simple command.
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "codex", []string{"app-server"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "CLAUDECODE=1")
	}

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

	a := &CodexAgent{
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
		model:      opts.Model,
		workingDir: opts.WorkingDir,
		sink:       sink,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start codex: %w", err)
	}

	// Drain stderr in background.
	a.drainStderr(stderrPipe)

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
	if _, err := a.sendRequest("initialize", json.RawMessage(`{
		"clientInfo": {"name": "leapmux", "version": "1.0.0"},
		"capabilities": {"experimentalApi": true}
	}`), timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}

	// 2. Send "initialized" notification.
	if err := a.sendNotification("initialized", nil); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialized notification", err)
	}

	// 3. Use the permission mode directly as the Codex approval policy.
	// The DB stores provider-native values (e.g. "never", "on-request", "untrusted" for Codex).
	approvalPolicy := opts.PermissionMode
	if approvalPolicy == "" {
		approvalPolicy = "never"
	}
	a.approvalPolicy = approvalPolicy

	// 4. Send "thread/start" or "thread/resume" request.
	threadParams := map[string]interface{}{
		"model":          opts.Model,
		"cwd":            opts.WorkingDir,
		"approvalPolicy": approvalPolicy,
		"sandbox":        "danger-full-access",
	}

	threadMethod := "thread/start"
	if opts.ResumeSessionID != "" {
		threadMethod = "thread/resume"
		threadParams["threadId"] = opts.ResumeSessionID
	}

	threadParamsJSON, _ := json.Marshal(threadParams)
	threadResp, err := a.sendRequest(threadMethod, threadParamsJSON, timeout)
	if err != nil {
		// If thread/resume fails, fall back to thread/start.
		if threadMethod == "thread/resume" {
			slog.Warn("codex thread/resume failed, falling back to thread/start",
				"agent_id", opts.AgentID, "error", err)
			delete(threadParams, "threadId")
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

// SendInput writes a user message to the agent via turn/start.
func (a *CodexAgent) SendInput(content string) error {
	// Read shared state under lock, then release before the blocking RPC.
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID := a.threadID
	a.mu.Unlock()

	if threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}

	params := map[string]interface{}{
		"threadId": threadID,
		"input": []map[string]interface{}{
			{"type": "text", "text": content},
		},
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}

	// Send turn/start — the response arrives quickly (just the turn ID),
	// streaming content comes via notifications.
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
		a.mu.Lock()
		a.turnID = turnResult.Turn.ID
		a.mu.Unlock()
	}

	return nil
}

// SupportsModelEffort returns true — Codex supports model/effort via turn/start params.
func (a *CodexAgent) SupportsModelEffort() bool {
	return true
}

// ConfirmedPermissionMode returns the mapped permission mode.
func (a *CodexAgent) ConfirmedPermissionMode() string {
	return a.approvalPolicy
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

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-a.processDone:
		return nil, a.processExitError()
	case <-a.ctx.Done():
		return nil, a.ctx.Err()
	case <-timer.C:
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
	a.skipPreamble(scanner)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		if a.handleJSONRPCResponse(lineCopy) {
			continue
		}

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
// JSON-RPC responses have a numeric "id" field. Notifications do not.
// We unmarshal just the top-level id/method fields — notifications bail out
// quickly at the nil-ID check without inspecting the payload.
func (a *CodexAgent) handleJSONRPCResponse(line []byte) bool {
	var envelope struct {
		ID     *json.Number    `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return false
	}

	// Notifications have "method" but no "id"; responses have "id" but no "method".
	if envelope.ID == nil || envelope.Method != "" {
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

// formatStartupError includes stderr and preamble output for diagnostics.
func (a *CodexAgent) formatStartupError(phase string, err error) error {
	return a.processBase.formatStartupError(phase, err, a.PreambleOutput())
}
