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
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR created_at < sqlc.narg(cursor_time)::timestamptz
       OR (created_at = sqlc.narg(cursor_time)::timestamptz AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
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

-- The admin worker listing is a 2x2 matrix over (status nil/set) x (user_id
-- nil/set), implemented as FOUR separate queries. The user_id dimension CANNOT
-- be an opt-in `(narg(user_id) IS NULL OR registered_by = narg(user_id))` probe:
-- sqlc emits the IS-NULL-probed narg as an untyped interface{}, and binding NULL
-- for it raises SQLSTATE 42P08 "could not determine data type of parameter"
-- (verified: TestPostgresStore/workers/list_admin_filter_by_user_and_status
-- fails on this commit's two-query collapse; YugabyteDB inherits the break via
-- this store). Splitting user_id into its own REQUIRED-equality query
-- (sqlc.arg, not narg) yields a typed `string` param and restores the index
-- seek. The status dimension stays split for partial-index eligibility:
-- status=nil keeps `deleted_at IS NULL`; status=set drops it so status=3 can
-- surface soft-deleted rows.

-- ListWorkersAdmin: status=nil, user_id=nil.
-- name: ListWorkersAdmin :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR w.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (w.created_at = sqlc.narg(cursor_time)::timestamptz AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('limit');

-- ListWorkersAdminByUser: status=nil, user_id=set.
-- name: ListWorkersAdminByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR w.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (w.created_at = sqlc.narg(cursor_time)::timestamptz AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('limit');

-- ListWorkersAdminByStatus: status=set, user_id=nil.
-- name: ListWorkersAdminByStatus :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.status = sqlc.arg(status)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR w.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (w.created_at = sqlc.narg(cursor_time)::timestamptz AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('limit');

-- ListWorkersAdminByUserAndStatus: status=set, user_id=set.
-- name: ListWorkersAdminByUserAndStatus :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.status = sqlc.arg(status)
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR w.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (w.created_at = sqlc.narg(cursor_time)::timestamptz AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('limit');

-- name: HardDeleteWorkersBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
WITH to_delete AS (
    SELECT w.id FROM workers w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < $1 LIMIT 1000
)
DELETE FROM workers WHERE id IN (SELECT id FROM to_delete);
