-- name: CreateOrg :exec
INSERT INTO orgs (id, name, is_personal) VALUES (?, ?, ?);

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = ? AND deleted_at IS NULL;

-- name: GetOrgByIDIncludeDeleted :one
SELECT * FROM orgs WHERE id = ?;

-- name: GetOrgByName :one
SELECT * FROM orgs WHERE name = ? AND deleted_at IS NULL;

-- name: CountOrgs :one
SELECT count(*) FROM orgs WHERE deleted_at IS NULL;

-- name: UpdateOrgName :exec
UPDATE orgs SET name = ? WHERE id = ? AND is_personal = 0;

-- name: DeleteOrg :exec
UPDATE orgs SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND is_personal = 0;

-- name: SoftDeleteOrg :exec
UPDATE orgs SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: HardDeleteOrgsBefore :execresult
DELETE FROM orgs WHERE rowid IN (SELECT o.rowid FROM orgs o WHERE o.deleted_at IS NOT NULL AND o.deleted_at < ? LIMIT 1000);
