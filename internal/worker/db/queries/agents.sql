-- name: CreateAgent :exec
INSERT INTO agents (id, workspace_id, working_dir, home_dir, title, model, system_prompt, effort) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentByID :one
SELECT * FROM agents WHERE id = ?;

-- name: ListAgentsByWorkspaceID :many
SELECT * FROM agents WHERE workspace_id = ? AND closed_at IS NULL ORDER BY created_at ASC;

-- name: ListOpenAgentIDsByWorkspaceID :many
SELECT id FROM agents WHERE workspace_id = ? AND closed_at IS NULL;

-- name: ListAllOpenAgentIDs :many
SELECT id FROM agents WHERE closed_at IS NULL;

-- name: ListAllAgentIDsAndWorkspaces :many
SELECT id, workspace_id FROM agents;

-- name: CloseAgent :exec
UPDATE agents SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: CloseOpenAgentsByWorkspace :exec
UPDATE agents SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE workspace_id = ? AND closed_at IS NULL;

-- name: RenameAgent :execresult
UPDATE agents SET title = ? WHERE id = ?;

-- name: UpdateAgentSessionID :exec
UPDATE agents SET agent_session_id = ? WHERE id = ?;

-- name: ReopenAgent :exec
UPDATE agents SET closed_at = NULL WHERE id = ?;

-- name: SetAgentPermissionMode :exec
UPDATE agents SET permission_mode = ? WHERE id = ?;

-- name: UpdateAgentModelAndEffort :exec
UPDATE agents SET model = ?, effort = ? WHERE id = ?;

-- name: UpdateAgentHomeDir :exec
UPDATE agents SET home_dir = ? WHERE id = ?;

-- name: UpdateAgentPlanFilePath :exec
UPDATE agents SET plan_file_path = ? WHERE id = ?;

-- name: UpdateAgentPlanContent :exec
UPDATE agents SET plan_content = ?, plan_content_compression = ? WHERE id = ?;

-- name: UpdateAgentPlan :exec
UPDATE agents SET plan_file_path = ?, plan_content = ?, plan_content_compression = ?, plan_title = ? WHERE id = ?;

-- name: UpdateAgentPlanAndTitle :exec
UPDATE agents SET plan_file_path = ?, plan_content = ?, plan_content_compression = ?, plan_title = ?, title = ? WHERE id = ?;

-- name: GetAgentWorkspaceID :one
SELECT workspace_id FROM agents WHERE id = ?;

-- name: UpdateAgentWorkspace :exec
UPDATE agents SET workspace_id = ? WHERE id = ?;

-- name: DeleteClosedAgentsBefore :execresult
DELETE FROM agents WHERE closed_at < ?;
