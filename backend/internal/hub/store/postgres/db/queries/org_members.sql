-- name: CreateOrgMember :exec
INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3);

-- name: GetOrgMember :one
SELECT * FROM org_members WHERE org_id = $1 AND user_id = $2;

-- name: ListOrgMembersByOrgID :many
SELECT om.org_id, om.user_id, om.role, om.joined_at,
       u.username, u.display_name, u.email
FROM org_members om
JOIN users u ON u.id = om.user_id
WHERE om.org_id = $1 AND u.deleted_at IS NULL
ORDER BY om.joined_at;

-- name: ListOrgsByUserID :many
SELECT o.* FROM orgs o
JOIN org_members om ON o.id = om.org_id
WHERE om.user_id = $1 AND o.deleted_at IS NULL
ORDER BY o.name;

-- name: UpdateOrgMemberRole :exec
UPDATE org_members SET role = $1 WHERE org_id = $2 AND user_id = $3;

-- name: DeleteOrgMember :exec
DELETE FROM org_members WHERE org_id = $1 AND user_id = $2;

-- name: CountOrgMembersByRole :one
SELECT count(*)::integer FROM org_members WHERE org_id = $1 AND role = $2;

-- name: IsOrgMember :one
SELECT count(*) > 0 AS is_member FROM org_members WHERE org_id = $1 AND user_id = $2;
