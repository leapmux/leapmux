-- name: GrantWorkerAccess :exec
INSERT INTO worker_access_grants (worker_id, user_id, granted_by) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: RevokeWorkerAccess :exec
DELETE FROM worker_access_grants WHERE worker_id = $1 AND user_id = $2;

-- name: HasWorkerAccess :one
SELECT COUNT(*) > 0 AS has_access FROM worker_access_grants
WHERE worker_id = $1 AND user_id = $2;

-- name: ListWorkerAccessGrants :many
SELECT * FROM worker_access_grants WHERE worker_id = $1 ORDER BY created_at;

-- name: DeleteWorkerAccessGrantsByWorker :exec
DELETE FROM worker_access_grants WHERE worker_id = $1;

-- name: DeleteWorkerAccessGrantsByUser :exec
DELETE FROM worker_access_grants WHERE user_id = $1;

-- name: DeleteWorkerAccessGrantsByUserInOrg :exec
DELETE FROM worker_access_grants
WHERE worker_access_grants.user_id = sqlc.arg(user_id)
  AND worker_access_grants.worker_id IN (
    SELECT w.id FROM workers w
    JOIN org_members om ON w.registered_by = om.user_id
    WHERE om.org_id = sqlc.arg(org_id)
  );
