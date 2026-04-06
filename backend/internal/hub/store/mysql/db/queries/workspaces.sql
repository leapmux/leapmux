-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, org_id, owner_user_id, title)
VALUES (?, ?, ?, ?);

-- name: GetWorkspaceByID :one
SELECT * FROM workspaces WHERE id = ? AND is_deleted = 0;

-- name: GetWorkspaceByIDIncludeDeleted :one
SELECT * FROM workspaces WHERE id = ?;

-- name: ListAccessibleWorkspaces :many
SELECT DISTINCT w.* FROM workspaces w
LEFT JOIN workspace_access wa ON w.id = wa.workspace_id AND wa.user_id = sqlc.arg(user_id)
WHERE w.is_deleted = 0
  AND w.org_id = sqlc.arg(org_id)
  AND (w.owner_user_id = sqlc.arg(user_id) OR wa.user_id IS NOT NULL)
ORDER BY w.created_at DESC;

-- name: RenameWorkspace :execresult
UPDATE workspaces SET title = ? WHERE id = ? AND owner_user_id = ?;

-- name: SoftDeleteWorkspace :execresult
UPDATE workspaces SET is_deleted = 1, deleted_at = NOW(3) WHERE id = ? AND owner_user_id = ?;

-- name: SoftDeleteAllWorkspacesByUser :exec
UPDATE workspaces SET is_deleted = 1, deleted_at = NOW(3) WHERE owner_user_id = ? AND is_deleted = 0;

-- name: HardDeleteWorkspacesBefore :execresult
DELETE FROM workspaces WHERE id IN (SELECT w.id FROM (SELECT workspaces.id FROM workspaces WHERE workspaces.deleted_at IS NOT NULL AND workspaces.deleted_at < ? LIMIT 1000) w);
