-- name: ListAgentTodos :many
SELECT * FROM agent_todos WHERE agent_id = ? ORDER BY seq LIMIT ?;

-- UpsertAgentTodo inserts a new row or updates an existing one keyed by
-- (agent_id, row_key). Used by `create` and `detail` events (Claude
-- TaskCreate / TaskGet) where the row is addressed by task_id.
-- name: UpsertAgentTodo :exec
INSERT INTO agent_todos (
    agent_id, row_key, seq, task_id, content, active_form, description, status, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now')
)
ON CONFLICT(agent_id, row_key) DO UPDATE SET
    task_id     = excluded.task_id,
    content     = excluded.content,
    active_form = excluded.active_form,
    description = excluded.description,
    status      = excluded.status,
    updated_at  = excluded.updated_at;

-- UpdateAgentTodo overwrites every column with the caller-merged value.
-- The handler reads the row first and applies the Patch's nil-or-set
-- semantics in app code, so the caller is responsible for passing the
-- existing value when a field should stay unchanged.
-- name: UpdateAgentTodo :exec
UPDATE agent_todos SET
    content     = ?,
    active_form = ?,
    description = ?,
    status      = ?,
    updated_at  = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE agent_id = ? AND row_key = ?;

-- DeleteAgentTodoByRowKey returns the affected-row count so the caller
-- can short-circuit the broadcast on an unknown-id no-op delete.
-- name: DeleteAgentTodoByRowKey :execresult
DELETE FROM agent_todos WHERE agent_id = ? AND row_key = ?;

-- name: DeleteAllAgentTodos :exec
DELETE FROM agent_todos WHERE agent_id = ?;

-- name: InsertAgentTodo :exec
INSERT INTO agent_todos (
    agent_id, row_key, seq, task_id, content, active_form, description, status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
