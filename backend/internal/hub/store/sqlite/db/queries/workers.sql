-- name: CreateWorker :exec
INSERT INTO workers (id, auth_token, registered_by, public_key, mlkem_public_key, slhdsa_public_key, auto_registered)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = ? AND deleted_at IS NULL;

-- name: GetWorkerByIDIncludeDeleted :one
SELECT * FROM workers WHERE id = ?;

-- name: GetWorkerByAuthToken :one
SELECT * FROM workers WHERE auth_token = ? AND status != 3;

-- name: ListWorkersByUserID :many
SELECT * FROM workers
WHERE registered_by = sqlc.arg(registered_by) AND status = 1
  AND (sqlc.narg(cursor_time) IS NULL
       OR created_at < sqlc.narg(cursor_time)
       OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit);

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
UPDATE workers SET status = 3, deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE registered_by = ? AND status != 3;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = ?, mlkem_public_key = ?, slhdsa_public_key = ? WHERE id = ?;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = ? AND deleted_at IS NULL;

-- The admin worker listing is a 2x2 matrix over (status nil/set) x (user_id
-- nil/set), implemented as FOUR separate queries. Two reasons it cannot collapse
-- to two queries with an opt-in `(narg(user_id) IS NULL OR registered_by =
-- narg(user_id))` probe:
--   1. SQLite's OR-optimization falls back to a full partial-index scan +
--      TEMP B-TREE sort for the user_id-set cases (verified via EXPLAIN),
--      defeating both the seek and the ORDER-BY-rides-index invariant.
--   2. sqlc emits the IS-NULL-probed narg as an untyped interface{}; on Postgres
--      binding NULL raises SQLSTATE 42P08 "could not determine data type of
--      parameter" and breaks the listing entirely (YugabyteDB inherits the
--      break via the postgres store). Splitting user_id into its own
--      REQUIRED-equality query (sqlc.arg, not narg) yields a typed param and
--      restores the index seek.
-- The status dimension stays split for partial-index eligibility: status=nil
-- keeps `deleted_at IS NULL` (partial-index-eligible); status=set drops it so
-- status=3 (WORKER_STATUS_DELETED) can surface soft-deleted rows. See the
-- migration for the per-query index rationale.

-- ListWorkersAdmin: status=nil, user_id=nil (all live workers).
-- name: ListWorkersAdmin :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg(limit);

-- ListWorkersAdminByUser: status=nil, user_id=set.
-- name: ListWorkersAdminByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg(limit);

-- ListWorkersAdminByStatus: status=set, user_id=nil (all workers in a status).
-- name: ListWorkersAdminByStatus :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.status = sqlc.arg(status)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg(limit);

-- ListWorkersAdminByUserAndStatus: status=set, user_id=set.
-- name: ListWorkersAdminByUserAndStatus :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.status = sqlc.arg(status)
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg(limit);

-- name: HardDeleteWorkersBefore :execresult
-- Raw compare: deleted_at (canonical on every write) against the SQLiteTime
-- cutoff, which binds the same canonical layout instead of the driver layout a
-- raw time.Time would serialize. Sargable for idx_workers_deleted_at (SEARCH
-- deleted_at<?, not a SCAN-with-residual under datetime()).
DELETE FROM workers WHERE rowid IN (SELECT w.rowid FROM workers w WHERE w.deleted_at IS NOT NULL AND w.deleted_at < sqlc.arg(cutoff) LIMIT 1000);
