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
-- only millisecond-precision (DATETIME(3) / NOW(3)), and the SELECT
-- DISTINCT over the LEFT JOIN to workspace_access lets the planner
-- pick its own row order for ties. Without the tiebreaker the sidebar
-- shuffles workspaces created in the same millisecond on every refresh
-- -- most reproducibly for fresh accounts whose seed workspaces land
-- in a batch.
--
-- BINARY cast on the id tiebreaker pins byte-wise (case-sensitive)
-- ordering. MySQL's default `utf8mb4_general_ci` collation is
-- case-INsensitive, so two ids differing only in case (e.g. "Foo..."
-- vs "foo...") would sort non-deterministically across runs. SQLite
-- and PostgreSQL already collate case-sensitively by default.
SELECT DISTINCT w.* FROM workspaces w
LEFT JOIN workspace_access wa ON w.id = wa.workspace_id AND wa.user_id = sqlc.arg(user_id)
WHERE w.is_deleted = 0
  AND w.org_id = sqlc.arg(org_id)
  AND (w.owner_user_id = sqlc.arg(user_id) OR wa.user_id IS NOT NULL)
ORDER BY w.created_at DESC, BINARY w.id DESC;

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
-- (created_at, id) rather than w.*. The BINARY cast pins byte-wise
-- (case-sensitive) ordering on the id tiebreaker: MySQL's default
-- utf8mb4_general_ci collation is case-INsensitive, so two ids differing only in
-- case would otherwise sort non-deterministically across runs.
SELECT w.* FROM workspaces w
WHERE w.is_deleted = 0
  AND w.owner_user_id = sqlc.arg(user_id)
UNION
SELECT w.* FROM workspaces w
INNER JOIN workspace_access wa ON w.id = wa.workspace_id
WHERE w.is_deleted = 0
  AND wa.user_id = sqlc.arg(user_id)
ORDER BY created_at DESC, BINARY id DESC;

-- name: RenameWorkspace :execresult
UPDATE workspaces SET title = ? WHERE id = ? AND owner_user_id = ?;

-- name: SoftDeleteWorkspace :execresult
UPDATE workspaces SET is_deleted = 1, deleted_at = NOW(3) WHERE id = ? AND owner_user_id = ?;

-- name: SoftDeleteAllWorkspacesByUser :exec
UPDATE workspaces SET is_deleted = 1, deleted_at = NOW(3) WHERE owner_user_id = ? AND is_deleted = 0;

-- name: HardDeleteWorkspacesBefore :execresult
DELETE FROM workspaces WHERE id IN (SELECT w.id FROM (SELECT workspaces.id FROM workspaces WHERE workspaces.deleted_at IS NOT NULL AND workspaces.deleted_at < ? LIMIT 1000) w);
