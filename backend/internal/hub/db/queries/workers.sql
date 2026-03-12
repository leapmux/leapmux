-- name: CreateWorker :exec
INSERT INTO workers (id, org_id, auth_token, registered_by, public_key, mlkem_public_key, slhdsa_public_key)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = ? AND org_id = ?;

-- name: GetWorkerByIDInternal :one
SELECT * FROM workers WHERE id = ?;

-- name: GetWorkerByAuthToken :one
SELECT * FROM workers WHERE auth_token = ? AND status != 3;

-- name: ListWorkersByOrgID :many
SELECT * FROM workers WHERE org_id = ? AND status = 1 ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: ListOwnedWorkers :many
SELECT * FROM workers
WHERE org_id = sqlc.arg(org_id)
  AND status = 1
  AND registered_by = sqlc.arg(user_id)
ORDER BY created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: GetOwnedWorker :one
SELECT * FROM workers
WHERE id = sqlc.arg(worker_id)
  AND org_id = sqlc.arg(org_id)
  AND status = 1
  AND registered_by = sqlc.arg(user_id);

-- name: SetWorkerStatus :exec
UPDATE workers SET status = ? WHERE id = ?;

-- name: DeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = ? AND org_id = ? AND registered_by = ? AND status = 1;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = ? AND status = 1;

-- name: MarkWorkerDeleted :exec
UPDATE workers SET status = 3 WHERE id = ?;

-- name: ListWorkersByOrgAndRegisteredBy :many
SELECT id FROM workers WHERE org_id = ? AND registered_by = ? AND status = 1;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = ?, mlkem_public_key = ?, slhdsa_public_key = ? WHERE id = ?;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = ?;
