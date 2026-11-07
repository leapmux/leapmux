package goose

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Goose keeps its sessions in one SQLite database at
// `<data dir>/sessions/sessions.db`. Note the doubled name: the file sits
// inside a `sessions` DIRECTORY, and the path without it opens nothing and
// reports no error, only an empty store.

// gooseSessionsSQL selects the resumable top-level sessions of one working
// directory.
//
// `parent_session_id IS NULL` drops subagent sessions. It is the WHOLE filter
// on kind: `session_type` must NOT be narrowed to `'user'`, because LeapMux
// launches Goose over ACP and every session it creates is therefore typed
// `'acp'` -- narrowing to `'user'` would hide exactly the sessions this picker
// exists to offer. On the machine this was written against the split was 80
// `acp`, 34 `sub_agent`, 5 `user`, and `sub_agent` was exactly the set with a
// parent, so the parent test already covers what the type would have.
// The ordering runs through `julianday`, never over the raw text. SQLite has no
// timestamp type, so these columns hold whatever was written, and
// gooseTimestampLayouts lists four shapes that coexist. A text comparison sorts
// them by their bytes: `' '` is below `'T'`, so every driver-written
// `2026-08-30T00:01:00Z` outranks every `CURRENT_TIMESTAMP`-written
// `2026-08-30 23:59:00` of the same day, and the `LIMIT` then discards the row
// that is actually newest. `julianday` parses all four shapes and answers NULL
// for a value it cannot read, which sorts last under DESC -- the same place the
// zero time takes in parseGooseTimestamp, so the SQL cut and the Go order
// agree. The expression was never indexable, so nothing is lost.
const gooseSessionsSQL = `
SELECT id, COALESCE(name, ''), COALESCE(updated_at, created_at, '')
FROM sessions
WHERE working_dir = ?
  AND parent_session_id IS NULL
  AND archived_at IS NULL
ORDER BY julianday(COALESCE(updated_at, created_at, '')) DESC
LIMIT ?`

// gooseDBPath follows the current native directory strategy. An absolute override
// remains authoritative even before the runtime creates its database.
func gooseDBPath(q agent.StoredSessionQuery) string {
	return gooseDBPathForPlatform(q, runtime.GOOS)
}

func gooseDBPathForPlatform(q agent.StoredSessionQuery, platform string) string {
	if root := q.Env("GOOSE_PATH_ROOT"); filepath.IsAbs(root) {
		return filepath.Join(root, "data", "sessions", "sessions.db")
	}
	var base string
	if platform == "windows" {
		base = q.Env("APPDATA")
		if base == "" && q.Home() != "" {
			base = filepath.Join(q.Home(), "AppData", "Roaming")
		}
		if base == "" {
			return ""
		}
		return filepath.Join(base, "Block", "goose", "data", "sessions", "sessions.db")
	}
	base = q.Env("XDG_DATA_HOME")
	if !filepath.IsAbs(base) {
		if q.Home() == "" {
			return ""
		}
		base = filepath.Join(q.Home(), ".local", "share")
	}
	return filepath.Join(base, "goose", "sessions", "sessions.db")
}

// gooseStoredSessions is Goose's Provider.ListStoredSessions.
func gooseStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return sessionstore.Query(ctx, gooseDBPath(q), gooseSessionsSQL, q, scanGooseSession)
}

// scanGooseSession reads Goose's (id, name, timestamp text) row shape. Its
// timestamp is TEXT rather than the epoch milliseconds the other SQL stores
// hold, which is why it does not share sessionstore.ScanEpochMillisSession.
func scanGooseSession(rows *sql.Rows) (agent.StoredSession, bool) {
	var id, name, updated string
	if err := rows.Scan(&id, &name, &updated); err != nil {
		return agent.StoredSession{}, false
	}
	return agent.StoredSession{
		Handle:    strings.TrimSpace(id),
		Title:     sessionstore.TrimTitle(name),
		UpdatedAt: parseGooseTimestamp(updated),
	}, true
}

// gooseTimestampLayouts are the shapes Goose's TIMESTAMP columns hold.
//
// SQLite has no timestamp type, so the value is whatever was written: the
// column default is `CURRENT_TIMESTAMP`, which is `YYYY-MM-DD HH:MM:SS` with no
// zone, and a value written through a driver can carry fractional seconds or an
// explicit `Z`. All are UTC.
var gooseTimestampLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.999999999",
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
}

// parseGooseTimestamp reads one of those shapes, or returns the zero time,
// which sorts last rather than to the epoch.
func parseGooseTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range gooseTimestampLayouts {
		if ts, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return ts.UTC()
		}
	}
	return time.Time{}
}
