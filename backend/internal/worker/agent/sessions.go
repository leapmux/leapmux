package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// This file defines provider-neutral session discovery. It supplies the record and query types.
// It also supplies the shared display order. Readers live in providers/internal/sessionstore.
// Each Provider.ListStoredSessions implementation selects its own store.

// DefaultStoredSessionLimit sets the default result limit. Readers order candidates before
// opening their files. They preserve the complete boundary timestamp group, which can exceed the
// nominal read limit.
const DefaultStoredSessionLimit = 50

// StoredSession is one resumable session that a provider's own storage holds.
type StoredSession struct {
	// Handle uses the same resume identifier that UpdateSessionID reports at runtime.
	// This equality lets the caller compare it directly with agents.agent_session_id without
	// provider-specific conversion. Pi accepts either a session ID or a file path. Its reader must
	// return the ID because the running process reports that form.
	Handle string
	// Title is a human-readable summary, or empty when the provider stores
	// none. The caller shows the handle in its place rather than inventing one.
	Title string
	// UpdatedAt is the last activity. The zero value means the store gave no
	// answer, and sorts last.
	UpdatedAt time.Time
}

// StoredSessionQuery states which sessions a reader must collect.
type StoredSessionQuery struct {
	// WorkingDir identifies the exact directory that owns the requested sessions. Each provider
	// compares its stored cwd form. Some stores encode the path in a directory name.
	WorkingDir string
	// HomeDir supplies the explicit home directory for store paths.
	// Empty reads EnvEntries when present, or the process home otherwise.
	HomeDir string
	// EnvEntries supplies the ordered native environment when it is non-nil.
	// An empty slice supplies an empty environment. It takes precedence over Getenv.
	EnvEntries []string
	// RuntimeLocator supplies a trusted native command locator for this query.
	// Nil uses the provider registration. An invalid supplied locator must fail.
	RuntimeLocator *launch.Locator
	// Getenv supplies single-key lookups when EnvEntries is nil.
	// A nil lookup reads the process environment.
	//
	// Session discovery normally reads the Worker's process environment.
	// A running provider can supply its exact child environment through EnvEntries.
	Getenv func(string) string
	// Limit caps the returned records. Zero means DefaultStoredSessionLimit.
	Limit int
	// Shell and LoginShell select the shell environment for a provider that lists sessions through
	// its own CLI. Amp stores threads on its server and requires amp threads list. The CLI uses the
	// same PATH and login environment as the agent, so it reads the same account. Empty values
	// select the platform defaults.
	Shell      string
	LoginShell bool
}

// Env reads one environment variable through the query's seam.
func (q StoredSessionQuery) Env(key string) string {
	if q.EnvEntries != nil {
		if key == "" {
			return ""
		}
		for index := len(q.EnvEntries) - 1; index >= 0; index-- {
			entryKey, value, valid := strings.Cut(q.EnvEntries[index], "=")
			if !valid || entryKey == "" {
				continue
			}
			if entryKey == key || (runtime.GOOS == "windows" && strings.EqualFold(entryKey, key)) {
				return value
			}
		}
		return ""
	}
	if q.Getenv != nil {
		return q.Getenv(key)
	}
	return os.Getenv(key)
}

// Environ returns an independent ordered snapshot, or nil for a lookup-only seam.
func (q StoredSessionQuery) Environ() []string {
	if q.EnvEntries != nil {
		entries := make([]string, len(q.EnvEntries))
		copy(entries, q.EnvEntries)
		return entries
	}
	if q.Getenv != nil {
		return nil
	}
	return os.Environ()
}

// Home is the directory to resolve a store path against.
func (q StoredSessionQuery) Home() string {
	if q.HomeDir != "" {
		return q.HomeDir
	}
	if q.EnvEntries != nil {
		if runtime.GOOS == "windows" {
			return q.Env("USERPROFILE")
		}
		return q.Env("HOME")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// EffectiveLimit is the query's cap, defaulted.
func (q StoredSessionQuery) EffectiveLimit() int {
	if q.Limit > 0 {
		return q.Limit
	}
	return DefaultStoredSessionLimit
}

// XDGDataHome follows the xdg-basedir package. It reads XDG_DATA_HOME and defaults to
// ~/.local/share on every platform, including macOS. OpenCode and Kilo use this convention. MiMo
// Code and Goose use it also. The native macOS Application Support location would find no
// sessions.
func (q StoredSessionQuery) XDGDataHome() string {
	if dir := strings.TrimSpace(q.Env("XDG_DATA_HOME")); dir != "" {
		return dir
	}
	home := q.Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// SortAndCapSessions returns an independent slice in newest-first order. The handle orders equal
// timestamps. Identical ordering keys retain input order. Blank handles contribute no resumable
// session and are excluded.
// The worker service uses this same order when it merges stored and live records. One shared
// implementation prevents the orders from diverging.
func SortAndCapSessions(sessions []StoredSession, limit int) []StoredSession {
	collector := NewStoredSessionCollector(limit)
	for _, session := range sessions {
		collector.Add(session)
	}
	return collector.Sessions()
}
