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
SELECT * FROM user_sessions WHERE id = ? AND expires_at > NOW(3);

-- name: TouchUserSession :execrows
UPDATE user_sessions
SET last_active_at = NOW(3),
    expires_at = ?
WHERE id = ? AND last_active_at < ?;

-- name: GetUserSessionForUpdate :one
SELECT id, user_id FROM user_sessions
WHERE id = ?
FOR UPDATE;

-- name: DeleteUserSession :execresult
DELETE FROM user_sessions WHERE id = ?;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.org_id, u.username, u.is_admin, u.email_verified, u.email, s.created_at, s.expires_at, s.auth_generation
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = ?
  AND s.expires_at > NOW(3)
  AND u.deleted_at IS NULL
  AND s.auth_generation >= u.auth_generation;

-- name: RefreshUserSessionAuthGeneration :execresult
UPDATE user_sessions s
JOIN users u ON u.id = s.user_id AND u.deleted_at IS NULL
SET s.auth_generation = u.auth_generation
WHERE s.id = sqlc.arg(session_id) AND s.user_id = sqlc.arg(user_id);

-- name: DeleteExpiredUserSessions :execresult
DELETE FROM user_sessions WHERE expires_at < NOW(3);

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = ?;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = ? AND id != ?;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = sqlc.arg(user_id) AND expires_at > NOW(3)
  AND (sqlc.narg(cursor_time) IS NULL OR last_active_at < sqlc.narg(cursor_time) OR (last_active_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY last_active_at DESC, id DESC
LIMIT ?;

-- name: ListAllActiveSessions :many
SELECT s.id, s.user_id, COALESCE(u.username, '') AS username, (u.id IS NULL) AS user_deleted, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
LEFT JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE s.expires_at > NOW(3)
  AND (sqlc.narg(cursor_time) IS NULL OR s.last_active_at < sqlc.narg(cursor_time) OR (s.last_active_at = sqlc.narg(cursor_time) AND s.id < sqlc.narg(cursor_id)))
ORDER BY s.last_active_at DESC, s.id DESC
LIMIT ?;
