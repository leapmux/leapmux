package agent

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

	"github.com/leapmux/leapmux/util/procutil"
)

// maxStderrSize is the maximum amount of stderr to buffer.
const maxStderrSize = 1 << 20 // 1MB

// processBase contains the shared process lifecycle state and methods.
// ClaudeCodeAgent embeds it directly; ACP agents (OpenCodeAgent, CursorCLIAgent)
// and CodexAgent embed it via jsonrpcBase which adds JSON-RPC request plumbing.
type processBase struct {
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

	mu      sync.Mutex
	stopped bool
	// processExited freezes exitCompletion at the instant cmd.Wait returns.
	// A later cleanup call must not reclassify a natural failure as a stop.
	processExited  bool
	exitCompletion MessageCompletion
	// intentionalStop is set before a provider sends its graceful stop request.
	// Wait can then classify retained content while Stop still owns that request.
	intentionalStop atomic.Bool

	// turnSeqSource issues the ordering token that rides every publish of this
	// provider's turn flag. It lives here so all five providers get it from one
	// embed and a sixth cannot forget it, and because mu -- the lock that makes
	// the token atomic with the flag read -- lives here too.
	turnSeqSource

	// stdinMu guards the write queue below. It is NOT held across a Write, so a
	// slow stdin (a full kernel pipe buffer, for example while a large inline
	// image attachment ships) cannot stall state operations or Stop. It is never
	// held together with p.mu, so callers must check `stopped` separately.
	stdinMu sync.Mutex
	// stdinQueue carries every outbound frame to ONE writer goroutine, which is
	// what serializes the writes.
	//
	// Order is the reason it is one goroutine and not a pool. A cancel that
	// overtook the answer it follows would re-open the hang the answer exists to
	// end: acpBase.Interrupt and CodexAgent.Interrupt both send their answers
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
	// left the next writeStdin waiting on a queue whose writer had already exited
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
	turnToolUses int           // number of tool uses in the current turn

	// cumulativeOutput tracks cumulative snapshots and limited tails per scope.
	// Guarded by p.mu.
	cumulativeOutput map[string]*CumulativeOutputCounter
}

// observeCumulativeOutput records one cumulative output snapshot.
func (p *processBase) observeCumulativeOutput(scopeID, value string, limited bool) CumulativeOutputObservation {
	p.mu.Lock()
	defer p.mu.Unlock()
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

// clearCumulativeOutput removes one completed output scope.
func (p *processBase) clearCumulativeOutput(scopeID string) {
	p.mu.Lock()
	delete(p.cumulativeOutput, scopeID)
	p.mu.Unlock()
}

// resetCumulativeDeltas drops all cumulative-broadcast bookkeeping, used at
// turn boundaries to recover from aborted streams that never sent a
// terminating event. Caller must NOT hold p.mu.
func (p *processBase) resetCumulativeOutput() {
	p.mu.Lock()
	clear(p.cumulativeOutput)
	p.mu.Unlock()
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
// Lazily, because a processBase is built in several places and a test assigns
// `stdin` after construction. A process that never writes costs no goroutine.
func (p *processBase) stdinWriterLocked() (chan stdinFrame, chan struct{}) {
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
func (p *processBase) runStdinWriter(queue chan stdinFrame, closed chan struct{}) {
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
func (p *processBase) writeStdinNow(data []byte) error {
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
		return fmt.Errorf("%w: %w", ErrDeliveryUncertain, err)
	}
	return err
}

// writeStdin queues one frame and waits for its write.
//
// It never acquires p.mu, so state operations and Stop stay available during a
// slow write. Callers must check stopped when they need a specific
// stopped-process error.
func (p *processBase) writeStdin(data []byte) error {
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
func (p *processBase) writeStdinDetached(data []byte, describe string) {
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
func (p *processBase) SendRawInput(data []byte) error {
	p.mu.Lock()
	stopped := p.stopped
	p.mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}

	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if err := p.writeStdin(data); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	return nil
}

// Stop terminates the process gracefully. It closes stdin and gives the
// process a short grace period to exit on its own. If the grace period
// elapses, the process tree is torn down: on Windows via the job object
// (kills orphaned grandchildren too), then via context cancellation as a
// fallback (SIGTERM + WaitDelay).
func (p *processBase) Stop() {
	p.noteIntentionalStop()
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.mu.Unlock()

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
func (p *processBase) IsStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

func (p *processBase) processExitCompletion() MessageCompletion {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.processExited {
		return p.exitCompletion
	}
	if p.intentionalStop.Load() || p.stopped {
		return MessageCompletionInterrupted
	}
	return MessageCompletionError
}

func (p *processBase) noteIntentionalStop() {
	p.mu.Lock()
	if !p.processExited {
		p.intentionalStop.Store(true)
	}
	p.mu.Unlock()
}

func (p *processBase) recordProcessExit(err error) {
	p.mu.Lock()
	p.waitErr = err
	p.processExited = true
	p.exitCompletion = MessageCompletionError
	if p.intentionalStop.Load() || p.stopped {
		p.exitCompletion = MessageCompletionInterrupted
	}
	p.mu.Unlock()
}

// APITimeout returns the configured API timeout, or DefaultAPITimeout if unset.
func (p *processBase) APITimeout() time.Duration {
	if p.apiTimeout > 0 {
		return p.apiTimeout
	}
	return DefaultAPITimeout
}

func (p *processBase) ClearContext() (string, error) { return "", ErrContextClearUnsupported }

// DiscardOutput marks the process so that the readOutput loop silently
// drops all remaining lines. Use this before stopping an agent that will
// be restarted (e.g. plan execution) to avoid persisting spurious error
// messages from closed streams.
func (p *processBase) DiscardOutput() {
	p.discardOutput.Store(true)
}

func (p *processBase) isDiscardingOutput() bool {
	return p.discardOutput.Load()
}

// Wait blocks until the process exits and returns its exit error.
func (p *processBase) Wait() error {
	<-p.processDone
	return p.waitErr
}

// AgentID returns the unique identifier for this agent.
func (p *processBase) AgentID() string {
	return p.agentID
}

// Stderr returns the captured stderr output. It waits for the stderr
// goroutine to finish draining the pipe (up to 3 seconds).
func (p *processBase) Stderr() string {
	select {
	case <-p.stderrDone:
	case <-time.After(3 * time.Second):
	}
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	return p.stderrBuf.String()
}

// processExitError returns a descriptive error for a process that exited
// unexpectedly. It includes the exit code when available.
func (p *processBase) processExitError() error {
	if p.waitErr != nil {
		if exitErr, ok := p.waitErr.(*exec.ExitError); ok {
			return fmt.Errorf("agent process exited with code %d", exitErr.ExitCode())
		}
	}
	return fmt.Errorf("agent process exited unexpectedly")
}

// formatStartupError returns a descriptive error including stderr and
// preamble output for frontend diagnostics.
func (p *processBase) formatStartupError(phase string, err error) error {
	parts := []string{fmt.Sprintf("%s: %s", phase, err)}
	if stderr := strings.TrimSpace(p.Stderr()); stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if preamble := strings.TrimSpace(p.PreambleOutput()); preamble != "" {
		parts = append(parts, "shell preamble: "+preamble)
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// resumeFailedError reports that the agent refused to reopen a stored session,
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
func resumeFailedError(sessionID string, err error) error {
	return fmt.Errorf("could not resume session %q: %w (send /clear to start a fresh session)", sessionID, err)
}

// skipPreamble reads lines from the scanner until the preamble delimiter is
// found. Shell login preamble (motd, .zshrc output, etc.) appears before the
// agent's JSONL stream. Metadata lines (key=value pairs prefixed with
// preambleMetaPrefix) are parsed and stored; other preamble lines are captured
// for diagnostics. This is a no-op if preambleDelimiter is empty.
//
// Writes to preambleMeta and preambleOutput are guarded by p.mu so concurrent
// readers (e.g. AvailableModels called from the hub goroutine) observe
// consistent state.
func (p *processBase) skipPreamble(scanner *bufio.Scanner) {
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
			TraceStartupPhase(p.agentID, "preamble_delimiter_seen")
			break
		}
		if len(metaPrefixBytes) > 0 && bytes.HasPrefix(trimmed, metaPrefixBytes) {
			kv := string(trimmed[len(metaPrefixBytes):])
			if eqIdx := strings.IndexByte(kv, '='); eqIdx >= 0 {
				p.mu.Lock()
				p.preambleMeta[kv[:eqIdx]] = kv[eqIdx+1:]
				p.mu.Unlock()
			}
			continue
		}
		p.mu.Lock()
		if len(p.preambleOutput) < maxPreambleLines {
			p.preambleOutput = append(p.preambleOutput, string(line))
		}
		p.mu.Unlock()
	}
}

// preambleMetaValue returns the parsed preamble metadata value for key, or
// the empty string if absent. Safe to call concurrently with skipPreamble.
func (p *processBase) preambleMetaValue(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.preambleMeta[key]
}

// PreambleOutput returns the captured stdout preamble lines (before the
// delimiter) when running under a login shell wrapper.
func (p *processBase) PreambleOutput() string {
	p.mu.Lock()
	defer p.mu.Unlock()
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
func (p *processBase) attachJobObject(cmd *exec.Cmd) {
	job, err := procutil.AssignCmd(cmd)
	if err != nil {
		slog.Warn("attach job object failed", "agent_id", p.agentID, "error", err)
		return
	}
	p.jobObject = job
}

// newProcessBase builds the embedded processBase every agent shares.
// Folded out of each Start* call site so the 11-line struct-literal
// (Channels, ctx, processDone, preambleDelimiter, metaPrefix,
// preambleMeta map, apiTimeout) lives in one place. Providers that
// need to set other processBase fields can do so after construction.
func newProcessBase(opts Options, providerName string, cmd *exec.Cmd, stdin io.WriteCloser, ctx context.Context, cancel func(), preambleDelimiter, preambleMetaPrefix string) processBase {
	return processBase{
		agentID:            opts.AgentID,
		providerName:       providerName,
		cmd:                cmd,
		stdin:              stdin,
		ctx:                ctx,
		cancel:             cancel,
		stderrDone:         make(chan struct{}),
		processDone:        make(chan struct{}),
		preambleDelimiter:  preambleDelimiter,
		preambleMetaPrefix: preambleMetaPrefix,
		preambleMeta:       make(map[string]string),
		apiTimeout:         opts.apiTimeout(),
	}
}

// startCmd runs cmd.Start and, on success, attaches the process to a Windows
// kill-on-close job object so later force-kills reap the whole tree.
// On failure, cancel is invoked and the error is wrapped as "start <providerName>".
// Callers must populate p.agentID and p.providerName before calling.
func (p *processBase) startCmd(cmd *exec.Cmd, cancel func()) error {
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start %s: %w", p.providerName, err)
	}
	p.attachJobObject(cmd)
	return nil
}

// setupProcessPipes configures the command's cancel/wait behavior and opens
// stdin, stdout, and stderr pipes. On error it calls cancel() and returns.
func setupProcessPipes(cmd *exec.Cmd, cancel func()) (stdin io.WriteCloser, stdout, stderr io.ReadCloser, err error) {
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

// parsedLine holds the JSON-parsed superset of all agent output envelope
// fields. Raw is the original bytes; the typed fields are populated by a
// single json.Unmarshal in readOutput so downstream consumers never need to
// re-parse the envelope.
//
// ID is a json.RawMessage rather than a typed integer/string because
// providers disagree on the wire format: JSON-RPC 2.0 (Codex, ACP) uses
// integer ids, while Pi uses opaque string ids. Capturing the raw bytes
// avoids unmarshal errors that would otherwise wipe out the rest of the
// envelope when the form does not match a typed field. Use IDInt64 /
// IDString to interpret the value at the point of use.
type parsedLine struct {
	Raw    []byte
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// HasID reports whether the line carried a non-null id field.
func (p *parsedLine) HasID() bool {
	return len(p.ID) > 0 && string(p.ID) != "null"
}

// IDInt64 returns the line's id parsed as an int64. Returns false when the
// id is missing, null, or not numeric.
func (p *parsedLine) IDInt64() (int64, bool) {
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
func (p *parsedLine) IDString() string {
	if !p.HasID() {
		return ""
	}
	var s string
	if err := json.Unmarshal(p.ID, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(p.ID))
}

// parseLine creates a parsedLine from raw bytes. Used by HandleOutput methods
// that accept []byte (e.g. for tests) to bridge into the single-parse pipeline.
func parseLine(content []byte) *parsedLine {
	line := &parsedLine{Raw: content}
	if err := json.Unmarshal(content, line); err != nil {
		slog.Warn("parse line unmarshal failed", "error", err)
	}
	return line
}

// outputInterceptor is a function that inspects a parsed line before it is
// forwarded to HandleOutput. If it returns true, the line is consumed (not
// forwarded).
type outputInterceptor func(line *parsedLine) bool

// outputHandler processes a single parsed output line from the agent process.
type outputHandler func(line *parsedLine)

// readOutput reads JSONL lines from stdout, JSON-parses them once into a
// parsedLine, optionally intercepts responses, then forwards remaining lines
// to the output handler.
func (p *processBase) readOutput(scanner *bufio.Scanner, intercept outputInterceptor, handle outputHandler) {
	p.skipPreamble(scanner)

	firstLineTraced := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !firstLineTraced {
			TraceStartupPhase(p.agentID, "first_agent_line")
			firstLineTraced = true
		}

		if p.isDiscardingOutput() {
			continue
		}

		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		parsed := &parsedLine{Raw: lineCopy}
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

// messageWithToolUses captures the completed tool count as separate worker metadata.
func (p *processBase) messageWithToolUses(original []byte) MessageContent {
	p.mu.Lock()
	count := p.turnToolUses
	p.mu.Unlock()
	return withToolUseCount(MessageContent{Original: original}, count)
}

// drainStderr starts a goroutine that reads from the given reader into
// the stderr buffer, capped at maxStderrSize. It closes stderrDone when
// the reader is exhausted. The lock is only held for individual writes,
// not the entire drain loop.
func (p *processBase) drainStderr(r io.Reader) {
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
