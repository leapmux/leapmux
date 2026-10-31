-- name: CreateWorkspace :exec
INSERT INTO workspaces (id, owner_user_id, title)
VALUES (?, ?, ?);

-- name: GetWorkspaceByID :one
SELECT * FROM workspaces WHERE id = ? AND deleted_at IS NULL;

-- name: GetWorkspaceByIDIncludeDeleted :one
SELECT * FROM workspaces WHERE id = ?;

-- name: ListWorkspacesByIDs :many
SELECT * FROM workspaces
WHERE id IN (sqlc.slice('workspace_ids'))
  AND deleted_at IS NULL;

-- name: ListAccessibleWorkspaces :many
-- Secondary sort on `id` is the deterministic tiebreaker: created_at is
-- only millisecond-precision (see CURRENT_TIMESTAMP3 / strftime('%Y-%m-%dT%H:%M:%fZ')),
-- so workspaces created in the same millisecond would otherwise shuffle
-- on every refresh -- most reproducibly for fresh accounts whose seed
-- workspaces land in a batch.
SELECT w.* FROM workspaces w
WHERE w.deleted_at IS NULL
  AND w.owner_user_id = sqlc.arg(user_id)
ORDER BY w.created_at DESC, w.id DESC;

-- name: RenameWorkspace :execresult
UPDATE workspaces SET title = ? WHERE id = ? AND owner_user_id = ?;

-- name: SoftDeleteWorkspace :execresult
-- The deleted_at IS NULL guard makes a concurrent delete racing this one match zero
-- rows, so the service's rows-affected NotFound check fires for the loser
-- instead of reporting success for a workspace the winner already deleted
-- (and queueing a second lifecycle-outbox row / channel-close pass). Matches
-- SoftDeleteAllWorkspacesByUser's guard.
UPDATE workspaces SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL;

-- name: SoftDeleteAllWorkspacesByUser :exec
UPDATE workspaces SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE owner_user_id = ? AND deleted_at IS NULL;

-- name: HardDeleteWorkspacesBefore :execresult
-- Raw compare: deleted_at (canonical on every write) against the SQLiteTime
-- cutoff (same canonical layout). Sargable for idx_workspaces_deleted_at
-- (SEARCH deleted_at<?, not SCAN-with-residual under datetime()).
DELETE FROM workspaces WHERE rowid IN (SELECT w.rowid FROM workspaces w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < sqlc.arg(cutoff) LIMIT 1000);
