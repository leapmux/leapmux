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
-- only millisecond-precision (see CURRENT_TIMESTAMP3 / strftime), so
-- workspaces created in the same millisecond would otherwise shuffle on
-- every refresh -- most reproducibly for fresh accounts whose seed
-- workspaces land in a batch.
SELECT w.* FROM workspaces w
WHERE w.is_deleted = FALSE
  AND w.org_id = sqlc.arg(org_id)
  AND w.owner_user_id = sqlc.arg(user_id)
ORDER BY w.created_at DESC, w.id DESC;

-- name: RenameWorkspace :execresult
UPDATE workspaces SET title = $1 WHERE id = $2 AND owner_user_id = $3;

-- name: SoftDeleteWorkspace :execresult
-- The is_deleted = FALSE guard makes a concurrent delete racing this one match
-- zero rows, so the service's rows-affected NotFound check fires for the loser
-- instead of reporting success for a workspace the winner already deleted
-- (and queueing a second lifecycle-outbox row / channel-close pass). Matches
-- SoftDeleteAllWorkspacesByUser's guard.
UPDATE workspaces SET is_deleted = TRUE, deleted_at = NOW() WHERE id = $1 AND owner_user_id = $2 AND is_deleted = FALSE;

-- name: SoftDeleteAllWorkspacesByUser :exec
UPDATE workspaces SET is_deleted = TRUE, deleted_at = NOW() WHERE owner_user_id = $1 AND is_deleted = FALSE;

-- name: HardDeleteWorkspacesBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
WITH to_delete AS (
    SELECT w.id FROM workspaces w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < $1 LIMIT 1000
)
DELETE FROM workspaces WHERE id IN (SELECT id FROM to_delete);
