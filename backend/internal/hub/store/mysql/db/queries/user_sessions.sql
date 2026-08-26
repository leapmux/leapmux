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
SELECT * FROM user_sessions WHERE id = ? AND expires_at > sqlc.arg(now);

-- name: TouchUserSession :execrows
-- The expires_at predicate is what keeps an expired session dead. The Hub
-- serves a validated session from an in-memory cache for a short window, so a
-- request can reach this UPDATE after the row expired; without the predicate
-- that request would slide a dead session forward and revive it.
UPDATE user_sessions
SET last_active_at = NOW(3),
    expires_at = ?
WHERE id = ? AND last_active_at < ? AND expires_at > sqlc.arg(now);

-- name: GetUserSessionForUpdate :one
SELECT id, user_id FROM user_sessions
WHERE id = ?
FOR UPDATE;

-- name: DeleteUserSession :execresult
DELETE FROM user_sessions WHERE id = ?;

-- name: ValidateSessionWithUser :one
SELECT u.id, u.username, u.is_admin, u.email_verified, u.email, s.created_at, s.expires_at, s.auth_generation, s.elevation_proven_at, s.elevation_expires_at
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = ?
  AND s.expires_at > sqlc.arg(now)
  AND u.deleted_at IS NULL
  AND s.auth_generation >= u.auth_generation;

-- Elevation ("sudo mode"). Three statements own every write to
-- elevation_proven_at / elevation_expires_at; no other query touches those columns.
--
-- elevation_proven_at is the instant a step-up factor was proven and is rewritten
-- ONLY by ElevateUserSession. elevation_expires_at slides forward on each
-- successful sensitive action, and the slide clamps itself to
-- elevation_proven_at + the maximum total window, so no Go path can extend an
-- elevation past its absolute cap.

-- name: ElevateUserSession :execresult
-- Proves a fresh factor: it restarts BOTH the anchor and the deadline, so a
-- new ceremony grants a whole new maximum window rather than topping up an
-- old one.
UPDATE user_sessions
SET elevation_proven_at = sqlc.arg(elevation_proven_at),
    elevation_expires_at = sqlc.arg(elevation_expires_at)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND expires_at > sqlc.arg(now);

-- name: SlideUserSessionElevation :execresult
-- GREATEST keeps the deadline monotone (a late request cannot shorten it),
-- and LEAST(..., elevation_proven_at + cap) enforces the absolute ceiling. Both run
-- in SQL so a slide cannot over-extend whatever the caller passes. The cap
-- is added in microseconds so DATETIME(3) keeps its millisecond precision.
--
-- There is no expires_at guard, unlike ElevateUserSession. A slide runs only
-- after a request that this session just authenticated, so
-- ValidateSessionWithUser already required a live session; and a lapsed
-- elevation cannot be resurrected here, because elevation_expires_at > now must
-- hold.
--
-- The window_deadline parameter is ALSO compared against elevation_expires_at
-- in the WHERE, which is what gives sqlc a column type for it: inside
-- min()/LEAST() it has none, and an untyped parameter escapes the
-- compile-time guarantee that only a canonical-layout valuer can be bound
-- (see TestGeneratedInterfaceParamsAreAllowlisted). The comparison is not
-- decoration: it also skips the write entirely when the caller's deadline is
-- not ahead of the stored one.
UPDATE user_sessions
SET elevation_expires_at = GREATEST(
        elevation_expires_at,
        LEAST(
            sqlc.arg(window_deadline),
            DATE_ADD(elevation_proven_at, INTERVAL CAST(sqlc.arg(max_total_micros) AS SIGNED) MICROSECOND)
        )
    )
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND elevation_proven_at IS NOT NULL
  AND elevation_expires_at IS NOT NULL
  AND elevation_expires_at > sqlc.arg(now)
  AND sqlc.arg(window_deadline) > elevation_expires_at;

-- name: DropUserSessionElevation :execresult
UPDATE user_sessions
SET elevation_proven_at = NULL,
    elevation_expires_at = NULL
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND elevation_expires_at IS NOT NULL;

-- name: RefreshUserSessionAuthGeneration :execresult
UPDATE user_sessions s
JOIN users u ON u.id = s.user_id AND u.deleted_at IS NULL
SET s.auth_generation = u.auth_generation
WHERE s.id = sqlc.arg(session_id) AND s.user_id = sqlc.arg(user_id);

-- name: DeleteExpiredUserSessions :execresult
DELETE FROM user_sessions WHERE expires_at < sqlc.arg(now);

-- name: DeleteUserSessionsByUser :exec
DELETE FROM user_sessions WHERE user_id = ?;

-- name: DeleteOtherUserSessions :exec
DELETE FROM user_sessions WHERE user_id = ? AND id != ?;

-- name: ListUserSessionsByUserID :many
SELECT * FROM user_sessions
WHERE user_id = sqlc.arg(user_id) AND expires_at > sqlc.arg(now)
  AND (sqlc.narg(cursor_time) IS NULL OR last_active_at < sqlc.narg(cursor_time) OR (last_active_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY last_active_at DESC, id DESC
LIMIT ?;

-- name: ListAllActiveSessions :many
SELECT s.id, s.user_id, COALESCE(u.username, '') AS username, (u.id IS NULL) AS user_deleted, s.created_at, s.last_active_at, s.expires_at, s.ip_address, s.user_agent
FROM user_sessions s
LEFT JOIN users u ON s.user_id = u.id AND u.deleted_at IS NULL
WHERE s.expires_at > sqlc.arg(now)
  AND (sqlc.narg(cursor_time) IS NULL OR s.last_active_at < sqlc.narg(cursor_time) OR (s.last_active_at = sqlc.narg(cursor_time) AND s.id < sqlc.narg(cursor_id)))
ORDER BY s.last_active_at DESC, s.id DESC
LIMIT ?;
