-- name: CreateAPIToken :exec
INSERT INTO api_tokens (
    id, user_id, client_type, client_name, secret_hash, refresh_hash,
    scope, expires_at, refresh_expires_at, auth_generation
) VALUES (
    sqlc.arg(id),
    sqlc.arg(user_id),
    sqlc.arg(client_type),
    sqlc.arg(client_name),
    sqlc.arg(secret_hash),
    sqlc.arg(refresh_hash),
    sqlc.arg(scope),
    sqlc.arg(expires_at),
    sqlc.arg(refresh_expires_at),
    (SELECT auth_generation FROM users WHERE users.id = sqlc.arg(user_id))
);

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

-- name: RotateAPITokenRefresh :execrows
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
    last_rotated_at = NOW()
WHERE id = sqlc.arg(id)
  AND revoked_at IS NULL
  AND refresh_hash = sqlc.arg(prev_refresh_hash);

-- name: RevokeAPIToken :one
UPDATE api_tokens
SET revoked_at = clock_timestamp()
WHERE id = $1 AND revoked_at IS NULL
RETURNING id, user_id, revoked_at;

-- name: RevokeAPITokensByUserFast :execrows
UPDATE api_tokens
SET revoked_at = clock_timestamp()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: DeleteRevokedAPITokensBefore :execrows
DELETE FROM api_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < $1;
