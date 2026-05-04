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

-- MySQL has no UPDATE ... RETURNING. The Go store wraps these two
-- statements in a transaction with the SELECT taking a row lock.
-- name: GetActiveRegistrationKeyForUpdate :one
SELECT * FROM worker_registration_keys WHERE id = ? AND expires_at > ? FOR UPDATE;

-- ConsumeSoftDeleteRegistrationKey runs inside the Consume transaction,
-- after GetActiveRegistrationKeyForUpdate has authorized the row via the
-- row lock. No ownership check here — Consume is invoked by the worker
-- presenting the bearer key, not by the key's owner.
-- name: ConsumeSoftDeleteRegistrationKey :exec
UPDATE worker_registration_keys SET expires_at = ? WHERE id = ?;

-- name: HardDeleteExpiredRegistrationKeysBefore :execresult
DELETE FROM worker_registration_keys WHERE id IN (
    SELECT id FROM (
        SELECT worker_registration_keys.id
        FROM worker_registration_keys
        WHERE worker_registration_keys.expires_at < ?
        LIMIT 1000
    ) inner_q
);
