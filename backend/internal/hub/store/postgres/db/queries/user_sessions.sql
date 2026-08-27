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
SELECT u.id, u.username, u.is_admin, u.email_verified, u.email, s.created_at, s.expires_at, s.auth_generation, s.elevation_proven_at, s.elevation_expires_at
FROM user_sessions s
JOIN users u ON s.user_id = u.id
WHERE s.id = $1
  AND s.expires_at > sqlc.arg(now)
  AND u.deleted_at IS NULL
  AND s.auth_generation >= u.auth_generation;

-- Elevation ("sudo mode"). Three statements own every write to
-- elevation_proven_at / elevation_expires_at; no other query touches those columns.
--
-- elevation_proven_at is the instant a step-up factor was proven and is rewritten
-- ONLY by ElevateUserSession. elevation_expires_at slides forward on each
-- successful sensitive action.
--
-- TWO writers hold the absolute cap, and both bind store.ElevationMaxTotal.
-- ElevateUserSession writes the anchor and the first deadline in one statement,
-- so the store clamps that deadline in Go before it binds it (see
-- ElevateSessionParams.ClampedExpiresAt). SlideUserSessionElevation measures
-- the ceiling from the STORED anchor, which Go never reads, so it clamps in
-- SQL. The slide alone is not sufficient: GREATEST keeps the deadline
-- monotone, so it can never SHORTEN an over-long deadline that the grant
-- wrote. Neither parameter struct carries a ceiling field, so no caller can
-- widen the cap, and none can pass 0 and make every slide a silent no-op.

-- name: ElevateUserSession :execrows
-- Proves a fresh factor: it restarts BOTH the anchor and the deadline, so a
-- new ceremony grants a whole new maximum window rather than adding to an
-- old one.
--
-- elevation_expires_at arrives ALREADY CLAMPED to elevation_proven_at +
-- store.ElevationMaxTotal; ElevateSessionParams.ClampedExpiresAt applies the
-- cap, because both instants are Go values here. See the block comment above.
UPDATE user_sessions
SET elevation_proven_at = sqlc.arg(elevation_proven_at),
    elevation_expires_at = sqlc.arg(elevation_expires_at)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND expires_at > sqlc.arg(now);

-- name: SlideUserSessionElevation :execrows
-- GREATEST keeps the deadline monotone (a late request cannot shorten it),
-- and LEAST(..., elevation_proven_at + cap) enforces the absolute ceiling. Both run
-- in SQL so a slide cannot over-extend whatever the caller passes.
-- max_total_micros is NOT a caller parameter: sessions.go binds
-- store.ElevationMaxTotal into it. The statement adds the cap in microseconds, so
-- Postgres and MySQL bind the same number.
-- TIMESTAMPTZ keeps every microsecond; MySQL's DATETIME(3) keeps three
-- fractional digits and rounds the rest away, so a cap with a sub-millisecond
-- part would land differently there. Every cap this hub passes is a whole
-- number of milliseconds.
--
-- make_interval(secs => ...) is the obvious way to write the cap and cannot
-- be used: CockroachDB's parser rejects the named-argument syntax with a
-- syntax error at the '>'. Multiplying INTERVAL '1 microsecond' by a BIGINT
-- is what every Postgres-wire dialect this hub supports accepts (see
-- revocation_events.sql, which does the same with milliseconds).
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
            elevation_proven_at + CAST(sqlc.arg(max_total_micros) AS BIGINT) * INTERVAL '1 microsecond'
        )
    )
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND elevation_proven_at IS NOT NULL
  AND elevation_expires_at IS NOT NULL
  AND elevation_expires_at > sqlc.arg(now)
  AND sqlc.arg(window_deadline) > elevation_expires_at;

-- name: DropUserSessionElevation :execrows
UPDATE user_sessions
SET elevation_proven_at = NULL,
    elevation_expires_at = NULL
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND elevation_expires_at IS NOT NULL;

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
