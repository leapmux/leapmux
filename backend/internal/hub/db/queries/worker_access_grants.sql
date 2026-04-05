-- name: GrantWorkerAccess :exec
INSERT INTO worker_access_grants (worker_id, user_id, granted_by) VALUES (?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: RevokeWorkerAccess :exec
DELETE FROM worker_access_grants WHERE worker_id = ? AND user_id = ?;

-- name: HasWorkerAccess :one
SELECT COUNT(*) > 0 AS has_access FROM worker_access_grants
WHERE worker_id = ? AND user_id = ?;

-- name: ListWorkerAccessGrants :many
SELECT * FROM worker_access_grants WHERE worker_id = ? ORDER BY created_at;

-- name: DeleteWorkerAccessGrantsByWorker :exec
DELETE FROM worker_access_grants WHERE worker_id = ?;

-- name: DeleteWorkerAccessGrantsByUser :exec
DELETE FROM worker_access_grants WHERE user_id = ?;
