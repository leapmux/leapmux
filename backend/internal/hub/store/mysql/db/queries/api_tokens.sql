-- name: CreateAPIToken :exec
INSERT INTO api_tokens (
    id, user_id, client_type, client_name, secret_hash, refresh_hash,
    expires_at, refresh_expires_at, admin_scope, auth_generation
) VALUES (
    sqlc.arg(id),
    sqlc.arg(user_id),
    sqlc.arg(client_type),
    sqlc.arg(client_name),
    sqlc.arg(secret_hash),
    sqlc.arg(refresh_hash),
    sqlc.arg(expires_at),
    sqlc.arg(refresh_expires_at),
    sqlc.arg(admin_scope),
    (SELECT auth_generation FROM users WHERE users.id = sqlc.arg(user_id))
);

-- name: GetAPITokenByID :one
SELECT * FROM api_tokens WHERE id = ?;

-- name: ListAPITokensByUser :many
-- The user's OWN device listing (Preferences -> CLI tokens). Keyset on
-- (created_at DESC, id DESC) like every other listing in the hub, and it
-- rides idx_api_tokens_user_created.
SELECT * FROM api_tokens
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.arg(client_type) = '' OR client_type = sqlc.arg(client_type))
  AND revoked_at IS NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR created_at < sqlc.narg(cursor_time)
       OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListAllAPITokens :many
-- Admin listing across all users (LEFT JOIN users for the owner username).
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL OR t.created_at < sqlc.narg(cursor_time) OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT ?;

-- name: ListAllAPITokensIncludingRevoked :many
-- Forensics variant of ListAllAPITokens: includes revoked rows
-- (--include-revoked). No matching partial index serves this shape -- an
-- occasional admin forensics page may top-N sort, which is deliberate; the
-- live listings keep their partial-index seeks.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL OR t.created_at < sqlc.narg(cursor_time) OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT ?;

-- name: ListAllAPITokensByUser :many
-- Per-user variant of ListAllAPITokens (the admin --user path): required
-- user_id equality on top of the same keyset + owner join.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND t.user_id = sqlc.arg(user_id)
  AND (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL OR t.created_at < sqlc.narg(cursor_time) OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT ?;

-- name: ListAllAPITokensByUserIncludingRevoked :many
-- Forensics variant of ListAllAPITokensByUser: includes revoked rows
-- (--include-revoked); see ListAllAPITokensIncludingRevoked for the
-- no-matching-index note.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.user_id = sqlc.arg(user_id)
  AND (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL OR t.created_at < sqlc.narg(cursor_time) OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT ?;

-- name: TouchAPIToken :exec
UPDATE api_tokens
SET last_used_at = NOW(3)
WHERE id = ?;

-- name: RotateAPITokenRefresh :execresult
-- Rotation rewrites BOTH secrets in place on the existing row: the
-- access secret_hash + access expires_at (so freshly-issued access
-- bearers validate against this row and use the new access TTL),
-- and the refresh_hash + refresh_expires_at (so the new refresh
-- replaces the rotated-out one). The previous refresh hash and its
-- grace window are preserved so any Hub can recognize a racing retry
-- and deterministically derive the same replacement pair.
UPDATE api_tokens
SET secret_hash = sqlc.arg(new_secret_hash),
    expires_at = sqlc.arg(new_expires_at),
    refresh_hash = sqlc.arg(new_refresh_hash),
    refresh_expires_at = sqlc.arg(new_refresh_expires_at),
    previous_refresh_hash = sqlc.arg(prev_refresh_hash),
    previous_refresh_expires_at = sqlc.arg(prev_refresh_expires_at),
    last_rotated_at = NOW(3)
WHERE id = sqlc.arg(id)
  AND revoked_at IS NULL
  AND refresh_hash = sqlc.arg(prev_refresh_hash);

-- name: GetLiveAPITokenForUpdate :one
SELECT id, user_id FROM api_tokens
WHERE id = ? AND revoked_at IS NULL
FOR UPDATE;

-- name: RevokeAPITokenAt :execresult
UPDATE api_tokens
SET revoked_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: GetLiveOwnedAPITokenForUpdate :one
-- MySQL has no RETURNING, so self-service revocation is the same
-- SELECT ... FOR UPDATE + UPDATE pair the sibling Revoke path uses. The
-- owner equality is in BOTH statements, so the row cannot change hands
-- between them.
SELECT id, user_id FROM api_tokens
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL
FOR UPDATE;

-- name: RevokeOwnedAPITokenAt :execresult
-- Self-service revocation. The owner check is IN the statement, so no Go
-- path can revoke a row it read but does not own: a read-then-revoke pair
-- would leave a window where the row changes hands between the two.
UPDATE api_tokens
SET revoked_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id) AND revoked_at IS NULL;

-- name: RevokeAPITokensByUserFast :execresult
UPDATE api_tokens
SET revoked_at = CURRENT_TIMESTAMP(6)
WHERE user_id = ? AND revoked_at IS NULL;

-- name: DeleteExpiredAPITokensBefore :execresult
-- Hard-deletes a live row that can no longer authenticate AND can no longer
-- renew. The caller passes a cutoff behind "now" by a retention margin, so a
-- user who asks why the CLI stopped working can still be shown the row.
--
-- BOTH legs must be closed, and the access expiry is the one that decides
-- whether the row still works: bearer validation reads expires_at alone
-- (auth.validateRow), so a row whose access token is live must survive
-- whatever its refresh window says. An administrator issues a token with a
-- TTL of up to a year and a refresh window of ninety days, and a sweep on
-- the refresh column alone deleted that credential on day ninety-seven
-- while it still authenticated.
--
-- expires_at IS NULL means a token that never expires, so it is never swept
-- here. refresh_expires_at IS NULL means a row with no refresh leg, which is
-- a closed leg rather than an open one.
DELETE FROM api_tokens
WHERE revoked_at IS NULL
  AND expires_at IS NOT NULL
  AND expires_at < sqlc.arg(cutoff)
  AND (refresh_expires_at IS NULL OR refresh_expires_at < sqlc.arg(cutoff));

-- name: DeleteRevokedAPITokensBefore :execresult
DELETE FROM api_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < ?;

-- Elevation ("sudo mode") for a command-line credential. Three statements own
-- every write to elevation_proven_at / elevation_expires_at, exactly as the
-- three on user_sessions do, and no other query touches those columns.
--
-- The rule is the SAME rule, deliberately. A bearer that could not elevate was
-- admitted unconditionally by the gate that protects hub settings, the user
-- surface and the mint, so possession of the credential file was the whole of
-- the check. It proves a factor through the browser step-up leg now, and one
-- proof admits gated actions for the same sliding window a session gets.
--
-- The expires_at guard on the grant is NULL-tolerant: an api_tokens row with
-- no expires_at never expires on its own account, where a session row always
-- carries one.

-- name: ElevateAPIToken :execresult
-- Proves a fresh factor: it restarts BOTH the anchor and the deadline, so a
-- new ceremony grants a whole new maximum window rather than topping up an
-- old one.
UPDATE api_tokens
SET elevation_proven_at = sqlc.arg(elevation_proven_at),
    elevation_expires_at = sqlc.arg(elevation_expires_at)
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > sqlc.arg(now));

-- name: SlideAPITokenElevation :execresult
-- The clamping, the monotonicity and the untyped-parameter note are the same
-- as SlideUserSessionElevation's in user_sessions.sql; read that one first.
UPDATE api_tokens
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

-- name: DropAPITokenElevation :execresult
UPDATE api_tokens
SET elevation_proven_at = NULL,
    elevation_expires_at = NULL
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND elevation_expires_at IS NOT NULL;
