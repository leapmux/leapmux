-- name: ListAgentBackgroundTasks :many
SELECT * FROM agent_background_tasks WHERE owner_agent_id = ? ORDER BY seq LIMIT ?;

-- UpsertAgentBackgroundTask inserts a new row or updates an existing one keyed
-- by (owner_agent_id, row_key). seq/created_at are set only on insert (never
-- overwritten on update); the CASE on child_agent_id means a later upsert that
-- omits the id can never blank one recorded earlier.
-- name: UpsertAgentBackgroundTask :exec
INSERT INTO agent_background_tasks (
    owner_agent_id, row_key, seq, kind, child_agent_id, parent_agent_id,
    group_key, group_label, title, description, active_form, status, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'), strftime('%Y-%m-%dT%H:%M:%fZ','now')
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
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE owner_agent_id = ? AND row_key = ?;

-- StampAgentBackgroundTaskEndedAt sets ended_at on a row that is already
-- terminal but still has a NULL ended_at. This covers the status-update path:
-- providers call UpdateBackgroundTaskStatus(terminal) then CloseBackgroundTask,
-- but the close early-returns on IsTerminal(), so ended_at was never stamped.
-- The status filter makes this idempotent (only terminal rows qualify).
-- name: StampAgentBackgroundTaskEndedAt :exec
UPDATE agent_background_tasks SET
    ended_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE owner_agent_id = ? AND row_key = ?
  AND ended_at IS NULL
  AND status IN ('completed','failed','stopped','interrupted');

-- CloseAgentBackgroundTask stamps the terminal status and ended_at. The
-- status-IN filter means a terminal row can never be resurrected or re-closed
-- by a late/duplicate event.
-- name: CloseAgentBackgroundTask :exec
UPDATE agent_background_tasks SET
    status     = ?,
    ended_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE owner_agent_id = ? AND row_key = ? AND status IN ('pending','running');

-- name: DeleteAgentBackgroundTaskByRowKey :execresult
DELETE FROM agent_background_tasks WHERE owner_agent_id = ? AND row_key = ?;

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
    ended_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE owner_agent_id = ? AND status IN ('pending','running');

-- MarkAllActiveAgentBackgroundTasksInterrupted runs at worker boot before any
-- caches exist: every active row left over from the previous process is
-- honestly labeled "interrupted" (the worker restarted and cut the task off).
-- name: MarkAllActiveAgentBackgroundTasksInterrupted :execrows
UPDATE agent_background_tasks SET
    status     = 'interrupted',
    ended_at   = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE status IN ('pending','running');
