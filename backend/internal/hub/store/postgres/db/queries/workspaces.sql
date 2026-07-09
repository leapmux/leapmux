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

-- name: ListAllAccessibleWorkspaces :many
-- Every non-deleted workspace the user can read -- owner OR explicit grant --
-- across ALL orgs. The org-unfiltered counterpart of ListAccessibleWorkspaces:
-- it surfaces workspaces shared with a user who is not a member of the owning
-- org (cross-org collaboration) alongside the user's own workspaces in every org.
--
-- Built as a UNION of two index-driven seeks instead of one predicate over a
-- LEFT JOIN. The old form ORed a base-table column against a LEFT-JOIN column
-- (w.owner_user_id = ? OR wa.user_id IS NOT NULL), which no single index can
-- satisfy, so the planner full-scanned the workspaces table. Splitting the OR
-- lets each branch seek: the owner branch on idx_workspaces_owner_user_id, the
-- grant branch on idx_workspace_access_user_id (joined back to workspaces by
-- primary key). UNION (not UNION ALL) collapses a workspace the user both owns
-- and was granted to a single row, preserving the old SELECT DISTINCT semantics.
-- The trailing ORDER BY ranks the union result, so it names the output columns
-- (created_at, id) rather than w.*; id is the deterministic tiebreaker for rows
-- sharing a millisecond created_at.
SELECT w.* FROM workspaces w
WHERE w.is_deleted = FALSE
  AND w.owner_user_id = sqlc.arg(user_id)
UNION
SELECT w.* FROM workspaces w
INNER JOIN workspace_access wa ON w.id = wa.workspace_id
WHERE w.is_deleted = FALSE
  AND wa.user_id = sqlc.arg(user_id)
ORDER BY created_at DESC, id DESC;

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
