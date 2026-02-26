-- name: CreateAgent :exec
INSERT INTO agents (id, workspace_id, worker_id, working_dir, home_dir, title, model, system_prompt, effort) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentByID :one
SELECT * FROM agents WHERE id = ?;

-- name: ListAgentsByWorkspaceID :many
SELECT * FROM agents WHERE workspace_id = ? ORDER BY created_at ASC;

-- name: ListActiveAgentIDsByWorkspaceID :many
SELECT id FROM agents WHERE workspace_id = ? AND status = 1;

-- name: ListActiveAgentIDsByWorker :many
SELECT id FROM agents WHERE worker_id = ? AND status = 1;

-- name: ListAgentsByWorker :many
SELECT * FROM agents WHERE worker_id = ? ORDER BY created_at ASC;

-- name: CloseAgent :exec
UPDATE agents SET status = 2, closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: CloseActiveAgentsByWorkspace :exec
UPDATE agents SET status = 2, closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE workspace_id = ? AND status = 1;

-- name: CloseActiveAgentsByWorker :exec
UPDATE agents SET status = 2, closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE worker_id = ? AND status = 1;

-- name: CloseAllActiveAgents :exec
UPDATE agents SET status = 2, closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE status = 1;

-- name: RenameAgent :execresult
UPDATE agents SET title = ? WHERE id = ?;

-- name: UpdateAgentSessionID :exec
UPDATE agents SET agent_session_id = ? WHERE id = ?;

-- name: ReopenAgent :exec
UPDATE agents SET status = 1, closed_at = NULL WHERE id = ?;

-- name: SetAgentPermissionMode :exec
UPDATE agents SET permission_mode = ? WHERE id = ?;

-- name: UpdateAgentModelAndEffort :exec
UPDATE agents SET model = ?, effort = ? WHERE id = ?;

-- name: UpdateAgentHomeDir :exec
UPDATE agents SET home_dir = ? WHERE id = ?;
