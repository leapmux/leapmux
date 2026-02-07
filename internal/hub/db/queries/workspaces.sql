-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, org_id, created_by, title)
VALUES (?, ?, ?, ?);

-- name: GetWorkspaceByID :one
SELECT * FROM workspaces WHERE id = ? AND org_id = ?;

-- name: GetWorkspaceByIDInternal :one
SELECT * FROM workspaces WHERE id = ?;

-- name: ListWorkspacesByOrgID :many
SELECT * FROM workspaces
WHERE org_id = sqlc.arg(org_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: ListVisibleWorkspaces :many
SELECT DISTINCT w.* FROM workspaces w
LEFT JOIN workspace_shares ws ON w.id = ws.workspace_id AND ws.user_id = sqlc.arg(user_id)
WHERE w.org_id = sqlc.arg(org_id)
  AND w.is_deleted = 0
  AND (
    w.created_by = sqlc.arg(user_id)
    OR w.share_mode = 2
    OR (w.share_mode = 3 AND ws.user_id IS NOT NULL)
  )
ORDER BY w.created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: GetVisibleWorkspace :one
SELECT DISTINCT w.* FROM workspaces w
LEFT JOIN workspace_shares ws ON w.id = ws.workspace_id AND ws.user_id = sqlc.arg(user_id)
WHERE w.id = sqlc.arg(workspace_id)
  AND w.org_id = sqlc.arg(org_id)
  AND w.is_deleted = 0
  AND (
    w.created_by = sqlc.arg(user_id)
    OR w.share_mode = 2
    OR (w.share_mode = 3 AND ws.user_id IS NOT NULL)
  );

-- name: RenameWorkspace :execresult
UPDATE workspaces SET title = ? WHERE id = ? AND created_by = ?;

-- name: SoftDeleteWorkspace :execresult
UPDATE workspaces SET is_deleted = 1 WHERE id = ? AND created_by = ?;

-- name: UpdateWorkspaceShareMode :execresult
UPDATE workspaces SET share_mode = ? WHERE id = ? AND created_by = ?;

