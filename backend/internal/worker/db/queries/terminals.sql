-- name: UpsertTerminal :exec
-- Initial records retain the existing shell on conflict.
-- A terminal keeps its shell for its entire lifetime.
-- Later title and exit writes use dedicated UPDATE statements that preserve closed_at.
INSERT INTO terminals (id, working_dir, shell_start_dir, shell, title, cols, rows, screen, closed_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(working_dir),
  sqlc.arg(shell_start_dir),
  sqlc.arg(shell),
  sqlc.arg(title),
  sqlc.arg(cols),
  sqlc.arg(rows),
  sqlc.arg(screen),
  -- Initial records can carry a persisted closed state.
  sqlc.narg(closed_at)
)
ON CONFLICT (id) DO UPDATE SET
  working_dir     = excluded.working_dir,
  shell_start_dir = excluded.shell_start_dir,
  title           = excluded.title,
  cols            = excluded.cols,
  rows            = excluded.rows,
  screen          = excluded.screen,
  closed_at       = excluded.closed_at;

-- name: GetTerminal :one
SELECT * FROM terminals WHERE id = ?;

-- Late persistence cannot insert a tab or change its closed state.
-- Use named parameters throughout. Mixing sqlc.arg with bare placeholders produces incorrect SQLite parameter numbers.
-- name: SaveTerminalExit :exec
UPDATE terminals SET
  title = CASE WHEN title = sqlc.arg(previous_title) THEN sqlc.arg(title) ELSE title END,
  cols = sqlc.arg(cols), rows = sqlc.arg(rows), screen = sqlc.arg(screen)
WHERE id = sqlc.arg(id);

-- Read before the runtime snapshot so a concurrent rename cannot be overwritten by that snapshot.
-- name: GetTerminalTitle :one
SELECT title FROM terminals WHERE id = ?;

-- ListTerminalTitlesByIDs batches GetTerminalTitle for busy-work
-- inspection so quitting or switching mode avoids N+1 lookups while still
-- skipping the 100KB screen BLOB.
-- name: ListTerminalTitlesByIDs :many
SELECT id, title FROM terminals WHERE id IN (sqlc.slice('ids'));

-- name: UpdateTerminalTitle :exec
UPDATE terminals SET title = ? WHERE id = ?;

-- GetTerminalID is the existence probe behind requireTerminalID: it answers
-- sql.ErrNoRows for an unknown id while reading two narrow columns, so the
-- per-keystroke SendInput / per-resize ResizeTerminal paths never load the
-- screen BLOB SELECT * would.
--
-- workspace_archived rides along for the same reason as in GetAgentID: the
-- registrar refuses every terminal write handler for an archived workspace,
-- and an INTEGER column costs nothing next to the screen BLOB this query
-- exists to skip.
-- name: GetTerminalID :one
SELECT id, workspace_archived FROM terminals WHERE id = ?;

-- name: GetTerminalForReady :one
-- Narrow lookup used by the post-spawn tail of runTerminalStartup /
-- runTerminalRestart. closed_at drives the close-race teardown; title
-- absorbs the value the frontend may have persisted between the
-- handler returning and StartTerminal registering in-memory metadata
-- (restart ignores the title field). Three columns in one round-trip,
-- avoiding the SELECT * scan of the screen BLOB.
SELECT closed_at, title, workspace_archived FROM terminals WHERE id = ?;

-- name: GetTerminalForRestart :one
-- Restart hot path: returns the metadata the handler needs to respawn
-- (shell, dimensions, working directory) plus length(screen)
-- so it can seed the cumulative byte counter when no in-memory
-- ScreenBuffer exists. Reading length(screen) instead of screen avoids
-- loading the BLOB on every Enter-press restart, which is wasted work
-- in the common case (in-memory entry still present, Respawn carries
-- the live buffer forward and length is ignored).
SELECT working_dir, shell_start_dir, shell, cols, rows,
       length(screen) AS screen_length, workspace_archived
FROM terminals WHERE id = ? AND closed_at IS NULL;

-- name: CloseTerminal :execresult
-- closed_at IS NULL keeps this idempotent -- see the note on CloseAgent, which
-- also covers why this reports an affected-row count rather than :exec. A
-- terminal row carries a 100KB screen blob, so a re-stamped closed_at that
-- keeps it out of reach of DeleteClosedTerminalsBefore is the more expensive
-- half of that leak.
UPDATE terminals SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND closed_at IS NULL;

-- name: ListAllTerminalIDs :many
-- Every caller (the orphan reconciler's inventory scan and its emptiness
-- probe, and BuildTabSync's reconnect report) needs only the id, and a
-- terminals row carries a 100KB screen blob -- so `SELECT *` here read every
-- terminal's full scrollback on every hourly pass and every reconnect just to
-- collect ids. Mirrors ListAllAgentIDs.
SELECT id FROM terminals;

-- name: ListAllOpenTerminalIDs :many
-- Open terminal tabs only. Mirrors ListAllOpenAgentIDs. Quake terminals live in
-- memory and never appear in this table, so every open row is a real tab.
SELECT id FROM terminals WHERE closed_at IS NULL;

-- name: ListTerminalsByIDs :many
SELECT * FROM terminals WHERE id IN (sqlc.slice('ids')) AND closed_at IS NULL;

-- name: DeleteClosedTerminalsBefore :execresult
-- Raw compare: closed_at is stored canonical on every write path
-- (CloseTerminal SET strftime, UpsertTerminal
-- binds a SQLiteNullTime), and the Go side binds a SQLiteNullTime cutoff (same
-- canonical layout), so the lexicographic < is byte-exact. A raw time.Time bind
-- here would compare in the driver's own layout and skip every same-day row
-- until the date rolled over.
DELETE FROM terminals WHERE rowid IN (SELECT t.rowid FROM terminals t WHERE t.closed_at < sqlc.arg(cutoff) LIMIT 1000);

-- name: SetTerminalStartupError :exec
UPDATE terminals SET startup_error = ? WHERE id = ?;

-- SetTerminalWorkspaceArchived changes only the cached workspace lifecycle
-- state. The row, final screen, and worktree link stay unchanged.
--
-- closed_at IS NULL matters for the same reason as SetAgentWorkspaceArchived:
-- the caller stops and broadcasts for every row this statement changes, and a
-- closed row stays here for the whole cleanup retention window.
-- name: SetTerminalWorkspaceArchived :execrows
UPDATE terminals SET workspace_archived = sqlc.arg(workspace_archived)
WHERE id = sqlc.arg(id) AND closed_at IS NULL
  AND workspace_archived <> sqlc.arg(workspace_archived);

-- GetTerminalWorkingDir reads the working directory on terminal close to reap
-- the directory's quake terminal without scanning the 100KB screen blob.
-- name: GetTerminalWorkingDir :one
SELECT working_dir FROM terminals WHERE id = ?;

-- ListOpenTabsByWorkingDirs is the WHOLE answer to "does any open tab still
-- work in this directory?", which is the entire liveness question for a quake
-- terminal.
--
-- Reads tab_locations rather than restating the per-type rule a fourth time.
-- That view already encodes which tables hold tabs, what OPEN means for each
-- (agents and terminals stamp closed_at; a payload-backed tab is hard deleted,
-- so its presence IS its openness), and which kinds carry an owner.
--
-- tab_type comes back because the identity is the PAIR. A payload-backed tab id
-- is minted client-side and unique only within one account, so the id alone
-- cannot say whether two rows are the same tab -- see dirTabRef.
-- name: ListOpenTabsByWorkingDirs :many
SELECT tab_type, tab_id, working_dir, workspace_archived FROM tab_locations
WHERE working_dir IN (sqlc.slice('working_dirs'));

-- name: CreateTerminal :exec
-- Inserts a new terminal record; fails on primary-key conflict.
-- A terminal keeps its shell for its entire lifetime.
-- Later title and exit writes use dedicated UPDATE statements that preserve closed_at.
INSERT INTO terminals (id, working_dir, shell_start_dir, shell, title, cols, rows, screen, closed_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(working_dir),
  sqlc.arg(shell_start_dir),
  sqlc.arg(shell),
  sqlc.arg(title),
  sqlc.arg(cols),
  sqlc.arg(rows),
  sqlc.arg(screen),
  -- Initial records can carry a persisted closed state.
  sqlc.narg(closed_at)
);

-- name: SaveCreationTerminalDimensions :execrows
-- Retain resizes while a published creation transfers its startup claim.
UPDATE terminals SET cols = sqlc.arg(cols), rows = sqlc.arg(rows)
WHERE id = sqlc.arg(terminal_id) AND closed_at IS NULL
  AND EXISTS (
    SELECT 1 FROM tab_creations tc
    WHERE tc.user_id = sqlc.arg(user_id) AND tc.worker_id = sqlc.arg(worker_id)
      AND tc.tab_id = terminals.id AND tc.tab_type = sqlc.arg(tab_type)
      AND tc.state IN (sqlc.arg(pending_state), sqlc.arg(published_state))
  );
