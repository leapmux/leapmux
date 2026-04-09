-- name: CreateOrg :exec
INSERT INTO orgs (id, name, is_personal) VALUES (?, ?, ?);

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = ? AND deleted_at IS NULL;

-- name: GetOrgByIDIncludeDeleted :one
SELECT * FROM orgs WHERE id = ?;

-- name: GetOrgByName :one
SELECT * FROM orgs WHERE name = ? AND deleted_at IS NULL;

-- name: HasAnyOrg :one
SELECT EXISTS(SELECT 1 FROM orgs WHERE deleted_at IS NULL LIMIT 1) AS has_any;

-- name: ListAllOrgs :many
SELECT * FROM orgs WHERE deleted_at IS NULL
  AND (? IS NULL OR created_at < ?)
ORDER BY created_at DESC LIMIT ?;

-- name: SearchOrgs :many
SELECT * FROM orgs WHERE deleted_at IS NULL
  AND (sqlc.narg(query) IS NULL OR LOWER(name) LIKE CONCAT(LOWER(sqlc.narg(query)), '%'))
  AND (? IS NULL OR created_at < ?)
ORDER BY created_at DESC LIMIT ?;

-- name: UpdateOrgName :exec
UPDATE orgs SET name = ? WHERE id = ? AND is_personal = 0;

-- name: SoftDeleteNonPersonalOrg :exec
UPDATE orgs SET deleted_at = NOW(3) WHERE id = ? AND is_personal = 0;

-- name: SoftDeleteOrg :exec
UPDATE orgs SET deleted_at = NOW(3) WHERE id = ?;

-- name: HardDeleteOrgsBefore :execresult
DELETE FROM orgs WHERE id IN (SELECT o.id FROM (SELECT orgs.id FROM orgs WHERE orgs.deleted_at IS NOT NULL AND orgs.deleted_at < ? LIMIT 1000) o);
