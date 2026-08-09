package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"unicode/utf8"

	utilid "github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Default retained output size when terminal/create omits outputByteLimit.
// Matches Reasonix's host-terminal cap (1 MiB).
const acpDefaultOutputByteLimit = 1 << 20

// acpTerminalEnvVar is one ACP EnvVariable entry on terminal/create.
type acpTerminalEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type acpTerminalCreateParams struct {
	SessionID       string              `json:"sessionId"`
	Command         string              `json:"command"`
	Args            []string            `json:"args"`
	Cwd             string              `json:"cwd"`
	Env             []acpTerminalEnvVar `json:"env"`
	OutputByteLimit *int                `json:"outputByteLimit"`
}

type acpTerminalIDParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// acpTerminalSession is one host-run ACP terminal command. It is not a
// LeapMux interactive PTY tab — just a process + retained output buffer
// that agents poll via terminal/output and wait_for_exit.
type acpTerminalSession struct {
	id      string
	command string
	cmd     *exec.Cmd
	cancel  context.CancelFunc

	mu             sync.Mutex
	buf            []byte
	truncated      bool
	byteLimit      int
	exited         bool
	exitCode       *int
	signal         *string
	registryClosed bool

	// done is closed once the process has exited and exit fields are set.
	done chan struct{}
}

func (s *acpTerminalSession) appendOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	if s.byteLimit > 0 && len(s.buf) > s.byteLimit {
		s.truncated = true
		s.buf = truncateACPTerminalOutput(s.buf, s.byteLimit)
	}
}

// truncateACPTerminalOutput drops the oldest bytes so retained is at most
// limit bytes, cutting at a UTF-8 character boundary (ACP requirement).
func truncateACPTerminalOutput(buf []byte, limit int) []byte {
	if limit <= 0 || len(buf) <= limit {
		return buf
	}
	start := len(buf) - limit
	for start < len(buf) && !utf8.RuneStart(buf[start]) {
		start++
	}
	if start >= len(buf) {
		return nil
	}
	out := make([]byte, len(buf)-start)
	copy(out, buf[start:])
	return out
}

func (s *acpTerminalSession) snapshot() (output string, truncated bool, exitCode *int, signal *string, exited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buf), s.truncated, s.exitCode, s.signal, s.exited
}

func (s *acpTerminalSession) recordExit(ps *os.ProcessState) {
	code, sig := exitStatusFromProcessState(ps)
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	s.signal = sig
	s.mu.Unlock()
}

func exitStatusFromProcessState(ps *os.ProcessState) (exitCode *int, signal *string) {
	if ps == nil {
		return nil, nil
	}
	code := ps.ExitCode()
	if code >= 0 {
		c := code
		return &c, nil
	}
	// Negative ExitCode means the process was stopped by a signal (Unix) or
	// never produced a normal exit code. Report a signal token so agents can
	// tell timeout/kill apart from a numeric failure.
	sig := "terminated"
	return nil, &sig
}

func (s *acpTerminalSession) kill() {
	s.mu.Lock()
	exited := s.exited
	cancel := s.cancel
	cmd := s.cmd
	s.mu.Unlock()
	if exited {
		return
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// handleTerminalMethod dispatches an inbound ACP terminal/* JSON-RPC request.
// wait_for_exit replies asynchronously so the stdout read loop is never blocked.
func (b *acpBase) handleTerminalMethod(line *parsedLine) {
	if !line.HasID() {
		slog.Warn("acp terminal method missing id", "agent_id", b.agentID, "method", line.Method)
		return
	}
	id := line.ID

	switch line.Method {
	case acpMethodTerminalCreate:
		b.terminalCreate(id, line.Params)
	case acpMethodTerminalOutput:
		b.terminalOutput(id, line.Params)
	case acpMethodTerminalWaitForExit:
		b.terminalWaitForExit(id, line.Params)
	case acpMethodTerminalKill:
		b.terminalKill(id, line.Params)
	case acpMethodTerminalRelease:
		b.terminalRelease(id, line.Params)
	}
}

func (b *acpBase) terminalError(id json.RawMessage, code int, message string) {
	if err := b.sendErrorResponse(id, code, message); err != nil {
		slog.Warn("acp terminal error response", "agent_id", b.agentID, "error", err)
	}
}

func (b *acpBase) terminalOK(id json.RawMessage, result any) {
	if result == nil {
		result = map[string]interface{}{}
	}
	if err := b.sendResponse(id, result); err != nil {
		slog.Warn("acp terminal response", "agent_id", b.agentID, "error", err)
	}
}

func (b *acpBase) currentSessionID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionID
}

func (b *acpBase) currentWorkingDir() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.workingDir
}

func (b *acpBase) requireSession(paramsSessionID string) error {
	current := b.currentSessionID()
	if current == "" {
		return fmt.Errorf("no active session")
	}
	if paramsSessionID != "" && paramsSessionID != current {
		return fmt.Errorf("sessionId mismatch")
	}
	return nil
}

func (b *acpBase) getTerminal(terminalID string) (*acpTerminalSession, bool) {
	b.terminalsMu.Lock()
	defer b.terminalsMu.Unlock()
	s, ok := b.terminals[terminalID]
	return s, ok
}

func (b *acpBase) terminalCreate(id json.RawMessage, rawParams json.RawMessage) {
	var params acpTerminalCreateParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		b.terminalError(id, -32602, "invalid terminal/create params")
		return
	}
	if err := b.requireSession(params.SessionID); err != nil {
		b.terminalError(id, -32602, err.Error())
		return
	}
	if params.Command == "" {
		b.terminalError(id, -32602, "command is required")
		return
	}

	cwd := params.Cwd
	if cwd == "" {
		cwd = b.currentWorkingDir()
	}
	if cwd == "" {
		b.terminalError(id, -32602, "cwd is required")
		return
	}
	if !filepath.IsAbs(cwd) {
		b.terminalError(id, -32602, "cwd must be an absolute path")
		return
	}

	limit := acpDefaultOutputByteLimit
	if params.OutputByteLimit != nil {
		if *params.OutputByteLimit < 0 {
			b.terminalError(id, -32602, "outputByteLimit must be non-negative")
			return
		}
		limit = *params.OutputByteLimit
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := buildACPTerminalCmd(ctx, params.Command, params.Args)
	cmd.Dir = cwd
	cmd.Env = mergeACPTerminalEnv(os.Environ(), params.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		b.terminalError(id, -32603, "stdout pipe: "+err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		b.terminalError(id, -32603, "stderr pipe: "+err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		cancel()
		b.terminalError(id, -32603, "start command: "+err.Error())
		return
	}

	termID := "term_" + utilid.Generate()
	sess := &acpTerminalSession{
		id:        termID,
		command:   params.Command,
		cmd:       cmd,
		cancel:    cancel,
		byteLimit: limit,
		done:      make(chan struct{}),
	}

	b.terminalsMu.Lock()
	if b.terminals == nil {
		b.terminals = make(map[string]*acpTerminalSession)
	}
	b.terminals[termID] = sess
	b.terminalsMu.Unlock()

	title := bgtask.FirstLine(params.Command)
	if title == "" {
		title = "shell"
	}
	if err := b.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:        termID,
		Kind:          bgtask.KindShell,
		ParentAgentID: b.agentID,
		Title:         title,
		Description:   title,
		Status:        bgtask.StatusRunning,
	}); err != nil {
		slog.Warn("acp terminal upsert failed", "agent_id", b.agentID, "terminal_id", termID, "error", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b.copyTerminalOutput(sess, stdout)
	}()
	go func() {
		defer wg.Done()
		b.copyTerminalOutput(sess, stderr)
	}()
	go func() {
		wg.Wait()
		waitErr := cmd.Wait()
		var ps *os.ProcessState
		if cmd.ProcessState != nil {
			ps = cmd.ProcessState
		} else if waitErr != nil {
			if ee, ok := waitErr.(*exec.ExitError); ok {
				ps = ee.ProcessState
			}
		}
		sess.recordExit(ps)
		close(sess.done)
		b.closeTerminalRegistry(sess)
	}()

	b.terminalOK(id, map[string]interface{}{"terminalId": termID})
}

func (b *acpBase) copyTerminalOutput(sess *acpTerminalSession, r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sess.appendOutput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (b *acpBase) closeTerminalRegistry(sess *acpTerminalSession) {
	sess.mu.Lock()
	if sess.registryClosed {
		sess.mu.Unlock()
		return
	}
	sess.registryClosed = true
	exitCode := sess.exitCode
	sess.mu.Unlock()

	status := bgtask.StatusCompleted
	if exitCode == nil || *exitCode != 0 {
		status = bgtask.StatusFailed
	}
	if err := b.sink.CloseBackgroundTask(sess.id, status); err != nil {
		slog.Warn("acp terminal close registry failed", "agent_id", b.agentID, "terminal_id", sess.id, "error", err)
	}
}

func (b *acpBase) terminalOutput(id json.RawMessage, rawParams json.RawMessage) {
	var params acpTerminalIDParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		b.terminalError(id, -32602, "invalid terminal/output params")
		return
	}
	if err := b.requireSession(params.SessionID); err != nil {
		b.terminalError(id, -32602, err.Error())
		return
	}
	sess, ok := b.getTerminal(params.TerminalID)
	if !ok {
		b.terminalError(id, -32602, "unknown terminalId")
		return
	}
	output, truncated, exitCode, signal, exited := sess.snapshot()
	result := map[string]interface{}{
		"output":    output,
		"truncated": truncated,
	}
	if exited {
		result["exitStatus"] = map[string]interface{}{
			"exitCode": exitCode,
			"signal":   signal,
		}
	}
	b.terminalOK(id, result)
}

func (b *acpBase) terminalWaitForExit(id json.RawMessage, rawParams json.RawMessage) {
	var params acpTerminalIDParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		b.terminalError(id, -32602, "invalid terminal/wait_for_exit params")
		return
	}
	if err := b.requireSession(params.SessionID); err != nil {
		b.terminalError(id, -32602, err.Error())
		return
	}
	sess, ok := b.getTerminal(params.TerminalID)
	if !ok {
		b.terminalError(id, -32602, "unknown terminalId")
		return
	}

	// Reply on a goroutine: the caller runs on the agent stdout read loop and
	// must not block waiting for the child process.
	go func() {
		<-sess.done
		_, _, exitCode, signal, _ := sess.snapshot()
		b.terminalOK(id, map[string]interface{}{
			"exitCode": exitCode,
			"signal":   signal,
		})
	}()
}

func (b *acpBase) terminalKill(id json.RawMessage, rawParams json.RawMessage) {
	var params acpTerminalIDParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		b.terminalError(id, -32602, "invalid terminal/kill params")
		return
	}
	if err := b.requireSession(params.SessionID); err != nil {
		b.terminalError(id, -32602, err.Error())
		return
	}
	sess, ok := b.getTerminal(params.TerminalID)
	if !ok {
		b.terminalError(id, -32602, "unknown terminalId")
		return
	}
	sess.kill()
	b.terminalOK(id, map[string]interface{}{})
}

func (b *acpBase) terminalRelease(id json.RawMessage, rawParams json.RawMessage) {
	var params acpTerminalIDParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		b.terminalError(id, -32602, "invalid terminal/release params")
		return
	}
	if err := b.requireSession(params.SessionID); err != nil {
		b.terminalError(id, -32602, err.Error())
		return
	}

	b.terminalsMu.Lock()
	sess, ok := b.terminals[params.TerminalID]
	if ok {
		delete(b.terminals, params.TerminalID)
	}
	b.terminalsMu.Unlock()
	if !ok {
		b.terminalError(id, -32602, "unknown terminalId")
		return
	}

	// Kill + wait off the read loop so a still-running command cannot stall
	// further agent→client requests. Response fires once resources are free.
	go func() {
		sess.kill()
		<-sess.done
		b.closeTerminalRegistry(sess)
		b.terminalOK(id, map[string]interface{}{})
	}()
}

// releaseAllTerminals kills and forgets every host terminal. Called from Stop.
func (b *acpBase) releaseAllTerminals() {
	b.terminalsMu.Lock()
	sessions := make([]*acpTerminalSession, 0, len(b.terminals))
	for _, s := range b.terminals {
		sessions = append(sessions, s)
	}
	b.terminals = nil
	b.terminalsMu.Unlock()

	for _, s := range sessions {
		s.kill()
		<-s.done
		b.closeTerminalRegistry(s)
	}
}

// buildACPTerminalCmd builds the process for a terminal/create request.
// Goose and Reasonix pass a full shell string with empty args; ACP also
// allows an argv-style command+args pair.
func buildACPTerminalCmd(ctx context.Context, command string, args []string) *exec.Cmd {
	if len(args) == 0 {
		if runtime.GOOS == "windows" {
			return exec.CommandContext(ctx, "cmd", "/C", command)
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	return exec.CommandContext(ctx, command, args...)
}

func mergeACPTerminalEnv(base []string, overrides []acpTerminalEnvVar) []string {
	if len(overrides) == 0 {
		return base
	}
	env := append([]string(nil), base...)
	index := make(map[string]int, len(env))
	for i, kv := range env {
		if name, _, ok := splitEnvKV(kv); ok {
			index[name] = i
		}
	}
	for _, o := range overrides {
		if o.Name == "" {
			continue
		}
		entry := o.Name + "=" + o.Value
		if i, ok := index[o.Name]; ok {
			env[i] = entry
			continue
		}
		index[o.Name] = len(env)
		env = append(env, entry)
	}
	return env
}

func splitEnvKV(kv string) (name, value string, ok bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}
