-- name: CreateWorker :exec
INSERT INTO workers (id, auth_token_hash, registered_by, auto_registered,
                     x25519_public_key, mlkem_public_key, slhdsa_public_key,
                     registration_attempt_id, registration_key_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = ? AND deleted_at IS NULL;

-- name: GetWorkerByIDIncludeDeleted :one
SELECT * FROM workers WHERE id = ?;

-- name: GetWorkerByRegistrationAttemptID :one
-- Includes deleted rows: the registration recovery path must tell a
-- completed-then-deleted attempt from an unknown one.
SELECT * FROM workers WHERE registration_attempt_id = ?;

-- name: GetWorkerByAuthTokenHashIncludeDeleted :one
-- The token read of AuthenticateWorkerBearer, which every worker credential
-- check uses. It returns a deleted row too; AuthenticateWorkerBearer refuses
-- one with ErrWorkerDeleted.
SELECT * FROM workers WHERE auth_token_hash = ?;

-- name: RotateWorkerAuthToken :execrows
UPDATE workers SET auth_token_hash = sqlc.arg(auth_token_hash)
WHERE id = sqlc.arg(id) AND deleted_at IS NULL AND deregistering_at IS NULL
  AND auth_token_hash = sqlc.arg(expected_auth_token_hash);

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

-- name: GetWorkerInventorySnapshot :many
-- Reads the enrollment revision and active worker IDs in one statement so a
-- single InnoDB read view covers both tables even under READ COMMITTED.
SELECT
    CAST(COALESCE((SELECT revision FROM worker_enrollment_revisions WHERE user_id = sqlc.arg(user_id)), 0) AS SIGNED) AS revision,
    COALESCE(w.id, '') AS worker_id
FROM (SELECT 1 AS anchor) AS anchor
LEFT JOIN workers w USE INDEX (idx_workers_registered_by_state_created)
  ON w.registered_by = sqlc.arg(user_id)
 AND (w.deleted_at IS NOT NULL) = FALSE
 AND (w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL) = FALSE;

-- name: AdvanceWorkerEnrollment :exec
INSERT INTO worker_enrollment_revisions (user_id, revision)
VALUES (sqlc.arg(user_id), 1)
ON DUPLICATE KEY UPDATE revision = revision + 1;

-- name: ListWorkersByUserIDAndExactID :many
SELECT * FROM workers
WHERE registered_by = sqlc.arg(registered_by)
  AND deleted_at IS NULL
  AND deregistering_at IS NULL
  AND id = sqlc.arg(id);

-- name: ListWorkersByUserIDAndIDPrefix :many
SELECT * FROM workers
WHERE registered_by = sqlc.arg(registered_by)
  AND deleted_at IS NULL
  AND deregistering_at IS NULL
  AND id >= sqlc.arg(id_lo)
  AND id < sqlc.arg(id_hi)
  AND id > sqlc.arg(id_gt)
ORDER BY id ASC
LIMIT ?;

-- name: ListWorkersAdminByExactID :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id = sqlc.arg(id)
  AND (sqlc.arg(user_id) = '' OR w.registered_by = sqlc.arg(user_id))
  AND (
      (CAST(sqlc.arg(status_filter) AS SIGNED) = 0 AND w.deleted_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS SIGNED) = 1 AND w.deleted_at IS NULL AND w.deregistering_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS SIGNED) = 2 AND w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL)
      OR (CAST(sqlc.arg(status_filter) AS SIGNED) = 3 AND w.deleted_at IS NOT NULL)
  );

-- name: ListWorkersAdminByIDPrefix :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id >= sqlc.arg(id_lo)
  AND w.id < sqlc.arg(id_hi)
  AND w.id > sqlc.arg(id_gt)
  AND (sqlc.arg(user_id) = '' OR w.registered_by = sqlc.arg(user_id))
  AND (
      (CAST(sqlc.arg(status_filter) AS SIGNED) = 0 AND w.deleted_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS SIGNED) = 1 AND w.deleted_at IS NULL AND w.deregistering_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS SIGNED) = 2 AND w.deleted_at IS NULL AND w.deregistering_at IS NOT NULL)
      OR (CAST(sqlc.arg(status_filter) AS SIGNED) = 3 AND w.deleted_at IS NOT NULL)
  )
ORDER BY w.id ASC
LIMIT ?;
