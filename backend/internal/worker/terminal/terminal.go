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

	pty "github.com/aymanbagabas/go-pty"

	"github.com/leapmux/leapmux/util/procutil"
)

const screenBufferSize = 100 * 1024 // 100KB ring buffer for screen restore

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

// Write appends data to the ring buffer and returns the cumulative byte
// offset at the end of the write. Callers forward that offset to watchers
// so they can persist it as their resume cursor.
func (sb *ScreenBuffer) Write(data []byte) int64 {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	// Observe the bytes for sticky-mode tracking before the ring copy.
	// Order is irrelevant for correctness (feed is a pure function of
	// data), but feeding first reads naturally and keeps the tracker
	// state consistent with what a fresh xterm receiving these same
	// bytes would hold.
	sb.tracker.feed(data)
	sb.total += int64(len(data))
	// Writes larger than the ring would overwrite themselves; only the
	// final len(buf) bytes can survive, so skip ahead to them.
	if len(data) >= len(sb.buf) {
		copy(sb.buf, data[len(data)-len(sb.buf):])
		sb.pos = 0
		sb.full = true
		return sb.total
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
	return sb.total
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
//   - afterOffset has fallen out of the retained window, is negative, or
//     is larger than total (PTY recreated beneath a stale client):
//     returns the full retained buffer with isSnapshot=true so the caller
//     drops any stale state.
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
	// so copy only the missing suffix directly from the ring.
	if afterOffset >= windowStart && afterOffset < total {
		return sb.tailBytesLocked(int(total - afterOffset)), total, false
	}

	// Cold subscribe, negative offset, stale offset > total, or caller
	// has fallen behind the retained window: send everything we have
	// with the snapshot flag, prefixed with the tracker's mode-restore
	// bytes so a TUI in alt screen still renders correctly after the
	// xterm reset+replay.
	body := sb.tailBytesLocked(sb.retainedLocked())
	return prependPrefix(sb.tracker.snapshotPrefix(), body), total, true
}

// OutputHandler is called for each chunk of output from the PTY. The
// endOffset is the cumulative byte counter *after* this chunk; callers
// forward it to watchers as the resume cursor for this event.
type OutputHandler func(data []byte, endOffset int64)

// Terminal manages a single PTY session.
type Terminal struct {
	id        string
	cmd       *pty.Cmd
	ptmx      pty.Pty
	jobObject *procutil.JobObject
	// outputFn is the internal dispatch for both live PTY reads and
	// AppendOutput. It writes into screenBuf and forwards the resulting
	// cumulative offset to the user-provided OutputHandler.
	outputFn  func(data []byte)
	screenBuf *ScreenBuffer
	mu        sync.Mutex
	stopped   bool
	exitCode  int
	exitCh    chan struct{}
}

// Options configures a new Terminal.
type Options struct {
	ID            string
	WorkspaceID   string
	Shell         string
	WorkingDir    string
	ShellStartDir string
	Cols          uint16
	Rows          uint16
}

// Start creates a new PTY terminal session. The supplied context
// governs the spawn itself: if it is already cancelled Start returns
// ctx.Err() without forking, and a later cancellation sends the child
// process the usual exec.CommandContext kill signal. The caller still
// owns the long-running Terminal — its lifetime is independent of ctx
// once Start returns successfully.
func Start(ctx context.Context, opts Options, outputFn OutputHandler) (*Terminal, error) {
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
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
	)
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

	screenBuf := NewScreenBuffer()
	wrappedOutput := func(data []byte) {
		endOffset := screenBuf.Write(data)
		outputFn(data, endOffset)
	}

	t := &Terminal{
		id:        opts.ID,
		cmd:       cmd,
		ptmx:      ptmx,
		jobObject: jobObject,
		outputFn:  wrappedOutput,
		screenBuf: screenBuf,
		exitCh:    make(chan struct{}),
	}

	go t.readOutput()
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
	defer t.mu.Unlock()

	if t.stopped {
		return fmt.Errorf("terminal is stopped")
	}

	_, err := t.ptmx.Write(data)
	return err
}

// Resize changes the terminal dimensions.
func (t *Terminal) Resize(cols, rows uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return fmt.Errorf("terminal is stopped")
	}

	return t.ptmx.Resize(int(cols), int(rows))
}

// Stop terminates the terminal session and every process spawned beneath
// the shell. Closing the PTY master triggers the kernel's normal hang-up
// flow; Terminate then reaps anything still alive in the shell's kill
// group (JobObject on Windows, process-group SIGHUP+SIGKILL on Unix).
func (t *Terminal) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}
	t.stopped = true

	_ = t.ptmx.Close()
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
// wrappedOutput path as live PTY data so the cumulative offset advances.
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

	slog.Info("terminal exited",
		"terminal_id", t.id,
		"exit_code", t.exitCode,
	)
}
