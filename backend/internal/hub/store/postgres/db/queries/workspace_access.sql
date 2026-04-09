-- name: GrantWorkspaceAccess :exec
INSERT INTO workspace_access (workspace_id, user_id) VALUES ($1, $2)
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
