-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, org_id, owner_user_id, title)
VALUES ($1, $2, $3, $4);

-- name: GetWorkspaceByID :one
SELECT * FROM workspaces WHERE id = $1 AND is_deleted = FALSE;

-- name: GetWorkspaceByIDIncludeDeleted :one
SELECT * FROM workspaces WHERE id = $1;

-- name: ListWorkspacesByIDs :many
SELECT * FROM workspaces
WHERE id = ANY(sqlc.arg(workspace_ids)::TEXT[])
  AND is_deleted = FALSE;

-- name: ListAccessibleWorkspaces :many
-- Secondary sort on `id` is the deterministic tiebreaker: created_at is
-- only millisecond-precision (see CURRENT_TIMESTAMP3 / strftime), and
-- the SELECT DISTINCT over the LEFT JOIN to workspace_access lets the
-- planner pick its own row order for ties. Without the tiebreaker the
-- sidebar shuffles workspaces created in the same millisecond on every
-- refresh -- most reproducibly for fresh accounts whose seed
-- workspaces land in a batch.
SELECT DISTINCT w.* FROM workspaces w
LEFT JOIN workspace_access wa ON w.id = wa.workspace_id AND wa.user_id = sqlc.arg(user_id)
WHERE w.is_deleted = FALSE
  AND w.org_id = sqlc.arg(org_id)
  AND (w.owner_user_id = sqlc.arg(user_id) OR wa.user_id IS NOT NULL)
ORDER BY w.created_at DESC, w.id DESC;

-- name: RenameWorkspace :execresult
UPDATE workspaces SET title = $1 WHERE id = $2 AND owner_user_id = $3;

-- name: SoftDeleteWorkspace :execresult
UPDATE workspaces SET is_deleted = TRUE, deleted_at = NOW() WHERE id = $1 AND owner_user_id = $2;

-- name: SoftDeleteAllWorkspacesByUser :exec
UPDATE workspaces SET is_deleted = TRUE, deleted_at = NOW() WHERE owner_user_id = $1 AND is_deleted = FALSE;

-- name: HardDeleteWorkspacesBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
WITH to_delete AS (
    SELECT w.id FROM workspaces w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < $1 LIMIT 1000
)
DELETE FROM workspaces WHERE id IN (SELECT id FROM to_delete);
