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
-- julianday() normalizes modernc's bound time format and SQLite's strftime
-- format without discarding the fractional second at the expiry boundary.
SELECT * FROM user_sessions WHERE id = ? AND julianday(expires_at) > julianday('now');

-- name: DeleteUserSession :one
DELETE FROM user_sessions WHERE id = ? RETURNING id, user_id;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.org_id, u.username, u.is_admin, u.email_verified, u.email, s.created_at, s.expires_at, s.auth_generation
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = ?
  AND julianday(s.expires_at) > julianday('now')
  AND u.deleted_at IS NULL
  AND s.auth_generation >= u.auth_generation;

-- name: RefreshUserSessionAuthGeneration :execresult
UPDATE user_sessions
SET auth_generation = (
    SELECT auth_generation FROM users
    WHERE users.id = sqlc.arg(user_id) AND deleted_at IS NULL
)
WHERE user_sessions.id = sqlc.arg(session_id)
  AND user_sessions.user_id = sqlc.arg(user_id)
  AND EXISTS (
    SELECT 1 FROM users
    WHERE users.id = sqlc.arg(user_id) AND deleted_at IS NULL
  );

-- name: DeleteExpiredUserSessions :execresult
DELETE FROM user_sessions WHERE julianday(expires_at) < julianday('now');

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = ?;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = ? AND id != ?;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = ? AND julianday(expires_at) > julianday('now')
ORDER BY last_active_at DESC
LIMIT 1000;

-- name: ListAllActiveSessions :many
SELECT s.id, s.user_id, u.username, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE julianday(s.expires_at) > julianday('now')
  AND (sqlc.narg(cursor) IS NULL OR julianday(s.last_active_at) < julianday(sqlc.narg(cursor)))
ORDER BY s.last_active_at DESC
LIMIT sqlc.arg(limit);
