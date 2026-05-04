-- name: CreateUserSession :exec
INSERT INTO user_sessions (id, user_id, expires_at, user_agent, ip_address) VALUES (?, ?, ?, ?, ?);

-- name: GetUserSessionByID :one
SELECT * FROM user_sessions WHERE id = ? AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: TouchUserSession :exec
UPDATE user_sessions
SET last_active_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    expires_at = ?
WHERE id = ? AND last_active_at < ?;

-- name: DeleteUserSession :execresult
DELETE FROM user_sessions WHERE id = ?;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.org_id, u.username, u.is_admin, u.email_verified, u.email
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = ? AND s.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now') AND u.deleted_at IS NULL;

-- name: DeleteExpiredUserSessions :execresult
DELETE FROM user_sessions WHERE expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = ?;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = ? AND id != ?;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = ? AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
ORDER BY last_active_at DESC
LIMIT 1000;

-- name: ListAllActiveSessions :many
SELECT s.id, s.user_id, u.username, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE s.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  AND (sqlc.narg(cursor) IS NULL OR s.last_active_at < sqlc.narg(cursor))
ORDER BY s.last_active_at DESC
LIMIT sqlc.arg(limit);
