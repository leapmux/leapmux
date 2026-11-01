-- name: CreateWorker :exec
INSERT INTO workers (id, auth_token, registered_by, auto_registered)
VALUES (?, ?, ?, ?);

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

-- Each state and owner filter has its own query. Literal predicates let
-- the planner select an ordered index for exactly the requested population.
-- The owner filter stays a required equality, avoiding an untyped NULL probe
-- on PostgreSQL and a broad OR scan on SQLite.
-- Select the indexed ID page before joining owner metadata, so any outer
-- reordering processes only that page instead of the full matching population.

-- name: ListWorkersAdminLive :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NULL
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminLiveByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NULL
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminActive :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NULL AND w.deregistering_at IS NULL
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminActiveByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NULL AND w.deregistering_at IS NULL
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeregistering :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeregisteringByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeleted :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NOT NULL
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeletedByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w
    WHERE w.deleted_at IS NOT NULL
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT sqlc.arg(limit)
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

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
