package terminal

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrTerminalNotFound is returned when a terminal operation targets an ID
// the Manager does not know about. Callers distinguish this from other
// failures with errors.Is so they can decide whether to retry or stash —
// e.g. the ResizeTerminal handler stashes dims for a terminal whose PTY
// is still being spawned in the background startup goroutine.
var ErrTerminalNotFound = errors.New("terminal not found")

// TerminalMeta holds the workspace ID and dimensions for a terminal.
type TerminalMeta struct {
	WorkspaceID   string
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

// Manager tracks active terminal sessions.
type Manager struct {
	mu        sync.RWMutex
	terminals map[string]*Terminal    // terminalID -> Terminal
	meta      map[string]TerminalMeta // terminalID -> metadata
}

// NewManager creates a new terminal Manager.
func NewManager() *Manager {
	return &Manager{
		terminals: make(map[string]*Terminal),
		meta:      make(map[string]TerminalMeta),
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

	m.mu.Lock()
	m.terminals[opts.ID] = t
	m.meta[opts.ID] = TerminalMeta{
		WorkspaceID:   opts.WorkspaceID,
		WorkingDir:    opts.WorkingDir,
		ShellStartDir: opts.ShellStartDir,
		Cols:          uint32(opts.Cols),
		Rows:          uint32(opts.Rows),
	}
	m.mu.Unlock()

	// Notify when the terminal exits but keep it in the map
	// so that ScreenSnapshot and ListTerminals still work.
	// The entry is removed by RemoveTerminal (explicit close).
	go func() {
		exitCode := t.Wait()

		if exitFn != nil {
			exitFn(opts.ID, exitCode)
		}
	}()

	return nil
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

// WaitForReadDrained blocks until the terminal's read goroutine has
// drained (see Terminal.WaitForReadDrained). Returns false if the
// terminal is unknown.
func (m *Manager) WaitForReadDrained(terminalID string) bool {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return false
	}
	t.WaitForReadDrained()
	return true
}

// UpdateTitle updates the title of a terminal in the in-memory metadata.
func (m *Manager) UpdateTitle(terminalID, title string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, ok := m.meta[terminalID]
	if !ok {
		return false
	}
	meta.Title = title
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
	if !hasMeta || meta.WorkspaceID == "" {
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
	// Screen. Equal to len(Screen) before the ring wraps and strictly
	// greater once old bytes have fallen off. Subscribers use this to
	// seed their WatchEvents after_offset so resubscribes pick up
	// exactly where the snapshot left off instead of replaying Screen.
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

// ListByWorkspace returns all terminals belonging to the given workspace.
func (m *Manager) ListByWorkspace(workspaceID string) []TerminalEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []TerminalEntry
	for id, meta := range m.meta {
		if meta.WorkspaceID != workspaceID {
			continue
		}
		result = append(result, m.buildEntryLocked(id, meta))
	}
	return result
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
	m.mu.Unlock()

	for _, t := range terminals {
		t.Stop()
	}
}
