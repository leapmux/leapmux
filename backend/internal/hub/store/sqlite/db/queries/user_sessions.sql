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
-- expires_at is stored in the canonical strftime('%Y-%m-%dT%H:%M:%fZ') layout
-- (CreateUserSession binds a SQLiteTime), so the liveness filter compares it raw
-- against the same layout -- millisecond-exact at the expiry boundary.
SELECT * FROM user_sessions WHERE id = ? AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: DeleteUserSession :one
DELETE FROM user_sessions WHERE id = ? RETURNING id, user_id;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.org_id, u.username, u.is_admin, u.email_verified, u.email, s.created_at, s.expires_at, s.auth_generation
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = ?
  AND s.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
-- expires_at is stored canonical (CreateUserSession + Touch both bind a
-- SQLiteTime), so comparing it raw against the same canonical RHS is
-- sargable for idx_user_sessions_expires_at_last_active (SEARCH expires_at<?,
-- not a SCAN-with-residual under julianday()) -- the index was orphaned under
-- the julianday wrap. RHS is strftime('now'), evaluated once, no binding.
DELETE FROM user_sessions WHERE expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = ?;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = ? AND id != ?;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = sqlc.arg(user_id) AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  AND (sqlc.narg(cursor_time) IS NULL
       OR last_active_at < sqlc.narg(cursor_time)
       OR (last_active_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY last_active_at DESC, id DESC
LIMIT sqlc.arg(limit);

-- name: ListAllActiveSessions :many
-- Both timestamp filters compare the raw canonical strftime('%Y-%m-%dT%H:%M:%fZ')
-- column against the same layout. expires_at is stored canonical because BOTH
-- write paths canonicalize it: CreateUserSession binds a SQLiteTime, and Touch
-- (the inline UPDATE in sqlite/sessions.go) also binds a SQLiteTime.
-- last_active_at is written SQL-side by the column DEFAULT and Touch. A future
-- session write path MUST keep this invariant -- binding a raw time.Time stores
-- modernc's driver layout
-- ("... ...+00:00", space at byte 10) and silently breaks every raw-string
-- liveness filter below; see TestTouchStoresExpiresAtCanonical.
-- last_active_at also carries the keyset cursor (decodeCursorParams formats it
-- identically), so the predicate is a byte-exact raw-string compare -- exact
-- equality for the id tiebreak, consistent with the raw-column ORDER BY, and
-- sargable for the index.
SELECT s.id, s.user_id, COALESCE(u.username, '') AS username, CAST(u.id IS NULL AS BOOLEAN) AS user_deleted, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
LEFT JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE s.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  AND (sqlc.narg(cursor_time) IS NULL
       OR s.last_active_at < sqlc.narg(cursor_time)
       OR (s.last_active_at = sqlc.narg(cursor_time) AND s.id < sqlc.narg(cursor_id)))
ORDER BY s.last_active_at DESC, s.id DESC
LIMIT sqlc.arg(limit);
