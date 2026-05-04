-- name: CreateRegistrationKey :exec
INSERT INTO worker_registration_keys (id, created_by, expires_at) VALUES (?, ?, ?);

-- name: GetRegistrationKeyByID :one
SELECT * FROM worker_registration_keys WHERE id = ?;

-- name: GetOwnedRegistrationKey :one
SELECT * FROM worker_registration_keys WHERE id = ? AND created_by = ?;

-- ExtendRegistrationKey atomically rewrites expires_at iff the row is
-- owned by created_by AND still live (current expires_at > now). The
-- liveness guard closes the resurrection race against a concurrent
-- Consume: an Extend that started before Consume but lands after must
-- not revive the dead row.
-- name: ExtendRegistrationKey :execresult
UPDATE worker_registration_keys
SET expires_at = sqlc.arg(new_expires_at)
WHERE id = sqlc.arg(id)
  AND created_by = sqlc.arg(created_by)
  AND expires_at > sqlc.arg(now);

-- SoftDeleteRegistrationKey is the user-initiated path; ownership lives
-- in the WHERE clause so a single roundtrip both authorizes and acts.
-- Re-running on an already-dead row still updates expires_at (the WHERE
-- matches by id+owner only); the service layer maps 0 rows to NotFound.
-- name: SoftDeleteRegistrationKey :execresult
UPDATE worker_registration_keys
SET expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id) AND created_by = sqlc.arg(created_by);

-- ConsumeRegistrationKey atomically marks a live key as consumed and
-- returns its row. SQLite's UPDATE ... RETURNING was added in 3.35
-- (March 2021) and is used by all of our prod targets.
-- name: ConsumeRegistrationKey :one
UPDATE worker_registration_keys
SET expires_at = sqlc.arg(soft_deleted_at)
WHERE id = sqlc.arg(id) AND expires_at > sqlc.arg(now)
RETURNING *;

-- name: HardDeleteExpiredRegistrationKeysBefore :execresult
DELETE FROM worker_registration_keys
WHERE rowid IN (
    SELECT k.rowid
    FROM worker_registration_keys k
    WHERE k.expires_at < sqlc.arg(cutoff)
    LIMIT 1000
);
