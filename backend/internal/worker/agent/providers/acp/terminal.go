package acp

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

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	utilid "github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/leapmux/leapmux/util/validate"
)

// This retained-output limit applies when terminal/create omits outputByteLimit.
// It matches Reasonix's host-terminal limit of 1 mebibyte (MiB).
// An explicit zero retains no output and still reports truncation when the process produces any bytes.
const acpDefaultOutputByteLimit = 1 << 20

// The provider can request a smaller retained tail, but it cannot reserve an
// arbitrary part of the Worker heap for one command.
const acpMaxOutputByteLimit = 8 << 20
const acpMaxTerminalsPerPrompt = 32

// This duration limits how long release and Stop wait for a killed terminal's waiter goroutine.
// Stopping the process tree should end that wait promptly.
// The limit prevents Stop from blocking the agent lifecycle indefinitely when a descendant refuses to exit.
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

// acpTerminalSession holds one ACP command process and its retained output buffer.
// It represents no interactive LeapMux pseudoterminal (PTY) tab.
// Agents read the process output through terminal/output and wait for its exit through wait_for_exit.
type acpTerminalSession struct {
	id      string
	command string
	owner   *procutil.ProcessOwner
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
	// killed records that the host intentionally stops the process through one of these paths:
	//   - terminal/kill.
	//   - release.
	//   - Stop.
	//   - ClearContext.
	//   - A stopped prompt.
	// The registry then reports StatusStopped instead of StatusFailed.
	killed bool
	// awaited counts the terminal/wait_for_exit requests that have no reply
	// yet. A pending wait marks the command as the foreground work of a tool
	// call, which a stopped prompt ends (see releaseAwaitedTerminals).
	awaited int

	// The waiter closes done after the process exits and the exit fields receive their values.
	done chan struct{}
}

type acpTerminalServices interface {
	agent.ProgressServices
	agent.BackgroundTaskServices
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
	completedTerminals map[string]contracts.ACPTerminalResult
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

func (b *Base) terminalAgentID() string { return b.AgentID() }

func (b *Base) currentTerminalSessionID() string { return b.CurrentSessionID() }

func (b *Base) terminalServices() acpTerminalServices { return b.sink }

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

// snapshot copies the retained ring-buffer suffix into an independent string.
// The string must not retain the ring buffer because appendOutput overwrites its bytes on the next read.
//
// The ring retains only the last byteLimit bytes.
// Its oldest byte can therefore continue a rune whose first byte already left the ring.
// ACP requires a character boundary, so discard that incomplete rune and report the lost bytes.
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
	// A negative ExitCode means a Unix signal stopped the process or no normal exit code exists.
	// Report a signal token so the agent can distinguish a timeout or kill from a numeric failure.
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
	owner := s.owner
	s.mu.Unlock()
	if exited {
		return
	}
	// Stop verified descendants before waiting for the stdout and stderr readers.
	if err := owner.Terminate(); err != nil {
		slog.Warn("terminate owned ACP terminal processes", "terminal_id", s.id, "error", err)
	}
	if cancel != nil {
		cancel()
	}
}

// beginAwait records one terminal/wait_for_exit request that waits for the exit.
func (s *acpTerminalSession) beginAwait() {
	s.mu.Lock()
	s.awaited++
	s.mu.Unlock()
}

// endAwait records the reply to one terminal/wait_for_exit request.
func (s *acpTerminalSession) endAwait() {
	s.mu.Lock()
	s.awaited--
	s.mu.Unlock()
}

// awaitedWhileRunning reports whether the command still runs while the agent
// waits for its exit.
func (s *acpTerminalSession) awaitedWhileRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.awaited > 0 && !s.exited
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
func (b *acpTerminalHost) handleTerminalMethod(line *providerkit.ParsedLine) {
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

// terminalError and terminalOK send every terminal response through the stdin writer without waiting for the write.
//
// The goroutine that drains the child's stdout answers each of these requests:
//   - terminal/create.
//   - terminal/output.
//   - terminal/kill.
// Waiting for the write there can deadlock.
// A child that reads no stdin blocks that write, while its unread stdout fills the output pipe and blocks the child.
// Neither side can then continue.
// One writer preserves frame order, so a later frame cannot overtake the response.
func (b *Base) terminalError(id json.RawMessage, code int, message string) {
	b.SendErrorResponseDetached(id, code, message, "terminal error")
}

func (b *Base) terminalOK(id json.RawMessage, result any) {
	if result == nil {
		result = map[string]interface{}{}
	}
	b.SendResponseDetached(id, result, "terminal reply")
}

func (b *Base) CurrentSessionID() string {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.sessionID
}

func (b *Base) currentWorkingDir() string {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	return b.workingDir
}

func (b *acpTerminalHost) getTerminal(terminalID string) (*acpTerminalSession, bool) {
	b.terminalsMu.Lock()
	defer b.terminalsMu.Unlock()
	s, ok := b.terminals[terminalID]
	return s, ok
}

// terminalBaseEnv returns the host command's inherited environment.
// Prefer the agent process environment, which already runs through FinalizeAgentEnv and removes AppImage-specific variables at launch.
// These environment rules then match the agent:
//   - GIT_OPTIONAL_LOCKS.
//   - LEAPMUX_WORKER.
//   - ExtraEnv.
//   - Identity-variable removal.
// When no agent command exists, as in unit tests, use FinalizeAgentEnv(os.Environ()).
func (b *Base) terminalBaseEnv() []string {
	var base []string
	if b.Cmd() != nil {
		base = append([]string(nil), b.Cmd().Environ()...)
	} else {
		base = providerkit.FinalizeAgentEnv(os.Environ(), agent.Options{})
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
	owner := procutil.PrepareProcess(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		b.sendError(id, -32603, "stdout pipe: "+err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		cancel()
		b.sendError(id, -32603, "stderr pipe: "+err.Error())
		return
	}

	if err := owner.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		b.sendError(id, -32603, "start command: "+err.Error())
		return
	}

	termID := "term_" + utilid.Generate()
	sess := &acpTerminalSession{
		id:        termID,
		command:   params.Command,
		owner:     owner,
		cancel:    cancel,
		byteLimit: limit,
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

	// Upsert before starting waiters or sending the response.
	// A quickly exiting command must not close its row before that Running row exists.
	// terminal/create supplies the command without a description, so the title is a verbatim command that the client can display as code.
	// The shell fallback is prose, so TitleIsCommand follows that selection rather than the row kind.
	//
	// Clean the command here before selecting the shell fallback, even though the sink also applies the same title rule.
	// A command containing only stripped characters otherwise becomes an empty stored title after bypassing that fallback.
	// CleanName is idempotent, so the sink's later call preserves this cleaned title.
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
		waitErr := owner.Wait()
		if closeErr := owner.Close(); closeErr != nil {
			slog.Warn("close owned ACP terminal processes", "terminal_id", sess.id, "error", closeErr)
		}
		sess.recordExit(processStateFromWait(waitErr, owner.ProcessState()))
		// Set the registry row's final status before closing done.
		// terminal/wait_for_exit replies when done closes.
		// The opposite order would let the client receive the exit response and still read Running from its own terminal row.
		b.closeTerminalRegistry(sess)
		b.rememberCompletedTerminal(sess)
		b.services().ReportProgress(agent.CompleteOutputProgress("terminal:" + sess.id))
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
	waitErr := sess.owner.Wait()
	if closeErr := sess.owner.Close(); closeErr != nil {
		slog.Warn("close unregistered ACP terminal processes", "terminal_id", sess.id, "error", closeErr)
	}
	sess.recordExit(processStateFromWait(waitErr, sess.owner.ProcessState()))
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
		b.completedTerminals = make(map[string]contracts.ACPTerminalResult)
	}
	b.completedTerminals[sess.id] = contracts.ACPTerminalResult{
		Output: output, Truncated: truncated, ExitCode: exitCode, Signal: signal,
	}
	b.terminalsMu.Unlock()
}

func (b *acpTerminalHost) takeCompletedTerminal(terminalID string) (contracts.ACPTerminalResult, bool) {
	b.terminalsMu.Lock()
	defer b.terminalsMu.Unlock()
	result, ok := b.completedTerminals[terminalID]
	delete(b.completedTerminals, terminalID)
	return result, ok
}

// terminalResultFor returns the output that a stored tool row uses for one terminal.
//
// For an exited process, read and consume its retained result from the completed set.
// For a running process, read its current output without removing it.
// LeapMux owns that process and retains every produced byte.
// After a stopped turn, the agent can immediately send its final tool update while the process still runs.
// Without this live read, the row would report `[output unavailable]` despite retained output.
//
// Return false only when LeapMux no longer holds the process or its retained result.
// The row then correctly reports that its output is unavailable.
func (b *acpTerminalHost) terminalResultFor(terminalID string) (contracts.ACPTerminalResult, bool) {
	if result, present := b.takeCompletedTerminal(terminalID); present {
		return result, true
	}
	b.terminalsMu.Lock()
	session := b.terminals[terminalID]
	b.terminalsMu.Unlock()
	if session == nil {
		return contracts.ACPTerminalResult{}, false
	}
	output, truncated, exitCode, signal, _ := session.snapshot()
	return contracts.ACPTerminalResult{Output: output, Truncated: truncated, ExitCode: exitCode, Signal: signal}, true
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
			b.services().ReportProgress(agent.OutputDeltaProgress("terminal:"+sess.id, int64(n)))
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

	status := bgtask.StatusSucceeded
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

	// Count the wait here, on the read loop, so a prompt response that the
	// agent sends after this request already sees it.
	sess.beginAwait()
	// Reply on a goroutine: the caller runs on the agent stdout read loop and
	// must not block waiting for the child process.
	go func() {
		<-sess.done
		_, _, exitCode, signal, _ := sess.snapshot()
		sess.endAwait()
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

	// Kill and wait outside the read loop so a running command cannot block later agent-to-client requests.
	// Send the response after releasing resources or reaching the wait limit.
	go func() {
		b.releaseTerminal(sess)
		b.sendOK(id, map[string]interface{}{})
	}()
}

func (b *acpTerminalHost) releaseTerminal(sess *acpTerminalSession) {
	sess.kill()
	sess.waitDone(acpTerminalReleaseWait)
	b.closeTerminalRegistry(sess)
	b.services().ReportProgress(agent.CompleteOutputProgress("terminal:" + sess.id))
}

// releaseAwaitedTerminals kills and removes each running terminal whose agent waits for its exit.
// finishPromptRequest calls it when the reader stops a prompt.
//
// ACP requires the agent to abort each tool call before answering a cancel and release every terminal that it no longer needs.
// fast-agent 0.10.42 does neither.
// Its terminal runtime catches only Exception, while cancellation raises asyncio.CancelledError, which inherits BaseException.
// Its terminal/wait_for_exit therefore survives the cancelled reply, and nothing releases the terminal.
// The retained Running row keeps the agent active after the stop.
//
// A pending wait identifies foreground work from an interrupted tool call.
// A terminal without a pending wait can hold background work for a later read, so preserve it.
// Each pending wait still receives a response that reports the kill.
//
// Do not acquire lifecycleMu here.
// Release can wait for the kill, and each terminal request on the reader acquires that lock.
// terminalsMu alone serializes removal from the shared map.
// terminal/release and session release remove the same map entry under that mutex.
// Only the operation that removes the entry releases its terminal.
func (b *acpTerminalHost) releaseAwaitedTerminals() {
	var awaited []*acpTerminalSession
	b.terminalsMu.Lock()
	for id, s := range b.terminals {
		if s.awaitedWhileRunning() {
			awaited = append(awaited, s)
			delete(b.terminals, id)
		}
	}
	b.terminalsMu.Unlock()
	for _, s := range awaited {
		b.releaseTerminal(s)
	}
}

// releaseAllTerminals kills and removes every host terminal, then closes the store against new entries.
// A concurrent terminal/create therefore cannot leave orphaned processes after Stop.
// Stop and Wait call it for intentional stops, crashes, and natural process exits.
func (b *acpTerminalHost) releaseAllTerminals() {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	b.releaseTerminals(true)
}

// releaseSessionTerminals kills host terminals attached to the outgoing ACP session without closing the store.
// ClearContext can then create terminals under the replacement sessionId.
func (b *acpTerminalHost) releaseSessionTerminals() {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	b.releaseTerminals(false)
}

// replaceTerminalSession releases terminals and changes the session as one boundary.
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

// buildACPTerminalCmd builds the process for terminal/create.
// Agents usually supply a complete shell command string with empty args.
// ACP also permits a command and separate argument array.
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
	// Set each override explicitly so it replaces a duplicate inherited key rather than appending another entry.
	// exec uses the last value, but tests and inspection must see only one entry.
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
