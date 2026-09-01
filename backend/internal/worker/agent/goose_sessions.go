package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
const gooseSessionsSQL = `
SELECT id, COALESCE(name, ''), COALESCE(updated_at, created_at, '')
FROM sessions
WHERE working_dir = ?
  AND parent_session_id IS NULL
  AND archived_at IS NULL
ORDER BY COALESCE(updated_at, created_at, '') DESC
LIMIT ?`

// gooseSessionDBRelPath is the database's path under Goose's data directory.
var gooseSessionDBRelPath = []string{"sessions", "sessions.db"}

// gooseDBCandidates lists the paths Goose's session database can be at, in the
// order the CLI itself would resolve them.
//
// GOOSE_PATH_ROOT wins when it is ABSOLUTE -- Goose ignores a relative value
// rather than resolving it, and so does this.
//
// Otherwise Goose asks etcetera's `choose_app_strategy`, which follows the XDG
// layout when the XDG variables decide it and the platform-native layout
// otherwise. Rather than reimplement that choice, both are probed and the one
// that HOLDS a database wins. The observed installation on macOS with no XDG
// variables set resolved to `~/.local/share/goose`, so XDG is first; the
// `Block` directory is the legacy Apple location Goose's own source keeps for
// installations that predate the change.
func gooseDBCandidates(q StoredSessionQuery) []string {
	var dirs []string
	if root := strings.TrimSpace(q.env("GOOSE_PATH_ROOT")); root != "" && filepath.IsAbs(root) {
		dirs = append(dirs, filepath.Join(root, "data"))
	}
	if base := q.xdgDataHome(); base != "" {
		dirs = append(dirs, filepath.Join(base, "goose"))
	}
	if runtime.GOOS == "darwin" {
		if home := q.home(); home != "" {
			dirs = append(dirs, filepath.Join(home, "Library", "Application Support", "Block", "goose"))
		}
	}
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(append([]string{dir}, gooseSessionDBRelPath...)...))
	}
	return paths
}

// gooseDBPath picks the first candidate that exists, and otherwise the first
// candidate at all, so an absent store is reported against the path a reader
// would expect to find.
func gooseDBPath(q StoredSessionQuery) string {
	candidates := gooseDBCandidates(q)
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

// gooseStoredSessions is Goose's Provider.ListStoredSessions.
func gooseStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	if strings.TrimSpace(q.WorkingDir) == "" {
		return nil, nil
	}
	db, err := openSessionStoreDB(gooseDBPath(q))
	if err != nil {
		if errors.Is(err, errSessionStoreAbsent) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = db.Close() }()

	limit := q.limit()
	rows, err := db.QueryContext(ctx, gooseSessionsSQL, filepath.Clean(q.WorkingDir), limit)
	if err != nil {
		return nil, fmt.Errorf("query goose session store: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]StoredSession, 0, limit)
	for rows.Next() {
		var id, name, updated string
		if err := rows.Scan(&id, &name, &updated); err != nil {
			continue
		}
		sessions = append(sessions, StoredSession{
			Handle:    strings.TrimSpace(id),
			Title:     trimTitle(name),
			UpdatedAt: parseGooseTimestamp(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan goose session store: %w", err)
	}
	return sortAndCapSessions(sessions, limit), nil
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
