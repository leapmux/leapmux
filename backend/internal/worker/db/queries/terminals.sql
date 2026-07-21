-- name: UpsertTerminal :exec
-- shell is intentionally NOT updated on conflict: it is the binary the
-- terminal was spawned with and never changes for the lifetime of the
-- row. Only the initial OpenTerminal INSERT writes it; subsequent
-- exit/restart upserts pass whatever value (commonly empty) and the
-- existing column survives unchanged.
INSERT INTO terminals (id, workspace_id, working_dir, home_dir, shell_start_dir, shell, title, cols, rows, screen, exit_code, closed_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(workspace_id),
  sqlc.arg(working_dir),
  sqlc.arg(home_dir),
  sqlc.arg(shell_start_dir),
  sqlc.arg(shell),
  sqlc.arg(title),
  sqlc.arg(cols),
  sqlc.arg(rows),
  sqlc.arg(screen),
  sqlc.arg(exit_code),
  -- The title-update path re-binds a DB-roundtripped closed_at; without this
  -- wrap that rewrite stores the driver's own layout and splits the column
  -- into two layouts under the raw-string cleanup sweep. The DO UPDATE below
  -- reuses the transformed excluded value.
  strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(closed_at))
)
ON CONFLICT (id) DO UPDATE SET
  workspace_id    = excluded.workspace_id,
  working_dir     = excluded.working_dir,
  home_dir        = excluded.home_dir,
  shell_start_dir = excluded.shell_start_dir,
  title           = excluded.title,
  cols            = excluded.cols,
  rows            = excluded.rows,
  screen          = excluded.screen,
  exit_code       = excluded.exit_code,
  closed_at       = excluded.closed_at;

-- name: GetTerminal :one
SELECT * FROM terminals WHERE id = ?;

-- name: GetTerminalForReady :one
-- Narrow lookup used by the post-spawn tail of runTerminalStartup /
-- runTerminalRestart. closed_at drives the close-race teardown; title
-- absorbs the value the frontend may have persisted between the
-- handler returning and StartTerminal registering in-memory metadata
-- (restart ignores the title field). Two columns in one round-trip,
-- avoiding the SELECT * scan of the screen BLOB.
SELECT closed_at, title FROM terminals WHERE id = ?;

-- name: GetTerminalForRestart :one
-- Restart hot path: returns the metadata the handler needs to respawn
-- (workspace, shell, dimensions, working directory) plus length(screen)
-- so it can seed the cumulative byte counter when no in-memory
-- ScreenBuffer exists. Reading length(screen) instead of screen avoids
-- loading the BLOB on every Enter-press restart, which is wasted work
-- in the common case (in-memory entry still present, Respawn carries
-- the live buffer forward and length is ignored).
SELECT workspace_id, working_dir, shell_start_dir, shell, cols, rows,
       length(screen) AS screen_length
FROM terminals WHERE id = ?;

-- name: CloseTerminal :exec
UPDATE terminals SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: ListAllTerminals :many
SELECT * FROM terminals;

-- name: CloseOpenTerminalsByWorkspace :exec
UPDATE terminals SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE workspace_id = ? AND closed_at IS NULL;

-- name: ListTerminalsByWorkspace :many
SELECT * FROM terminals WHERE workspace_id = ? AND closed_at IS NULL;

-- name: ListTerminalsByIDs :many
SELECT * FROM terminals WHERE id IN (sqlc.slice('ids')) AND closed_at IS NULL;

-- name: DeleteClosedTerminalsBefore :execresult
-- Raw compare: closed_at is stored canonical on every write path
-- (CloseTerminal/CloseOpenTerminalsByWorkspace SET strftime, UpsertTerminal
-- wraps its bound value), and the Go side binds a timefmt.Format cutoff
-- (CAST AS TEXT -> string param), so the lexicographic < is byte-exact. A raw
-- time.Time bind here would compare in the driver's own layout and skip every
-- same-day row until the date rolled over.
DELETE FROM terminals WHERE rowid IN (SELECT t.rowid FROM terminals t WHERE t.closed_at < CAST(sqlc.arg(cutoff) AS TEXT) LIMIT 1000);

-- name: GetTerminalWorkspaceID :one
SELECT workspace_id FROM terminals WHERE id = ?;

-- name: UpdateTerminalWorkspace :exec
UPDATE terminals SET workspace_id = ? WHERE id = ?;

-- name: SetTerminalStartupError :exec
UPDATE terminals SET startup_error = ? WHERE id = ?;
