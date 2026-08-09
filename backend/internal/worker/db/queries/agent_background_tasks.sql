-- name: ListAgentBackgroundTasks :many
SELECT * FROM agent_background_tasks WHERE owner_agent_id = ? ORDER BY seq LIMIT ?;

-- UpsertAgentBackgroundTask inserts a new row or updates an existing one keyed
-- by (owner_agent_id, row_key). seq is set only on insert (never overwritten on
-- update); the CASE on child_agent_id means a later upsert that omits the id
-- can never blank one recorded earlier. created_at is bound by the caller
-- (sqltime floors to ms) only on insert via the excluded alias; updated_at is
-- always the caller's stamp. Binding both from one Go instant keeps the
-- in-memory cache and the persisted row byte-identical (no strftime-vs-time.Now
-- drift across a reconnect).
-- name: UpsertAgentBackgroundTask :exec
INSERT INTO agent_background_tasks (
    owner_agent_id, row_key, seq, kind, child_agent_id, parent_agent_id,
    group_key, group_label, title, description, active_form, status, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
ON CONFLICT(owner_agent_id, row_key) DO UPDATE SET
    kind           = excluded.kind,
    child_agent_id = CASE WHEN excluded.child_agent_id <> '' THEN excluded.child_agent_id ELSE agent_background_tasks.child_agent_id END,
    parent_agent_id = excluded.parent_agent_id,
    group_key      = excluded.group_key,
    group_label    = excluded.group_label,
    title          = excluded.title,
    description    = excluded.description,
    active_form    = excluded.active_form,
    status         = excluded.status,
    updated_at     = excluded.updated_at;

-- name: UpdateAgentBackgroundTaskStatus :exec
UPDATE agent_background_tasks SET
    status     = ?,
    active_form = ?,
    updated_at = ?
WHERE owner_agent_id = ? AND row_key = ?;

-- StampAgentBackgroundTaskEndedAt sets ended_at on a row that is already
-- terminal but still has a NULL ended_at. This covers the status-update path:
-- providers call UpdateBackgroundTaskStatus(terminal) then CloseBackgroundTask,
-- but the close early-returns on IsTerminal(), so ended_at was never stamped.
-- The status filter makes this idempotent (only terminal rows qualify), so a
-- transient failure here self-corrects on the next terminal write (or the boot
-- sweep): the status is already terminal in both DB and cache, and a NULL
-- ended_at reads back as the zero time, matching the cache's untouched value.
-- Kept as a separate query because sqlc cannot infer the type of a positional
-- parameter that appears only inside a CASE WHEN condition (a `? IN (...)`
-- predicate), so the atomic single-statement form is not generatable with this
-- toolchain version. The close path (CloseAgentBackgroundTask) sets both
-- status and ended_at in one statement because its WHERE guard
-- (status IN pending/running) is a literal, not a parameter.
-- name: StampAgentBackgroundTaskEndedAt :exec
UPDATE agent_background_tasks SET
    ended_at   = ?
WHERE owner_agent_id = ? AND row_key = ?
  AND ended_at IS NULL
  AND status IN ('completed','failed','stopped','interrupted');

-- CloseAgentBackgroundTask stamps the terminal status and ended_at. The
-- status-IN filter means a terminal row can never be resurrected or re-closed
-- by a late/duplicate event.
-- name: CloseAgentBackgroundTask :exec
UPDATE agent_background_tasks SET
    status     = ?,
    ended_at   = ?,
    updated_at = ?
WHERE owner_agent_id = ? AND row_key = ? AND status IN ('pending','running');

-- name: DeleteAgentBackgroundTaskByRowKey :execresult
DELETE FROM agent_background_tasks WHERE owner_agent_id = ? AND row_key = ?;

-- RenameAgentBackgroundTask re-keys a row (owner_agent_id, old_row_key) to
-- new_row_key. The row_key is the provider linkage key; a provider that learns
-- the stable child id only on the terminal update (OpenCode: the row opens under
-- the toolCallId and the session id surfaces late) renames so a single row
-- tracks the whole lifecycle instead of a spawn row + a separately-keyed row.
-- No status-IN guard: a rename is key-only and applies to any row state.
-- name: RenameAgentBackgroundTask :execrows
UPDATE agent_background_tasks SET row_key = ? WHERE owner_agent_id = ? AND row_key = ?;

-- GetAgentBackgroundTaskByChildAgentID is the reverse lookup behind
-- send-to-subagent and interrupt routing: child agent id -> (owner, row_key).
-- name: GetAgentBackgroundTaskByChildAgentID :one
SELECT * FROM agent_background_tasks WHERE child_agent_id = ?;

-- MarkAgentBackgroundTasksEnded terminalizes every still-active row owned by
-- an agent (used on clean process exit). Returns the affected-row count so the
-- caller can skip the broadcast when nothing moved.
-- name: MarkAgentBackgroundTasksEnded :execrows
UPDATE agent_background_tasks SET
    status     = ?,
    ended_at   = ?,
    updated_at = ?
WHERE owner_agent_id = ? AND status IN ('pending','running');

-- MarkAllActiveAgentBackgroundTasksInterrupted runs at worker boot before any
-- caches exist: every active row left over from the previous process is
-- honestly labeled "interrupted" (the worker restarted and cut the task off).
-- name: MarkAllActiveAgentBackgroundTasksInterrupted :execrows
UPDATE agent_background_tasks SET
    status     = 'interrupted',
    ended_at   = ?,
    updated_at = ?
WHERE status IN ('pending','running');
