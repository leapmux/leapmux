-- name: CreateOrg :exec
INSERT INTO orgs (id, name, is_personal) VALUES (?, ?, ?);

-- name: GetOrgByID :one
SELECT * FROM orgs WHERE id = ?;

-- name: GetOrgByName :one
SELECT * FROM orgs WHERE name = ?;

-- name: CountOrgs :one
SELECT count(*) FROM orgs;

-- name: UpdateOrgName :exec
UPDATE orgs SET name = ? WHERE id = ? AND is_personal = 0;

-- name: DeleteOrg :exec
DELETE FROM orgs WHERE id = ? AND is_personal = 0;
