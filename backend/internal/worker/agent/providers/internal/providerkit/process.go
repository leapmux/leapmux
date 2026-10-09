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
	"syscall"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
)

// maxStderrSize caps the captured stderr at one mebibyte.
const maxStderrSize = 1 << 20

// Process contains the shared lifecycle state and methods.
// claude.Agent embeds it directly.
// opencode.Agent and cursor.Agent use the Agent Client Protocol (ACP).
// Those agents and codex.Agent embed it through JSONRPCProcess, which adds JSON-RPC transport.
type Process struct {
	agentID      string
	providerName string // Log and error messages use the provider's process name.
	stdin        io.WriteCloser

	cmd           *exec.Cmd
	owner         *procutil.ProcessOwner
	pipes         *ProcessPipes
	shutdownGrace time.Duration
	stopDone      chan struct{}
	ctx           context.Context
	cancel        func()
	processDone   chan struct{}
	waitErr       error
	// stopSignal is the signal that Stop sends to the process group, after it
	// captures the process tree. Zero sends none. See ProcessLaunch.StopSignal.
	stopSignal syscall.Signal
	// One Process permits one start attempt. A repeated call must keep the original child intact.
	startMu        sync.Mutex
	startAttempted bool
	// clock supplies the timers for process waits, including AwaitResponse. See Clock.
	clock quartz.Clock

	stderrBuf  bytes.Buffer
	stderrMu   sync.Mutex
	stderrDone chan struct{}

	Mu      sync.Mutex
	stopped bool
	// processExited freezes exitCompletion when the native wait returns.
	// A later cleanup call must not reclassify a natural failure as a stop.
	processExited  bool
	exitCompletion agent.MessageCompletion
	// intentionalStop is set before a provider sends its graceful stop request.
	// Wait can then classify retained content while Stop still owns that request.
	intentionalStop atomic.Bool

	// TurnSeq supplies the ordering token for each published turn flag.
	// Each provider receives this token through its embedded Process.
	// Mu protects both the token and the provider's turn flag.
	TurnSeq

	// stdinMu protects the write queue below.
	// Queue callers release it before Write, so a full pipe cannot block state operations or Stop.
	// A large inline image can fill that pipe.
	// No caller holds stdinMu with p.Mu. Check stopped separately.
	stdinMu sync.Mutex
	// stdinQueue sends every outbound frame to one writer goroutine, which serializes writes.
	// Base.Interrupt and codex.Agent.Interrupt send an answer before a cancel.
	// A cancel that precedes its answer can leave the native request blocked.
	// First-in, first-out (FIFO) order preserves this requirement for both synchronous and detached writes.
	//
	// The queue also caps the cost of an unresponsive child.
	// A reply cannot block the stdout reader on a child that does not read stdin.
	// A separate goroutine for each reply would permit unlimited stacks.
	// Each queued frame holds its bytes. Each former goroutine also held an 8 KiB stack.
	stdinQueue chan stdinFrame
	// stdinClosed ends the writer. Stop closes it after it closes stdin.
	// stdinClosedOnce prevents a repeated close. Keep the channel after closure.
	// A nil channel never selects its case.
	// Clearing this channel would leave WriteStdin blocked after the writer exits.
	stdinClosed     chan struct{}
	stdinClosedOnce bool

	discardOutput atomic.Bool

	// The shell wrapper supplies the preamble.
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

// ResetCumulativeOutput clears cumulative output state at a turn boundary.
// This clears streams that end without a final event. The caller must not hold p.Mu.
func (p *Process) ResetCumulativeOutput() {
	p.Mu.Lock()
	clear(p.cumulativeOutput)
	p.Mu.Unlock()
}

// stdinQueueDepth caps the frames that an unresponsive child can retain.
// A child that does not read stdin blocks the writer and fills this queue.
// The detached path refuses a frame when all 256 positions are occupied.
// Refusal keeps the stdout reader available when the queue cannot accept another frame.
const stdinQueueDepth = 256

// errStdinClosed refuses a frame for a process whose stdin is gone.
var errStdinClosed = errors.New("agent stdin is closed")

// stdinFrame is one outbound write waiting for the process's stdin.
type stdinFrame struct {
	data []byte
	// done carries the write error to a caller that waits.
	// With nil done, the writer logs a failure because the caller does not wait.
	done chan error
	// describe identifies a detached frame in the failure log.
	describe string
}

// stdinWriterLocked returns the queue and starts the writer on first use.
// The caller holds stdinMu.
// Some tests assign stdin after construction, so construction cannot start the writer.
// A process that never writes needs no writer goroutine.
func (p *Process) stdinWriterLocked() (chan stdinFrame, chan struct{}) {
	// Create the close signal first, and reuse an existing signal.
	// Stop can close it before the first write.
	// The new writer then answers the frame and exits through that closed signal.
	// Replacing the signal would leave the writer without a shutdown signal.
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
			// Answer each queued frame so that no caller waits for an absent result.
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
// The writer goroutine serializes these calls while it runs.
// After Stop ends the writer, the two closed-channel paths call this function directly.
// Their writes fail through the closed stdin.
// A failed write that transfers bytes has an uncertain delivery outcome.
// The returned error retains the original write error.
func (p *Process) writeStdinNow(data []byte) error {
	// Return an error for nil stdin. A panic in the writer would stop the whole Worker.
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
// It never acquires p.Mu, so a slow write does not block state operations or Stop.
// Callers must check stopped when they need an error for a stopped process.
func (p *Process) WriteStdin(data []byte) error {
	p.stdinMu.Lock()
	queue, closed := p.stdinWriterLocked()
	p.stdinMu.Unlock()
	select {
	case <-closed:
		// Write directly after the writer exits. Stop closes stdin before this signal.
		// The caller then receives the original error from the closed stdin.
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

// writeStdinDetached queues one frame and returns without waiting for its write.
// The stdout reader uses this path because a child can stop reading stdin.
// Waiting for that write would prevent the same reader from draining stdout.
// describe identifies the frame in the failure log because its caller does not wait.
// A full queue rejects the frame so that the stdout reader cannot block.
func (p *Process) writeStdinDetached(data []byte, describe string) {
	p.stdinMu.Lock()
	queue, closed := p.stdinWriterLocked()
	p.stdinMu.Unlock()
	select {
	case <-closed:
		// Write directly after the writer exits. Stop already closed stdin.
		// Log the write refusal for a request that arrives during shutdown.
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

// SendRawInput writes raw bytes without an envelope and ensures a trailing newline.
func (p *Process) SendRawInput(data []byte, stop agent.StopContext) error {
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

// Stop captures the owned process tree and sends the provider's configured stop signal.
// It then closes stdin and gives the process a grace period to exit.
// After that period, the process owner terminates the captured tree.
// The Windows owner uses its job object, which includes orphaned grandchildren.
// Context cancellation supplies the fallback through SIGTERM and WaitDelay.
func (p *Process) Stop() {
	p.NoteIntentionalStop()
	p.Mu.Lock()
	if p.stopped {
		done := p.stopDone
		p.Mu.Unlock()
		if done != nil {
			<-done
		}
		return
	}
	p.stopped = true
	p.stopDone = make(chan struct{})
	done := p.stopDone
	p.Mu.Unlock()
	defer close(done)

	ctx, cancelCapture := context.WithTimeout(context.Background(), 2*time.Second)
	if err := p.owner.Capture(ctx); err != nil {
		slog.Warn("capture agent process ownership", "agent_id", p.agentID, "error", err)
	}
	cancelCapture()
	p.sendStopSignal()
	if p.stdin != nil {
		if err := p.stdin.Close(); err != nil {
			slog.Debug("close agent stdin", "agent_id", p.agentID, "error", err)
		}
	}
	p.stdinMu.Lock()
	if p.stdinClosed == nil {
		p.stdinClosed = make(chan struct{})
	}
	if !p.stdinClosedOnce {
		p.stdinClosedOnce = true
		close(p.stdinClosed)
	}
	p.stdinMu.Unlock()

	grace := p.shutdownGrace
	if grace <= 0 {
		grace = 3 * time.Second
	}
	timer := p.Clock().NewTimer(grace, "process-stop-grace")
	defer timer.Stop("process-stop-grace")
	select {
	case <-p.processDone:
		return
	case <-timer.C:
		if err := p.owner.Terminate(); err != nil {
			slog.Warn("stop owned agent processes", "agent_id", p.agentID, "error", err)
		}
		if p.cancel != nil {
			p.cancel()
		}
	}
	<-p.processDone
}

// sendStopSignal sends the provider's stop signal to the process group.
// Run it after Stop captures the process tree.
// A child in its own session sits outside the group and does not receive this signal.
// Its parent can exit before a later tree read identifies that child.
// The earlier ownership capture retains the child for cleanup.
func (p *Process) sendStopSignal() {
	if p.stopSignal == 0 {
		return
	}
	if err := procutil.SignalProcessGroup(p.cmd, p.stopSignal); err != nil {
		slog.Debug("signal the agent process group", "agent_id", p.agentID, "signal", p.stopSignal, "error", err)
	}
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

// DiscardOutput makes ReadOutput drop all remaining lines.
// Call it before stopping an agent that will restart, including plan execution.
// This prevents closed-stream errors from entering that agent's transcript.
func (p *Process) DiscardOutput() {
	p.discardOutput.Store(true)
}

func (p *Process) IsDiscardingOutput() bool {
	return p.discardOutput.Load()
}

// Wait blocks until the process exits and returns its exit error.
func (p *Process) Wait() error {
	<-p.processDone
	return errors.Join(p.waitErr, p.owner.Err())
}

// AgentID returns the unique identifier for this agent.
func (p *Process) AgentID() string {
	return p.agentID
}

// ProviderName returns the process name that log and error messages use, such as "claude".
func (p *Process) ProviderName() string {
	return p.providerName
}

// Context returns the process context. Stop cancels it after its grace period expires.
func (p *Process) Context() context.Context {
	return p.ctx
}

// Cmd returns the command so that a caller can read its environment or process ID.
func (p *Process) Cmd() *exec.Cmd {
	return p.cmd
}

// BindDescendants records observed process ownership before startup returns.
func (p *Process) BindDescendants(ctx context.Context) error {
	if p.owner == nil {
		return errors.New("the process has no prepared owner")
	}
	return p.owner.BindDescendants(ctx)
}

// ProcessDone returns a channel that closes when the process exits.
func (p *Process) ProcessDone() <-chan struct{} {
	return p.processDone
}

// HasStdin reports whether the process has a stdin writer.
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

// IntentionalStopRequested distinguishes a requested graceful stop from a native crash.
func (p *Process) IntentionalStopRequested() bool {
	return p.intentionalStop.Load()
}

// SkipStderr records that this process has no stderr reader.
// A caller that omits DrainStderr calls it once before the process starts.
// Stderr then need not wait for an absent reader.
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

type startupDiagnosticError struct {
	message string
	cause   error
}

func (e *startupDiagnosticError) Error() string { return e.message }
func (e *startupDiagnosticError) Unwrap() error { return e.cause }

// FormatStartupError preserves the cause and adds stderr and shell diagnostics.
func (p *Process) FormatStartupError(phase string, err error) error {
	details := "no error details"
	if err != nil {
		details = fmt.Sprintf("%s: %s", phase, err)
	} else if phase != "" {
		details = phase + ": " + details
	}
	parts := []string{details}
	if stderr := strings.TrimSpace(p.Stderr()); stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if preamble := strings.TrimSpace(p.PreambleOutput()); preamble != "" {
		parts = append(parts, "shell preamble: "+preamble)
	}
	return &startupDiagnosticError{message: strings.Join(parts, "; "), cause: err}
}

// ResumeFailedError reports a refused stored session and the command that recovers the tab.
// Every provider must report a failed resume as a startup failure.
// A fresh session would discard the conversation that the user asked to continue.
// The visible failure preserves the stored resume handle for another start.
// A later start can succeed after a transient host or worktree failure ends.
// The user can send /clear to restart without that handle and clear its stored value.
func ResumeFailedError(sessionID string, err error) error {
	return fmt.Errorf("could not resume session %q: %w (send /clear to start a fresh session)", sessionID, err)
}

// skipPreamble reads the shell login output before the agent's JSON Lines (JSONL) stream.
// It stops at preambleDelimiter and returns immediately when that delimiter is empty.
// Lines with preambleMetaPrefix supply stored key=value metadata.
// Other lines remain available for startup diagnostics, including motd and .zshrc output.
// p.Mu protects metadata and captured output from concurrent readers, including AvailableModels.
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

// ProcessLaunch supplies only the metadata that the generic process needs.
type ProcessLaunch struct {
	ProviderName       string
	ShutdownGrace      time.Duration
	PreambleDelimiter  string
	PreambleMetaPrefix string
	// StopSignal is the signal that ends the process when it does not end on
	// the close of its stdin. Stop sends it to the process group after it
	// captures the process tree and before it closes stdin. Zero sends none.
	StopSignal syscall.Signal
}

// NewProcess consumes the command and owner that SetupProcessPipes created together.
func NewProcess(opts agent.Options, launch ProcessLaunch, pipes *ProcessPipes, ctx context.Context, cancel func()) Process {
	return NewProcessFrom(ProcessConfig{AgentID: opts.AgentID, ProviderName: launch.ProviderName, Ctx: ctx, Cancel: cancel, APITimeout: opts.EffectiveAPITimeout(), PreambleDelimiter: launch.PreambleDelimiter, PreambleMetaPrefix: launch.PreambleMetaPrefix, pipes: pipes, shutdownGrace: launch.ShutdownGrace, stopSignal: launch.StopSignal})
}

// ProcessConfig supplies the initial Process state.
// Each field is optional. A zero value leaves that part unset for a controlled test process.
type ProcessConfig struct {
	pipes         *ProcessPipes
	shutdownGrace time.Duration
	stopSignal    syscall.Signal
	AgentID       string
	ProviderName  string // Log and error messages use this name, such as "claude".
	Cmd           *exec.Cmd
	Stdin         io.WriteCloser
	Ctx           context.Context
	Cancel        func()
	// APITimeout is the timeout for JSON-RPC requests. Zero leaves the process
	// on DefaultAPITimeout.
	APITimeout time.Duration
	// ProcessDone closes after process exit. StderrDone closes after the stderr reader ends.
	// A nil value creates a fresh channel. A controlled test can supply its own channel.
	ProcessDone        chan struct{}
	StderrDone         chan struct{}
	PreambleDelimiter  string
	PreambleMetaPrefix string
	// PreambleMeta seeds the preamble metadata the shell wrapper would report.
	// nil starts it empty.
	PreambleMeta map[string]string
	// Clock times the waits of the process (AwaitResponse), and a provider reads
	// its own timers from it (Process.Clock). nil selects the real clock.
	Clock quartz.Clock
}

// NewProcessFrom builds a fresh Process from c.
// Assigning it to an embedded Process copies no held mutex.
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
	cmd, stdin := c.Cmd, c.Stdin
	var owner *procutil.ProcessOwner
	if c.pipes != nil {
		cmd, stdin, owner = c.pipes.cmd, c.pipes.stdin, c.pipes.owner
	}
	return Process{
		owner:              owner,
		pipes:              c.pipes,
		shutdownGrace:      c.shutdownGrace,
		stopSignal:         c.stopSignal,
		agentID:            c.AgentID,
		providerName:       c.ProviderName,
		cmd:                cmd,
		stdin:              stdin,
		ctx:                c.Ctx,
		cancel:             c.Cancel,
		stderrDone:         stderrDone,
		processDone:        processDone,
		preambleDelimiter:  c.PreambleDelimiter,
		preambleMetaPrefix: c.PreambleMetaPrefix,
		preambleMeta:       meta,
		apiTimeout:         c.APITimeout,
		clock:              c.Clock,
	}
}

// realClock is the clock of a process that states none.
var realClock = quartz.NewReal()

// Clock returns the configured clock or the real clock.
// Provider timers use this same clock, so a mock clock controls all agent waits.
func (p *Process) Clock() quartz.Clock {
	if p.clock == nil {
		return realClock
	}
	return p.clock
}

// StartCmd starts the command through its prepared owner.
func (p *Process) StartCmd() error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.startAttempted {
		return errors.New("the process start was already attempted")
	}
	p.startAttempted = true
	if p.processDone == nil {
		p.processDone = make(chan struct{})
	}
	var startErr error
	if p.owner == nil {
		startErr = errors.New("the process has no prepared owner")
	} else {
		startErr = p.owner.Start()
	}
	if startErr == nil {
		return nil
	}
	startErr = fmt.Errorf("start %s: %w", p.providerName, errors.Join(startErr, p.pipes.Close()))
	// No output reader starts after this failure. Finish the lifecycle here so cleanup can wait safely.
	p.recordProcessExit(startErr)
	close(p.processDone)
	if p.cancel != nil {
		p.cancel()
	}
	return startErr
}

// ParsedLine holds the shared fields of an agent output envelope.
// Raw retains the original bytes. ReadOutput decodes the envelope once with json.Unmarshal.
// Consumers then read those fields without another envelope decode.
// ID retains json.RawMessage because providers can use integers or opaque strings.
// A fixed ID type would reject a different native ID and discard the envelope fields.
// Use IDInt64 or IDString to read the native ID at its consumption site.
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

// IDInt64 reads the line's ID as an int64.
// It returns false for an absent or null ID. An unreadable or noninteger number also returns false.
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

// IDString returns the unquoted contents of a string ID.
// For another nonnull value, it returns its trimmed JSON bytes, such as "42" for a numeric ID.
// It returns an empty string for an absent or null ID.
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

// ParseLine creates a ParsedLine from raw bytes.
// HandleOutput methods that accept []byte use it to enter the shared envelope decoder, including tests.
func ParseLine(content []byte) *ParsedLine {
	line := &ParsedLine{Raw: content}
	if err := json.Unmarshal(content, line); err != nil {
		slog.Warn("parse line unmarshal failed", "error", err)
	}
	return line
}

// outputInterceptor checks a parsed line before HandleOutput receives it.
// A true result consumes the line and prevents that dispatch.
type outputInterceptor func(line *ParsedLine) bool

// LineHandler processes a single parsed output line from the agent process.
type LineHandler func(line *ParsedLine)

// ReadOutput reads JSONL lines from stdout and decodes each envelope once.
// The interceptor consumes responses that it handles. The output handler receives every other line.
func (p *Process) ReadOutput(scanner *bufio.Scanner, intercept outputInterceptor, handle LineHandler) {
	p.ReadLines(scanner, func(line []byte) {
		parsed := &ParsedLine{Raw: line}
		if err := json.Unmarshal(line, parsed); err != nil {
			slog.Warn("invalid agent output JSON", "agent_id", p.agentID, "error", err)
			return
		}
		if intercept(parsed) {
			return
		}
		handle(parsed)
	})
}

// ReadLines reads the process's stdout as plain text lines and hands each
// non-empty line to handle, until stdout closes. It then records the process
// exit. ReadOutput reads its JSON lines through it, so both keep one loop: the
// preamble skip, the first-line trace, the discard check and the exit record.
//
// It is for a provider whose CLI runs a local server and prints plain text on
// stdout -- the address it listens on, and log lines -- while the protocol
// itself runs over HTTP. ReadOutput would drop every such line as invalid JSON,
// and with it the address the provider waits for.
//
// handle runs on the reader goroutine. A full output pipe stops the child until this loop drains it.
// A handler can apply backpressure while an OS stdin write completes.
// It must not wait for a response that this same reader must deliver, because that wait creates a deadlock.
func (p *Process) ReadLines(scanner *bufio.Scanner, handle func(line []byte)) {
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
		handle(lineCopy)
	}

	p.finishOutput(scanner)
}

// finishOutput runs after stdout closes. It records a framing failure, waits for
// the process, and closes processDone, which is what Wait and every stop path
// block on.
func (p *Process) finishOutput(scanner *bufio.Scanner) {
	if err := scanner.Err(); err != nil {
		slog.Warn("agent stdout read error",
			"agent_id", p.agentID,
			"error", err,
		)
		// The worker cannot receive further replies after framing fails.
		p.cancel()
	}

	var waitErr error
	if p.owner != nil {
		waitErr = p.owner.Wait()
	} else {
		// ProcessConfig permits a test to supply its own started command.
		waitErr = p.cmd.Wait()
	}
	p.recordProcessExit(waitErr)
	if err := p.owner.Close(); err != nil {
		slog.Warn("close owned agent processes", "agent_id", p.agentID, "error", err)
	}
	close(p.processDone)
	// Work that a provider binds to the process context -- an event stream, a
	// reconnect loop, a poller -- ends with the process, whether it exited by
	// itself or crashed. Stop cancels earlier only when the process outlives its
	// grace. The cancel comes after processDone closes, so a wait that sees both
	// can report the exit (see AwaitResponse).
	if p.cancel != nil {
		p.cancel()
	}
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
