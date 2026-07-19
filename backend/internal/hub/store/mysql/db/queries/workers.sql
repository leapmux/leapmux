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
  AND (sqlc.narg(cursor_time) IS NULL OR created_at < sqlc.narg(cursor_time) OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT ?;

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
UPDATE workers SET status = 3, deleted_at = NOW(3) WHERE id = ?;

-- name: MarkAllWorkersDeletedByUser :exec
UPDATE workers SET status = 3, deleted_at = NOW(3) WHERE registered_by = ? AND status != 3;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = NOW(3) WHERE id = ?;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = ?, mlkem_public_key = ?, slhdsa_public_key = ? WHERE id = ?;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = ? AND deleted_at IS NULL;

-- The admin worker listing is a 2x2 matrix over (status nil/set) x (user_id
-- nil/set), implemented as FOUR separate queries. The user_id dimension cannot
-- be an opt-in `(? IS NULL OR registered_by = ?)` probe: the doubled
-- placeholders produced opaque ColumnN param names and required binding the
-- user_id twice. Splitting user_id into its own REQUIRED-equality query
-- (sqlc.arg) yields a single typed param per query. The status dimension stays
-- split: status=nil keeps `deleted_at IS NULL`; status=set drops it so status=3
-- can surface soft-deleted rows. MySQL has no partial indexes, so deleted_at IS
-- NULL is a residual either way, but the split lets each half ride its
-- leading-column index (created_at vs status). sqlc's MySQL engine supports
-- sqlc.narg: a repeated narg reference still compiles to one `?` per
-- occurrence, but all of them are fed from a single typed Go param field by
-- the generated code, so the cursor predicate uses narg rather than the old
-- hand-doubled `?` pairs with their opaque ColumnN fields.

-- ListWorkersAdmin: status=nil, user_id=nil.
-- name: ListWorkersAdmin :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- ListWorkersAdminByUser: status=nil, user_id=set.
-- name: ListWorkersAdminByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- ListWorkersAdminByStatus: status=set, user_id=nil.
-- name: ListWorkersAdminByStatus :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.status = sqlc.arg(status)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- ListWorkersAdminByUserAndStatus: status=set, user_id=set.
-- name: ListWorkersAdminByUserAndStatus :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.status = sqlc.arg(status)
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- name: HardDeleteWorkersBefore :execresult
DELETE FROM workers WHERE id IN (SELECT w.id FROM (SELECT workers.id FROM workers WHERE workers.deleted_at IS NOT NULL AND workers.deleted_at < ? LIMIT 1000) w);
