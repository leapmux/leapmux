package terminal

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Manager tracks active terminal sessions.
type Manager struct {
	mu        sync.RWMutex
	terminals map[string]*Terminal // terminalID -> Terminal
}

// NewManager creates a new terminal Manager.
func NewManager() *Manager {
	return &Manager{
		terminals: make(map[string]*Terminal),
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

	return t.Resize(cols, rows)
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

// StopAll stops all terminals and clears the map.
func (m *Manager) StopAll() {
	m.mu.Lock()
	terminals := make([]*Terminal, 0, len(m.terminals))
	for _, t := range m.terminals {
		terminals = append(terminals, t)
	}
	m.terminals = make(map[string]*Terminal)
	m.mu.Unlock()

	for _, t := range terminals {
		t.Stop()
	}
}

// savedScreenMeta is the on-disk JSON format for screen.json (cols/rows only).
type savedScreenMeta struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

// SavedTerminal holds a persisted terminal's metadata and screen buffer.
type SavedTerminal struct {
	WorkspaceID string
	Cols        uint32
	Rows        uint32
	Screen      []byte
}

// SaveScreens writes each terminal's screen buffer to
// {dataDir}/workspaces/{workspaceID}/terminals/{terminalID}/screen.buffer.
// The getMeta function provides the workspace ID for each terminal ID.
func (m *Manager) SaveScreens(dataDir string, getMeta func(terminalID string) (workspaceID string, cols, rows uint32)) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for id, t := range m.terminals {
		screen := t.ScreenSnapshot()
		if len(screen) == 0 {
			continue
		}
		wsID, _, _ := getMeta(id)
		if wsID == "" {
			continue
		}
		dir := filepath.Join(dataDir, "workspaces", wsID, "terminals", id)
		if err := os.MkdirAll(dir, 0755); err != nil {
			slog.Error("failed to create terminal dir", "terminal_id", id, "error", err)
			continue
		}
		path := filepath.Join(dir, "screen.buffer")
		if err := os.WriteFile(path, screen, 0644); err != nil {
			slog.Error("failed to save terminal screen", "terminal_id", id, "error", err)
		}
	}

	return nil
}

// SaveTerminalMeta writes per-terminal metadata as JSON to
// {dataDir}/workspaces/{workspaceID}/terminals/{terminalID}/screen.json.
// The getMeta function provides workspace/cols/rows for each terminal ID.
func (m *Manager) SaveTerminalMeta(dataDir string, getMeta func(terminalID string) (workspaceID string, cols, rows uint32)) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for id := range m.terminals {
		wsID, cols, rows := getMeta(id)
		if wsID == "" {
			continue
		}
		dir := filepath.Join(dataDir, "workspaces", wsID, "terminals", id)
		if err := os.MkdirAll(dir, 0755); err != nil {
			slog.Error("failed to create terminal dir", "terminal_id", id, "error", err)
			continue
		}
		data, err := json.Marshal(savedScreenMeta{Cols: cols, Rows: rows})
		if err != nil {
			slog.Error("failed to marshal terminal meta", "terminal_id", id, "error", err)
			continue
		}
		path := filepath.Join(dir, "screen.json")
		if err := os.WriteFile(path, data, 0644); err != nil {
			slog.Error("failed to save terminal meta", "terminal_id", id, "error", err)
		}
	}

	return nil
}

// LoadSavedTerminals reads saved terminal metadata and screen buffers from disk.
// It walks {dataDir}/workspaces/*/terminals/* directories, reading screen.json
// and screen.buffer from each. It also cleans up the legacy {dataDir}/screens/
// directory if present.
func LoadSavedTerminals(dataDir string) (map[string]SavedTerminal, error) {
	result := make(map[string]SavedTerminal)

	// Clean up legacy screens/ directory if it exists.
	legacyDir := filepath.Join(dataDir, "screens")
	_ = os.RemoveAll(legacyDir)

	workspacesDir := filepath.Join(dataDir, "workspaces")
	wsEntries, err := os.ReadDir(workspacesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read workspaces dir: %w", err)
	}

	for _, wsEntry := range wsEntries {
		if !wsEntry.IsDir() {
			continue
		}
		wsID := wsEntry.Name()
		terminalsDir := filepath.Join(workspacesDir, wsID, "terminals")
		termEntries, err := os.ReadDir(terminalsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			slog.Warn("failed to read terminals dir", "workspace_id", wsID, "error", err)
			continue
		}

		for _, termEntry := range termEntries {
			if !termEntry.IsDir() {
				continue
			}
			termID := termEntry.Name()
			termDir := filepath.Join(terminalsDir, termID)

			// Read screen.json metadata.
			metaPath := filepath.Join(termDir, "screen.json")
			metaBytes, err := os.ReadFile(metaPath)
			if err != nil {
				slog.Warn("failed to read terminal screen meta", "terminal_id", termID, "error", err)
				continue
			}
			var meta savedScreenMeta
			if err := json.Unmarshal(metaBytes, &meta); err != nil {
				slog.Warn("failed to unmarshal terminal screen meta", "terminal_id", termID, "error", err)
				continue
			}

			// Read screen.buffer.
			bufferPath := filepath.Join(termDir, "screen.buffer")
			screen, err := os.ReadFile(bufferPath)
			if err != nil {
				slog.Warn("failed to read terminal screen buffer", "terminal_id", termID, "error", err)
				screen = nil
			}

			result[termID] = SavedTerminal{
				WorkspaceID: wsID,
				Cols:        meta.Cols,
				Rows:        meta.Rows,
				Screen:      screen,
			}
		}

		// Clean up terminals dir after loading.
		_ = os.RemoveAll(terminalsDir)
	}

	return result, nil
}
