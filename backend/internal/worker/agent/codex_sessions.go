package agent

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/util/pathutil"
)

// Codex writes one JSONL rollout per session under
// `$CODEX_HOME/sessions/YYYY/MM/DD/`, and maintains a SQLite INDEX of those
// rollouts in `state_5.sqlite`. This reader uses the index.
//
// The index is complete, not a cache of recent activity: on the machine this
// was written against it held 2741 rows against 2741 rollout files. It is also
// the only source that carries a title -- a rollout's own first record is a
// `session_meta` with the cwd but no title, so the JSONL route would have to
// read into each file to find the first user message. One indexed query
// replaces thousands of file reads.
//
// There is deliberately no JSONL fallback. A machine with rollouts but no index
// runs a Codex older than the index, and reading its files would offer resume
// handles to a CLI whose `codex resume` predates them.

// codexSessionsSQL selects the resumable threads of one working directory.
//
// `archived = 0` drops what the user put away. Codex has no parent column: a
// sub-thread is not a row here, so nothing else needs excluding.
// `idx_threads_updated_at` serves the ordering.
const codexSessionsSQL = `
SELECT id,
       COALESCE(NULLIF(name, ''), NULLIF(title, ''), NULLIF(preview, ''), ''),
       COALESCE(NULLIF(updated_at_ms, 0), NULLIF(recency_at_ms, 0), updated_at * 1000, 0)
FROM threads
WHERE cwd = ?
  AND COALESCE(archived, 0) = 0
ORDER BY 3 DESC
LIMIT ?`

// codexHome resolves `$CODEX_HOME`, default `~/.codex`.
func codexHome(q StoredSessionQuery) string {
	return homeDirFromEnv(q, "CODEX_HOME", ".codex")
}

// codexStateDBPath resolves Codex's session index.
//
// CODEX_SQLITE_HOME moves the databases away from CODEX_HOME; without it they
// sit in CODEX_HOME itself, which is what the CLI does.
func codexStateDBPath(q StoredSessionQuery) string {
	dir := strings.TrimSpace(q.env("CODEX_SQLITE_HOME"))
	if dir != "" {
		dir = pathutil.ExpandHome(dir, q.home())
	} else {
		dir = codexHome(q)
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "state_5.sqlite")
}

// codexStoredSessions is Codex's Provider.ListStoredSessions.
func codexStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	return queryStoredSessions(ctx, codexStateDBPath(q), codexSessionsSQL, q, scanEpochMillisSession)
}
