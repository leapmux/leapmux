package agent

import (
	"context"
	"path/filepath"
)

// OpenCode keeps its sessions in one SQLite database, in a `session` table
// whose columns this file reads. Kilo is a fork of OpenCode and ZCode is a
// derivative of it, and all three ship the same table: `id`, `parent_id`,
// `directory`, `title`, `time_updated`, `time_archived`. Verified against all
// three live databases -- the columns are identical, and ZCode's own
// `task_type` discriminator agrees exactly with `parent_id IS NULL` (every
// `subagent_child` row has a parent), so the shared query needs no column that
// only one of them has.
//
// Hence ONE reader with three callers, each supplying its own database path
// from its own file. The alternative -- three near-identical queries -- would
// drift the moment one of the three renames a column.

// openCodeFamilySessionsSQL selects the resumable top-level sessions of one
// working directory.
//
// `parent_id IS NULL` drops subagent sessions, which outnumber real ones by an
// order of magnitude in a live store (1524 to 138 in the ZCode database this
// was written against), so a picker without the filter shows almost nothing a
// user recognises. `time_archived IS NULL` drops what the user already put
// away. `directory` is matched exactly; see queryStoredSessions, which states
// why the comparison stays in SQL.
//
// NULLIF around each timestamp, not COALESCE alone: COALESCE skips a NULL and
// keeps a stored 0, and a 0 here means the same thing a NULL does -- no
// recorded time -- so without it a row that carries one would sort to the epoch
// instead of falling back to when it was created.
const openCodeFamilySessionsSQL = `
SELECT id,
       COALESCE(title, ''),
       COALESCE(NULLIF(time_updated, 0), NULLIF(time_created, 0), 0)
FROM session
WHERE directory = ?
  AND parent_id IS NULL
  AND time_archived IS NULL
ORDER BY 3 DESC
LIMIT ?`

// openCodeFamilySessions reads the sessions one OpenCode-schema database holds
// for the query's working directory.
func openCodeFamilySessions(ctx context.Context, dbPath string, q StoredSessionQuery) ([]StoredSession, error) {
	return queryStoredSessions(ctx, dbPath, openCodeFamilySessionsSQL, q, scanEpochMillisSession)
}

// opencodeDataDir is where OpenCode keeps its data, following the `xdg-basedir`
// npm package it is built on -- `~/.local/share` on every platform, macOS
// included. OPENCODE_CONFIG_DIR is deliberately NOT consulted: it moves the
// config, not the data.
func opencodeDataDir(q StoredSessionQuery) string {
	base := q.xdgDataHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "opencode")
}

// opencodeDBPath resolves OpenCode's session database.
//
// OPENCODE_DB takes either an absolute path or a bare file name relative to the
// data directory, which is how the CLI itself reads it.
//
// A non-stable install gives the file the name `opencode-<channel>.db`, and
// this reader does not find that: nothing in the launch path tells the worker
// which channel the installed CLI is, and probing every name that matches the
// pattern would pick a store the running CLI does not use. An operator on such
// a channel points OPENCODE_DB at the file.
func opencodeDBPath(q StoredSessionQuery) string {
	dir := opencodeDataDir(q)
	if path, ok := storeOverridePath(q, "OPENCODE_DB", dir); ok {
		return path
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "opencode.db")
}

// opencodeStoredSessions is OpenCode's Provider.ListStoredSessions.
func opencodeStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return openCodeFamilySessions(ctx, opencodeDBPath(q), q)
}
