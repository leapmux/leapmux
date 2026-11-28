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
WHERE id = sqlc.arg(id) AND deleted_at IS NULL
  AND auth_token_hash = sqlc.arg(expected_auth_token_hash);

-- name: ListWorkersByUserID :many
SELECT * FROM workers
WHERE registered_by = sqlc.arg(registered_by) AND deleted_at IS NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR created_at < sqlc.narg(cursor_time)
       OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit);

-- name: DeregisterWorker :execresult
UPDATE workers SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND registered_by = ? AND deleted_at IS NULL;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND deleted_at IS NULL;

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

-- GetWorkerAdmin is the single-row form of the admin listing: the same
-- LEFT JOIN, so a soft-deleted owner reports the same empty username and
-- owner_deleted=true that the list reports. Soft-deleted workers are
-- included, because the admin surface inspects them.
-- name: GetWorkerAdmin :one
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id = ?;

-- name: GetWorkerInventorySnapshot :many
-- Reads the enrollment revision and active worker IDs in one statement so a
-- single read snapshot covers both tables without taking a write lock.
SELECT
    CAST(COALESCE((SELECT revision FROM worker_enrollment_revisions WHERE user_id = sqlc.arg(user_id)), 0) AS INTEGER) AS revision,
    CAST(COALESCE(w.id, '') AS TEXT) AS worker_id
FROM (SELECT 1 AS anchor) AS anchor
LEFT JOIN workers w
  ON w.registered_by = sqlc.arg(user_id)
 AND w.deleted_at IS NULL;

-- name: AdvanceWorkerEnrollment :exec
INSERT INTO worker_enrollment_revisions (user_id, revision)
VALUES (sqlc.arg(user_id), 1)
ON CONFLICT (user_id) DO UPDATE SET revision = worker_enrollment_revisions.revision + 1;

-- name: ListWorkersByUserIDExact :many
SELECT * FROM workers
WHERE id = sqlc.arg(id)
  AND registered_by = sqlc.arg(registered_by)
  AND deleted_at IS NULL;

-- name: ListWorkersByUserIDPrefix :many
SELECT * FROM workers
WHERE id >= sqlc.arg(id_lo)
  AND id < sqlc.arg(id_hi)
  AND id > sqlc.arg(id_gt)
  AND registered_by = sqlc.arg(registered_by)
  AND deleted_at IS NULL
ORDER BY id ASC
LIMIT sqlc.arg(limit);

-- name: ListWorkersAdminByIDExact :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id = sqlc.arg(id)
  AND (w.registered_by = sqlc.arg(user_id) OR CAST(sqlc.arg(user_id) AS TEXT) = '')
  AND (
      (CAST(sqlc.arg(status_filter) AS INTEGER) = 0 AND w.deleted_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS INTEGER) = 1 AND w.deleted_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS INTEGER) = 2 AND w.deleted_at IS NOT NULL)
  );

-- name: ListWorkersAdminByIDPrefix :many
SELECT sqlc.embed(w), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM workers w
LEFT JOIN users u ON w.registered_by = u.id AND u.deleted_at IS NULL
WHERE w.id >= sqlc.arg(id_lo)
  AND w.id < sqlc.arg(id_hi)
  AND w.id > sqlc.arg(id_gt)
  AND (w.registered_by = sqlc.arg(user_id) OR CAST(sqlc.arg(user_id) AS TEXT) = '')
  AND (
      (CAST(sqlc.arg(status_filter) AS INTEGER) = 0 AND w.deleted_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS INTEGER) = 1 AND w.deleted_at IS NULL)
      OR (CAST(sqlc.arg(status_filter) AS INTEGER) = 2 AND w.deleted_at IS NOT NULL)
  )
ORDER BY w.id ASC
LIMIT sqlc.arg(limit);
