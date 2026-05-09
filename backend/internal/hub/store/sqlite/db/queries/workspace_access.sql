-- name: GrantWorkspaceAccess :exec
INSERT INTO workspace_access (workspace_id, user_id) VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: RevokeWorkspaceAccess :exec
DELETE FROM workspace_access WHERE workspace_id = ? AND user_id = ?;

-- name: ListWorkspaceAccessByWorkspaceID :many
SELECT * FROM workspace_access WHERE workspace_id = ? ORDER BY created_at;

-- name: ClearWorkspaceAccess :exec
DELETE FROM workspace_access WHERE workspace_id = ?;

-- name: HasWorkspaceAccess :one
SELECT COUNT(*) > 0 AS has_access FROM workspace_access
WHERE workspace_id = ? AND user_id = ?;

-- name: ListWorkspaceAccessForUserIn :many
SELECT workspace_id FROM workspace_access
WHERE user_id = sqlc.arg(user_id)
  AND workspace_id IN (sqlc.slice('workspace_ids'));
