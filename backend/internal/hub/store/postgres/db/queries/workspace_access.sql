-- name: GrantWorkspaceAccess :exec
INSERT INTO workspace_access (workspace_id, user_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: BulkGrantWorkspaceAccess :exec
-- Single-statement bulk grant via UNNEST. Caller passes parallel
-- arrays; ON CONFLICT skips rows that already exist.
WITH input AS (
  SELECT
    UNNEST(sqlc.arg(workspace_ids)::TEXT[]) AS workspace_id,
    UNNEST(sqlc.arg(user_ids)::TEXT[]) AS user_id
)
INSERT INTO workspace_access (workspace_id, user_id)
SELECT workspace_id, user_id FROM input
ON CONFLICT DO NOTHING;

-- name: RevokeWorkspaceAccess :exec
DELETE FROM workspace_access WHERE workspace_id = $1 AND user_id = $2;

-- name: ListWorkspaceAccessByWorkspaceID :many
SELECT * FROM workspace_access WHERE workspace_id = $1 ORDER BY created_at;

-- name: ClearWorkspaceAccess :exec
DELETE FROM workspace_access WHERE workspace_id = $1;

-- name: HasWorkspaceAccess :one
SELECT COUNT(*) > 0 AS has_access FROM workspace_access
WHERE workspace_id = $1 AND user_id = $2;

-- name: ListWorkspaceAccessForUserIn :many
SELECT workspace_id FROM workspace_access
WHERE user_id = sqlc.arg(user_id)
  AND workspace_id = ANY(sqlc.arg(workspace_ids)::TEXT[]);
