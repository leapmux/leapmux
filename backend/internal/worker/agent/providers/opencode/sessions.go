package opencode

import (
	"context"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// opencodeDataDir follows OpenCode's XDG data directory. Its default is
// ~/.local/share/opencode on every platform, including macOS. OPENCODE_CONFIG_DIR
// changes the config directory, not the data directory.
func opencodeDataDir(q agent.StoredSessionQuery) string {
	base := q.XDGDataHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "opencode")
}

// opencodeDBPath resolves OpenCode's session database.
//
// The CLI resolves OPENCODE_DB from the data directory unless the path is
// absolute. This reader follows the same rule.
//
// An install on a custom channel can use `opencode-<channel>.db`, and
// this reader does not find that: nothing in the launch path tells the worker
// which channel the installed CLI is, and probing every name that matches the
// pattern would pick a store the running CLI does not use. An operator on such
// a channel points OPENCODE_DB at the file.
func opencodeDBPath(q agent.StoredSessionQuery) string {
	override, configured := q.LookupEnv("OPENCODE_DB")
	// An empty override resolves to the data directory, which cannot be a database.
	// The native :memory: database has no persistent file either.
	if configured && (override == "" || override == ":memory:") {
		return ""
	}
	if configured && filepath.IsAbs(override) {
		return override
	}
	dir := opencodeDataDir(q)
	if dir == "" {
		return ""
	}
	if configured {
		return filepath.Join(dir, override)
	}
	return filepath.Join(dir, "opencode.db")
}

// opencodeStoredSessions is OpenCode's Provider.ListStoredSessions.
func opencodeStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return sessionstore.Query(ctx, opencodeDBPath(q), openCodeSessionsQuery, q, sessionstore.ScanEpochMillisSession)
}

// OpenCode v2 stores top-level and child sessions in session_v2.
// A zero update time falls back to creation time, as an absent update time does.
const openCodeSessionsQuery = `
SELECT id,
       COALESCE(title, ''),
       COALESCE(NULLIF(time_updated, 0), NULLIF(time_created, 0), 0)
FROM session_v2
WHERE directory = ?
  AND parent_id IS NULL
  AND time_archived IS NULL
ORDER BY 3 DESC, id ASC
LIMIT ?`
