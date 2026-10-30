-- name: CreateWorker :exec
INSERT INTO workers (id, auth_token, registered_by, public_key, mlkem_public_key, slhdsa_public_key, auto_registered)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = ? AND deleted_at IS NULL;

-- name: GetWorkerByIDIncludeDeleted :one
SELECT * FROM workers WHERE id = ?;

-- name: GetWorkerByAuthToken :one
-- A deregistering worker must still connect: that is how it is told to stop.
SELECT * FROM workers WHERE auth_token = ? AND deleted_at IS NULL;

-- name: ListWorkersByUserID :many
SELECT * FROM workers
WHERE registered_by = sqlc.arg(registered_by) AND deleted_at IS NULL AND deregistering_at IS NULL
  AND (sqlc.narg(cursor_time) IS NULL OR created_at < sqlc.narg(cursor_time) OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: SetWorkerStatus :exec
-- Test-only setter for the two LIVE states. deregistering keeps an existing
-- deregistering_at rather than restarting the stale-deregistration clock.
-- IF rather than the CASE the sqlite and postgres twins use: sqlc's MySQL
-- engine types a CASE WHEN parameter from the CASE result, a DATETIME, and
-- would generate a timestamp field for a boolean flag.
UPDATE workers
SET deregistering_at = IF(sqlc.arg(deregistering), COALESCE(deregistering_at, NOW(3)), NULL)
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeregisterWorker :execresult
UPDATE workers SET deregistering_at = NOW(3)
WHERE id = ? AND registered_by = ? AND deregistering_at IS NULL AND deleted_at IS NULL;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET deregistering_at = NOW(3)
WHERE id = ? AND deregistering_at IS NULL AND deleted_at IS NULL;

-- name: MarkWorkerDeleted :exec
UPDATE workers SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL;

-- name: MarkAllWorkersDeletedByUser :exec
UPDATE workers SET deleted_at = NOW(3) WHERE registered_by = ? AND deleted_at IS NULL;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = NOW(3) WHERE id = ?;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = ?, mlkem_public_key = ?, slhdsa_public_key = ? WHERE id = ?;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = ? AND deleted_at IS NULL;

-- The admin worker listing is a 2x2 matrix over (user_id none/set) x (live or
-- deleted), implemented as FOUR separate queries. The user_id dimension cannot
-- be an opt-in `(? IS NULL OR registered_by = ?)` probe: the doubled
-- placeholders produced opaque ColumnN param names and required binding the
-- user_id twice. Splitting user_id into its own REQUIRED-equality query
-- (sqlc.arg) yields a single typed param per query. The live/deleted dimension
-- stays split: the live pair keeps `deleted_at IS NULL` and selects ACTIVE,
-- DEREGISTERING or both through the two include flags; the deleted pair keeps
-- `deleted_at IS NOT NULL`. MySQL has no partial indexes, so the deleted_at
-- term is a residual either way. sqlc's MySQL engine supports sqlc.narg: a
-- repeated narg reference still compiles to one `?` per occurrence, but all of
-- them are fed from a single typed Go param field by the generated code, so the
-- cursor predicate uses narg rather than hand-doubled `?` pairs with opaque
-- ColumnN fields.

-- ListWorkersAdminLive: user_id=none, live workers in the included states.
-- name: ListWorkersAdminLive :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND ((w.deregistering_at IS NULL AND sqlc.arg(include_active))
       OR (w.deregistering_at IS NOT NULL AND sqlc.arg(include_deregistering)))
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- ListWorkersAdminLiveByUser: user_id=set, live workers in the included states.
-- name: ListWorkersAdminLiveByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND ((w.deregistering_at IS NULL AND sqlc.arg(include_active))
       OR (w.deregistering_at IS NOT NULL AND sqlc.arg(include_deregistering)))
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- ListWorkersAdminDeleted: user_id=none, soft-deleted workers.
-- name: ListWorkersAdminDeleted :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NOT NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- ListWorkersAdminDeletedByUser: user_id=set, soft-deleted workers.
-- name: ListWorkersAdminDeletedByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NOT NULL
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT ?;

-- name: HardDeleteWorkersBefore :execresult
DELETE FROM workers WHERE id IN (SELECT w.id FROM (SELECT workers.id FROM workers WHERE workers.deleted_at IS NOT NULL AND workers.deleted_at < ? LIMIT 1000) w);

-- name: CompleteStaleDeregistrations :execresult
-- Deletes every worker that has waited longer than the grace period for its
-- DEREGISTER instruction: a lost acknowledgement, a failed deletion, or a hub
-- that restarted without the worker. A single-table UPDATE takes LIMIT
-- directly; the caller drains until a pass changes nothing.
UPDATE workers SET deleted_at = NOW(3)
WHERE deregistering_at IS NOT NULL AND deregistering_at < sqlc.arg(cutoff) AND deleted_at IS NULL
LIMIT 1000;

-- GetWorkerAdmin is the single-row form of the admin listing: the same
-- LEFT JOIN, so a soft-deleted owner reports the same empty username and
-- owner_deleted=true that the list reports. Soft-deleted workers are
-- included, because the admin surface inspects them.
-- name: GetWorkerAdmin :one
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id = ?;
