-- name: CreateWorker :exec
INSERT INTO workers (id, auth_token, registered_by, public_key, mlkem_public_key, slhdsa_public_key, auto_registered)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = $1 AND deleted_at IS NULL;

-- name: GetWorkerByIDIncludeDeleted :one
SELECT * FROM workers WHERE id = $1;

-- name: GetWorkerByAuthToken :one
SELECT * FROM workers WHERE auth_token = $1 AND status != 3;

-- name: ListWorkersByUserID :many
SELECT * FROM workers
WHERE registered_by = sqlc.arg(registered_by) AND status = 1
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');

-- name: ListOwnedWorkers :many
SELECT id, auth_token, registered_by, status, created_at, last_seen_at,
       public_key, mlkem_public_key, slhdsa_public_key, auto_registered, deleted_at
FROM (
  SELECT workers.id, workers.auth_token, workers.registered_by, workers.status, workers.created_at,
         workers.last_seen_at, workers.public_key, workers.mlkem_public_key, workers.slhdsa_public_key,
         workers.auto_registered, workers.deleted_at
  FROM workers
  WHERE workers.registered_by = sqlc.arg(user_id) AND workers.status = 1
    AND (sqlc.narg(cursor)::timestamptz IS NULL OR workers.created_at < sqlc.narg(cursor))
  UNION
  SELECT w.id, w.auth_token, w.registered_by, w.status, w.created_at,
         w.last_seen_at, w.public_key, w.mlkem_public_key, w.slhdsa_public_key,
         w.auto_registered, w.deleted_at
  FROM workers w
  INNER JOIN worker_access_grants g ON w.id = g.worker_id
  WHERE g.user_id = sqlc.arg(user_id) AND w.status = 1
    AND (sqlc.narg(cursor)::timestamptz IS NULL OR w.created_at < sqlc.narg(cursor))
) sub
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');

-- name: GetOwnedWorker :one
SELECT * FROM workers
WHERE id = sqlc.arg(worker_id)
  AND status = 1
  AND registered_by = sqlc.arg(user_id);

-- name: SetWorkerStatus :exec
UPDATE workers SET status = $1 WHERE id = $2;

-- name: DeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = $1 AND registered_by = $2 AND status = 1;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = $1 AND status = 1;

-- name: MarkWorkerDeleted :exec
UPDATE workers SET status = 3, deleted_at = NOW() WHERE id = $1;

-- name: MarkAllWorkersDeletedByUser :exec
UPDATE workers SET status = 3, deleted_at = NOW() WHERE registered_by = $1 AND status != 3;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = NOW() WHERE id = $1;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = $1, mlkem_public_key = $2, slhdsa_public_key = $3 WHERE id = $4;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = $1 AND deleted_at IS NULL;

-- name: ListWorkersAdminAll :many
SELECT w.*, COALESCE(u.username, '(deleted)') AS owner_username
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR w.created_at < sqlc.narg(cursor))
ORDER BY w.created_at DESC
LIMIT sqlc.arg('limit');

-- name: ListWorkersAdminByStatus :many
SELECT w.*, COALESCE(u.username, '(deleted)') AS owner_username
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.status = sqlc.arg(status)
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR w.created_at < sqlc.narg(cursor))
ORDER BY w.created_at DESC
LIMIT sqlc.arg('limit');

-- name: ListWorkersAdminByUser :many
SELECT w.*, COALESCE(u.username, '(deleted)') AS owner_username
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.registered_by = sqlc.arg(user_id) AND w.deleted_at IS NULL
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR w.created_at < sqlc.narg(cursor))
ORDER BY w.created_at DESC
LIMIT sqlc.arg('limit');

-- name: ListWorkersAdminByUserAndStatus :many
SELECT w.*, COALESCE(u.username, '(deleted)') AS owner_username
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.registered_by = sqlc.arg(user_id) AND w.status = sqlc.arg(status)
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR w.created_at < sqlc.narg(cursor))
ORDER BY w.created_at DESC
LIMIT sqlc.arg('limit');

-- name: HardDeleteWorkersBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
WITH to_delete AS (
    SELECT w.id FROM workers w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < $1 LIMIT 1000
)
DELETE FROM workers WHERE id IN (SELECT id FROM to_delete);
