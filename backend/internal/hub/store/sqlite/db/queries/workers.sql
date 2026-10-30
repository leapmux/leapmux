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
  AND (sqlc.narg(cursor_time) IS NULL
       OR created_at < sqlc.narg(cursor_time)
       OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit);

-- name: SetWorkerStatus :exec
-- Test-only setter for the two LIVE states. deregistering keeps an existing
-- deregistering_at rather than restarting the stale-deregistration clock.
UPDATE workers
SET deregistering_at = CASE WHEN CAST(sqlc.arg(deregistering) AS BOOLEAN) THEN COALESCE(deregistering_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) ELSE NULL END
WHERE id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: DeregisterWorker :execresult
UPDATE workers SET deregistering_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND registered_by = ? AND deregistering_at IS NULL AND deleted_at IS NULL;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET deregistering_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND deregistering_at IS NULL AND deleted_at IS NULL;

-- name: MarkWorkerDeleted :exec
UPDATE workers SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ? AND deleted_at IS NULL;

-- name: MarkAllWorkersDeletedByUser :exec
UPDATE workers SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE registered_by = ? AND deleted_at IS NULL;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: UpdateWorkerPublicKey :exec
UPDATE workers SET public_key = ?, mlkem_public_key = ?, slhdsa_public_key = ? WHERE id = ?;

-- name: GetWorkerPublicKey :one
SELECT public_key, mlkem_public_key, slhdsa_public_key FROM workers WHERE id = ? AND deleted_at IS NULL;

-- The admin worker listing is a 2x2 matrix over (user_id none/set) x (live or
-- deleted), implemented as FOUR separate queries. Two reasons the user_id
-- dimension cannot collapse into an opt-in `(narg(user_id) IS NULL OR
-- registered_by = narg(user_id))` probe:
--   1. SQLite's OR-optimization falls back to a full partial-index scan +
--      TEMP B-TREE sort for the user_id-set cases (verified via EXPLAIN),
--      defeating both the seek and the ORDER-BY-rides-index invariant.
--   2. sqlc emits the IS-NULL-probed narg as an untyped interface{}; on Postgres
--      binding NULL raises SQLSTATE 42P08 "could not determine data type of
--      parameter" and breaks the listing entirely (YugabyteDB inherits the
--      break via the postgres store). Splitting user_id into its own
--      REQUIRED-equality query (sqlc.arg, not narg) yields a typed param and
--      restores the index seek.
-- The live/deleted dimension stays split for partial-index eligibility: the
-- live pair keeps `deleted_at IS NULL` and selects ACTIVE, DEREGISTERING or
-- both through the two include flags; the deleted pair keeps
-- `deleted_at IS NOT NULL`. The status a row reports is derived from
-- (deregistering_at, deleted_at); no column stores it. See the migration for
-- the per-query index rationale.

-- ListWorkersAdminLive: user_id=none, live workers in the included states.
-- name: ListWorkersAdminLive :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND ((w.deregistering_at IS NULL AND CAST(sqlc.arg(include_active) AS BOOLEAN))
       OR (w.deregistering_at IS NOT NULL AND CAST(sqlc.arg(include_deregistering) AS BOOLEAN)))
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg(limit);

-- ListWorkersAdminLiveByUser: user_id=set, live workers in the included states.
-- name: ListWorkersAdminLiveByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NULL
  AND ((w.deregistering_at IS NULL AND CAST(sqlc.arg(include_active) AS BOOLEAN))
       OR (w.deregistering_at IS NOT NULL AND CAST(sqlc.arg(include_deregistering) AS BOOLEAN)))
  AND w.registered_by = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg(limit);

-- ListWorkersAdminDeleted: user_id=none, soft-deleted workers.
-- name: ListWorkersAdminDeleted :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NOT NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR w.created_at < sqlc.narg(cursor_time)
       OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
ORDER BY w.created_at DESC, w.id DESC
LIMIT sqlc.arg(limit);

-- ListWorkersAdminDeletedByUser: user_id=set, soft-deleted workers.
-- name: ListWorkersAdminDeletedByUser :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.deleted_at IS NOT NULL
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

-- name: CompleteStaleDeregistrations :execresult
-- Deletes every worker that has waited longer than the grace period for its
-- DEREGISTER instruction: a lost acknowledgement, a failed deletion, or a hub
-- that restarted without the worker. Raw compare: deregistering_at is written
-- canonical (strftime) and the cutoff binds a SQLiteTime, so the < is
-- byte-exact and sargable for idx_workers_deregistering. Batched like
-- HardDeleteWorkersBefore; the caller drains until a pass changes nothing.
UPDATE workers SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE rowid IN (
    SELECT w.rowid FROM workers w
    WHERE w.deregistering_at IS NOT NULL AND w.deregistering_at < sqlc.arg(cutoff) AND w.deleted_at IS NULL
    LIMIT 1000
);

-- GetWorkerAdmin is the single-row form of the admin listing: the same
-- LEFT JOIN, so a soft-deleted owner reports the same empty username and
-- owner_deleted=true that the list reports. Soft-deleted workers are
-- included, because the admin surface inspects them.
-- name: GetWorkerAdmin :one
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id = ?;
