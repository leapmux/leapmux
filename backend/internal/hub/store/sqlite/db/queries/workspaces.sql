-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, org_id, owner_user_id, title)
VALUES (?, ?, ?, ?);

-- name: GetWorkspaceByID :one
SELECT * FROM workspaces WHERE id = ? AND is_deleted = 0;

-- name: GetWorkspaceByIDIncludeDeleted :one
SELECT * FROM workspaces WHERE id = ?;

-- name: ListWorkspacesByIDs :many
SELECT * FROM workspaces
WHERE id IN (sqlc.slice('workspace_ids'))
  AND is_deleted = 0;

-- name: ListAccessibleWorkspaces :many
-- Secondary sort on `id` is the deterministic tiebreaker: created_at is
-- only millisecond-precision (see CURRENT_TIMESTAMP3 / strftime('%Y-%m-%dT%H:%M:%fZ')),
-- so workspaces created in the same millisecond would otherwise shuffle
-- on every refresh -- most reproducibly for fresh accounts whose seed
-- workspaces land in a batch.
SELECT w.* FROM workspaces w
WHERE w.is_deleted = 0
  AND w.org_id = sqlc.arg(org_id)
  AND w.owner_user_id = sqlc.arg(user_id)
ORDER BY w.created_at DESC, w.id DESC;

-- name: RenameWorkspace :execresult
UPDATE workspaces SET title = ? WHERE id = ? AND owner_user_id = ?;

-- name: SoftDeleteWorkspace :execresult
-- The is_deleted = 0 guard makes a concurrent delete racing this one match zero
-- rows, so the service's rows-affected NotFound check fires for the loser
-- instead of reporting success for a workspace the winner already deleted
-- (and queueing a second lifecycle-outbox row / channel-close pass). Matches
-- SoftDeleteAllWorkspacesByUser's guard.
UPDATE workspaces SET is_deleted = 1, deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND owner_user_id = ? AND is_deleted = 0;

-- name: SoftDeleteAllWorkspacesByUser :exec
UPDATE workspaces SET is_deleted = 1, deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE owner_user_id = ? AND is_deleted = 0;

-- name: HardDeleteWorkspacesBefore :execresult
DELETE FROM workspaces WHERE rowid IN (SELECT w.rowid FROM workspaces w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < ? LIMIT 1000);
