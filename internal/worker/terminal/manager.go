package terminal

import (
	"fmt"
	"log/slog"
	"sync"
)

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

// StartTerminal creates a new PTY terminal.
func (m *Manager) StartTerminal(opts Options, outputFn OutputHandler, exitFn ExitHandler) error {
	m.mu.Lock()
	if _, exists := m.terminals[opts.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("terminal already exists: %s", opts.ID)
	}
	m.mu.Unlock()

	t, err := Start(opts, outputFn)
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

		slog.Info("terminal exited (kept in map)",
			"terminal_id", opts.ID,
			"exit_code", exitCode,
		)

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
		return fmt.Errorf("no terminal: %s", terminalID)
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
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no terminal: %s", terminalID)
	}
	if t.IsExited() {
		return fmt.Errorf("terminal exited: %s", terminalID)
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

// ScreenSnapshot returns the screen buffer snapshot for a terminal.
func (m *Manager) ScreenSnapshot(terminalID string) []byte {
	m.mu.RLock()
	t, ok := m.terminals[terminalID]
	m.mu.RUnlock()

	if !ok {
		return nil
	}
	return t.ScreenSnapshot()
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
	screen := t.ScreenSnapshot()
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
	Exited bool
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
		entry := TerminalEntry{
			ID:   id,
			Meta: meta,
		}
		if t, ok := m.terminals[id]; ok {
			entry.Screen = t.ScreenSnapshot()
			entry.Exited = t.IsExited()
		} else {
			entry.Exited = true
		}
		result = append(result, entry)
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
