package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/procutil"
)

const screenBufferSize = 100 * 1024 // 100KB ring buffer for screen restore

const (
	terminalChildCloseTimerTag = "terminal-child-close"
	terminalReadDrainTimerTag  = "terminal-read-drain"
)

// ScreenBuffer is a thread-safe ring buffer that stores recent PTY output.
// It also tracks a cumulative byte counter so callers can resume from a
// known offset instead of re-reading the full buffer on every subscribe.
//
// Snapshot replies (the bytes a fallen-behind or page-refreshing
// subscriber needs to reset its xterm and replay) are prefixed with the
// output of an internal modeTracker that observes a small set of sticky
// xterm modes — alt-screen toggle, cursor visibility, autowrap, app
// cursor keys, bracketed paste, mouse tracking/encoding, window title.
// Programs that entered alt screen well before the retained window
// still render correctly after a reconnect because the prefix
// re-establishes the mode before the body bytes replay.
//
// What's still out of reach by byte-replay alone (and why the tracker
// stops where it does): SGR colors / bold / italic, scrolling regions
// (DECSTBM), saved cursor (DECSC/DECRC), character-set designations,
// origin mode (DECOM), and the cell content of bytes that fell out of
// the retained window. SGR self-heals on the next color change. The
// cell content beyond the ring is irrecoverable in any byte-replay
// design — only a parsed cell grid (tmux-style emulation) can
// reconstruct it, which is deliberately out of scope.
type ScreenBuffer struct {
	// deliverMu serializes WriteAndDeliver, so that the ring write and
	// the delivery to the OutputHandler are one step for every writer of
	// this buffer. It is never held by a reader. Lock order is deliverMu
	// then mu; nothing takes them the other way round, and the delivery
	// runs with mu released so a handler can read the buffer.
	deliverMu sync.Mutex

	mu      sync.Mutex
	buf     []byte
	pos     int
	full    bool
	total   int64 // Total bytes ever written (monotonic within a PTY session).
	tracker modeTracker
}

// NewScreenBuffer creates a new screen buffer.
func NewScreenBuffer() *ScreenBuffer {
	return &ScreenBuffer{buf: make([]byte, screenBufferSize)}
}

// NewScreenBufferWithOffset creates a new screen buffer whose cumulative
// byte counter starts at initialOffset. Used by RestartTerminal when no
// in-memory *Terminal exists (e.g. after a worker restart): seeding the
// counter with len(persistedScreen) keeps the cumulative offset above
// the frontend's hydrated lastOffset, so newly-emitted end_offset values
// stay monotonically ahead and don't trip the snapshot-replay path on
// the next WatchEvents resubscribe.
func NewScreenBufferWithOffset(initialOffset int64) *ScreenBuffer {
	return &ScreenBuffer{buf: make([]byte, screenBufferSize), total: initialOffset}
}

// Write appends data to the ring buffer and returns the cumulative byte
// offset at the end of the write plus any notification-class signals the
// tracker observed in this chunk. Watchers persist that offset as their
// resume cursor, so a caller that forwards it to one must use
// WriteAndDeliver instead: this method assigns the offset under the lock
// but leaves the delivery that follows it to the scheduler.
func (sb *ScreenBuffer) Write(data []byte) (int64, []Signal) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	// Observe the bytes for sticky-mode tracking before the ring copy.
	// Order is irrelevant for correctness (feed is a pure function of
	// data), but feeding first reads naturally and keeps the tracker
	// state consistent with what a fresh xterm receiving these same
	// bytes would hold.
	sb.tracker.beginChunk()
	sb.tracker.feed(data)
	signals := sb.tracker.drainSignals()
	sb.total += int64(len(data))
	// Writes larger than the ring would overwrite themselves; only the
	// final len(buf) bytes can survive, so skip ahead to them.
	if len(data) >= len(sb.buf) {
		copy(sb.buf, data[len(data)-len(sb.buf):])
		sb.pos = 0
		sb.full = true
		return sb.total, signals
	}
	for len(data) > 0 {
		n := copy(sb.buf[sb.pos:], data)
		data = data[n:]
		sb.pos += n
		if sb.pos >= len(sb.buf) {
			sb.pos = 0
			sb.full = true
		}
	}
	return sb.total, signals
}

// WriteAndDeliver writes data and hands the resulting cumulative offset
// and signals to deliver, with the two steps serialized against every
// other writer of this buffer. Use it for any write whose offset reaches
// a subscriber; plain Write assigns the offset correctly but leaves the
// delivery order to the scheduler.
//
// The order matters because a subscriber treats the offsets as
// authoritative. A PTY read and an AppendOutput run on separate
// goroutines: without this serialization the later chunk's offset can
// reach the subscriber first, and the earlier chunk then arrives with a
// range wholly below the subscriber's cursor, which reads as already
// rendered. Those bytes are dropped and never appear.
//
// deliver runs with mu released, so it may read this buffer (Snapshot,
// SnapshotSince). It must not write to it again — WriteAndDeliver is not
// reentrant.
//
// deliver does run under deliverMu, so a handler that parks (the worker's
// handler broadcasts, and SendStream can block on a stalled transport)
// holds up the other writers for that time. This costs one extra wait,
// not a new way to block: the PTY reader already calls its handler
// synchronously between reads, and AppendOutput already ran the same
// handler on its own goroutine.
func (sb *ScreenBuffer) WriteAndDeliver(data []byte, deliver OutputHandler) {
	sb.deliverMu.Lock()
	defer sb.deliverMu.Unlock()

	endOffset, signals := sb.Write(data)
	deliver(data, endOffset, signals)
}

// TotalBytes returns the cumulative byte count ever written to this buffer.
// Monotonic within a single PTY session; a new Terminal starts at 0.
func (sb *ScreenBuffer) TotalBytes() int64 {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.total
}

// Snapshot returns a copy of every retained byte in chronological order
// (prefixed with the mode tracker's snapshotPrefix so xterm reset+replay
// still lands the program in the right mode), and the cumulative offset
// at the end of the body bytes. The prefix is synthesized — it does NOT
// count toward the offset.
func (sb *ScreenBuffer) Snapshot() ([]byte, int64) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	body := sb.tailBytesLocked(sb.retainedLocked())
	return prependPrefix(sb.tracker.snapshotPrefix(), body), sb.total
}

// prependPrefix concatenates prefix and body. Returns body unchanged
// when prefix is nil so the common case (default tracker state) avoids
// the extra alloc + copy.
func prependPrefix(prefix, body []byte) []byte {
	if len(prefix) == 0 {
		return body
	}
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix...)
	out = append(out, body...)
	return out
}

// HasSuffix reports whether the retained bytes end with needle. Used by
// the disconnect-notice path to check idempotency without allocating a
// copy of the full buffer.
func (sb *ScreenBuffer) HasSuffix(needle []byte) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if len(needle) == 0 {
		return true
	}
	head, wrap := sb.tailLocked(len(needle))
	if len(head)+len(wrap) < len(needle) {
		return false
	}
	return bytes.Equal(head, needle[:len(head)]) &&
		bytes.Equal(wrap, needle[len(head):])
}

// retainedLocked reports the number of bytes currently in the ring: pos
// before the first wrap, the full buffer length after. Caller must hold
// sb.mu.
func (sb *ScreenBuffer) retainedLocked() int {
	if sb.full {
		return len(sb.buf)
	}
	return sb.pos
}

// tailLocked returns the trailing n retained bytes as two ring segments
// — head then wrap in chronological order, head+wrap == last n bytes.
// If fewer than n bytes are retained, returns what's available. Caller
// must hold sb.mu.
func (sb *ScreenBuffer) tailLocked(n int) (head, wrap []byte) {
	if retained := sb.retainedLocked(); n > retained {
		n = retained
	}
	start := sb.pos - n
	if start >= 0 {
		return nil, sb.buf[start:sb.pos]
	}
	headLen := -start
	return sb.buf[len(sb.buf)-headLen:], sb.buf[:sb.pos]
}

// tailBytesLocked returns the trailing n retained bytes as a freshly
// allocated, flattened slice. Zero-length when n <= 0 or the buffer is
// empty. Caller must hold sb.mu.
func (sb *ScreenBuffer) tailBytesLocked(n int) []byte {
	head, wrap := sb.tailLocked(n)
	out := make([]byte, len(head)+len(wrap))
	copy(out, head)
	copy(out[len(head):], wrap)
	return out
}

// SnapshotSince returns the bytes the caller needs in order to advance
// from afterOffset to the current head, the cumulative offset at the end
// of those bytes, and whether the returned bytes should be treated as a
// full-state replacement (caller must reset its terminal buffer before
// writing) rather than an incremental append.
//
//   - afterOffset == total: caller is caught up. Returns (nil, total, false).
//   - afterOffset within the retained window: returns the incremental
//     delta since afterOffset. isSnapshot is false.
//   - afterOffset <= 0 (a forced-resync subscribe: the caller knows it
//     lost bytes and an incremental delta cannot fill the hole), afterOffset
//     has fallen out of the retained window, or is larger than total (PTY
//     recreated beneath a stale client): returns the full retained buffer
//     with isSnapshot=true so the caller drops any stale state.
func (sb *ScreenBuffer) SnapshotSince(afterOffset int64) (data []byte, endOffset int64, isSnapshot bool) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	total := sb.total
	if afterOffset == total {
		return nil, total, false
	}
	// Retained window is [total-retained, total).
	windowStart := total - int64(sb.retainedLocked())

	// Incremental catch-up: afterOffset is inside the retained window,
	// so copy only the missing suffix directly from the ring. afterOffset 0
	// is excluded on purpose: it is the forced-resync subscribe, and when the
	// whole history is still retained an incremental-from-zero delta would
	// come back instead of the snapshot the caller asked for.
	if afterOffset > 0 && afterOffset >= windowStart && afterOffset < total {
		return sb.tailBytesLocked(int(total - afterOffset)), total, false
	}

	// Cold subscribe, forced resync (afterOffset <= 0), stale offset > total,
	// or caller has fallen behind the retained window: send everything we
	// have with the snapshot flag, prefixed with the tracker's mode-restore
	// bytes so a TUI in alt screen still renders correctly after the
	// xterm reset+replay.
	body := sb.tailBytesLocked(sb.retainedLocked())
	return prependPrefix(sb.tracker.snapshotPrefix(), body), total, true
}

// OutputHandler is called for each chunk of output from the PTY. The
// endOffset is the cumulative byte counter *after* this chunk; callers
// forward it to watchers as the resume cursor for this event. Signals
// carry notification-class escape sequences (bell, title, notification,
// progress) interpreted worker-side from this chunk.
type OutputHandler func(data []byte, endOffset int64, signals []Signal)

// Terminal manages a single PTY session.
type Terminal struct {
	id        string
	cmd       *pty.Cmd
	ptmx      pty.Pty
	jobObject *procutil.JobObject
	// clock supplies the two timers waitForReadDrainedWithin arms. The
	// constructor requires it, so a Terminal cannot exist without one and a
	// timer added later in this file finds the right clock already in scope.
	// The alternative is a caller that forgets to thread it and falls back to
	// the `time` package, which is the drift a mock clock cannot see.
	//
	// Respawn passes t.clock through, so a restarted terminal inherits it
	// instead of depending on its caller to remember.
	clock quartz.Clock
	// outputFn is the internal dispatch for both live PTY reads and
	// AppendOutput. It writes into screenBuf and forwards the resulting
	// cumulative offset to the user-provided OutputHandler, through
	// ScreenBuffer.WriteAndDeliver so those two writers cannot deliver
	// their chunks out of offset order.
	outputFn  func(data []byte)
	screenBuf *ScreenBuffer
	mu        sync.Mutex
	stopped   bool
	exitCode  int
	exitCh    chan struct{}
	// readDoneCh is closed when readOutput returns. exitCh tracks
	// cmd.Wait() in a separate goroutine, so a closed exitCh does NOT
	// imply screenBuf writes have stopped — wait on this instead.
	readDoneCh chan struct{}
	// childSideClosedCh is closed once the natural-exit path finished closing
	// the child side of the pty, which is the act that ends the reader.
	//
	// It exists so a caller that waits for the drain measures only the
	// READER's own scheduling, as the drain grace claims to. exitCh closes
	// first, and on Windows the close between the two blocks until the console
	// host flushes -- so a grace started at exitCh would spend itself on that
	// flush and give up before the reader ever ran.
	//
	// Only waitForExit closes it. Stop's own close does not, because Stop
	// discards the unread output by design and nothing waits for its drain.
	childSideClosedCh chan struct{}
	// childSideOnce guards closePTYChildSide. Both the natural-exit path and
	// Stop reach it, and on Windows it must run exactly once — see the Windows
	// implementation for what a second call closes.
	childSideOnce sync.Once
	// ptyMu serializes a pty TEARDOWN against a pty CALL. Resize and SendInput
	// take it for reading; closeChildSide and closeWorkerSide take it for
	// writing.
	//
	// It exists because a natural exit now tears the pty down, and that path
	// holds neither t.mu nor the `stopped` flag those two methods check: only
	// Stop sets `stopped`, and a terminal that exits on its own stays in the
	// manager until the user closes the tab. On Windows the teardown is
	// ClosePseudoConsole on a raw handle that go-pty never clears, and go-pty's
	// conPty takes its OWN mutex around both Close and Resize for exactly this
	// reason -- so a resize that straddles the close would call
	// ResizePseudoConsole on a handle another goroutine is closing.
	//
	// readOutput stays OUTSIDE it. Its Read blocks until the teardown ends it,
	// so a reader holding this lock would deadlock the writer that must run to
	// wake it. That is safe: on Windows Read uses the output pipe, which
	// ClosePseudoConsole does not touch.
	ptyMu sync.RWMutex
}

// Options configures a new Terminal.
type Options struct {
	ID            string
	Shell         string
	WorkingDir    string
	ShellStartDir string
	Cols          uint16
	Rows          uint16
	// ExtraEnv is appended verbatim to the spawned shell's environment
	// after TERM is set. The service.Service populates this with
	// LEAPMUX_CONTROL_* so scripts inside the shell can drive LeapMux
	// via `leapmux control`.
	ExtraEnv []string
}

// spawnEnv builds the environment a terminal's login shell is started with,
// from the worker's own environ plus the caller's extras.
//
// GitOptionalLocksOff for the same reason the worker's own git commands carry
// it: the user's shell is the third contender for .git/index.lock in a repo
// LeapMux is driving, and an interactive `git status` taking the lock purely to
// write back a refreshed index can kill a concurrent worker checkout. The cost
// is local to this shell -- status still reports correctly, it just leaves the
// stat cache unrefreshed.
//
// PinEnv, not append: TERM is exported by essentially every shell this worker
// could have been started from, and GIT_OPTIONAL_LOCKS is inherited whenever
// that shell was itself a LeapMux terminal -- appending would hand the child
// two entries for each.
//
// A named function rather than inline construction because it is the only part
// of the spawn a test can assert on: `exec.Cmd` resolves duplicates last-wins,
// so a layered pin is invisible to the child process and visible only here.
func spawnEnv(environ, extraEnv []string) []string {
	env := envutil.ScrubAppImageEnvSlice(envutil.PinEnv(environ,
		"TERM=xterm-256color",
		gitutil.GitOptionalLocksOff,
	))
	if len(extraEnv) == 0 {
		return env
	}
	// Strip any pre-existing LEAPMUX_CONTROL_* (defensive — leapmux worker
	// doesn't normally set them, but a recursive launch would inherit) before
	// injecting the canonical values.
	return append(envutil.StripByPrefix(env, "LEAPMUX_CONTROL_"), extraEnv...)
}

// Start creates a new PTY terminal session. The supplied context
// governs the spawn itself: if it is already cancelled Start returns
// ctx.Err() without forking, and a later cancellation sends the child
// process the usual exec.CommandContext kill signal. The caller still
// owns the long-running Terminal — its lifetime is independent of ctx
// once Start returns successfully.
func Start(ctx context.Context, opts Options, clock quartz.Clock, outputFn OutputHandler) (*Terminal, error) {
	return startWithScreenBuffer(ctx, opts, clock, NewScreenBuffer(), outputFn)
}

// startWithScreenBuffer is the actual spawn implementation, parameterized
// over the ScreenBuffer so RestartTerminal can carry the cumulative
// offset (and any retained bytes) across PTY incarnations.
func startWithScreenBuffer(
	ctx context.Context,
	opts Options,
	clock quartz.Clock,
	screenBuf *ScreenBuffer,
	outputFn OutputHandler,
) (*Terminal, error) {
	if clock == nil {
		panic("terminal: clock must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	shell := opts.Shell
	if shell == "" {
		shell = ResolveDefaultShell()
	}

	args := LoginShellArgs(shell)

	ptmx, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("new pty: %w", err)
	}

	cmd := ptmx.CommandContext(ctx, shell, args...)
	cmd.Dir = opts.WorkingDir
	cmd.Env = spawnEnv(os.Environ(), opts.ExtraEnv)
	// No procutil.HideConsoleWindow here: on Windows, CREATE_NO_WINDOW is
	// incompatible with ConPTY — the pseudo console already serves as the
	// child's console, and the flag would leave it with none.

	cols, rows := opts.Cols, opts.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 25
	}
	if err := ptmx.Resize(int(cols), int(rows)); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("resize pty: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		return nil, fmt.Errorf("start pty: %w", err)
	}

	// Put the shell and its descendants into a kill group so closing the
	// tab reaps the whole tree, not just the direct shell. Failure is
	// non-fatal: the terminal still works, it just loses the tree-kill
	// guarantee for this session.
	jobObject, err := procutil.AssignPID(cmd.Process.Pid)
	if err != nil {
		slog.Warn("terminal attach job object failed",
			"terminal_id", opts.ID,
			"error", err,
		)
	}

	// The buffer owns the serialization, not this closure: Respawn hands
	// the same ScreenBuffer to the next incarnation, whose reader can
	// start while the previous one still drains (an exited terminal is
	// not a drained one — see readDoneCh). A mutex captured here would be
	// a fresh one for each incarnation and would leave that overlap
	// unserialized.
	wrappedOutput := func(data []byte) {
		screenBuf.WriteAndDeliver(data, outputFn)
	}

	t := &Terminal{
		id:                opts.ID,
		clock:             clock,
		cmd:               cmd,
		ptmx:              ptmx,
		jobObject:         jobObject,
		outputFn:          wrappedOutput,
		screenBuf:         screenBuf,
		exitCh:            make(chan struct{}),
		readDoneCh:        make(chan struct{}),
		childSideClosedCh: make(chan struct{}),
	}

	go func() {
		t.readOutput()
		close(t.readDoneCh)
	}()
	go t.waitForExit()

	slog.Info("terminal started",
		"terminal_id", opts.ID,
		"shell", shell,
		"args", args,
		"pid", cmd.Process.Pid,
	)

	return t, nil
}

// SendInput writes data to the PTY.
func (t *Terminal) SendInput(data []byte) error {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return fmt.Errorf("terminal is stopped")
	}
	t.mu.Unlock()

	// ptyMu, not t.mu: `stopped` covers only a Stop, and a terminal that exited
	// on its own tore its child side down without setting it. See ptyMu.
	t.ptyMu.RLock()
	defer t.ptyMu.RUnlock()
	_, err := t.ptmx.Write(data)
	return err
}

// Resize changes the terminal dimensions.
func (t *Terminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return fmt.Errorf("terminal is stopped")
	}
	t.mu.Unlock()

	t.ptyMu.RLock()
	defer t.ptyMu.RUnlock()
	return t.ptmx.Resize(int(cols), int(rows))
}

// Stop terminates the terminal session and every process spawned beneath
// the shell. Closing the PTY triggers the kernel's normal hang-up
// flow; Terminate then reaps anything still alive in the shell's kill
// group (JobObject on Windows, process-group SIGHUP+SIGKILL on Unix).
//
// Both pty ends close here, child side first. A forced stop discards whatever
// the child wrote and nobody read yet, which is the point: the caller wants the
// session gone, not drained.
//
// `stopped` is claimed under t.mu and the lock is RELEASED before the closes.
// On Windows the child-side close blocks until the console host flushes, and a
// natural exit can already be inside that call -- so holding t.mu across it
// would stall every SendInput and Resize for the length of a flush this
// terminal is not even performing. The claim is what makes the early return
// above correct; the closes need only ptyMu, which they take themselves.
func (t *Terminal) Stop() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	t.mu.Unlock()

	t.closeChildSide()
	t.closeWorkerSide()
	if err := t.jobObject.Terminate(); err != nil {
		slog.Debug("terminal job object terminate failed",
			"terminal_id", t.id,
			"error", err,
		)
	}
	if t.jobObject == nil && t.cmd.Process != nil {
		// Fallback when AssignPID failed at startup; better than leaking
		// the direct shell process even if we lose the tree guarantee.
		_ = t.cmd.Process.Kill()
	}
}

// Wait blocks until the terminal process exits.
func (t *Terminal) Wait() int {
	<-t.exitCh
	return t.exitCode
}

// waitForReadDone blocks with no limit until readOutput returns, after which
// the screen buffer's cumulative offset is stable.
//
// Only a caller that already stopped the terminal uses this. Stop closes the
// worker side of the pty, which ends the reader at once.
// waitForReadDrainedWithin serves the caller that stopped nothing, where a
// descendant that escaped the shell's kill group can hold the reader open for
// ever.
func (t *Terminal) waitForReadDone() {
	<-t.readDoneCh
}

// childCloseOutcome says what ended the first stage of the drain wait.
type childCloseOutcome int

const (
	// childSideClosed means the close that ends the reader finished. The
	// reader-drain stage runs next.
	childSideClosed childCloseOutcome = iota
	// readerFinishedFirst means readOutput returned before the close, so the
	// screen is already stable and no drain wait remains.
	readerFinishedFirst
	// childCloseGaveUp means the close did not finish inside the grace.
	childCloseGaveUp
)

// waitForChildSideClose blocks until the natural-exit path closes the child
// side of the pty -- the act that ends the reader -- or `within` runs out.
//
// A method of its own, so the deferred stop ends with THIS stage. A defer in
// the caller runs when the caller returns. It would leave this timer armed for
// the whole of the reader-drain stage, and the code would then hold two live
// timers for one budget.
func (t *Terminal) waitForChildSideClose(within time.Duration) childCloseOutcome {
	timer := t.clock.NewTimer(within, terminalChildCloseTimerTag)
	defer timer.Stop(terminalChildCloseTimerTag)
	select {
	case <-t.childSideClosedCh:
		return childSideClosed
	case <-t.readDoneCh:
		// Already finished, by Stop's close or by the child's own EOF.
		return readerFinishedFirst
	case <-timer.C:
		return childCloseGaveUp
	}
}

// waitForReadDrainedWithin blocks until readOutput returns, after which the
// screen buffer's cumulative offset is stable, and reports whether the reader
// finished inside `within`.
//
// The limit exists for a pty this worker cannot end. The natural-exit path
// closes the child side, and on a Unix tty that ends the reader only once the
// LAST descriptor for it is gone -- so a descendant that escaped the shell's
// kill group (a setsid daemon that inherited the tty) holds the reader open,
// and this process cannot reach that descendant. A wait with no limit would
// strand the caller for ever. Giving up returns a screen that can be one chunk
// short of the shell's last write, which is what the caller received before the
// drain wait existed at all.
//
// `within` is spent TWICE at most: once waiting for the child side to close --
// the act that ends the reader, and not instant on Windows -- and once waiting
// for the reader itself. Measured as one budget from the exit, the flush would
// consume the grace and it would expire before the reader was ever scheduled.
// Stop never closes childSideClosedCh, so a caller that stopped the terminal
// calls waitForReadDone instead -- it discards the unread output by design.
//
// `within` must be positive. A non-positive limit fires both timers at once on
// the real clock and on a mock, which turns the grace into no wait at all.
func (t *Terminal) waitForReadDrainedWithin(within time.Duration) bool {
	if within <= 0 {
		panic("terminal: waitForReadDrainedWithin needs a positive limit; use waitForReadDone")
	}
	// TWO waits, each with its OWN budget of `within`, because each can hang for
	// a different reason and neither may spend the other's budget.
	//
	// The close is itself a wait: on Windows ClosePseudoConsole blocks until the
	// console host drains the output pipe, which a parked output handler can
	// hold up for ever. One shared timer would put that flush inside the
	// reader's grace and give up before the reader was ever scheduled -- on the
	// one platform this whole path exists for. No timer at all would strand the
	// caller there instead, which loses the exit outright.
	switch t.waitForChildSideClose(within) {
	case readerFinishedFirst:
		return true
	case childCloseGaveUp:
		return false
	case childSideClosed:
		// The reader is ending now. It gets its own budget below.
	}
	drained := t.clock.NewTimer(within, terminalReadDrainTimerTag)
	defer drained.Stop(terminalReadDrainTimerTag)
	select {
	case <-t.readDoneCh:
		return true
	case <-drained.C:
		return false
	}
}

// closeChildSide closes the pty end the child process wrote through, at most
// once per terminal. The reader then drains what the child left behind and
// returns; see closePTYChildSide for the per-platform mechanism.
//
// Under ptyMu, so no Resize or SendInput is inside a pty call while the handle
// under it closes. The Once is INSIDE the lock, so a second caller waits for
// the first close rather than returning while it is still in flight.
func (t *Terminal) closeChildSide() {
	if t.ptmx == nil {
		return
	}
	t.ptyMu.Lock()
	defer t.ptyMu.Unlock()
	t.childSideOnce.Do(func() { closePTYChildSide(t.ptmx) })
}

// closeWorkerSide closes the ends this process reads and writes through. Only
// Stop calls it: a terminal that exited on its own keeps them open so
// ScreenSnapshot and a later restart still work off the same instance.
func (t *Terminal) closeWorkerSide() {
	if t.ptmx == nil {
		return
	}
	t.ptyMu.Lock()
	defer t.ptyMu.Unlock()
	closePTYWorkerSide(t.ptmx)
}

// IsExited returns true if the terminal process has exited.
func (t *Terminal) IsExited() bool {
	select {
	case <-t.exitCh:
		return true
	default:
		return false
	}
}

// ID returns the terminal's ID.
func (t *Terminal) ID() string {
	return t.id
}

// ScreenSnapshot returns the full retained PTY output and the cumulative
// byte offset at its end.
func (t *Terminal) ScreenSnapshot() ([]byte, int64) {
	return t.screenBuf.Snapshot()
}

// Respawn forks a fresh PTY for this terminal, transferring the existing
// ScreenBuffer to the new instance so the cumulative byte counter and any
// retained ring bytes survive across PTY incarnations. Returns the new
// *Terminal; the caller (Manager.RestartTerminal) is responsible for
// swapping this terminal out of the manager's map.
//
// Caller must guarantee t has exited (IsExited == true) so the prior PTY
// is no longer writing to t.screenBuf. Manager.RestartTerminal enforces
// this under m.mu; external callers must do their own ordering.
func (t *Terminal) Respawn(ctx context.Context, opts Options, outputFn OutputHandler) (*Terminal, error) {
	return startWithScreenBuffer(ctx, opts, t.clock, t.screenBuf, outputFn)
}

// ScreenSnapshotSince returns the bytes a subscriber needs to advance
// from afterOffset to the current head of the screen buffer, the
// cumulative offset at the end of those bytes, and whether the returned
// bytes are a full-state replacement (subscriber must reset its
// terminal) rather than an append. See ScreenBuffer.SnapshotSince for
// the detailed contract.
func (t *Terminal) ScreenSnapshotSince(afterOffset int64) (data []byte, endOffset int64, isSnapshot bool) {
	return t.screenBuf.SnapshotSince(afterOffset)
}

// ScreenHasSuffix reports whether the retained screen buffer ends with
// needle. Avoids the allocation of ScreenSnapshot for callers that only
// need to check a trailing marker.
func (t *Terminal) ScreenHasSuffix(needle []byte) bool {
	return t.screenBuf.HasSuffix(needle)
}

// AppendOutput injects synthetic output into the terminal stream and screen
// buffer without writing to the PTY. This is used for system notices that
// should be restorable like normal terminal output. Runs through the same
// wrappedOutput path as live PTY data so the cumulative offset advances,
// and takes its place in the one delivery order that path keeps. Safe to
// call while the shell still runs: the shutdown sweep appends the
// disconnect notice to terminals it does not stop.
func (t *Terminal) AppendOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	t.outputFn(data)
}

func (t *Terminal) readOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			t.outputFn(data)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Debug("terminal read error",
					"terminal_id", t.id,
					"error", err,
				)
			}
			return
		}
	}
}

func (t *Terminal) waitForExit() {
	err := t.cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.exitCode = exitErr.ExitCode()
		} else {
			t.exitCode = -1
		}
	}
	if closeErr := t.jobObject.Close(); closeErr != nil {
		slog.Debug("terminal job object close failed",
			"terminal_id", t.id,
			"error", closeErr,
		)
	}
	close(t.exitCh)

	// End the reader now that the shell and its kill group are gone. Nothing
	// else will: the pty stays open for a terminal that exited on its own --
	// the manager keeps the entry so restart-via-Enter can respawn into the
	// same screen buffer -- and this process holds the child side of it, so
	// readOutput would block on a pty that can never produce another byte.
	// Buffered output survives: both platforms hand the reader what is already
	// there before they end the read.
	//
	// It runs AFTER exitCh, so nothing that waits on the exit has to wait for
	// it. On Windows this closes the pseudoconsole, which blocks until the
	// console host flushes; a handler that parks the reader mid-chunk therefore
	// parks this call too. Exiting is the fact the child established, and it is
	// reported either way -- only the drain waits, and that wait has a limit.
	//
	// childSideClosedCh is what keeps that limit measuring what it claims to.
	// installTerminal's exit goroutine wakes on exitCh, so without this signal
	// its grace would start HERE and the console-host flush would run inside
	// it -- on the one platform this whole path exists for.
	t.closeChildSide()
	close(t.childSideClosedCh)

	slog.Info("terminal exited",
		"terminal_id", t.id,
		"exit_code", t.exitCode,
	)
}
