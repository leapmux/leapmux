-- name: CreateWorker :exec
INSERT INTO workers (id, auth_token, registered_by, public_key, mlkem_public_key, slhdsa_public_key, auto_registered)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = $1 AND deleted_at IS NULL;

-- name: GetWorkerByIDIncludeDeleted :one
SELECT * FROM workers WHERE id = $1;

-- name: GetWorkerByAuthToken :one
-- A deregistering worker must still connect: that is how it is told to stop.
SELECT * FROM workers WHERE auth_token = $1 AND deleted_at IS NULL;

-- name: ListWorkersByUserID :many
SELECT * FROM workers
WHERE registered_by = sqlc.arg(registered_by) AND deleted_at IS NULL AND deregistering_at IS NULL
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR created_at < sqlc.narg(cursor_time)::timestamptz
       OR (created_at = sqlc.narg(cursor_time)::timestamptz AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit');

-- name: SetWorkerStatus :exec
-- Test-only setter for the two LIVE states. deregistering keeps an existing
-- deregistering_at rather than restarting the stale-deregistration clock.
UPDATE workers
SET deregistering_at = CASE WHEN sqlc.arg(deregistering)::boolean THEN COALESCE(deregistering_at, NOW()) ELSE NULL END
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeregisterWorker :execresult
UPDATE workers SET deregistering_at = NOW()
WHERE id = $1 AND registered_by = $2 AND deregistering_at IS NULL AND deleted_at IS NULL;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET deregistering_at = NOW()
WHERE id = $1 AND deregistering_at IS NULL AND deleted_at IS NULL;

-- name: MarkWorkerDeleted :exec
UPDATE workers SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL;

-- name: MarkAllWorkersDeletedByUser :exec
UPDATE workers SET deleted_at = NOW() WHERE registered_by = $1 AND deleted_at IS NULL;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = NOW() WHERE id = $1;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = $1, mlkem_public_key = $2, slhdsa_public_key = $3 WHERE id = $4;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = $1 AND deleted_at IS NULL;

-- The admin worker listing is a 2x2 matrix over (user_id none/set) x (live or
-- deleted), implemented as FOUR separate queries. The user_id dimension CANNOT
-- be an opt-in `(narg(user_id) IS NULL OR registered_by = narg(user_id))` probe:
-- sqlc emits the IS-NULL-probed narg as an untyped interface{}, and binding NULL
-- for it raises SQLSTATE 42P08 "could not determine data type of parameter"
-- (YugabyteDB inherits the break via this store). Splitting user_id into its
-- own REQUIRED-equality query (sqlc.arg, not narg) yields a typed `string`
-- param and restores the index seek. The live/deleted dimension stays split
-- for partial-index eligibility: the live pair keeps `deleted_at IS NULL` and
-- selects ACTIVE, DEREGISTERING or both through the two include flags; the
-- deleted pair keeps `deleted_at IS NOT NULL`.

-- ListWorkersAdminLive: user_id=none, live workers in the included states.
-- name: ListWorkersAdminLive :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND ((w.deregistering_at IS NULL AND sqlc.arg(include_active)::boolean)
       OR (w.deregistering_at IS NOT NULL AND sqlc.arg(include_deregistering)::boolean))
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR w.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (w.created_at = sqlc.narg(cursor_time)::timestamptz AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('limit');

-- ListWorkersAdminLiveByUser: user_id=set, live workers in the included states.
-- name: ListWorkersAdminLiveByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND ((w.deregistering_at IS NULL AND sqlc.arg(include_active)::boolean)
       OR (w.deregistering_at IS NOT NULL AND sqlc.arg(include_deregistering)::boolean))
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR w.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (w.created_at = sqlc.narg(cursor_time)::timestamptz AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('limit');

-- ListWorkersAdminDeleted: user_id=none, soft-deleted workers.
-- name: ListWorkersAdminDeleted :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NOT NULL
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR w.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (w.created_at = sqlc.narg(cursor_time)::timestamptz AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg('limit');

-- ListWorkersAdminDeletedByUser: user_id=set, soft-deleted workers.
-- name: ListWorkersAdminDeletedByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NOT NULL
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

-- name: CompleteStaleDeregistrations :execresult
-- Deletes every worker that has waited longer than the grace period for its
-- DEREGISTER instruction: a lost acknowledgement, a failed deletion, or a hub
-- that restarted without the worker. CTE form (not LIMIT in a subquery) for
-- CockroachDB, as HardDeleteWorkersBefore; the caller drains until a pass
-- changes nothing.
WITH to_complete AS (
    SELECT w.id FROM workers w
    WHERE w.deregistering_at IS NOT NULL AND w.deregistering_at < sqlc.arg(cutoff) AND w.deleted_at IS NULL
    LIMIT 1000
)
UPDATE workers SET deleted_at = NOW() WHERE id IN (SELECT id FROM to_complete);

-- GetWorkerAdmin is the single-row form of the admin listing: the same
-- LEFT JOIN, so a soft-deleted owner reports the same empty username and
-- owner_deleted=true that the list reports. Soft-deleted workers are
-- included, because the admin surface inspects them.
-- name: GetWorkerAdmin :one
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id = $1;
