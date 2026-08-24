-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened
-- (created_at, updated_at, last_active_at) keep the database clock.

-- name: CreateUserSession :exec
INSERT INTO user_sessions (
    id, user_id, expires_at, user_agent, ip_address, auth_generation
) VALUES (
    sqlc.arg(id),
    sqlc.arg(user_id),
    sqlc.arg(expires_at),
    sqlc.arg(user_agent),
    sqlc.arg(ip_address),
    (SELECT auth_generation FROM users WHERE users.id = sqlc.arg(user_id))
);

-- name: GetUserSessionByID :one
SELECT * FROM user_sessions WHERE id = $1 AND expires_at > sqlc.arg(now);

-- name: TouchUserSession :execrows
-- The expires_at predicate is what keeps an expired session dead. The Hub
-- serves a validated session from an in-memory cache for a short window, so a
-- request can reach this UPDATE after the row expired; without the predicate
-- that request would slide a dead session forward and revive it.
UPDATE user_sessions
SET last_active_at = NOW(),
    expires_at = $1
WHERE id = $2 AND last_active_at < $3 AND expires_at > sqlc.arg(now);

-- name: DeleteUserSession :one
DELETE FROM user_sessions WHERE id = $1 RETURNING id, user_id;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.username, u.is_admin, u.email_verified, u.email, s.created_at, s.expires_at, s.auth_generation
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = $1
  AND s.expires_at > sqlc.arg(now)
  AND u.deleted_at IS NULL
  AND s.auth_generation >= u.auth_generation;

-- name: RefreshUserSessionAuthGeneration :execrows
UPDATE user_sessions AS s
SET auth_generation = u.auth_generation
FROM users AS u
WHERE s.id = sqlc.arg(session_id)
  AND s.user_id = sqlc.arg(user_id)
  AND u.id = s.user_id
  AND u.deleted_at IS NULL;

-- name: DeleteExpiredUserSessions :execresult
DELETE FROM user_sessions WHERE expires_at < sqlc.arg(now);

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = $1;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = $1 AND id != $2;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = sqlc.arg(user_id) AND expires_at > sqlc.arg(now)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR last_active_at < sqlc.narg(cursor_time)::timestamptz
       OR (last_active_at = sqlc.narg(cursor_time)::timestamptz AND id < sqlc.narg(cursor_id)))
ORDER BY last_active_at DESC, id DESC
LIMIT sqlc.arg('limit');

-- name: ListAllActiveSessions :many
SELECT s.id, s.user_id, COALESCE(u.username, '') AS username, (u.id IS NULL)::boolean AS user_deleted, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
LEFT JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE s.expires_at > sqlc.arg(now)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR s.last_active_at < sqlc.narg(cursor_time)::timestamptz
       OR (s.last_active_at = sqlc.narg(cursor_time)::timestamptz AND s.id < sqlc.narg(cursor_id)))
ORDER BY s.last_active_at DESC, s.id DESC
LIMIT sqlc.arg('limit');
