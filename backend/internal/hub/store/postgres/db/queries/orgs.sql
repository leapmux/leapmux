-- name: CreateOrg :exec
INSERT INTO orgs (id, name, is_personal) VALUES ($1, $2, $3);

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = $1 AND deleted_at IS NULL;

-- name: GetOrgByIDIncludeDeleted :one
SELECT * FROM orgs WHERE id = $1;

-- name: GetOrgByName :one
SELECT * FROM orgs WHERE name = $1 AND deleted_at IS NULL;

-- name: HasAnyOrg :one
SELECT EXISTS(SELECT 1 FROM orgs WHERE deleted_at IS NULL LIMIT 1);

-- name: ListAllOrgs :many
SELECT * FROM orgs WHERE deleted_at IS NULL
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC LIMIT sqlc.arg('limit');

-- name: SearchOrgs :many
SELECT * FROM orgs WHERE deleted_at IS NULL
  AND (sqlc.narg(query)::text IS NULL OR name ILIKE sqlc.narg(query) || '%')
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC LIMIT sqlc.arg('limit');

-- name: UpdateOrgName :exec
UPDATE orgs SET name = $1 WHERE id = $2 AND is_personal = FALSE;

-- name: SoftDeleteNonPersonalOrg :exec
UPDATE orgs SET deleted_at = NOW() WHERE id = $1 AND is_personal = FALSE;

-- name: SoftDeleteOrg :exec
UPDATE orgs SET deleted_at = NOW() WHERE id = $1;

-- name: HardDeleteOrgsBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
WITH to_delete AS (
    SELECT o.id FROM orgs o WHERE o.deleted_at IS NOT NULL AND o.deleted_at < $1 LIMIT 1000
)
DELETE FROM orgs WHERE id IN (SELECT id FROM to_delete);
