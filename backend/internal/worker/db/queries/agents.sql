-- name: CreateAgent :exec
INSERT INTO agents (id, workspace_id, working_dir, home_dir, title, model, system_prompt, effort, extra_settings, agent_provider, resumed) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

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
UPDATE agents SET agent_session_id = ?, session_start_seq = (SELECT COALESCE(MAX(m.seq), 0) FROM messages m WHERE m.agent_id = agents.id) WHERE agents.id = ?;

-- name: ReopenAgent :exec
UPDATE agents SET closed_at = NULL WHERE id = ?;

-- name: SetAgentPermissionMode :exec
UPDATE agents SET permission_mode = ? WHERE id = ?;

-- name: UpdateAgentModelAndEffort :exec
UPDATE agents SET model = ?, effort = ? WHERE id = ?;

-- name: SetAgentExtraSettings :exec
UPDATE agents SET extra_settings = ? WHERE id = ?;

-- name: UpdateAgentAllSettings :exec
UPDATE agents SET model = ?, effort = ?, permission_mode = ?, extra_settings = ? WHERE id = ?;

-- UpdateAgentConfirmedSettings persists both the confirmed request
-- settings (model/effort/permissionMode/extraSettings) and the
-- provider-reported catalogs (availableModels/availableOptionGroups)
-- in a single UPDATE, returning the full row. Saves the trailing
-- SELECT that would otherwise be needed to build the ACTIVE broadcast.
-- name: UpdateAgentConfirmedSettings :one
UPDATE agents SET
  model = ?,
  effort = ?,
  permission_mode = ?,
  extra_settings = ?,
  available_models = ?,
  available_option_groups = ?
WHERE id = ?
RETURNING *;

-- name: SetAgentStartupError :exec
UPDATE agents SET startup_error = ? WHERE id = ?;

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
DELETE FROM agents WHERE rowid IN (SELECT a.rowid FROM agents a WHERE a.closed_at < ? LIMIT 1000);
