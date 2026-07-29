-- name: UpsertTerminal :exec
-- shell is intentionally NOT updated on conflict: it is the binary the
-- terminal was spawned with and never changes for the lifetime of the
-- row. Only the initial OpenTerminal INSERT writes it; subsequent
-- exit/restart upserts pass whatever value (commonly empty) and the
-- existing column survives unchanged.
INSERT INTO terminals (id, working_dir, home_dir, shell_start_dir, shell, title, cols, rows, screen, exit_code, closed_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(working_dir),
  sqlc.arg(home_dir),
  sqlc.arg(shell_start_dir),
  sqlc.arg(shell),
  sqlc.arg(title),
  sqlc.arg(cols),
  sqlc.arg(rows),
  sqlc.arg(screen),
  sqlc.arg(exit_code),
  -- The title-update path re-binds a DB-roundtripped closed_at; binding a
  -- SQLiteNullTime re-canonicalizes it so the rewrite cannot split the column
  -- into two layouts under the raw-string cleanup sweep. The DO UPDATE below
  -- reuses the excluded value.
  sqlc.narg(closed_at)
)
ON CONFLICT (id) DO UPDATE SET
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

-- GetTerminalID is the existence probe behind requireTerminalID: it answers
-- sql.ErrNoRows for an unknown id while reading only the primary key, so the
-- per-keystroke SendInput / per-resize ResizeTerminal paths never load the
-- screen BLOB SELECT * would.
-- name: GetTerminalID :one
SELECT id FROM terminals WHERE id = ?;

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
-- (shell, dimensions, working directory) plus length(screen)
-- so it can seed the cumulative byte counter when no in-memory
-- ScreenBuffer exists. Reading length(screen) instead of screen avoids
-- loading the BLOB on every Enter-press restart, which is wasted work
-- in the common case (in-memory entry still present, Respawn carries
-- the live buffer forward and length is ignored).
SELECT working_dir, shell_start_dir, shell, cols, rows,
       length(screen) AS screen_length
FROM terminals WHERE id = ?;

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
-- Open terminals only. Mirrors ListAllOpenAgentIDs, and exists for the same
-- reason the orphan reconciler needs it: a closed row has nothing left to
-- converge, so comparing it against the hub's live list only re-runs a teardown
-- that already happened. Reads the id alone, so it never touches the 100KB
-- screen blob.
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
