-- name: CreateOrgMember :exec
INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?);

-- name: GetOrgMember :one
SELECT * FROM org_members WHERE org_id = ? AND user_id = ?;

-- name: ListOrgMembersByOrgID :many
SELECT om.org_id, om.user_id, om.role, om.joined_at,
       u.username, u.display_name, u.email
FROM org_members om
JOIN users u ON u.id = om.user_id
WHERE om.org_id = ?
ORDER BY om.joined_at;

-- name: ListOrgsByUserID :many
SELECT o.* FROM orgs o
JOIN org_members om ON o.id = om.org_id
WHERE om.user_id = ?
ORDER BY o.name;

-- name: UpdateOrgMemberRole :exec
UPDATE org_members SET role = ? WHERE org_id = ? AND user_id = ?;

-- name: DeleteOrgMember :exec
DELETE FROM org_members WHERE org_id = ? AND user_id = ?;

-- name: CountOrgMembersByRole :one
SELECT CAST(count(*) AS INTEGER) FROM org_members WHERE org_id = ? AND role = ?;

-- name: IsOrgMember :one
SELECT count(*) > 0 AS is_member FROM org_members WHERE org_id = ? AND user_id = ?;
