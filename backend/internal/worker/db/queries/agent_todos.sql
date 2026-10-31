-- ListAgentTodosNewestFirst loads the cold-start seed for one agent's to-do registry.
-- DESC for the same reason as ListAgentBackgroundTasksNewestFirst: the registry is capped
-- and evicts the oldest final row, so the seed must take the NEWEST rows. The
-- caller reverses the result to restore ascending seq order.
-- name: ListAgentTodosNewestFirst :many
SELECT * FROM agent_todos WHERE agent_id = ? ORDER BY seq DESC LIMIT ?;

-- InsertAgentTodo adds one row at the seq the registry cache allocates. A plain
-- INSERT, not an upsert: the cache mirrors the table, so a row that already
-- holds the seq or the task id is a divergence, and the primary key or
-- idx_agent_todos_task_id makes it fail instead of absorbing it.
-- name: InsertAgentTodo :exec
INSERT INTO agent_todos (agent_id, seq, task_id, content, active_form, description, status)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- UpdateAgentTodo overwrites every column with the caller-merged value.
-- The handler reads the row first and applies the Patch's nil-or-set
-- semantics in app code, so the caller is responsible for passing the
-- existing value when a field should stay unchanged. It reports the rows it
-- changed, so the caller can refuse an update of a row that is gone.
-- name: UpdateAgentTodo :execrows
UPDATE agent_todos SET content = ?, active_form = ?, description = ?, status = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE agent_id = ? AND seq = ?;

-- DeleteAgentTodoBySeq deletes one row, the one a cap eviction chose.
-- name: DeleteAgentTodoBySeq :exec
DELETE FROM agent_todos WHERE agent_id = ? AND seq = ?;

-- name: DeleteAllAgentTodos :exec
DELETE FROM agent_todos WHERE agent_id = ?;
