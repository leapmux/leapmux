-- name: GrantWorkspaceAccess :exec
INSERT IGNORE INTO workspace_access (workspace_id, user_id) VALUES (?, ?);

-- name: RevokeWorkspaceAccess :exec
DELETE FROM workspace_access WHERE workspace_id = ? AND user_id = ?;

-- name: ListWorkspaceAccessByWorkspaceID :many
SELECT * FROM workspace_access WHERE workspace_id = ? ORDER BY created_at;

-- name: ClearWorkspaceAccess :exec
DELETE FROM workspace_access WHERE workspace_id = ?;

-- name: HasWorkspaceAccess :one
SELECT COUNT(*) > 0 AS has_access FROM workspace_access
WHERE workspace_id = ? AND user_id = ?;
