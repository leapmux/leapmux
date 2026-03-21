-- name: CreateAgent :exec
INSERT INTO agents (id, workspace_id, working_dir, home_dir, title, model, system_prompt, effort, codex_sandbox_policy, codex_network_access, codex_collaboration_mode, agent_provider, resumed) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAgentByID :one
SELECT * FROM agents WHERE id = ?;

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

-- name: SetAgentCodexSandboxPolicy :exec
UPDATE agents SET codex_sandbox_policy = ? WHERE id = ?;

-- name: SetAgentCodexNetworkAccess :exec
UPDATE agents SET codex_network_access = ? WHERE id = ?;

-- name: SetAgentCodexCollaborationMode :exec
UPDATE agents SET codex_collaboration_mode = ? WHERE id = ?;

-- name: UpdateAgentAllSettings :exec
UPDATE agents SET model = ?, effort = ?, permission_mode = ?, codex_sandbox_policy = ?, codex_network_access = ?, codex_collaboration_mode = ? WHERE id = ?;

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

-- name: ListAgentsByIDs :many
SELECT * FROM agents WHERE id IN (sqlc.slice('ids')) AND closed_at IS NULL;

-- name: DeleteClosedAgentsBefore :execresult
DELETE FROM agents WHERE closed_at < ?;
