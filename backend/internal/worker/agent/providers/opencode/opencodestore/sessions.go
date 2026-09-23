// Package opencodestore reads the session store that OpenCode, Kilo and ZCode
// share.
//
// OpenCode keeps its sessions in one SQLite database, in a `session` table
// whose columns this package reads. Kilo is a fork of OpenCode and ZCode is a
// derivative of it, and all three ship the same table: `id`, `parent_id`,
// `directory`, `title`, `time_updated`, `time_archived`. Verified against all
// three live databases -- the columns are identical, and ZCode's own
// `task_type` discriminator agrees exactly with `parent_id IS NULL` (every
// `subagent_child` row has a parent), so the shared query needs no column that
// only one of them has.
//
// Hence ONE reader with three callers, each supplying its own database path
// from its own package. The alternative -- three near-identical queries -- would
// drift the moment one of the three renames a column.
package opencodestore

import (
	"context"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// openCodeFamilySessionsSQL selects the resumable top-level sessions of one
// working directory.
//
// `parent_id IS NULL` drops subagent sessions, which outnumber real ones by an
// order of magnitude in a live store (1524 to 138 in the ZCode database this
// was written against), so a picker without the filter shows almost nothing a
// user recognises. `time_archived IS NULL` drops what the user already put
// away. `directory` is matched exactly; see sessionstore.Query, which states
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

// ListSessions reads the sessions one OpenCode-schema database holds
// for the query's working directory.
func ListSessions(ctx context.Context, dbPath string, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	return sessionstore.Query(ctx, dbPath, openCodeFamilySessionsSQL, q, sessionstore.ScanEpochMillisSession)
}
