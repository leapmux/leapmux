-- name: CreateAgent :exec
INSERT INTO agents (id, workspace_id, working_dir, home_dir, title, options, agent_provider, resumed) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

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

-- name: SetAgentOptions :exec
UPDATE agents SET options = ? WHERE id = ?;

-- SetAgentOptionGroups persists only the provider-reported catalog (option_groups),
-- leaving the chosen option values untouched. Used when a running ACP provider discovers
-- its catalog (e.g. a dynamic model list reported only after the session/new handshake)
-- after the startup handoff already persisted a narrower one, so the post-exit offline
-- read surfaces the discovered options instead of a stale static fallback.
-- name: SetAgentOptionGroups :exec
UPDATE agents SET option_groups = ? WHERE id = ?;

-- SetAgentOptionsIfUnchanged is a compare-and-swap: it overwrites options only when
-- the column still equals expected_options -- the snapshot the new value was merged
-- from. Returns the number of rows changed (0 when the row moved on between the read
-- and the write), so a concurrent PersistSettingsRefresh can re-read, re-merge, and
-- retry instead of clobbering the other writer's keys with a stale full-map blob.
-- name: SetAgentOptionsIfUnchanged :execrows
UPDATE agents SET options = sqlc.arg(options)
WHERE id = sqlc.arg(id) AND options = sqlc.arg(expected_options);

-- SetAgentOptionGroupsIfUnchanged is the compare-and-swap form of SetAgentOptionGroups: it
-- overwrites the provider-reported catalog only while the column still equals
-- expected_option_groups -- the snapshot the new catalog is replacing. A running ACP provider
-- that discovers a richer catalog (e.g. a dynamic model list reported only after the
-- session/new handshake) and persists it via SetAgentOptionGroups on a separate, unsynchronized
-- path must not be clobbered by a (re)start handoff's narrower catalog: when the column moved on
-- (option_groups != expected_option_groups) this write is a no-op and the newer catalog is kept.
-- It is the standalone mirror of the option_groups CASE in
-- UpdateAgentConfirmedSettingsPreservingStartedSettings, for the synchronous
-- persistConfirmedAgentSettings path that writes the catalog separately from the options CAS.
-- Returns the number of rows changed (0 when the catalog moved on).
-- name: SetAgentOptionGroupsIfUnchanged :execrows
UPDATE agents SET option_groups = sqlc.arg(option_groups)
WHERE id = sqlc.arg(id) AND option_groups = sqlc.arg(expected_option_groups);

-- UpdateAgentConfirmedSettingsPreservingStartedSettings is used by
-- asynchronous startup. It persists the provider-reported catalog and the
-- confirmed option values via a compare-and-swap: the options column is only
-- overwritten when it still equals expected_options -- the row snapshot the
-- confirmed_options blob was derived from. confirmed_options already folds in
-- any setting the user changed during startup, so writing it when the row still
-- matches preserves those edits AND applies the provider's resolutions; a newer
-- change that landed after the snapshot (row != expected_options) is left
-- untouched.
-- The option_groups (catalog) column is CAS-guarded INDEPENDENTLY against
-- expected_option_groups: a running ACP provider that discovers its dynamic model
-- list AFTER this handoff and persists it via SetAgentOptionGroups must not be
-- clobbered by the (now narrower) startup catalog. Both writers touch only the
-- option_groups column with no shared lock, so without this guard a late-landing
-- handoff would overwrite the richer discovered catalog. When the catalog moved on
-- (option_groups != expected_option_groups) we keep the newer one.
-- name: UpdateAgentConfirmedSettingsPreservingStartedSettings :one
UPDATE agents SET
  options = CASE
    WHEN options = sqlc.arg(expected_options) THEN sqlc.arg(confirmed_options)
    ELSE options
  END,
  option_groups = CASE
    WHEN option_groups = sqlc.arg(expected_option_groups) THEN sqlc.arg(option_groups)
    ELSE option_groups
  END
WHERE id = sqlc.arg(id)
RETURNING *;

-- UpdateAgentConfirmedSettings atomically writes the confirmed options blob AND the provider
-- option-group catalog in ONE statement, for the synchronous persistConfirmedAgentSettings path, so
-- a concurrent options writer can't land BETWEEN two separate column writes and leave the row
-- showing this handoff's options beside a foreign catalog. The options column is a compare-and-swap
-- on expected_options (the snapshot the blob was merged from); the caller retries on a miss
-- (the returned options != options). The option_groups column is written ONLY on that SAME
-- successful options CAS (gated on options = expected_options) AND while it still equals
-- expected_option_groups, so the two columns move together-or-neither -- a richer catalog a running
-- provider discovered concurrently (option_groups != expected_option_groups) is preserved, and a
-- lost options CAS writes nothing (the caller re-merges and retries, keeping both atomic). Pass
-- option_groups = '' with expected_option_groups = '' to leave the catalog untouched (a no-op write,
-- e.g. when its marshal failed).
-- name: UpdateAgentConfirmedSettings :one
UPDATE agents SET
  options = CASE
    WHEN options = sqlc.arg(expected_options) THEN sqlc.arg(options)
    ELSE options
  END,
  option_groups = CASE
    WHEN options = sqlc.arg(expected_options) AND option_groups = sqlc.arg(expected_option_groups)
    THEN sqlc.arg(option_groups)
    ELSE option_groups
  END
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
