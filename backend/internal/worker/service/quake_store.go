package service

import (
	"slices"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqltime"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// QuakeTerminal holds the in-memory metadata for an active quake terminal.
// Quake terminals live strictly in memory for the current Worker process and
// never persist to SQLite.
type QuakeTerminal struct {
	ID            string
	WorkingDir    string
	ShellStartDir string
	Shell         string
	Title         string
	Cols          int64
	Rows          int64
	StartupError  string
	CreatedAt     time.Time
}

func (q QuakeTerminal) toDBTerminal() db.Terminal {
	return db.Terminal{
		ID:            q.ID,
		WorkingDir:    q.WorkingDir,
		ShellStartDir: q.ShellStartDir,
		Shell:         q.Shell,
		Title:         q.Title,
		Cols:          q.Cols,
		Rows:          q.Rows,
		Screen:        []byte{},
		StartupError:  q.StartupError,
		CreatedAt:     sqltime.NewSQLiteTime(q.CreatedAt),
	}
}

// QuakeTerminalStore tracks open quake terminals in memory, enforcing at most
// one active quake terminal per working directory.
type QuakeTerminalStore struct {
	mu    sync.Mutex
	byID  map[string]*QuakeTerminal
	byDir map[string]*QuakeTerminal

	// Package-internal test hooks for deterministic concurrent-open tests.
	afterRegister  func(id string)
	hideLookupOnce func(workingDir string) bool
}

// NewQuakeTerminalStore constructs an empty in-memory QuakeTerminalStore.
func NewQuakeTerminalStore() *QuakeTerminalStore {
	return &QuakeTerminalStore{
		byID:  make(map[string]*QuakeTerminal),
		byDir: make(map[string]*QuakeTerminal),
	}
}

// RegisterOrAdopt inserts q when its working directory has no active quake
// terminal and returns (q, true). When another quake terminal already owns
// q.WorkingDir, it returns a copy of the existing entry and created=false.
func (s *QuakeTerminalStore) RegisterOrAdopt(q QuakeTerminal) (QuakeTerminal, bool) {
	s.mu.Lock()
	if existing, ok := s.byDir[q.WorkingDir]; ok {
		adopted := *existing
		s.mu.Unlock()
		return adopted, false
	}
	if q.CreatedAt.IsZero() {
		q.CreatedAt = nowMillis()
	}
	stored := new(QuakeTerminal)
	*stored = q
	s.byID[q.ID] = stored
	s.byDir[q.WorkingDir] = stored
	afterHook := s.afterRegister
	s.mu.Unlock()
	if afterHook != nil {
		afterHook(q.ID)
	}
	return q, true
}

// Get returns a copy of the quake terminal for id.
func (s *QuakeTerminalStore) Get(id string) (QuakeTerminal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.byID[id]
	if !ok {
		return QuakeTerminal{}, false
	}
	return *q, true
}

// GetByWorkingDir returns a copy of the quake terminal registered for dir.
func (s *QuakeTerminalStore) GetByWorkingDir(dir string) (QuakeTerminal, bool) {
	s.mu.Lock()
	if s.hideLookupOnce != nil && s.hideLookupOnce(dir) {
		s.hideLookupOnce = nil
		s.mu.Unlock()
		return QuakeTerminal{}, false
	}
	q, ok := s.byDir[dir]
	if !ok {
		s.mu.Unlock()
		return QuakeTerminal{}, false
	}
	copied := *q
	s.mu.Unlock()
	return copied, true
}

// Has reports whether id is an active quake terminal.
func (s *QuakeTerminalStore) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byID[id]
	return ok
}

// ListOpen returns copies of every active quake terminal.
func (s *QuakeTerminalStore) ListOpen() []QuakeTerminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.byID) == 0 {
		return nil
	}
	out := make([]QuakeTerminal, 0, len(s.byID))
	for _, q := range s.byID {
		out = append(out, *q)
	}
	slices.SortFunc(out, func(a, b QuakeTerminal) int {
		if cmp := a.CreatedAt.Compare(b.CreatedAt); cmp != 0 {
			return cmp
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return out
}

// ListByWorkingDirs returns copies of active quake terminals whose working
// directory is in dirs.
func (s *QuakeTerminalStore) ListByWorkingDirs(dirs []string) []QuakeTerminal {
	if len(dirs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(dirs))
	var out []QuakeTerminal
	for _, dir := range dirs {
		if _, dup := seen[dir]; dup {
			continue
		}
		seen[dir] = struct{}{}
		if q, ok := s.byDir[dir]; ok {
			out = append(out, *q)
		}
	}
	return out
}

// UpdateTitle updates the stored title for an active quake terminal.
func (s *QuakeTerminalStore) UpdateTitle(id, title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.byID[id]
	if !ok {
		return false
	}
	q.Title = title
	return true
}

// SetStartupError sets or clears the startup error for an active quake terminal.
func (s *QuakeTerminalStore) SetStartupError(id, errMsg string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.byID[id]
	if !ok {
		return false
	}
	q.StartupError = errMsg
	return true
}

// Delete removes id from the store.
func (s *QuakeTerminalStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.byID[id]
	if !ok {
		return
	}
	delete(s.byID, id)
	if s.byDir[q.WorkingDir] == q {
		delete(s.byDir, q.WorkingDir)
	}
}
