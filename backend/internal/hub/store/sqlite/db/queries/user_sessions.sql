-- name: CreateUserSession :exec
INSERT INTO user_sessions (id, user_id, expires_at, user_agent, ip_address) VALUES (?, ?, ?, ?, ?);

-- name: GetUserSessionByID :one
-- datetime() wraps both sides so the comparison is done on canonicalised
-- "YYYY-MM-DD HH:MM:SS" values instead of the raw stored text. Without
-- this wrapper the test (and any same-day TTL) silently fails because
-- modernc's stored format ("YYYY-MM-DD HH:MM:SS.SSS+HH:MM") sorts
-- before strftime('YYYY-MM-DDTHH:MM:SS.SSSZ', 'now') for matching dates.
SELECT * FROM user_sessions WHERE id = ? AND datetime(expires_at) > datetime('now');

-- name: TouchUserSession :exec
UPDATE user_sessions
SET last_active_at = datetime('now'),
    expires_at = ?
WHERE id = ? AND datetime(last_active_at) < datetime(?);

-- name: DeleteUserSession :execresult
DELETE FROM user_sessions WHERE id = ?;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.org_id, u.username, u.is_admin, u.email_verified, u.email
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = ? AND datetime(s.expires_at) > datetime('now') AND u.deleted_at IS NULL;

-- name: DeleteExpiredUserSessions :execresult
DELETE FROM user_sessions WHERE datetime(expires_at) < datetime('now');

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = ?;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = ? AND id != ?;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = ? AND datetime(expires_at) > datetime('now')
ORDER BY last_active_at DESC
LIMIT 1000;

-- name: ListAllActiveSessions :many
SELECT s.id, s.user_id, u.username, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE datetime(s.expires_at) > datetime('now')
  AND (sqlc.narg(cursor) IS NULL OR datetime(s.last_active_at) < datetime(sqlc.narg(cursor)))
ORDER BY s.last_active_at DESC
LIMIT sqlc.arg(limit);
