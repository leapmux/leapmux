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
-- only millisecond-precision (DATETIME(3) / NOW(3)), so workspaces
-- created in the same millisecond would otherwise shuffle on every
-- refresh -- most reproducibly for fresh accounts whose seed
-- workspaces land in a batch.
--
-- The id tiebreaker is byte-wise (case-sensitive) because every table is
-- created COLLATE=utf8mb4_bin, so no explicit cast is needed. MySQL's
-- session default `utf8mb4_general_ci` is case-INsensitive; the
-- table-level binary collation ensures two ids differing only in case
-- (e.g. "Foo..." vs "foo...") still sort deterministically. SQLite and
-- PostgreSQL already collate case-sensitively by default.
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
UPDATE workspaces SET is_deleted = 1, deleted_at = NOW(3) WHERE id = ? AND owner_user_id = ? AND is_deleted = 0;

-- name: SoftDeleteAllWorkspacesByUser :exec
UPDATE workspaces SET is_deleted = 1, deleted_at = NOW(3) WHERE owner_user_id = ? AND is_deleted = 0;

-- name: HardDeleteWorkspacesBefore :execresult
DELETE FROM workspaces WHERE id IN (SELECT w.id FROM (SELECT workspaces.id FROM workspaces WHERE workspaces.deleted_at IS NOT NULL AND workspaces.deleted_at < ? LIMIT 1000) w);
