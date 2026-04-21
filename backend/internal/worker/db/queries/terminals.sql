-- name: UpsertTerminal :exec
INSERT INTO terminals (id, workspace_id, working_dir, home_dir, shell_start_dir, title, cols, rows, screen, exit_code, closed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
DELETE FROM terminals WHERE rowid IN (SELECT t.rowid FROM terminals t WHERE t.closed_at < ? LIMIT 1000);

-- name: GetTerminalWorkspaceID :one
SELECT workspace_id FROM terminals WHERE id = ?;

-- name: UpdateTerminalWorkspace :exec
UPDATE terminals SET workspace_id = ? WHERE id = ?;

-- name: SetTerminalStartupError :exec
UPDATE terminals SET startup_error = ? WHERE id = ?;
