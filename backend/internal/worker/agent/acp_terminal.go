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
	"time"
	"unicode/utf8"

	"github.com/leapmux/leapmux/internal/util/envutil"
	utilid "github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/leapmux/leapmux/util/validate"
)

// Default retained output size when terminal/create omits outputByteLimit.
// Matches Reasonix's host-terminal cap (1 MiB). Explicit 0 means retain
// nothing (still reports truncated when any bytes were produced).
const acpDefaultOutputByteLimit = 1 << 20

// The provider can request a smaller retained tail, but it cannot reserve an
// arbitrary part of the Worker heap for one command.
const acpMaxOutputByteLimit = 8 << 20
const acpMaxTerminalsPerPrompt = 32

// How long release/Stop waits for a killed terminal's wait goroutine before
// giving up. Tree-kill should make this unnecessary; the bound keeps Stop
// from hanging the agent lifecycle if a descendant still refuses to die.
const acpTerminalReleaseWait = 10 * time.Second

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
	bufStart       int
	bufSize        int
	truncated      bool
	byteLimit      int
	exited         bool
	exitCode       *int
	signal         *string
	registryClosed bool
	// killed is set when the host intentionally terminates the process
	// (terminal/kill, release, Stop, ClearContext). Registry status then
	// becomes StatusStopped rather than Failed.
	killed    bool
	jobObject *procutil.JobObject

	// done is closed once the process has exited and exit fields are set.
	done chan struct{}
}

type acpTerminalResult struct {
	Output    string  `json:"output"`
	Truncated bool    `json:"truncated"`
	ExitCode  *int    `json:"exitCode,omitempty"`
	Signal    *string `json:"signal,omitempty"`
}

type acpTerminalServices interface {
	ProgressServices
	BackgroundTaskServices
}

type acpTerminalContext interface {
	terminalAgentID() string
	terminalServices() acpTerminalServices
	terminalError(id json.RawMessage, code int, message string)
	terminalOK(id json.RawMessage, result any)
	currentTerminalSessionID() string
	currentWorkingDir() string
	terminalBaseEnv() []string
}

// acpTerminalHost owns every terminal process and retained result. It uses a
// narrow context interface, so terminal lifecycle code cannot mutate session
// transport or turn-assembly state.
type acpTerminalHost struct {
	bindOnce sync.Once
	context  acpTerminalContext
	// lifecycleMu keeps terminal requests on one ACP session boundary.
	lifecycleMu sync.RWMutex

	terminalsMu        sync.Mutex
	terminals          map[string]*acpTerminalSession
	completedTerminals map[string]acpTerminalResult
	terminalsClosed    bool
}

func (h *acpTerminalHost) bind(context acpTerminalContext) {
	h.bindOnce.Do(func() { h.context = context })
}

func (h *acpTerminalHost) ownerAgentID() string { return h.context.terminalAgentID() }

func (h *acpTerminalHost) services() acpTerminalServices { return h.context.terminalServices() }

func (h *acpTerminalHost) sendError(id json.RawMessage, code int, message string) {
	h.context.terminalError(id, code, message)
}

func (h *acpTerminalHost) sendOK(id json.RawMessage, result any) {
	h.context.terminalOK(id, result)
}

func (h *acpTerminalHost) lockSession(sessionID string) (func(), error) {
	h.lifecycleMu.RLock()
	current := h.context.currentTerminalSessionID()
	if current == "" {
		h.lifecycleMu.RUnlock()
		return nil, fmt.Errorf("no active session")
	}
	if sessionID != "" && sessionID != current {
		h.lifecycleMu.RUnlock()
		return nil, fmt.Errorf("sessionId mismatch")
	}
	return h.lifecycleMu.RUnlock, nil
}

func (h *acpTerminalHost) workingDir() string { return h.context.currentWorkingDir() }

func (h *acpTerminalHost) baseEnv() []string { return h.context.terminalBaseEnv() }

func (b *acpBase) terminalAgentID() string { return b.agentID }

func (b *acpBase) currentTerminalSessionID() string { return b.currentSessionID() }

func (b *acpBase) terminalServices() acpTerminalServices { return b.sink }

func (s *acpTerminalSession) appendOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byteLimit == 0 {
		// Explicit retain-nothing: discard bytes but mark truncated so
		// agents know output existed.
		s.truncated = true
		return
	}
	if s.buf == nil {
		s.buf = make([]byte, s.byteLimit)
	}
	if len(p) >= s.byteLimit {
		copy(s.buf, p[len(p)-s.byteLimit:])
		s.bufStart = 0
		s.bufSize = s.byteLimit
		s.truncated = true
		return
	}
	overflow := max(0, s.bufSize+len(p)-s.byteLimit)
	if overflow > 0 {
		s.bufStart = (s.bufStart + overflow) % s.byteLimit
		s.bufSize -= overflow
		s.truncated = true
	}
	writeAt := (s.bufStart + s.bufSize) % s.byteLimit
	first := min(len(p), s.byteLimit-writeAt)
	copy(s.buf[writeAt:], p[:first])
	copy(s.buf, p[first:])
	s.bufSize += len(p)
}

// truncateACPTerminalOutput drops the oldest bytes so retained is at most
// limit bytes, cutting at a UTF-8 character boundary (ACP requirement).
// limit == 0 retains nothing.
func truncateACPTerminalOutput(buf []byte, limit int) []byte {
	if limit == 0 {
		return nil
	}
	if limit < 0 || len(buf) <= limit {
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
	retained := make([]byte, s.bufSize)
	if s.bufSize > 0 {
		first := min(s.bufSize, len(s.buf)-s.bufStart)
		copy(retained, s.buf[s.bufStart:s.bufStart+first])
		copy(retained[first:], s.buf[:s.bufSize-first])
	}
	for len(retained) > 0 && !utf8.RuneStart(retained[0]) {
		retained = retained[1:]
		truncated = true
	}
	return string(retained), s.truncated || truncated, s.exitCode, s.signal, s.exited
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
	if code, sig, ok := exitStatusFromWaitStatus(ps); ok {
		return code, sig
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
	if !exited {
		s.killed = true
	}
	cancel := s.cancel
	job := s.jobObject
	cmd := s.cmd
	s.mu.Unlock()
	if exited {
		return
	}
	// Tree-kill first so grandchildren holding the pipes die before we
	// wait on stdout/stderr readers.
	if err := job.Terminate(); err != nil {
		slog.Debug("acp terminal job terminate failed", "terminal_id", s.id, "error", err)
	}
	if cancel != nil {
		cancel()
	}
	if job == nil && cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (s *acpTerminalSession) waitDone(timeout time.Duration) {
	select {
	case <-s.done:
	case <-time.After(timeout):
		slog.Warn("acp terminal wait timed out after kill",
			"terminal_id", s.id,
			"timeout", timeout,
		)
	}
}

// handleTerminalMethod dispatches an inbound ACP terminal/* JSON-RPC request.
// wait_for_exit replies asynchronously so the stdout read loop is never blocked.
func (b *acpTerminalHost) handleTerminalMethod(line *parsedLine) {
	if !line.HasID() {
		slog.Warn("acp terminal method missing id", "agent_id", b.ownerAgentID(), "method", line.Method)
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
		slog.Warn("acp terminal error response", "agent_id", b.ownerAgentID(), "error", err)
	}
}

func (b *acpBase) terminalOK(id json.RawMessage, result any) {
	if result == nil {
		result = map[string]interface{}{}
	}
	if err := b.sendResponse(id, result); err != nil {
		slog.Warn("acp terminal response", "agent_id", b.ownerAgentID(), "error", err)
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

func (b *acpTerminalHost) getTerminal(terminalID string) (*acpTerminalSession, bool) {
	b.terminalsMu.Lock()
	defer b.terminalsMu.Unlock()
	s, ok := b.terminals[terminalID]
	return s, ok
}

// terminalBaseEnv returns the environment the host command should inherit.
// Prefer the agent process env (already FinalizeAgentEnv + AppImage-scrubbed
// at launch) so GIT_OPTIONAL_LOCKS, LEAPMUX_WORKER, ExtraEnv, and identity
// scrubbing match the agent. Fall back to FinalizeAgentEnv(os.Environ())
// when no agent cmd is attached (unit tests).
func (b *acpBase) terminalBaseEnv() []string {
	var base []string
	if b.cmd != nil {
		base = append([]string(nil), b.cmd.Environ()...)
	} else {
		base = FinalizeAgentEnv(os.Environ(), Options{})
	}
	return envutil.ScrubAppImageEnvSlice(base)
}

func (b *acpTerminalHost) terminalCreate(id json.RawMessage, rawParams json.RawMessage) {
	var params acpTerminalCreateParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		b.sendError(id, -32602, "invalid terminal/create params")
		return
	}
	releaseSession, err := b.lockSession(params.SessionID)
	if err != nil {
		b.sendError(id, -32602, err.Error())
		return
	}
	defer releaseSession()
	if params.Command == "" {
		b.sendError(id, -32602, "command is required")
		return
	}

	cwd := params.Cwd
	if cwd == "" {
		cwd = b.workingDir()
	}
	if cwd == "" {
		b.sendError(id, -32602, "cwd is required")
		return
	}
	if !filepath.IsAbs(cwd) {
		b.sendError(id, -32602, "cwd must be an absolute path")
		return
	}

	limit := acpDefaultOutputByteLimit
	if params.OutputByteLimit != nil {
		if *params.OutputByteLimit < 0 {
			b.sendError(id, -32602, "outputByteLimit must be non-negative")
			return
		}
		limit = min(*params.OutputByteLimit, acpMaxOutputByteLimit)
	}

	b.terminalsMu.Lock()
	closed := b.terminalsClosed
	full := len(b.terminals)+len(b.completedTerminals) >= acpMaxTerminalsPerPrompt
	b.terminalsMu.Unlock()
	if closed {
		b.sendError(id, -32603, "agent is stopped")
		return
	}
	if full {
		b.sendError(id, -32603, "terminal limit reached")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := buildACPTerminalCmd(ctx, params.Command, params.Args)
	configureACPTerminalCmd(cmd)
	cmd.Dir = cwd
	cmd.Env = mergeACPTerminalEnv(b.baseEnv(), params.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		b.sendError(id, -32603, "stdout pipe: "+err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		b.sendError(id, -32603, "stderr pipe: "+err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		cancel()
		b.sendError(id, -32603, "start command: "+err.Error())
		return
	}

	job, jobErr := procutil.AssignPID(cmd.Process.Pid)
	if jobErr != nil {
		slog.Warn("acp terminal attach job object failed",
			"agent_id", b.ownerAgentID(),
			"error", jobErr,
		)
	}

	termID := "term_" + utilid.Generate()
	sess := &acpTerminalSession{
		id:        termID,
		command:   params.Command,
		cmd:       cmd,
		cancel:    cancel,
		byteLimit: limit,
		jobObject: job,
		done:      make(chan struct{}),
	}

	b.terminalsMu.Lock()
	if b.terminalsClosed || len(b.terminals)+len(b.completedTerminals) >= acpMaxTerminalsPerPrompt {
		closed = b.terminalsClosed
		b.terminalsMu.Unlock()
		b.discardUnregisteredTerminal(sess, stdout, stderr)
		if closed {
			b.sendError(id, -32603, "agent is stopped")
		} else {
			b.sendError(id, -32603, "terminal limit reached")
		}
		return
	}
	if b.terminals == nil {
		b.terminals = make(map[string]*acpTerminalSession)
	}
	b.terminals[termID] = sess
	b.terminalsMu.Unlock()

	// Upsert before starting the waiters/reply so CloseBackgroundTask on a
	// fast-exiting command cannot land before the Running row exists.
	// terminal/create carries the command and nothing else -- there is no
	// description field for it to be confused with -- so this title IS the
	// command, and the client can set it as code. The "shell" fallback is not,
	// which is why the flag tracks the branch rather than the row's kind.
	//
	// CleanName runs HERE and not only at the sink, because the registry
	// applies the same name rule to every title. A command that holds nothing
	// but the characters that the rule strips would reach the row blank and
	// skip the "shell" fallback below, which leaves the row with no label at
	// all. CleanName is idempotent, so the sink's own call changes nothing
	// after this one.
	title := validate.CleanName(bgtask.FirstLine(params.Command))
	titleIsCommand := title != ""
	if title == "" {
		title = "shell"
	}
	if err := b.services().UpsertBackgroundTask(bgtask.Upsert{
		RowKey:         termID,
		Kind:           bgtask.KindShell,
		ParentAgentID:  b.ownerAgentID(),
		Title:          title,
		TitleIsCommand: titleIsCommand,
		Status:         bgtask.StatusRunning,
	}); err != nil {
		slog.Warn("acp terminal upsert failed", "agent_id", b.ownerAgentID(), "terminal_id", termID, "error", err)
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
		sess.recordExit(processStateFromWait(cmd.Wait(), cmd.ProcessState))
		// The registry row reaches its final status BEFORE done closes.
		// terminal/wait_for_exit replies off done, so the opposite order lets
		// the client read its own terminal's row and still see RUNNING.
		b.closeTerminalRegistry(sess)
		b.rememberCompletedTerminal(sess)
		b.services().ReportProgress(CompleteOutputProgress("terminal:" + sess.id))
		close(sess.done)
	}()

	b.sendOK(id, map[string]interface{}{"terminalId": termID})
}

func (b *acpTerminalHost) discardUnregisteredTerminal(
	sess *acpTerminalSession,
	stdout, stderr io.Reader,
) {
	sess.kill()
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		b.copyTerminalOutput(sess, stdout)
	}()
	go func() {
		defer readers.Done()
		b.copyTerminalOutput(sess, stderr)
	}()
	readers.Wait()
	sess.recordExit(processStateFromWait(sess.cmd.Wait(), sess.cmd.ProcessState))
	close(sess.done)
}

func processStateFromWait(waitErr error, state *os.ProcessState) *os.ProcessState {
	if state != nil {
		return state
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		return exitErr.ProcessState
	}
	return nil
}

func (b *acpTerminalHost) rememberCompletedTerminal(sess *acpTerminalSession) {
	output, truncated, exitCode, signal, _ := sess.snapshot()
	b.terminalsMu.Lock()
	if b.completedTerminals == nil {
		b.completedTerminals = make(map[string]acpTerminalResult)
	}
	b.completedTerminals[sess.id] = acpTerminalResult{
		Output: output, Truncated: truncated, ExitCode: exitCode, Signal: signal,
	}
	b.terminalsMu.Unlock()
}

func (b *acpTerminalHost) takeCompletedTerminal(terminalID string) (acpTerminalResult, bool) {
	b.terminalsMu.Lock()
	defer b.terminalsMu.Unlock()
	result, ok := b.completedTerminals[terminalID]
	delete(b.completedTerminals, terminalID)
	return result, ok
}

func (b *acpTerminalHost) clearCompletedTerminals() {
	b.terminalsMu.Lock()
	b.completedTerminals = nil
	b.terminalsMu.Unlock()
}

func (b *acpTerminalHost) copyTerminalOutput(sess *acpTerminalSession, r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sess.appendOutput(buf[:n])
			b.services().ReportProgress(OutputDeltaProgress("terminal:"+sess.id, int64(n)))
		}
		if err != nil {
			return
		}
	}
}

func (b *acpTerminalHost) closeTerminalRegistry(sess *acpTerminalSession) {
	sess.mu.Lock()
	if sess.registryClosed {
		sess.mu.Unlock()
		return
	}
	sess.registryClosed = true
	exitCode := sess.exitCode
	killed := sess.killed
	sess.mu.Unlock()

	status := bgtask.StatusCompleted
	switch {
	case killed:
		status = bgtask.StatusStopped
	case exitCode == nil || *exitCode != 0:
		status = bgtask.StatusFailed
	}
	if err := b.services().CloseBackgroundTask(sess.id, status); err != nil {
		slog.Warn("acp terminal close registry failed", "agent_id", b.ownerAgentID(), "terminal_id", sess.id, "error", err)
	}
}

func (b *acpTerminalHost) parseTerminalIDParams(
	id json.RawMessage,
	rawParams json.RawMessage,
	method string,
) (acpTerminalIDParams, func(), bool) {
	var params acpTerminalIDParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		b.sendError(id, -32602, "invalid "+method+" params")
		return acpTerminalIDParams{}, nil, false
	}
	releaseSession, err := b.lockSession(params.SessionID)
	if err != nil {
		b.sendError(id, -32602, err.Error())
		return acpTerminalIDParams{}, nil, false
	}
	return params, releaseSession, true
}

func (b *acpTerminalHost) terminalOutput(id json.RawMessage, rawParams json.RawMessage) {
	params, releaseSession, valid := b.parseTerminalIDParams(id, rawParams, acpMethodTerminalOutput)
	if !valid {
		return
	}
	defer releaseSession()
	sess, ok := b.getTerminal(params.TerminalID)
	if !ok {
		b.sendError(id, -32602, "unknown terminalId")
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
	b.sendOK(id, result)
}

func (b *acpTerminalHost) terminalWaitForExit(id json.RawMessage, rawParams json.RawMessage) {
	params, releaseSession, valid := b.parseTerminalIDParams(id, rawParams, acpMethodTerminalWaitForExit)
	if !valid {
		return
	}
	defer releaseSession()
	sess, ok := b.getTerminal(params.TerminalID)
	if !ok {
		b.sendError(id, -32602, "unknown terminalId")
		return
	}

	// Reply on a goroutine: the caller runs on the agent stdout read loop and
	// must not block waiting for the child process.
	go func() {
		<-sess.done
		_, _, exitCode, signal, _ := sess.snapshot()
		b.sendOK(id, map[string]interface{}{
			"exitCode": exitCode,
			"signal":   signal,
		})
	}()
}

func (b *acpTerminalHost) terminalKill(id json.RawMessage, rawParams json.RawMessage) {
	params, releaseSession, valid := b.parseTerminalIDParams(id, rawParams, acpMethodTerminalKill)
	if !valid {
		return
	}
	defer releaseSession()
	sess, ok := b.getTerminal(params.TerminalID)
	if !ok {
		b.sendError(id, -32602, "unknown terminalId")
		return
	}
	sess.kill()
	b.sendOK(id, map[string]interface{}{})
}

func (b *acpTerminalHost) terminalRelease(id json.RawMessage, rawParams json.RawMessage) {
	params, releaseSession, valid := b.parseTerminalIDParams(id, rawParams, acpMethodTerminalRelease)
	if !valid {
		return
	}
	defer releaseSession()

	b.terminalsMu.Lock()
	sess, ok := b.terminals[params.TerminalID]
	if ok {
		delete(b.terminals, params.TerminalID)
	}
	b.terminalsMu.Unlock()
	if !ok {
		b.sendError(id, -32602, "unknown terminalId")
		return
	}

	// Kill + wait off the read loop so a still-running command cannot stall
	// further agent→client requests. Response fires once resources are free
	// (or the limited wait expires).
	go func() {
		b.releaseTerminal(sess)
		b.sendOK(id, map[string]interface{}{})
	}()
}

func (b *acpTerminalHost) releaseTerminal(sess *acpTerminalSession) {
	sess.kill()
	sess.waitDone(acpTerminalReleaseWait)
	b.closeTerminalRegistry(sess)
	b.services().ReportProgress(CompleteOutputProgress("terminal:" + sess.id))
}

// releaseAllTerminals kills and forgets every host terminal and latches the
// store closed so a racing terminal/create cannot re-seed orphans after Stop.
// Called from Stop and Wait (agent crash / natural exit).
func (b *acpTerminalHost) releaseAllTerminals() {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	b.releaseTerminals(true)
}

// releaseSessionTerminals kills host terminals bound to the outgoing ACP
// session without latching the store closed — ClearContext may create new
// terminals under the replacement sessionId.
func (b *acpTerminalHost) releaseSessionTerminals() {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	b.releaseTerminals(false)
}

// replaceSession releases terminals and changes the session as one boundary.
func (b *acpTerminalHost) replaceTerminalSession(replace func()) {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	b.releaseTerminals(false)
	replace()
}

func (b *acpTerminalHost) releaseTerminals(closeLatch bool) {
	b.terminalsMu.Lock()
	if closeLatch {
		b.terminalsClosed = true
	}
	sessions := make([]*acpTerminalSession, 0, len(b.terminals))
	for _, s := range b.terminals {
		sessions = append(sessions, s)
	}
	b.terminals = nil
	b.completedTerminals = nil
	b.terminalsMu.Unlock()

	for _, s := range sessions {
		b.releaseTerminal(s)
	}
}

// buildACPTerminalCmd builds the process for a terminal/create request.
// Agents typically pass a full shell string with empty args; ACP also
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
	// Pin each override so a duplicate inherited key is replaced rather than
	// layered (exec last-wins, but tests and introspection see one entry).
	assignments := make([]string, 0, len(overrides))
	for _, o := range overrides {
		if o.Name == "" {
			continue
		}
		assignments = append(assignments, o.Name+"="+o.Value)
	}
	if len(assignments) == 0 {
		return base
	}
	return envutil.PinEnv(base, assignments...)
}
