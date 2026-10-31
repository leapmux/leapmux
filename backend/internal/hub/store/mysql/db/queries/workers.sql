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

-- Each functional index has its own hidden columns. MySQL substitutes the
-- first matching indexed hidden column. USE INDEX keeps the predicate
-- substitutions on this query's index. An unrestricted match can select a
-- global index for an owner page and scan other owners' workers.
-- TestMySQLWorkerStateListingsReadOnlyTheRequestedPage checks the actual reads.
-- name: ListWorkersByUserID :many
SELECT * FROM workers USE INDEX (idx_workers_registered_by_state_created)
WHERE registered_by = sqlc.arg(registered_by)
  AND (deleted_at IS NOT NULL) = FALSE
  AND (deleted_at IS NULL AND deregistering_at IS NOT NULL) = FALSE
  AND (sqlc.narg(cursor_time) IS NULL OR created_at < sqlc.narg(cursor_time) OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT ?;

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

-- Each state and owner filter has its own query. Literal predicates let
-- the planner select an ordered index for exactly the requested population.
-- The owner filter stays a required equality, avoiding an untyped NULL probe
-- on PostgreSQL and a broad OR scan on SQLite.
-- Select the indexed ID page before joining owner metadata, so any outer
-- reordering processes only that page instead of the full matching population.

-- name: ListWorkersAdminLive :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_live_created)
    WHERE (w.deleted_at IS NOT NULL) = FALSE
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminLiveByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_registered_by_live_created)
    WHERE (w.deleted_at IS NOT NULL) = FALSE
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminActive :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_state_created)
    WHERE (w.deleted_at IS NOT NULL) = FALSE
      AND (w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL) = FALSE
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminActiveByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_registered_by_state_created)
    WHERE (w.deleted_at IS NOT NULL) = FALSE
      AND (w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL) = FALSE
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeregistering :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_state_created)
    WHERE (w.deleted_at IS NOT NULL) = FALSE
      AND (w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL) = TRUE
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeregisteringByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_registered_by_state_created)
    WHERE (w.deleted_at IS NOT NULL) = FALSE
      AND (w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL) = TRUE
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeleted :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_state_created)
    WHERE (w.deleted_at IS NOT NULL) = TRUE
      AND (w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL) = FALSE
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

-- name: ListWorkersAdminDeletedByUser :many
WITH worker_page AS (
    SELECT w.id
    FROM workers w USE INDEX (idx_workers_registered_by_state_created)
    WHERE (w.deleted_at IS NOT NULL) = TRUE
      AND (w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL) = FALSE
      AND w.registered_by = sqlc.arg(user_id)
      AND (sqlc.narg(cursor_time) IS NULL
           OR w.created_at < sqlc.narg(cursor_time)
           OR (w.created_at = sqlc.narg(cursor_time) AND w.id < sqlc.narg(cursor_id)))
    ORDER BY w.created_at DESC, w.id DESC
    LIMIT ?
)
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM worker_page p
JOIN workers w ON w.id = p.id
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
ORDER BY w.created_at DESC, w.id DESC;

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
