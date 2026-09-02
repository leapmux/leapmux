package terminal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/util/validate"
)

// ErrTerminalNotFound is returned when a terminal operation targets an ID
// the Manager does not know about. Callers distinguish this from other
// failures with errors.Is so they can decide whether to retry or stash —
// e.g. the ResizeTerminal handler stashes dims for a terminal whose PTY
// is still being spawned in the background startup goroutine.
var ErrTerminalNotFound = errors.New("terminal not found")

// ErrTerminalStillRunning is returned by RestartTerminal when its target
// has not exited. Both the service handler (synchronous reject for better
// UX) and the manager (defense-in-depth for direct callers) surface this
// — sharing the sentinel keeps the user-visible message in one place and
// lets callers errors.Is instead of string-matching.
var ErrTerminalStillRunning = errors.New("terminal still running")

// TerminalMeta holds the working directory, title and dimensions for a
// terminal. Shell is intentionally NOT mirrored here: RestartTerminal reads the
// shell from the DB row via GetTerminalForRestart, which is the single
// source of truth (the column is written once at OpenTerminal time and
// never updated thereafter).
type TerminalMeta struct {
	WorkingDir    string
	ShellStartDir string
	Title         string
	Cols          uint32
	Rows          uint32
}

// TerminalSnapshot holds a point-in-time copy of a terminal's metadata and screen.
type TerminalSnapshot struct {
	TerminalMeta
	Screen []byte
}

// defaultReadDrainGrace is how long the exit goroutine waits for the reader to
// finish before it runs the exit handler anyway. Reaching it means a process
// this worker could not kill still holds the tty open, and a longer wait does
// not change that.
//
// The value only has to cover the scheduling of a reader the kernel already
// woke, because waitForReadDrained starts it only after the child side of the
// pty is closed -- the act that wakes that reader. Measured from the exit
// instead, this grace would also have to absorb the Windows console-host
// flush, which happens between the two.
const defaultReadDrainGrace = 2 * time.Second

// Manager tracks active terminal sessions.
type Manager struct {
	mu        sync.RWMutex
	terminals map[string]*Terminal     // terminalID -> Terminal
	meta      map[string]TerminalMeta  // terminalID -> metadata
	exitDone  map[string]chan struct{} // terminalID -> closed once the exit-handler goroutine returned
	// readDrainGrace limits the exit goroutine's wait for the reader. A field
	// rather than a constant so a test can drive the give-up path without
	// spending the real grace on it. Written only at construction -- by
	// NewManager, or by a test before it installs a terminal -- so no lock
	// guards it.
	//
	// A test reaching into an unexported field, and a REAL timer deciding what
	// a deterministic clock should: startupCore.clock does the same job the
	// other way. See https://github.com/leapmux/leapmux/issues/437.
	readDrainGrace time.Duration
}

// NewManager creates a new terminal Manager.
func NewManager() *Manager {
	return &Manager{
		terminals:      make(map[string]*Terminal),
		meta:           make(map[string]TerminalMeta),
		exitDone:       make(map[string]chan struct{}),
		readDrainGrace: defaultReadDrainGrace,
	}
}

// ExitHandler is called when a terminal process exits.
type ExitHandler func(terminalID string, exitCode int)

// StartTerminal creates a new PTY terminal. The supplied context
// governs only the spawn — once StartTerminal returns successfully,
// the terminal's lifetime is managed by RemoveTerminal / Stop.
// Cancelling ctx mid-spawn aborts the PTY fork (returning ctx.Err())
// so a CloseTerminal that lands during the sync-path phase of
// runTerminalStartup tears the nascent child down instead of leaking it.
func (m *Manager) StartTerminal(ctx context.Context, opts Options, outputFn OutputHandler, exitFn ExitHandler) error {
	m.mu.Lock()
	if _, exists := m.terminals[opts.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("terminal already exists: %s", opts.ID)
	}
	m.mu.Unlock()

	t, err := Start(ctx, opts, outputFn)
	if err != nil {
		return err
	}

	m.installTerminal(opts.ID, t, exitFn, func(TerminalMeta) TerminalMeta {
		return TerminalMeta{
			WorkingDir:    opts.WorkingDir,
			ShellStartDir: opts.ShellStartDir,
			Cols:          uint32(opts.Cols),
			Rows:          uint32(opts.Rows),
		}
	})
	return nil
}

// installTerminal records a freshly-spawned *Terminal in the manager
// and starts the goroutine that waits on its exit. Used by both
// StartTerminal and RestartTerminal. composeMeta is called under
// m.mu.Lock with the previous meta (zero value if none) and returns
// the meta to install: StartTerminal ignores prev and returns a fresh
// struct; RestartTerminal overlays opts onto prev so non-Options
// fields (Title) survive. Reading prev under the install lock closes
// the gap with concurrent UpdateTitle calls that would otherwise be
// silently overwritten by an outside-the-lock overlay.
//
// Notify when the terminal exits but keep it in the map so that
// ScreenSnapshot and ListTerminals still work. The entry is removed by
// RemoveTerminal (explicit close). The freshly-allocated exitDone chan
// is closed once the exit handler returned, so WaitForExit callers
// observe a definitive "the exit goroutine is done" signal.
func (m *Manager) installTerminal(id string, t *Terminal, exitFn ExitHandler, composeMeta func(prev TerminalMeta) TerminalMeta) {
	done := make(chan struct{})

	m.mu.Lock()
	meta := composeMeta(m.meta[id])
	m.terminals[id] = t
	m.meta[id] = meta
	m.exitDone[id] = done
	m.mu.Unlock()

	go func() {
		exitCode := t.Wait()
		// The handler persists the terminal's FINAL screen, so it must not run
		// until the reader applied the shell's last output. t.Wait returns when
		// the child is reaped, and that says nothing about readOutput: the bytes
		// the shell wrote just before it exited can still sit unread in the PTY
		// buffer. A snapshot taken here loses them, and because this is the only
		// persist a natural exit performs, the row keeps that truncated screen
		// for its whole life -- the user reopens an exited tab and the last
		// thing the shell printed is missing.
		//
		// What ends the reader is waitForExit closing the child side of the pty,
		// which it does right after it closes exitCh. The reaped child does not
		// end it: this process holds the child side too, and on Linux a master
		// read ends only when the last descriptor for the tty is gone. The
		// grace covers the holders this worker cannot reach -- see
		// Terminal.waitForReadDrained, which starts it only once that close
		// finished. A shell that leaves no such holder behind never reaches it.
		if !t.waitForReadDrained(m.readDrainGrace) {
			slog.Warn("terminal reader did not drain after the shell exited; "+
				"persisting the screen without its last output",
				"terminal_id", id,
				"grace", m.readDrainGrace,
			)
		}
		if exitFn != nil {
			exitFn(id, exitCode)
		}
		close(done)
	}()
}

// SendInput routes input to a terminal.
func (m *Manager) SendInput(terminalID string, data []byte) error {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrTerminalNotFound, terminalID)
	}
	if t.IsExited() {
		return fmt.Errorf("terminal exited: %s", terminalID)
	}

	return t.SendInput(data)
}

// Resize changes a terminal's dimensions.
func (m *Manager) Resize(terminalID string, cols, rows uint16) error {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	meta := m.meta[terminalID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrTerminalNotFound, terminalID)
	}
	if t.IsExited() {
		return fmt.Errorf("terminal exited: %s", terminalID)
	}

	// Skip if dimensions haven't changed to avoid a spurious SIGWINCH
	// that causes shells (e.g. zsh with starship) to redraw the prompt,
	// leaving the old prompt visible on screen.
	if meta.Cols == uint32(cols) && meta.Rows == uint32(rows) {
		return nil
	}

	if err := t.Resize(cols, rows); err != nil {
		return err
	}

	m.mu.Lock()
	if meta, exists := m.meta[terminalID]; exists {
		meta.Cols = uint32(cols)
		meta.Rows = uint32(rows)
		m.meta[terminalID] = meta
	}
	m.mu.Unlock()

	return nil
}

// RestartTerminal respawns a PTY for a terminal that has already exited,
// preserving the cumulative screen-buffer offset so the frontend's
// resume cursor stays valid across the restart. If no in-memory entry
// exists (e.g. after a worker restart), a new ScreenBuffer is created
// with its cumulative counter seeded from fallbackOffset so future
// end_offset broadcasts stay ahead of the client's lastOffset.
//
// The lock is intentionally released across the PTY spawn (Respawn /
// startWithScreenBuffer) and reacquired for the install. This mirrors
// StartTerminal: a slow fork must not block unrelated manager calls
// (SendInput, Resize, GetMeta, …) on the same Manager. Concurrent
// restarts of the same id are serialized one level up by the service
// handler's TerminalStartup.status + HasTerminal/!IsExited gates, so
// the second install's overwrite is a no-op-or-superseding swap rather
// than a race. The Title overlay is read inside installTerminal's
// lock so a concurrent UpdateTitle that lands during the spawn is not
// clobbered.
func (m *Manager) RestartTerminal(
	ctx context.Context,
	opts Options,
	fallbackOffset int64,
	outputFn OutputHandler,
	exitFn ExitHandler,
) error {
	m.mu.Lock()
	prev, hasPrev := m.terminals[opts.ID]
	m.mu.Unlock()

	var (
		t   *Terminal
		err error
	)
	if hasPrev {
		if !prev.IsExited() {
			return fmt.Errorf("%w: %s", ErrTerminalStillRunning, opts.ID)
		}
		// Release the pty this restart REPLACES, before the new one takes its
		// place in the map. Nothing else ever does: installTerminal overwrites
		// the entry, and RemoveTerminal only ever stops whichever terminal is
		// current by then, so every restart used to leave the old master (and
		// on Windows the old ConPTY pipes) open for the life of the Worker.
		//
		// It also ends a reader that the drain grace gave up on. Such a reader
		// still holds the screen buffer that Respawn hands to the NEW terminal,
		// so without this Stop the dead shell's last bytes could land in the
		// restarted session's screen -- at an offset the client already read.
		// Stop closes the master, which ends that parked Read at once, and
		// ScreenBuffer.WriteAndDeliver serializes whatever chunk is in flight
		// ahead of the new reader's first write.
		prev.Stop()
		t, err = prev.Respawn(ctx, opts, outputFn)
	} else {
		t, err = startWithScreenBuffer(ctx, opts, NewScreenBufferWithOffset(fallbackOffset), outputFn)
	}
	if err != nil {
		return err
	}

	m.installTerminal(opts.ID, t, exitFn, func(prev TerminalMeta) TerminalMeta {
		prev.WorkingDir = opts.WorkingDir
		prev.ShellStartDir = opts.ShellStartDir
		prev.Cols = uint32(opts.Cols)
		prev.Rows = uint32(opts.Rows)
		return prev
	})
	return nil
}

// StopTerminal stops a specific terminal's process without removing it.
func (m *Manager) StopTerminal(terminalID string) {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if ok {
		t.Stop()
	}
}

// RemoveTerminal stops and removes a terminal from the manager.
func (m *Manager) RemoveTerminal(terminalID string) {
	m.mu.Lock()
	t, ok := m.terminals[terminalID]
	if ok {
		delete(m.terminals, terminalID)
		delete(m.meta, terminalID)
		delete(m.exitDone, terminalID)
	}
	m.mu.Unlock()

	if ok {
		t.Stop()
	}
}

// HasTerminal returns true if a terminal exists (including exited ones).
func (m *Manager) HasTerminal(terminalID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.terminals[terminalID]
	return ok
}

// IsRunning returns true iff the terminal exists in the manager AND its
// PTY has not yet exited. Combines HasTerminal + !IsExited into one
// RLock so the RestartTerminal handler can decide synchronously
// whether to reject the request.
func (m *Manager) IsRunning(terminalID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.terminals[terminalID]
	return ok && !t.IsExited()
}

// WaitForExit blocks until the terminal's exit-handler goroutine has
// finished (PTY exited AND the install-time ExitHandler returned).
// Returns immediately if no in-memory entry exists. Use this in test
// teardown rather than polling IsExited — the poll only sees the PTY
// state and misses the gap before the exit handler completes, leaving
// callers' assertions racy.
func (m *Manager) WaitForExit(terminalID string) {
	m.mu.RLock()
	done, ok := m.exitDone[terminalID]
	m.mu.RUnlock()
	if !ok {
		return
	}
	<-done
}

// IsExited returns true if the terminal exists and has exited.
func (m *Manager) IsExited(terminalID string) bool {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return false
	}
	return t.IsExited()
}

// WaitForReadDrained blocks until the terminal's read goroutine drained (see
// Terminal.waitForReadDrained). Returns false if the terminal is unknown.
//
// It waits with NO limit, which is why it is a test-only entry point: every
// caller of it stops the terminal first, so the reader is already ending.
// Production waits through installTerminal, which passes the manager's grace.
func (m *Manager) WaitForReadDrained(terminalID string) bool {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return false
	}
	t.waitForReadDrained(0)
	return true
}

// UpdateTitle updates the title of a terminal in the in-memory metadata.
//
// THE CLEAN LIVES HERE, not at each caller, so no path can put a control
// character or a bidirectional override into a tab label. The rename RPC
// cleaned its own title and the post-spawn absorb did not, and the two writers
// are not the whole story: the manager's title is what ListTerminals reports
// while a terminal is live, so any future writer inherits the same duty.
// validate.CleanName is idempotent, so a caller that cleans first -- the rename
// RPC must, because it answers differently when the title cleans to nothing --
// passes its value through unchanged.
//
// A title that cleans to nothing LEAVES THE STORED ONE ALONE, the same answer
// the rename RPC gives: writing "" would leave the tab with no name at all.
// Reports whether the metadata now holds the cleaned title.
func (m *Manager) UpdateTitle(terminalID, title string) bool {
	clean := validate.CleanName(title)
	if clean == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, ok := m.meta[terminalID]
	if !ok {
		return false
	}
	meta.Title = clean
	m.meta[terminalID] = meta
	return true
}

// ScreenSnapshotSince returns the bytes a subscriber needs to advance
// from afterOffset to the current head of the terminal's screen buffer,
// for the watch-event catch-up path. Returns (nil, 0, false) if the
// terminal is unknown. See Terminal.ScreenSnapshotSince for the
// isSnapshot contract.
func (m *Manager) ScreenSnapshotSince(terminalID string, afterOffset int64) (data []byte, endOffset int64, isSnapshot bool) {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return nil, 0, false
	}
	return t.ScreenSnapshotSince(afterOffset)
}

// ScreenHasSuffix reports whether the live terminal's retained screen
// ends with needle. Returns false if the terminal is unknown.
func (m *Manager) ScreenHasSuffix(terminalID string, needle []byte) bool {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	return t.ScreenHasSuffix(needle)
}

// AppendOutput injects synthetic output into the tracked terminal's screen
// buffer and output stream.
func (m *Manager) AppendOutput(terminalID string, data []byte) bool {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return false
	}
	t.AppendOutput(data)
	return true
}

// SnapshotTerminal returns a point-in-time copy of a single terminal's
// metadata and screen buffer, or ok=false if the terminal doesn't exist
// or has no screen data.
func (m *Manager) SnapshotTerminal(terminalID string) (snap TerminalSnapshot, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	t, exists := m.terminals[terminalID]
	if !exists {
		return TerminalSnapshot{}, false
	}
	screen, _ := t.ScreenSnapshot()
	if len(screen) == 0 {
		return TerminalSnapshot{}, false
	}
	meta, hasMeta := m.meta[terminalID]
	if !hasMeta {
		return TerminalSnapshot{}, false
	}
	return TerminalSnapshot{
		TerminalMeta: meta,
		Screen:       screen,
	}, true
}

// GetMeta returns the metadata for a terminal, or ok=false if not found.
func (m *Manager) GetMeta(terminalID string) (meta TerminalMeta, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	meta, ok = m.meta[terminalID]
	return
}

// ListTerminalIDs returns the IDs of all currently tracked terminals.
func (m *Manager) ListTerminalIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.terminals))
	for id := range m.terminals {
		ids = append(ids, id)
	}
	return ids
}

// TerminalEntry holds the ID, metadata, screen data and exit state for a terminal.
type TerminalEntry struct {
	ID     string
	Meta   TerminalMeta
	Screen []byte
	// ScreenEndOffset is the cumulative PTY byte offset at the end of
	// Screen. It counts PTY bytes only, and Screen leads with the mode
	// tracker's synthesized restore prefix (see ScreenBuffer.Snapshot).
	// Before the ring wraps, len(Screen) is therefore larger than this
	// offset by the length of that prefix; after the ring wraps, this
	// offset is the larger of the two because old bytes fell out of
	// Screen. Subscribers use this to seed their WatchEvents
	// after_offset so resubscribes pick up exactly where the snapshot
	// left off instead of replaying Screen.
	ScreenEndOffset int64
	Exited          bool
}

// buildEntryLocked assembles a TerminalEntry for id, attaching the live
// screen snapshot and offset when a PTY is present. Caller must hold
// m.mu (read or write).
func (m *Manager) buildEntryLocked(id string, meta TerminalMeta) TerminalEntry {
	entry := TerminalEntry{ID: id, Meta: meta}
	if t, ok := m.terminals[id]; ok {
		entry.Screen, entry.ScreenEndOffset = t.ScreenSnapshot()
		entry.Exited = t.IsExited()
	} else {
		entry.Exited = true
	}
	return entry
}

// ListByIDs returns terminals matching the given IDs.
func (m *Manager) ListByIDs(ids []string) []TerminalEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []TerminalEntry
	for _, id := range ids {
		meta, ok := m.meta[id]
		if !ok {
			continue
		}
		result = append(result, m.buildEntryLocked(id, meta))
	}
	return result
}

// StopAll stops all terminals and clears the map.
func (m *Manager) StopAll() {
	m.mu.Lock()
	terminals := make([]*Terminal, 0, len(m.terminals))
	for _, t := range m.terminals {
		terminals = append(terminals, t)
	}
	m.terminals = make(map[string]*Terminal)
	m.meta = make(map[string]TerminalMeta)
	m.exitDone = make(map[string]chan struct{})
	m.mu.Unlock()

	for _, t := range terminals {
		t.Stop()
	}
}
