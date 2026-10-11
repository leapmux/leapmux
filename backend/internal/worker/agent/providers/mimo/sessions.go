package mimo

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore"
)

// MiMo Code is a fork of OpenCode, and its session database keeps OpenCode's
// `session` table with every column the shared reader uses. This file supplies
// the path and the fork's own exclusions, and opencodestore runs the query.

// mimoAppName is the directory MiMo keeps its data in under the XDG data
// directory.
const mimoAppName = "mimocode"

// mimoDBName is the database MiMo keeps in its data directory. A release
// channel other than latest, beta or prod can use `mimocode-<channel>.db`, but
// only when MIMOCODE_DISABLE_CHANNEL_DB is false, and that flag is true by
// default; the installed releases all ship the latest channel.
const mimoDBName = "mimocode.db"

// mimoMemoryDB is the MIMOCODE_DB value that keeps the database in memory. Such
// a store holds nothing another process can read.
const mimoMemoryDB = ":memory:"

// mimoImportExclusions are the tables in which MiMo marks the sessions it
// imported from another program: `external_import` for Claude Code, Codex and
// OpenCode, and `claude_import` for `mimo session import-claude`.
//
// Those sessions are left out of the picker. They are the other program's
// sessions, which that program's own picker already lists, and one real store
// held 3147 of them beside a single MiMo session, which buried it.
var mimoImportExclusions = []opencodestore.Exclusion{
	{Table: "external_import", Column: "session_id"},
	{Table: "claude_import", Column: "session_id"},
}

// mimoDataDir resolves MiMo's data directory: `<MIMOCODE_HOME>/data` when the
// variable is set, else `mimocode` under the XDG data directory. MiMo refuses
// to start with a relative MIMOCODE_HOME, so such a value has no store.
func mimoDataDir(q agent.StoredSessionQuery) string {
	if home := strings.TrimSpace(q.Env("MIMOCODE_HOME")); home != "" {
		if !filepath.IsAbs(home) {
			return ""
		}
		return filepath.Join(home, "data")
	}
	base := q.XDGDataHome()
	if base == "" {
		return ""
	}
	return filepath.Join(base, mimoAppName)
}

// mimoDBPath resolves MiMo's session database. MIMOCODE_DB wins, as an
// absolute path or as a name under the data directory; its in-memory value has
// no file to read.
func mimoDBPath(q agent.StoredSessionQuery) string {
	dir := mimoDataDir(q)
	if strings.TrimSpace(q.Env("MIMOCODE_DB")) == mimoMemoryDB {
		return ""
	}
	if path, ok := sessionstore.OverridePath(q, "MIMOCODE_DB", dir); ok {
		return path
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, mimoDBName)
}

// mimoStoredSessions is MiMo's Provider.ListStoredSessions.
func mimoStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return opencodestore.ListSessionsExcept(ctx, mimoDBPath(q), q, mimoImportExclusions...)
}
