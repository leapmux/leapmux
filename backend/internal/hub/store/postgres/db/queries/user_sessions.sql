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
SELECT * FROM user_sessions WHERE id = $1 AND expires_at > NOW();

-- name: TouchUserSession :execrows
UPDATE user_sessions
SET last_active_at = NOW(),
    expires_at = $1
WHERE id = $2 AND last_active_at < $3;

-- name: DeleteUserSession :one
DELETE FROM user_sessions WHERE id = $1 RETURNING id, user_id;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.org_id, u.username, u.is_admin, u.email_verified, u.email, s.created_at, s.expires_at, s.auth_generation
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = $1
  AND s.expires_at > NOW()
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
