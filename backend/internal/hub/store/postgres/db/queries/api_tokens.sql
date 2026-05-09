-- name: CreateAPIToken :exec
INSERT INTO api_tokens (
    id, user_id, client_type, client_name, secret_hash, refresh_hash,
    scope, expires_at, refresh_expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetAPITokenByID :one
SELECT * FROM api_tokens WHERE id = $1;

-- name: ListAPITokensByUser :many
SELECT * FROM api_tokens
WHERE user_id = $1
  AND (sqlc.arg(client_type)::text = '' OR client_type = sqlc.arg(client_type)::text)
  AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: TouchAPIToken :exec
UPDATE api_tokens
SET last_used_at = NOW()
WHERE id = $1;

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
    last_rotated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: RevokeAPIToken :execrows
UPDATE api_tokens
SET revoked_at = NOW()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeAPITokensByUser :execrows
UPDATE api_tokens
SET revoked_at = NOW()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: ListAPITokensRevokedSince :many
SELECT id, user_id, revoked_at FROM api_tokens
WHERE revoked_at IS NOT NULL AND revoked_at > $1
ORDER BY revoked_at ASC;

-- name: MaxAPITokenRevokedAt :one
-- Returns the most recent revoked_at across all revoked api_tokens
-- rows. The hub's revocation watcher calls this once at bootstrap
-- to seed its watermark in O(log N) (index seek) instead of
-- materializing every historical row. Written as ORDER BY + LIMIT 1
-- (not MAX()) so sqlc infers the return type from the underlying
-- column rather than emitting interface{}.
SELECT revoked_at FROM api_tokens
WHERE revoked_at IS NOT NULL
ORDER BY revoked_at DESC
LIMIT 1;

-- name: DeleteRevokedAPITokensBefore :execrows
DELETE FROM api_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < $1;
