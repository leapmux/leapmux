package providerkit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
)

// maxStderrSize is the maximum amount of stderr to buffer.
const maxStderrSize = 1 << 20 // 1MB

// Process contains the shared process lifecycle state and methods.
// claude.Agent embeds it directly; ACP agents (opencode.Agent, cursor.Agent)
// and codex.Agent embed it via JSONRPCProcess which adds JSON-RPC request plumbing.
type Process struct {
	agentID      string
	providerName string // e.g. "claude", "codex", "copilot" — used in log and error messages
	stdin        io.WriteCloser

	cmd         *exec.Cmd
	ctx         context.Context
	cancel      func()
	processDone chan struct{}
	waitErr     error

	stderrBuf  bytes.Buffer
	stderrMu   sync.Mutex
	stderrDone chan struct{}

	Mu      sync.Mutex
	stopped bool
	// processExited freezes exitCompletion at the instant cmd.Wait returns.
	// A later cleanup call must not reclassify a natural failure as a stop.
	processExited  bool
	exitCompletion agent.MessageCompletion
	// intentionalStop is set before a provider sends its graceful stop request.
	// Wait can then classify retained content while Stop still owns that request.
	intentionalStop atomic.Bool

	// TurnSeq issues the ordering token that rides every publish of this
	// provider's turn flag. It lives here so all five providers get it from one
	// embed and a sixth cannot forget it, and because Mu -- the lock that makes
	// the token atomic with the flag read -- lives here too.
	TurnSeq

	// stdinMu guards the write queue below. It is NOT held across a Write, so a
	// slow stdin (a full kernel pipe buffer, for example while a large inline
	// image attachment ships) cannot stall state operations or Stop. It is never
	// held together with p.Mu, so callers must check `stopped` separately.
	stdinMu sync.Mutex
	// stdinQueue carries every outbound frame to ONE writer goroutine, which is
	// what serializes the writes.
	//
	// Order is the reason it is one goroutine and not a pool. A cancel that
	// overtook the answer it follows would re-open the hang the answer exists to
	// end: Base.Interrupt and codex.Agent.Interrupt both send their answers
	// FIRST and then cancel, and each says so at its own site. FIFO keeps that
	// true whether the caller waits for its write or not.
	//
	// It also caps what an unresponsive child costs. A reply written from the read
	// loop had to be moved off it -- the loop must keep draining the child's
	// stdout, and the reply is a write to a stdin the child may not be reading --
	// and a goroutine for each one made that cost unlimited. A frame holds its
	// bytes; a goroutine held an 8 KiB stack as well.
	stdinQueue chan stdinFrame
	// stdinClosed ends the writer. Stop closes it, after it closes stdin.
	//
	// It is never set back to nil, and stdinClosedOnce is what makes the close
	// idempotent instead. A nil channel blocks a select arm FOREVER, so clearing it
	// left the next WriteStdin waiting on a queue whose writer had already exited
	// and on a `closed` arm that could never fire -- a permanent hang, not an error.
	stdinClosed     chan struct{}
	stdinClosedOnce bool

	// jobObject, when non-nil, is the Windows kill-on-close job group that
	// holds the shell wrapper and every descendant it spawns. Terminating or
	// closing it reaps the whole tree — the Windows analogue of Unix's
	// process-group signalling.
	jobObject *procutil.JobObject

	discardOutput atomic.Bool

	// Preamble handling (from shell wrapper).
	preambleDelimiter  string            // if set, skipPreamble skips lines until this delimiter
	preambleMetaPrefix string            // prefix for metadata lines (before delimiter)
	preambleMeta       map[string]string // parsed key=value metadata from preamble
	preambleOutput     []string          // captured preamble lines (before delimiter)

	apiTimeout   time.Duration // timeout for JSON-RPC requests
	TurnToolUses int           // number of tool uses in the current turn

	// cumulativeOutput tracks cumulative snapshots and limited tails per scope.
	// Guarded by p.Mu.
	cumulativeOutput map[string]*CumulativeOutputCounter
}

// ObserveCumulativeOutput records one cumulative output snapshot.
func (p *Process) ObserveCumulativeOutput(scopeID, value string, limited bool) CumulativeOutputObservation {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	if p.cumulativeOutput == nil {
		p.cumulativeOutput = make(map[string]*CumulativeOutputCounter)
	}
	counter := p.cumulativeOutput[scopeID]
	if counter == nil {
		counter = &CumulativeOutputCounter{}
		p.cumulativeOutput[scopeID] = counter
	}
	return counter.Observe(value, limited)
}

// ClearCumulativeOutput removes one completed output scope.
func (p *Process) ClearCumulativeOutput(scopeID string) {
	p.Mu.Lock()
	delete(p.cumulativeOutput, scopeID)
	p.Mu.Unlock()
}

// ResetCumulativeOutput drops all cumulative-broadcast bookkeeping, used at
// turn boundaries to recover from aborted streams that never sent a
// terminating event. Caller must NOT hold p.Mu.
func (p *Process) ResetCumulativeOutput() {
	p.Mu.Lock()
	clear(p.cumulativeOutput)
	p.Mu.Unlock()
}

// stdinQueueDepth caps the frames one unresponsive child can hold.
//
// A child that stops reading its stdin blocks the writer, and the queue then
// fills. 256 frames is far above any real burst and far below a memory cost that
// matters. The ASYNC path refuses beyond it, which loses a reply to a child that
// is not reading its stdin -- a child that would not have read that reply either.
const stdinQueueDepth = 256

// errStdinClosed refuses a frame for a process whose stdin is gone.
var errStdinClosed = errors.New("agent stdin is closed")

// stdinFrame is one outbound write waiting for the process's stdin.
type stdinFrame struct {
	data []byte
	// done carries the write's error back to a caller that waits for it. A nil
	// done means the caller does not wait, and the writer logs a failure instead.
	done chan error
	// describe labels a frame with no waiter in that log line.
	describe string
}

// stdinWriterLocked returns the queue, starting the writer on first use. The
// caller holds stdinMu.
//
// Lazily, because a Process is built in several places and a test assigns
// `stdin` after construction. A process that never writes costs no goroutine.
func (p *Process) stdinWriterLocked() (chan stdinFrame, chan struct{}) {
	// The close signal FIRST, and reused rather than replaced. Stop may have created
	// and closed it already, with no write ever asked for -- the writer then starts
	// here, sees it closed at once, answers this frame and exits, instead of running
	// for the life of the worker with nothing able to end it.
	if p.stdinClosed == nil {
		p.stdinClosed = make(chan struct{})
	}
	if p.stdinQueue == nil {
		p.stdinQueue = make(chan stdinFrame, stdinQueueDepth)
		go p.runStdinWriter(p.stdinQueue, p.stdinClosed)
	}
	return p.stdinQueue, p.stdinClosed
}

// runStdinWriter performs every write to this process's stdin, one at a time.
func (p *Process) runStdinWriter(queue chan stdinFrame, closed chan struct{}) {
	deliver := func(frame stdinFrame, err error) {
		if frame.done != nil {
			frame.done <- err
			return
		}
		if err != nil {
			slog.Warn("write to agent stdin", "agent_id", p.agentID, "frame", frame.describe, "error", err)
		}
	}
	for {
		select {
		case frame := <-queue:
			deliver(frame, p.writeStdinNow(frame.data))
		case <-closed:
			// Answer what is already queued rather than abandoning it, so no caller
			// waits on a `done` that never arrives.
			for {
				select {
				case frame := <-queue:
					deliver(frame, errStdinClosed)
				default:
					return
				}
			}
		}
	}
}

// writeStdinNow performs one Write.
//
// The writer goroutine is the caller while the writer runs, which is what serializes
// the syscalls. The two paths that run once Stop ended the writer call it directly,
// and by then stdin is closed, so those writes fail at once and cannot overlap
// anything.
//
// A failed write that transferred bytes has an uncertain delivery outcome. The
// returned error retains the writer's error for callers that inspect it.
func (p *Process) writeStdinNow(data []byte) error {
	// The writer runs on its OWN goroutine, so a nil stdin has to be an error here.
	// A panic on a bare goroutine takes down the whole worker process, where the
	// same panic used to reach the caller that asked for the write.
	if p.stdin == nil {
		return errStdinClosed
	}
	written, err := p.stdin.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil && written > 0 {
		return fmt.Errorf("%w: %w", agent.ErrDeliveryUncertain, err)
	}
	return err
}

// WriteStdin queues one frame and waits for its write.
//
// It never acquires p.Mu, so state operations and Stop stay available during a
// slow write. Callers must check stopped when they need a specific
// stopped-process error.
func (p *Process) WriteStdin(data []byte) error {
	p.stdinMu.Lock()
	queue, closed := p.stdinWriterLocked()
	p.stdinMu.Unlock()
	select {
	case <-closed:
		// The writer is gone, so write INLINE rather than refuse. Stop closed stdin
		// already, so this fails at once and cannot block -- and a caller that asks
		// for a write after Stop still gets the real error rather than a
		// substituted one.
		return p.writeStdinNow(data)
	default:
	}
	done := make(chan error, 1)
	select {
	case queue <- stdinFrame{data: data, done: done}:
	case <-closed:
		return p.writeStdinNow(data)
	}
	select {
	case err := <-done:
		return err
	case <-closed:
		return errStdinClosed
	}
}

// writeStdinDetached queues one frame and returns WITHOUT waiting for its write.
//
// For a caller on the goroutine that drains the child's stdout. A reply is a write
// to a stdin the child may not be reading, and waiting for it there stops the loop
// that has to keep draining the child's stdout -- which is the deadlock the reply
// was meant to prevent. `describe` labels the frame in the failure log, because no
// caller is left to report it.
//
// It refuses rather than blocks when the queue is full. A child that has not read
// 256 frames is not going to read this one either, and blocking here would put the
// read loop back where this exists to take it out of.
func (p *Process) writeStdinDetached(data []byte, describe string) {
	p.stdinMu.Lock()
	queue, closed := p.stdinWriterLocked()
	p.stdinMu.Unlock()
	select {
	case <-closed:
		// The writer is gone, so write INLINE. Stop closed stdin already, so the
		// write fails at once and cannot stall this goroutine -- and a request that
		// arrives during tear-down still draws its refusal instead of vanishing.
		if err := p.writeStdinNow(data); err != nil {
			slog.Debug("write to a closed agent stdin", "agent_id", p.agentID, "frame", describe, "error", err)
		}
	default:
		select {
		case queue <- stdinFrame{data: data, describe: describe}:
		default:
			slog.Warn("drop a frame for an unresponsive agent stdin",
				"agent_id", p.agentID, "frame", describe, "queued", stdinQueueDepth)
		}
	}
}

// SendRawInput writes raw bytes directly to the process's stdin without
// wrapping. Ensures a trailing newline.
func (p *Process) SendRawInput(data []byte) error {
	p.Mu.Lock()
	stopped := p.stopped
	p.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}

	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if err := p.WriteStdin(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	return nil
}

// Stop terminates the process gracefully. It closes stdin and gives the
// process a short grace period to exit on its own. If the grace period
// elapses, the process tree is torn down: on Windows via the job object
// (kills orphaned grandchildren too), then via context cancellation as a
// fallback (SIGTERM + WaitDelay).
func (p *Process) Stop() {
	p.NoteIntentionalStop()
	p.Mu.Lock()
	if p.stopped {
		p.Mu.Unlock()
		return
	}
	p.stopped = true
	p.Mu.Unlock()

	_ = p.stdin.Close()
	// The writer goes last. Closing stdin first makes every queued frame fail
	// rather than hang, and the writer then answers each waiting caller with that
	// failure instead of leaving it on a `done` nobody sends to.
	p.stdinMu.Lock()
	if p.stdinClosed == nil {
		p.stdinClosed = make(chan struct{})
	}
	if !p.stdinClosedOnce {
		p.stdinClosedOnce = true
		close(p.stdinClosed)
	}
	p.stdinMu.Unlock()

	select {
	case <-p.processDone:
		return
	case <-time.After(3 * time.Second):
		if err := p.jobObject.Terminate(); err != nil {
			slog.Debug("job object terminate failed", "agent_id", p.agentID, "error", err)
		}
		p.cancel()
	}

	<-p.processDone
}

// IsStopped returns true if the process was intentionally stopped via Stop().
func (p *Process) IsStopped() bool {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	return p.stopped
}

func (p *Process) ProcessExitCompletion() agent.MessageCompletion {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	if p.processExited {
		return p.exitCompletion
	}
	if p.intentionalStop.Load() || p.stopped {
		return agent.MessageCompletionInterrupted
	}
	return agent.MessageCompletionError
}

func (p *Process) NoteIntentionalStop() {
	p.Mu.Lock()
	if !p.processExited {
		p.intentionalStop.Store(true)
	}
	p.Mu.Unlock()
}

func (p *Process) recordProcessExit(err error) {
	p.Mu.Lock()
	p.waitErr = err
	p.processExited = true
	p.exitCompletion = agent.MessageCompletionError
	if p.intentionalStop.Load() || p.stopped {
		p.exitCompletion = agent.MessageCompletionInterrupted
	}
	p.Mu.Unlock()
}

// APITimeout returns the configured API timeout, or DefaultAPITimeout if unset.
func (p *Process) APITimeout() time.Duration {
	if p.apiTimeout > 0 {
		return p.apiTimeout
	}
	return agent.DefaultAPITimeout
}

func (p *Process) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }

// DiscardOutput marks the process so that the ReadOutput loop silently
// drops all remaining lines. Use this before stopping an agent that will
// be restarted (e.g. plan execution) to avoid persisting spurious error
// messages from closed streams.
func (p *Process) DiscardOutput() {
	p.discardOutput.Store(true)
}

func (p *Process) IsDiscardingOutput() bool {
	return p.discardOutput.Load()
}

// Wait blocks until the process exits and returns its exit error.
func (p *Process) Wait() error {
	<-p.processDone
	return p.waitErr
}

// AgentID returns the unique identifier for this agent.
func (p *Process) AgentID() string {
	return p.agentID
}

// ProviderName returns the provider's process name, e.g. "claude", which log
// lines and error messages identify the provider by.
func (p *Process) ProviderName() string {
	return p.providerName
}

// Context returns the context the process runs under. Stop cancels it.
func (p *Process) Context() context.Context {
	return p.ctx
}

// Cmd returns the command the process runs, for a caller that must read its
// environment or its process id.
func (p *Process) Cmd() *exec.Cmd {
	return p.cmd
}

// ProcessDone returns a channel that closes when the process exits.
func (p *Process) ProcessDone() <-chan struct{} {
	return p.processDone
}

// HasStdin reports whether the process was given a stdin pipe.
func (p *Process) HasStdin() bool {
	return p.stdin != nil
}

// StoppedLocked reports whether Stop ran. The caller holds Mu, so the answer
// is atomic with any provider state the caller reads under the same lock.
func (p *Process) StoppedLocked() bool {
	return p.stopped
}

// ProcessExitedLocked reports whether the process exited. The caller holds Mu.
func (p *Process) ProcessExitedLocked() bool {
	return p.processExited
}

// IntentionalStopRequested reports whether a graceful stop was requested, so a
// provider can tell an exit it asked for from a crash.
func (p *Process) IntentionalStopRequested() bool {
	return p.intentionalStop.Load()
}

// SkipStderr records that the process has no stderr to drain, so Stderr does
// not wait for a drain that will never run. A caller that wires a process
// without DrainStderr calls it once, before the process starts.
func (p *Process) SkipStderr() {
	close(p.stderrDone)
}

// Stderr returns the captured stderr output. It waits for the stderr
// goroutine to finish draining the pipe (up to 3 seconds).
func (p *Process) Stderr() string {
	select {
	case <-p.stderrDone:
	case <-time.After(3 * time.Second):
	}
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	return p.stderrBuf.String()
}

// ProcessExitError returns a descriptive error for a process that exited
// unexpectedly. It includes the exit code when available.
func (p *Process) ProcessExitError() error {
	if p.waitErr != nil {
		if exitErr, ok := p.waitErr.(*exec.ExitError); ok {
			return fmt.Errorf("agent process exited with code %d", exitErr.ExitCode())
		}
	}
	return fmt.Errorf("agent process exited unexpectedly")
}

// FormatStartupError returns a descriptive error including stderr and
// preamble output for frontend diagnostics.
func (p *Process) FormatStartupError(phase string, err error) error {
	parts := []string{fmt.Sprintf("%s: %s", phase, err)}
	if stderr := strings.TrimSpace(p.Stderr()); stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if preamble := strings.TrimSpace(p.PreambleOutput()); preamble != "" {
		parts = append(parts, "shell preamble: "+preamble)
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// ResumeFailedError reports that the agent refused to reopen a stored session,
// and states the one command that recovers the tab.
//
// A resume that fails is FATAL for every provider, and that uniformity is the
// point. The alternative -- open a fresh session, write a warning to the log,
// and report success -- discards the conversation that the user asked to
// continue. The tab comes up with no history, the agent has no memory of the
// work, and the only record is a log line on the worker that nobody reads. A
// user who wants a fresh session asks for one with `/clear`, which restarts
// with no resume handle and clears the stored one.
//
// A visible failure also keeps the stored handle, so a resume that fails for a
// reason that passes -- an app-server that is not ready yet, a worktree that a
// later step mounts -- succeeds on the next start.
func ResumeFailedError(sessionID string, err error) error {
	return fmt.Errorf("could not resume session %q: %w (send /clear to start a fresh session)", sessionID, err)
}

// skipPreamble reads lines from the scanner until the preamble delimiter is
// found. Shell login preamble (motd, .zshrc output, etc.) appears before the
// agent's JSONL stream. Metadata lines (key=value pairs prefixed with
// preambleMetaPrefix) are parsed and stored; other preamble lines are captured
// for diagnostics. This is a no-op if preambleDelimiter is empty.
//
// Writes to preambleMeta and preambleOutput are guarded by p.Mu so concurrent
// readers (e.g. AvailableModels called from the hub goroutine) observe
// consistent state.
func (p *Process) skipPreamble(scanner *bufio.Scanner) {
	if p.preambleDelimiter == "" {
		return
	}
	delimBytes := []byte(p.preambleDelimiter)
	metaPrefixBytes := []byte(p.preambleMetaPrefix)
	const maxPreambleLines = 50
	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimSpace(line)
		if bytes.Equal(trimmed, delimBytes) {
			agent.TraceStartupPhase(p.agentID, "preamble_delimiter_seen")
			break
		}
		if len(metaPrefixBytes) > 0 && bytes.HasPrefix(trimmed, metaPrefixBytes) {
			kv := string(trimmed[len(metaPrefixBytes):])
			if eqIdx := strings.IndexByte(kv, '='); eqIdx >= 0 {
				p.Mu.Lock()
				p.preambleMeta[kv[:eqIdx]] = kv[eqIdx+1:]
				p.Mu.Unlock()
			}
			continue
		}
		p.Mu.Lock()
		if len(p.preambleOutput) < maxPreambleLines {
			p.preambleOutput = append(p.preambleOutput, string(line))
		}
		p.Mu.Unlock()
	}
}

// PreambleMetaValue returns the parsed preamble metadata value for key, or
// the empty string if absent. Safe to call concurrently with skipPreamble.
func (p *Process) PreambleMetaValue(key string) string {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	return p.preambleMeta[key]
}

// PreambleOutput returns the captured stdout preamble lines (before the
// delimiter) when running under a login shell wrapper.
func (p *Process) PreambleOutput() string {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	if len(p.preambleOutput) == 0 {
		return ""
	}
	return strings.Join(p.preambleOutput, "\n")
}

// attachJobObject assigns a freshly-started command to a kill group so
// later force-kills reap the whole process tree (Windows job object;
// Unix process group after Setsid/Setpgid). Must be called immediately
// after cmd.Start. A job creation failure is logged but not fatal — the
// agent still works, we just lose the tree-kill guarantee for that
// session.
func (p *Process) attachJobObject(cmd *exec.Cmd) {
	job, err := procutil.AssignCmd(cmd)
	if err != nil {
		slog.Warn("attach job object failed", "agent_id", p.agentID, "error", err)
		return
	}
	p.jobObject = job
}

// NewProcess builds the embedded Process every agent shares, from the
// launch options and the process the provider started.
func NewProcess(opts agent.Options, providerName string, cmd *exec.Cmd, stdin io.WriteCloser, ctx context.Context, cancel func(), preambleDelimiter, preambleMetaPrefix string) Process {
	return NewProcessFrom(ProcessConfig{
		AgentID:            opts.AgentID,
		ProviderName:       providerName,
		Cmd:                cmd,
		Stdin:              stdin,
		Ctx:                ctx,
		Cancel:             cancel,
		APITimeout:         opts.EffectiveAPITimeout(),
		PreambleDelimiter:  preambleDelimiter,
		PreambleMetaPrefix: preambleMetaPrefix,
	})
}

// ProcessConfig states everything a Process starts with. Every field is
// optional: a zero field leaves that part of the process unset, which a test
// that stands a fake process in relies on.
type ProcessConfig struct {
	AgentID      string
	ProviderName string // e.g. "claude"; used in log and error messages
	Cmd          *exec.Cmd
	Stdin        io.WriteCloser
	Ctx          context.Context
	Cancel       func()
	// APITimeout is the timeout for JSON-RPC requests. Zero leaves the process
	// on DefaultAPITimeout.
	APITimeout time.Duration
	// ProcessDone and StderrDone close when the process exits and when its
	// stderr is drained. nil makes a fresh channel; a caller that must close one
	// itself -- a test that stands a fake process in -- passes its own.
	ProcessDone        chan struct{}
	StderrDone         chan struct{}
	PreambleDelimiter  string
	PreambleMetaPrefix string
	// PreambleMeta seeds the preamble metadata the shell wrapper would report.
	// nil starts it empty.
	PreambleMeta map[string]string
}

// NewProcessFrom builds a Process from c. It returns a fresh value,
// so assigning the result to an embedded Process copies no held lock.
func NewProcessFrom(c ProcessConfig) Process {
	processDone := c.ProcessDone
	if processDone == nil {
		processDone = make(chan struct{})
	}
	stderrDone := c.StderrDone
	if stderrDone == nil {
		stderrDone = make(chan struct{})
	}
	meta := c.PreambleMeta
	if meta == nil {
		meta = make(map[string]string)
	}
	return Process{
		agentID:            c.AgentID,
		providerName:       c.ProviderName,
		cmd:                c.Cmd,
		stdin:              c.Stdin,
		ctx:                c.Ctx,
		cancel:             c.Cancel,
		stderrDone:         stderrDone,
		processDone:        processDone,
		preambleDelimiter:  c.PreambleDelimiter,
		preambleMetaPrefix: c.PreambleMetaPrefix,
		preambleMeta:       meta,
		apiTimeout:         c.APITimeout,
	}
}

// StartCmd runs cmd.Start and, on success, attaches the process to a Windows
// kill-on-close job object so later force-kills reap the whole tree.
// On failure, cancel is invoked and the error is wrapped as "start <providerName>".
// Callers must populate p.agentID and p.providerName before calling.
func (p *Process) StartCmd(cmd *exec.Cmd, cancel func()) error {
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start %s: %w", p.providerName, err)
	}
	p.attachJobObject(cmd)
	return nil
}

// SetupProcessPipes configures the command's cancel/wait behavior and opens
// stdin, stdout, and stderr pipes. On error it calls cancel() and returns.
func SetupProcessPipes(cmd *exec.Cmd, cancel func()) (stdin io.WriteCloser, stdout, stderr io.ReadCloser, err error) {
	procutil.GracefulGroupCancel(cmd)

	stdin, err = cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err = cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err = cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}

	return stdin, stdout, stderr, nil
}

// ParsedLine holds the JSON-parsed superset of all agent output envelope
// fields. Raw is the original bytes; the typed fields are populated by a
// single json.Unmarshal in ReadOutput so downstream consumers never need to
// re-parse the envelope.
//
// ID is a json.RawMessage rather than a typed integer/string because
// providers disagree on the wire format: JSON-RPC 2.0 (Codex, ACP) uses
// integer ids, while Pi uses opaque string ids. Capturing the raw bytes
// avoids unmarshal errors that would otherwise wipe out the rest of the
// envelope when the form does not match a typed field. Use IDInt64 /
// IDString to interpret the value at the point of use.
type ParsedLine struct {
	Raw    []byte
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// HasID reports whether the line carried a non-null id field.
func (p *ParsedLine) HasID() bool {
	return len(p.ID) > 0 && string(p.ID) != "null"
}

// IDInt64 returns the line's id parsed as an int64. Returns false when the
// id is missing, null, or not numeric.
func (p *ParsedLine) IDInt64() (int64, bool) {
	if !p.HasID() {
		return 0, false
	}
	var n json.Number
	if err := json.Unmarshal(p.ID, &n); err != nil {
		return 0, false
	}
	v, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return v, true
}

// IDString returns the line's id as a string. For string-form ids it returns
// the unquoted contents; for numeric ids it returns the canonical string
// form (e.g. "42"). Returns "" when the id is missing or null.
func (p *ParsedLine) IDString() string {
	if !p.HasID() {
		return ""
	}
	var s string
	if err := json.Unmarshal(p.ID, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(p.ID))
}

// ParseLine creates a ParsedLine from raw bytes. Used by HandleOutput methods
// that accept []byte (e.g. for tests) to bridge into the single-parse pipeline.
func ParseLine(content []byte) *ParsedLine {
	line := &ParsedLine{Raw: content}
	if err := json.Unmarshal(content, line); err != nil {
		slog.Warn("parse line unmarshal failed", "error", err)
	}
	return line
}

// outputInterceptor is a function that inspects a parsed line before it is
// forwarded to HandleOutput. If it returns true, the line is consumed (not
// forwarded).
type outputInterceptor func(line *ParsedLine) bool

// LineHandler processes a single parsed output line from the agent process.
type LineHandler func(line *ParsedLine)

// ReadOutput reads JSONL lines from stdout, JSON-parses them once into a
// ParsedLine, optionally intercepts responses, then forwards remaining lines
// to the output handler.
func (p *Process) ReadOutput(scanner *bufio.Scanner, intercept outputInterceptor, handle LineHandler) {
	p.skipPreamble(scanner)

	firstLineTraced := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !firstLineTraced {
			agent.TraceStartupPhase(p.agentID, "first_agent_line")
			firstLineTraced = true
		}

		if p.IsDiscardingOutput() {
			continue
		}

		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		parsed := &ParsedLine{Raw: lineCopy}
		if err := json.Unmarshal(lineCopy, parsed); err != nil {
			slog.Warn("invalid agent output JSON", "agent_id", p.agentID, "error", err)
			continue
		}

		if intercept(parsed) {
			continue
		}

		handle(parsed)
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("agent stdout read error",
			"agent_id", p.agentID,
			"error", err,
		)
		// The worker cannot receive further replies after framing fails.
		p.cancel()
	}

	p.recordProcessExit(p.cmd.Wait())
	if err := p.jobObject.Close(); err != nil {
		slog.Debug("job object close failed", "agent_id", p.agentID, "error", err)
	}
	close(p.processDone)
}

// MessageWithToolUses captures the completed tool count as separate worker metadata.
func (p *Process) MessageWithToolUses(original []byte) agent.MessageContent {
	p.Mu.Lock()
	count := p.TurnToolUses
	p.Mu.Unlock()
	return agent.WithToolUseCount(agent.MessageContent{Original: original}, count)
}

// DrainStderr starts a goroutine that reads from the given reader into
// the stderr buffer, capped at maxStderrSize. It closes stderrDone when
// the reader is exhausted. The lock is only held for individual writes,
// not the entire drain loop.
func (p *Process) DrainStderr(r io.Reader) {
	go func() {
		defer close(p.stderrDone)
		buf := make([]byte, 32*1024)
		var total int64
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				p.stderrMu.Lock()
				if total < maxStderrSize {
					limit := int64(n)
					if total+limit > maxStderrSize {
						limit = maxStderrSize - total
					}
					p.stderrBuf.Write(buf[:limit])
					total += limit
				}
				p.stderrMu.Unlock()
			}
			if readErr != nil {
				if readErr != io.EOF {
					slog.Debug("stderr drain error", "agent_id", p.agentID, "error", readErr)
				}
				break
			}
		}
	}()
}

// The methods below are for a test that stands a fake process in. Each one
// changes a piece of base state that only a real process event changes in
// production, so a provider's test can reach the branch that state selects.
// Production code never refers to them. The sub-test "production code never
// refers to a test hook" of TestRepoInvariants (internal/audit) enforces that.

// SetStdinForTest replaces the process's stdin, as a restarted pipe would.
func (p *Process) SetStdinForTest(stdin io.WriteCloser) {
	p.stdin = stdin
}

// SetStoppedForTest sets the stopped flag without running Stop, so a test
// reaches a provider's stopped branch without a process to stop.
func (p *Process) SetStoppedForTest(stopped bool) {
	p.Mu.Lock()
	p.stopped = stopped
	p.Mu.Unlock()
}

// SetAPITimeoutForTest changes the timeout for JSON-RPC requests.
func (p *Process) SetAPITimeoutForTest(timeout time.Duration) {
	p.apiTimeout = timeout
}

// SetContextForTest replaces the context the process runs under.
func (p *Process) SetContextForTest(ctx context.Context) {
	p.ctx = ctx
}

// CancelForTest cancels the context the process runs under, as a kill does.
func (p *Process) CancelForTest() {
	p.cancel()
}

// SetCancelForTest replaces the function that cancels the process context, so
// a test observes the cancel that the base calls.
func (p *Process) SetCancelForTest(cancel context.CancelFunc) {
	p.cancel = cancel
}

// StdinForTest returns the process's stdin, so a test reads what a provider
// wrote to it.
func (p *Process) StdinForTest() io.WriteCloser {
	return p.stdin
}

// StderrWriterForTest returns a writer that appends to the captured stderr, as
// DrainStderr does, for a test that gives the command a writer rather than a
// pipe. Each write takes the stderr lock.
func (p *Process) StderrWriterForTest() io.Writer {
	return stderrCaptureWriter{p: p}
}

// stderrCaptureWriter is the writer that StderrWriterForTest returns.
type stderrCaptureWriter struct {
	p *Process
}

func (w stderrCaptureWriter) Write(data []byte) (int, error) {
	w.p.stderrMu.Lock()
	defer w.p.stderrMu.Unlock()
	return w.p.stderrBuf.Write(data)
}

// SkipPreambleForTest reads the shell preamble off scanner, as ReadOutput does
// before the first output line.
func (p *Process) SkipPreambleForTest(scanner *bufio.Scanner) {
	p.skipPreamble(scanner)
}

// HasCumulativeOutputForTest reports whether the base keeps a cumulative output
// record for toolCallID.
func (p *Process) HasCumulativeOutputForTest(toolCallID string) bool {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	_, ok := p.cumulativeOutput[toolCallID]
	return ok
}

// SimulateExitForTest closes the process-exit channel, as a real exit does,
// without a process to exit.
func (p *Process) SimulateExitForTest() {
	if p.processDone == nil {
		p.processDone = make(chan struct{})
	}
	close(p.processDone)
}
