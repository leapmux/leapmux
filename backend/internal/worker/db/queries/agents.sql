-- name: CreateAgent :exec
INSERT INTO agents (id, workspace_id, working_dir, home_dir, title, model, system_prompt, effort, permission_mode, extra_settings, agent_provider, resumed) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

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

-- UpdateAgentConfirmedSettingsPreservingStartedSettings is used by
-- asynchronous startup. It persists provider-reported catalogs and
-- confirmed settings, but only overwrites settings columns that still
-- match the values used to start the subprocess. If the user changed a
-- setting while startup was finishing, preserve that newer DB value.
-- name: UpdateAgentConfirmedSettingsPreservingStartedSettings :one
UPDATE agents SET
  model = CASE
    WHEN model = sqlc.arg(started_model) THEN sqlc.arg(confirmed_model)
    ELSE model
  END,
  effort = CASE
    WHEN effort = sqlc.arg(started_effort) THEN sqlc.arg(confirmed_effort)
    ELSE effort
  END,
  permission_mode = CASE
    WHEN permission_mode = sqlc.arg(started_permission_mode) THEN sqlc.arg(confirmed_permission_mode)
    ELSE permission_mode
  END,
  extra_settings = CASE
    WHEN extra_settings = sqlc.arg(started_extra_settings) THEN sqlc.arg(confirmed_extra_settings)
    ELSE extra_settings
  END,
  available_models = sqlc.arg(available_models),
  available_option_groups = sqlc.arg(available_option_groups)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetAgentStartupError :exec
UPDATE agents SET startup_error = ? WHERE id = ?;

-- name: UpdateAgentHomeDir :exec
UPDATE agents SET home_dir = ? WHERE id = ?;

-- name: UpdateAgentPlanFilePath :exec
UPDATE agents SET plan_file_path = ? WHERE id = ?;

-- name: UpdateAgentPlan :exec
UPDATE agents SET plan_file_path = ?, plan_title = ? WHERE id = ?;

-- name: UpdateAgentPlanAndTitle :exec
UPDATE agents SET plan_file_path = ?, plan_title = ?, title = ? WHERE id = ?;

-- name: GetAgentWorkspaceID :one
SELECT workspace_id FROM agents WHERE id = ?;

-- name: UpdateAgentWorkspace :exec
UPDATE agents SET workspace_id = ? WHERE id = ?;

-- name: ListAgentsByIDs :many
SELECT * FROM agents WHERE id IN (sqlc.slice('ids')) AND closed_at IS NULL;

-- name: DeleteClosedAgentsBefore :execresult
DELETE FROM agents WHERE rowid IN (SELECT a.rowid FROM agents a WHERE a.closed_at < ? LIMIT 1000);

-- ListAgentIDsWithPlanInDir returns the IDs of agents whose plan_file_path
-- begins with the provided literal byte sequence. Used by the plan-archive
-- task to skip year directories that still hold an active agent's plan.
-- instr() is used (not LIKE / GLOB) so data dirs containing wildcard
-- metacharacters cannot produce false positives or false negatives.
-- name: ListAgentIDsWithPlanInDir :many
SELECT id FROM agents WHERE instr(plan_file_path, ?) = 1;
