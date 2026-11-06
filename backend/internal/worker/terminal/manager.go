package terminal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/util/drain"
	"github.com/leapmux/leapmux/util/validate"
)

// TerminalProcesses is what one terminal is running. Total exceeds
// len(Processes) when the cap dropped some.
type TerminalProcesses struct {
	TerminalID string
	Processes  []ProcessInfo
	Total      int
}

// DescendantProcesses reports the processes running beneath each terminal's
// login shell, excluding the shell itself and the shell's own bookkeeping (see
// reportsAsWork). The result holds one entry per terminal that is running
// something, in the order asked.
//
// ONE process-table scan for the whole batch. The scan is the expensive part,
// and closing a tile asks about every terminal it holds at once; scanning per
// terminal cost a full pass per tab and could also disagree with itself, since
// two passes taken milliseconds apart can show a process under one tab and not
// the next.
//
// A terminal that is running nothing is OMITTED, and so is an id the manager
// does not hold. The two are the same answer to a close guard, and the second
// is routine rather than exceptional: the DB row a caller resolves exists for
// the whole async-startup window before the PTY does, and after a worker
// restart. A close-confirmation dialog must not fail because the PTY is 200ms
// from existing.
//
// The lock is RELEASED before the scan, mirroring ScreenSnapshotSince rather
// than SnapshotTerminal: a process-table scan is milliseconds of syscalls, and
// holding m.mu across it would freeze SendInput and Resize for every OTHER tab.
//
// IsExited brackets the scan on BOTH sides. The OS hands a reaped pid to the
// next fork, so a walk that starts -- or finishes -- after a shell exited can
// enumerate a STRANGER's children under that tab's name. The trailing check is
// the one that matters; the leading one only skips work for a shell already
// known to be gone.
func (m *Manager) DescendantProcesses(ctx context.Context, terminalIDs []string) ([]TerminalProcesses, error) {
	if len(terminalIDs) == 0 {
		return nil, nil
	}
	type target struct {
		id string
		t  *Terminal
	}
	// IsExited is a lock-free channel read, so the leading check costs nothing
	// to make here and saves the scan entirely when every id is already gone.
	//
	// Deduplicated: the answer for one terminal is the same however many times
	// the request names it, and each repeat costs a full walk of the process
	// table. The reply is keyed by terminal id, so a duplicate could only
	// produce a duplicate row.
	m.mu.RLock()
	targets := make([]target, 0, len(terminalIDs))
	seen := make(map[string]struct{}, len(terminalIDs))
	for _, id := range terminalIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if t, ok := m.currentTerminalLocked(id); ok && !t.IsExited() {
			targets = append(targets, target{id: id, t: t})
		}
	}
	m.mu.RUnlock()
	if len(targets) == 0 {
		return nil, nil
	}

	scan, err := newProcessScan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TerminalProcesses, 0, len(targets))
	for _, tg := range targets {
		// The walk itself takes no context, so the caller's deadline reaches it
		// only here. Without this check the scan's own timeout bounds the table
		// read alone, and a wide request keeps walking long after the answer can
		// still be delivered.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		procs, total := scan.descendantsOf(tg.t.ShellPID(), maxReportedProcesses)
		if len(procs) == 0 || tg.t.IsExited() {
			continue
		}
		out = append(out, TerminalProcesses{TerminalID: tg.id, Processes: procs, Total: total})
	}
	return out, nil
}

// ErrTerminalNotFound is returned when a terminal operation targets an ID
// the Manager does not know about. Callers distinguish this from other
// failures with errors.Is so they can decide whether to retry or stash —
// e.g. the ResizeTerminal handler stashes dims for a terminal whose PTY
// is still being spawned in the background startup goroutine.
var ErrTerminalNotFound = errors.New("terminal not found")

// ErrTerminalInstanceChanged reports a resize whose original terminal was
// removed or replaced before its metadata could commit.
var ErrTerminalInstanceChanged = errors.New("terminal closed or restarted during resize")

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
// woke, because waitForReadDrainedWithin starts it only after the child side of the
// pty is closed -- the act that wakes that reader. Measured from the exit
// instead, this grace would also have to absorb the Windows console-host
// flush, which happens between the two.
const defaultReadDrainGrace = 2 * time.Second

// terminalRegistration owns one physical incarnation and its metadata. It stays
// in draining until its exit handler returns, even after removal or replacement.
type terminalRegistration struct {
	id       string
	terminal *Terminal
	meta     TerminalMeta
	done     chan struct{}
}

// Manager tracks current terminals and every unfinished exit handler.
type Manager struct {
	mu       sync.RWMutex
	current  map[string]*terminalRegistration
	draining map[*terminalRegistration]struct{}
	// clock is the one this Manager stamps on every Terminal it spawns, which
	// makes "an installed terminal runs on the Manager's clock" hold by
	// construction. The exit goroutine's two drain timers use it, so
	// a test drives the give-up path on this clock and spends no real time.
	// NewManager installs the real clock; WithClock replaces it.
	clock quartz.Clock
}

// ManagerOption changes one NewManager setting.
type ManagerOption func(*Manager)

// WithClock sets the clock that supplies reader-drain timers.
func WithClock(clock quartz.Clock) ManagerOption {
	if clock == nil {
		panic("terminal: clock must not be nil")
	}
	return func(m *Manager) {
		m.clock = clock
	}
}

// NewManager creates a new terminal Manager.
func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		current:  make(map[string]*terminalRegistration),
		draining: make(map[*terminalRegistration]struct{}),
		clock:    quartz.NewReal(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// currentTerminalLocked resolves the physical terminal of a current registration.
// The caller holds m.mu for reading or writing.
func (m *Manager) currentTerminalLocked(id string) (*Terminal, bool) {
	registration, ok := m.current[id]
	if !ok {
		return nil, false
	}
	return registration.terminal, true
}

// ExitHandler is called when a terminal process exits.
type ExitHandler func(terminalID string, exitCode int)

// StartTerminal creates a new PTY terminal. The supplied context
// governs only the spawn — once StartTerminal returns successfully,
// the terminal's lifetime is managed by RemoveTerminal / Stop.
// Cancelling ctx mid-spawn aborts the PTY fork (returning ctx.Err())
// so a CloseTab that lands during the sync-path phase of
// runTerminalStartup tears the nascent child down instead of leaking it.
func (m *Manager) StartTerminal(ctx context.Context, opts Options, outputFn OutputHandler, exitFn ExitHandler) error {
	m.mu.Lock()
	if _, exists := m.current[opts.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("terminal already exists: %s", opts.ID)
	}
	m.mu.Unlock()

	t, err := Start(ctx, opts, m.clock, outputFn)
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
// RemoveTerminal (explicit close). The freshly allocated registration.done channel
// is closed once the exit handler returned, so WaitForExit callers
// observe a definitive "the exit goroutine is done" signal.
func (m *Manager) installTerminal(id string, t *Terminal, exitFn ExitHandler, composeMeta func(prev TerminalMeta) TerminalMeta) {
	m.mu.Lock()
	var previous TerminalMeta
	if current := m.current[id]; current != nil {
		previous = current.meta
	}
	registration := &terminalRegistration{id: id, terminal: t, meta: composeMeta(previous), done: make(chan struct{})}
	m.current[id] = registration
	m.draining[registration] = struct{}{}
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
		// Terminal.waitForReadDrainedWithin, which starts it only once that
		// close finished. A shell that leaves no such holder behind never
		// reaches it.
		if !t.waitForReadDrainedWithin(defaultReadDrainGrace) {
			slog.Warn("terminal reader did not drain after the shell exited; "+
				"persisting the screen without its last output",
				"terminal_id", id,
				"grace", defaultReadDrainGrace,
			)
		}
		if exitFn != nil {
			exitFn(id, exitCode)
		}
		close(registration.done)
		m.mu.Lock()
		delete(m.draining, registration)
		m.mu.Unlock()
	}()
}

// SendInput routes input to a terminal.
func (m *Manager) SendInput(terminalID string, data []byte) error {
	m.mu.RLock()
	t, ok := m.currentTerminalLocked(terminalID)
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
	registration, ok := m.current[terminalID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrTerminalNotFound, terminalID)
	}
	t := registration.terminal
	// Keep physical size and metadata in one per-instance operation. Read the
	// dimensions after admission so a queued duplicate sees the preceding commit.
	t.managedResizeMu.Lock()
	defer t.managedResizeMu.Unlock()
	m.mu.RLock()
	current := m.current[terminalID]
	meta := registration.meta
	m.mu.RUnlock()
	if current != registration {
		return fmt.Errorf("%w: %s", ErrTerminalInstanceChanged, terminalID)
	}
	if t.IsExited() {
		return fmt.Errorf("terminal exited: %s", terminalID)
	}
	// Skip if dimensions have not changed to avoid a spurious SIGWINCH
	// that causes shells (e.g. zsh with starship) to redraw the prompt,
	// leaving the old prompt visible on screen.
	if meta.Cols == uint32(cols) && meta.Rows == uint32(rows) {
		return nil
	}
	resizeErr := t.Resize(cols, rows)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current[terminalID] != registration {
		return fmt.Errorf("%w: %s", ErrTerminalInstanceChanged, terminalID)
	}
	if resizeErr != nil {
		return resizeErr
	}
	registration.meta.Cols = uint32(cols)
	registration.meta.Rows = uint32(rows)
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
	prev, hasPrev := m.currentTerminalLocked(opts.ID)
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
		t, err = startWithScreenBuffer(ctx, opts, m.clock, NewScreenBufferWithOffset(fallbackOffset), outputFn)
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
	t, ok := m.currentTerminalLocked(terminalID)
	m.mu.RUnlock()

	if ok {
		t.Stop()
	}
}

// RemoveTerminal stops and removes a terminal from the manager.
func (m *Manager) RemoveTerminal(terminalID string) {
	m.mu.Lock()
	registration, ok := m.current[terminalID]
	delete(m.current, terminalID)
	m.mu.Unlock()
	if ok {
		registration.terminal.Stop()
	}
}

// HasTerminal returns true if a terminal exists (including exited ones).
func (m *Manager) HasTerminal(terminalID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.current[terminalID]
	return ok
}

// IsRunning returns true iff the terminal exists in the manager AND its
// PTY has not yet exited. Combines HasTerminal + !IsExited into one
// RLock so the RestartTerminal handler can decide synchronously
// whether to reject the request.
func (m *Manager) IsRunning(terminalID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.currentTerminalLocked(terminalID)
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
	registration, ok := m.current[terminalID]
	m.mu.RUnlock()
	if ok {
		<-registration.done
	}
}

// ExitHandlerFinished reports completion of the current instance's reader drain
// and exit handler. An absent registration has no handler to wait for.
func (m *Manager) ExitHandlerFinished(terminalID string) bool {
	m.mu.RLock()
	registration, ok := m.current[terminalID]
	m.mu.RUnlock()
	if !ok {
		return true
	}
	select {
	case <-registration.done:
		return true
	default:
		return false
	}
}

// IsExited returns true if the terminal exists and has exited.
func (m *Manager) IsExited(terminalID string) bool {
	m.mu.RLock()
	t, ok := m.currentTerminalLocked(terminalID)
	m.mu.RUnlock()

	if !ok {
		return false
	}
	return t.IsExited()
}

// HangUpRequested reports whether the current terminal received a hang-up
// request before its exit was observed.
func (m *Manager) HangUpRequested(terminalID string) bool {
	m.mu.RLock()
	t, ok := m.currentTerminalLocked(terminalID)
	m.mu.RUnlock()
	return ok && t.HangUpRequested()
}

// WaitForReadDrained blocks until the terminal's read goroutine drained (see
// Terminal.waitForReadDone). Returns false if the terminal is unknown.
//
// It waits with NO limit, which is why it is a test-only entry point: every
// caller of it stops the terminal first, so the reader is already ending.
// Production waits through installTerminal, which passes defaultReadDrainGrace.
func (m *Manager) WaitForReadDrained(terminalID string) bool {
	m.mu.RLock()
	t, ok := m.currentTerminalLocked(terminalID)
	m.mu.RUnlock()

	if !ok {
		return false
	}
	t.waitForReadDone()
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
	registration, ok := m.current[terminalID]
	if !ok {
		return false
	}
	registration.meta.Title = clean
	return true
}

// ScreenSnapshotSince returns the bytes a subscriber needs to advance
// from afterOffset to the current head of the terminal's screen buffer,
// for the watch-event catch-up path. Returns (nil, 0, false) if the
// terminal is unknown. See Terminal.ScreenSnapshotSince for the
// isSnapshot contract.
func (m *Manager) ScreenSnapshotSince(terminalID string, afterOffset int64) (data []byte, endOffset int64, isSnapshot bool) {
	m.mu.RLock()
	t, ok := m.currentTerminalLocked(terminalID)
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
	t, ok := m.currentTerminalLocked(terminalID)
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
	t, ok := m.currentTerminalLocked(terminalID)
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
	registration, exists := m.current[terminalID]
	if !exists {
		return TerminalSnapshot{}, false
	}
	screen, _ := registration.terminal.ScreenSnapshot()
	if len(screen) == 0 {
		return TerminalSnapshot{}, false
	}
	return TerminalSnapshot{TerminalMeta: registration.meta, Screen: screen}, true
}

// GetMeta returns the current registration's metadata, or false when absent.
func (m *Manager) GetMeta(terminalID string) (TerminalMeta, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	registration, ok := m.current[terminalID]
	if !ok {
		return TerminalMeta{}, false
	}
	return registration.meta, true
}

// ListTerminalIDs returns the IDs of all currently tracked terminals.
func (m *Manager) ListTerminalIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.current))
	for id := range m.current {
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

// buildEntryLocked snapshots one coherent current registration. The caller
// holds m.mu for reading or writing.
func (m *Manager) buildEntryLocked(registration *terminalRegistration) TerminalEntry {
	screen, offset := registration.terminal.ScreenSnapshot()
	return TerminalEntry{ID: registration.id, Meta: registration.meta, Screen: screen,
		ScreenEndOffset: offset, Exited: registration.terminal.IsExited()}
}

// ListByIDs returns terminals matching the given IDs.
func (m *Manager) ListByIDs(ids []string) []TerminalEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []TerminalEntry
	for _, id := range ids {
		if registration, ok := m.current[id]; ok {
			result = append(result, m.buildEntryLocked(registration))
		}
	}
	return result
}

// hangUpJoinGrace is the extra budget the whole HangUpAll set gets on top of
// the per-shell grace, so the joins of the exit goroutines can outlast it.
const hangUpJoinGrace = 5 * time.Second

// HangUpAll hangs up every terminal, escalates to Stop for one that is still
// alive after grace, and joins every exit goroutine before it returns.
//
// It is the orderly-shutdown sweep for the terminals that outlive
// Service.Shutdown. The join is what makes it a shutdown primitive rather
// than a stop: an exit goroutine whose handler persists the screen would
// otherwise run past the caller's database close. The current registration
// cannot serve that join: removal and restart replace it. The draining set retains each old
// registration until its own handler finishes.
//
// The sweep snapshots both maps under the read lock and keeps them: a
// terminal row survives the sweep. Service.Shutdown records early notices,
// and the exit handler combines that record with the actual hang-up request
// to preserve natural exits and the final screen.
//
// A grace of zero or less escalates at once. The escalation follows t.exitCh,
// the shell's own exit, and never registration.done: that completion also waits for the
// reader, which can spend defaultReadDrainGrace twice, so a wait on it could
// reach Stop for a shell that already exited, and Stop kills the nohup jobs
// the hang-up leaves alive.
//
// It returns the ids whose join gave up at the deadline, or nil.
func (m *Manager) HangUpAll(grace time.Duration) []string {
	m.mu.RLock()
	terminals := make([]*Terminal, 0, len(m.current))
	for _, registration := range m.current {
		terminals = append(terminals, registration.terminal)
	}
	exits := make([]*terminalRegistration, 0, len(m.draining))
	for registration := range m.draining {
		exits = append(exits, registration)
	}
	m.mu.RUnlock()

	// pending counts, per id, the goroutines whose completion the join is
	// waiting for: one for the shell sweep, one per exit goroutine. A count
	// above zero at the deadline is an id the sweep abandoned.
	var pendingMu sync.Mutex
	pending := make(map[string]int, len(terminals)+len(exits))

	var drain drain.Counter
	beginPending := func(id string) func() {
		pendingMu.Lock()
		pending[id]++
		pendingMu.Unlock()
		drain.Add()
		return func() {
			pendingMu.Lock()
			pending[id]--
			pendingMu.Unlock()
			drain.Done()
		}
	}
	for _, t := range terminals {
		finish := beginPending(t.ID())
		go func(t *Terminal) {
			defer finish()
			// The grace starts before any PTY close can block. A mock clock
			// must advance it before a stubborn shell can reach escalation.
			timer := m.clock.NewTimer(max(grace, 0), terminalHangUpTimerTag)
			defer timer.Stop(terminalHangUpTimerTag)
			hungUp := t.beginHangUp()
			if grace <= 0 {
				t.Stop()
				<-hungUp
				return
			}
			select {
			case <-t.exitCh:
			case <-timer.C:
				if !t.IsExited() {
					t.Stop()
				}
			}
			<-hungUp
		}(t)
	}
	for _, exit := range exits {
		finish := beginPending(exit.id)
		go func(done chan struct{}) {
			defer finish()
			<-done
		}(exit.done)
	}

	// Keep the join allowance for immediate escalation, and cap the sum at
	// the largest duration so a very large grace cannot overflow it.
	const maxDuration = time.Duration(1<<63 - 1)
	joinTimeout := min(max(grace, 0), maxDuration-hangUpJoinGrace) + hangUpJoinGrace
	if drain.Wait(joinTimeout, "") {
		return nil
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	ids := make([]string, 0, len(pending))
	for id, count := range pending {
		if count > 0 {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}
