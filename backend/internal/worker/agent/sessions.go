package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// This file holds the provider-neutral half of session-store discovery: the
// record that a reader returns, the query that the reader answers, and the one
// order that every caller applies to the records. The shared readers live in
// providers/internal/sessionstore. Each provider decides which store to read,
// behind Provider.ListStoredSessions.

// DefaultStoredSessionLimit caps how many sessions one provider's reader
// collects when the caller states no limit of its own. It limits the I/O of a
// scan, not just the response: a reader stats its candidates, sorts them, and
// only then opens the newest few.
const DefaultStoredSessionLimit = 50

// StoredSession is one resumable session that a provider's own storage holds.
type StoredSession struct {
	// Handle is the resume handle, in the SAME form the provider reports at
	// runtime through UpdateSessionID.
	//
	// That equality is what lets the caller dedupe this record against
	// `agents.agent_session_id` with a plain string compare and no per-provider
	// key function. Pi is the case that fixes the rule: it identifies one
	// session by an ID and by a file path, and `pi --session` takes either, so
	// its reader must return the ID -- the form the running process reports --
	// and not the path it found the session at.
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
	// WorkingDir is the directory a session must have run in, compared
	// EXACTLY. Every store here records the session's cwd (some as a mangled
	// directory name, which is why the comparison belongs to each reader), and
	// a session picker offered inside a directory answers for that directory.
	WorkingDir string
	// HomeDir locates a store under the user's home. Empty falls back to the
	// process's own home directory.
	HomeDir string
	// Getenv reads the environment that locates a store (CODEX_HOME,
	// XDG_DATA_HOME, ...). Nil means os.Getenv.
	//
	// The worker's own environment is the right answer, not a stored copy:
	// FinalizeAgentEnv deliberately PRESERVES every home/config-dir variable
	// when it spawns an agent, so what this process sees is what the CLI sees.
	Getenv func(string) string
	// Limit caps the returned records. Zero means DefaultStoredSessionLimit.
	Limit int
	// Shell and LoginShell are the shell the worker launches agents through, for
	// a provider whose sessions only its own CLI can list (Amp keeps its threads
	// on its server, and `amp threads list` is the one reader). The CLI then runs
	// with the PATH and the login environment the agent itself would get, so the
	// list comes from the same account. Empty for a caller that states no shell;
	// such a provider falls back to the platform's default shell.
	Shell      string
	LoginShell bool
}

// Env reads one environment variable through the query's seam.
func (q StoredSessionQuery) Env(key string) string {
	if q.Getenv != nil {
		return q.Getenv(key)
	}
	return os.Getenv(key)
}

// Home is the directory to resolve a store path against.
func (q StoredSessionQuery) Home() string {
	if q.HomeDir != "" {
		return q.HomeDir
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

// XDGDataHome resolves the XDG data directory the way the `xdg-basedir` npm
// package does, which is what OpenCode, Kilo, MiMo Code and Goose are built on: it reads
// XDG_DATA_HOME and falls back to `~/.local/share` on EVERY platform, macOS
// included. Resolving to `~/Library/Application Support` there would look more
// native and find nothing.
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

// SortAndCapSessions orders newest first and truncates to `limit`.
//
// The handle breaks a timestamp tie, so a store whose timestamps have
// one-second resolution (Goose) still produces one stable order rather than
// whatever the scan happened to yield. Records with no handle are dropped:
// a row this code cannot resume is not a choice to offer.
//
// Exported because the worker's service layer merges these records with its own
// and must order the result by the SAME rule. A second copy of the rule there
// drifted from this one the moment either changed.
func SortAndCapSessions(sessions []StoredSession, limit int) []StoredSession {
	// A fresh slice, not `sessions[:0]`. Filtering in place would overwrite the
	// caller's backing array, so a reader that kept a reference to what it
	// passed would find it rewritten. At these sizes the copy costs nothing,
	// and it makes that mistake impossible rather than merely absent today.
	kept := make([]StoredSession, 0, len(sessions))
	for _, s := range sessions {
		if strings.TrimSpace(s.Handle) == "" {
			continue
		}
		kept = append(kept, s)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].UpdatedAt.Equal(kept[j].UpdatedAt) {
			return kept[i].UpdatedAt.After(kept[j].UpdatedAt)
		}
		return kept[i].Handle < kept[j].Handle
	})
	if limit > 0 && len(kept) > limit {
		kept = kept[:limit]
	}
	return kept
}
