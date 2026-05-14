-- name: CreateAPIToken :exec
INSERT INTO api_tokens (
    id, user_id, client_type, client_name, secret_hash, refresh_hash,
    scope, expires_at, refresh_expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAPITokenByID :one
SELECT * FROM api_tokens WHERE id = ?;

-- name: ListAPITokensByUser :many
SELECT * FROM api_tokens
WHERE user_id = ?
  AND (sqlc.arg(client_type) = '' OR client_type = sqlc.arg(client_type))
  AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: TouchAPIToken :exec
UPDATE api_tokens
SET last_used_at = NOW(3)
WHERE id = ?;

-- name: RotateAPITokenRefresh :exec
-- Rotation rewrites BOTH secrets in place on the existing row: the
-- access secret_hash + access expires_at (so freshly-issued access
-- bearers validate against this row and use the new access TTL),
-- and the refresh_hash + refresh_expires_at (so the new refresh
-- replaces the rotated-out one). The previous refresh hash and its
-- grace window are preserved so the racing retry path can still
-- re-emit the cached pair from the in-process grace cache while
-- the on-disk row only carries the current refresh hash.
UPDATE api_tokens
SET secret_hash = sqlc.arg(new_secret_hash),
    expires_at = sqlc.arg(new_expires_at),
    refresh_hash = sqlc.arg(new_refresh_hash),
    refresh_expires_at = sqlc.arg(new_refresh_expires_at),
    previous_refresh_hash = sqlc.arg(prev_refresh_hash),
    previous_refresh_expires_at = sqlc.arg(prev_refresh_expires_at),
    last_rotated_at = NOW(3)
WHERE id = sqlc.arg(id);

-- name: RevokeAPIToken :execresult
UPDATE api_tokens
SET revoked_at = NOW(3)
WHERE id = ? AND revoked_at IS NULL;

-- name: RevokeAPITokensByUser :execresult
UPDATE api_tokens
SET revoked_at = NOW(3)
WHERE user_id = ? AND revoked_at IS NULL;

-- name: ListAPITokensRevokedSince :many
SELECT id, user_id, revoked_at FROM api_tokens
WHERE revoked_at IS NOT NULL AND revoked_at > ?
ORDER BY revoked_at ASC;

-- name: MaxAPITokenRevokedAt :one
-- Returns the most recent revoked_at across all revoked api_tokens
-- rows. The hub's revocation watcher calls this once at bootstrap
-- to seed its watermark in O(log N) (index seek) instead of
-- materializing every historical row. Written as ORDER BY + LIMIT 1
-- (not MAX()) so sqlc can infer the return type from the underlying
-- column rather than emitting interface{}.
SELECT revoked_at FROM api_tokens
WHERE revoked_at IS NOT NULL
ORDER BY revoked_at DESC
LIMIT 1;

-- name: DeleteRevokedAPITokensBefore :execresult
DELETE FROM api_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < ?;
