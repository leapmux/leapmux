-- name: CreateWorker :exec
INSERT INTO workers (id, auth_token, registered_by, public_key, mlkem_public_key, slhdsa_public_key)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = ?;

-- name: GetWorkerByAuthToken :one
SELECT * FROM workers WHERE auth_token = ? AND status != 3;

-- name: ListWorkersByUserID :many
SELECT * FROM workers WHERE registered_by = ? AND status = 1 ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: ListOwnedWorkers :many
SELECT * FROM workers
WHERE status = 1
  AND registered_by = sqlc.arg(user_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: GetOwnedWorker :one
SELECT * FROM workers
WHERE id = sqlc.arg(worker_id)
  AND status = 1
  AND registered_by = sqlc.arg(user_id);

-- name: SetWorkerStatus :exec
UPDATE workers SET status = ? WHERE id = ?;

-- name: DeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = ? AND registered_by = ? AND status = 1;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = ? AND status = 1;

-- name: MarkWorkerDeleted :exec
UPDATE workers SET status = 3, deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: MarkAllWorkersDeletedByUser :exec
UPDATE workers SET status = 3, deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE registered_by = ?;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = ?, mlkem_public_key = ?, slhdsa_public_key = ? WHERE id = ?;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = ?;

-- name: ListAllWorkersAdmin :many
SELECT w.*, u.username AS owner_username
FROM workers w
JOIN users u ON w.registered_by = u.id
WHERE (sqlc.narg(user_id) IS NULL OR w.registered_by = sqlc.narg(user_id))
  AND (sqlc.narg(status) IS NULL OR w.status = sqlc.narg(status))
ORDER BY w.created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: HardDeleteWorkersBefore :execresult
DELETE FROM workers WHERE deleted_at IS NOT NULL AND deleted_at < ?;
