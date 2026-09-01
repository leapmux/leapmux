package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
// is running a Codex older than the index, and reading its files would offer
// resume handles to a CLI whose `codex resume` predates them.

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
	if dir := strings.TrimSpace(q.env("CODEX_HOME")); dir != "" {
		return expandHome(dir, q.home())
	}
	home := q.home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// codexStateDBPath resolves Codex's session index.
//
// CODEX_SQLITE_HOME moves the databases away from CODEX_HOME; without it they
// sit in CODEX_HOME itself, which is what the CLI does.
func codexStateDBPath(q StoredSessionQuery) string {
	dir := strings.TrimSpace(q.env("CODEX_SQLITE_HOME"))
	if dir != "" {
		dir = expandHome(dir, q.home())
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
	if strings.TrimSpace(q.WorkingDir) == "" {
		return nil, nil
	}
	db, err := openSessionStoreDB(codexStateDBPath(q))
	if err != nil {
		if errors.Is(err, errSessionStoreAbsent) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = db.Close() }()

	limit := q.limit()
	rows, err := db.QueryContext(ctx, codexSessionsSQL, filepath.Clean(q.WorkingDir), limit)
	if err != nil {
		return nil, fmt.Errorf("query codex session index: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]StoredSession, 0, limit)
	for rows.Next() {
		var (
			id      string
			title   string
			updated int64
		)
		if err := rows.Scan(&id, &title, &updated); err != nil {
			continue
		}
		sessions = append(sessions, StoredSession{
			Handle:    strings.TrimSpace(id),
			Title:     trimTitle(title),
			UpdatedAt: epochMillis(updated),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan codex session index: %w", err)
	}
	return sortAndCapSessions(sessions, limit), nil
}
