-- name: CreateRegistration :exec
INSERT INTO worker_registrations (id, version, public_key, mlkem_public_key, slhdsa_public_key, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetRegistrationByID :one
SELECT * FROM worker_registrations WHERE id = $1;

-- name: ApproveRegistration :exec
UPDATE worker_registrations
SET status = 2, worker_id = $1, approved_by = $2
WHERE id = $3 AND status = 1;

-- name: ExpireRegistrations :exec
UPDATE worker_registrations
SET status = 4
WHERE status = 1 AND expires_at < NOW();

-- name: HardDeleteExpiredRegistrationsBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
WITH to_delete AS (
    SELECT r.id FROM worker_registrations r WHERE r.status = 4 AND r.created_at < $1 LIMIT 1000
)
DELETE FROM worker_registrations WHERE id IN (SELECT id FROM to_delete);

-- name: CreateUserSession :exec
INSERT INTO user_sessions (id, user_id, expires_at, user_agent, ip_address) VALUES ($1, $2, $3, $4, $5);

-- name: GetUserSessionByID :one
SELECT * FROM user_sessions WHERE id = $1 AND expires_at > NOW();

-- name: TouchUserSession :exec
UPDATE user_sessions
SET last_active_at = NOW(),
    expires_at = $1
WHERE id = $2 AND last_active_at < $3;

-- name: DeleteUserSession :execresult
DELETE FROM user_sessions WHERE id = $1;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.org_id, u.username, u.is_admin, u.email_verified
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = $1 AND s.expires_at > NOW() AND u.deleted_at IS NULL;

-- name: DeleteExpiredUserSessions :execresult
DELETE FROM user_sessions WHERE expires_at < NOW();

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = $1;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = $1 AND id != $2;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = $1 AND expires_at > NOW()
ORDER BY last_active_at DESC
LIMIT 1000;

-- name: ListAllActiveSessions :many
SELECT s.id, s.user_id, u.username, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE s.expires_at > NOW()
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR s.last_active_at < sqlc.narg(cursor))
ORDER BY s.last_active_at DESC
LIMIT sqlc.arg('limit');
