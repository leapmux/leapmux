-- name: UpsertOAuthTokens :exec
-- expires_at is stored in the canonical layout (SQLiteTime binds it) so
-- ListExpiringOAuthTokens can compare it raw -- sargable for
-- idx_oauth_tokens_expires_at -- instead of wrapping the column in datetime()
-- and scanning every row. The DO UPDATE reuses the excluded value, so both
-- insert and update paths store the same layout.
INSERT INTO oauth_tokens (user_id, provider_id, access_token, refresh_token, token_type, expires_at, key_version)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(provider_id),
    sqlc.arg(access_token),
    sqlc.arg(refresh_token),
    sqlc.arg(token_type),
    sqlc.arg(expires_at),
    sqlc.arg(key_version)
)
ON CONFLICT (user_id, provider_id) DO UPDATE SET
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    token_type = excluded.token_type,
    expires_at = excluded.expires_at,
    key_version = excluded.key_version,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: GetOAuthTokens :one
SELECT * FROM oauth_tokens
WHERE user_id = ? AND provider_id = ?;

-- name: ListExpiringOAuthTokens :many
-- Raw compare: expires_at is stored canonical (UpsertOAuthTokens binds a
-- SQLiteTime), so comparing it raw against the same canonical RHS layout is
-- byte-exact and sargable -- idx_oauth_tokens_expires_at serves an
-- upper-bounded SEARCH instead of a full scan under a datetime() wrap.
SELECT * FROM oauth_tokens
WHERE expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+5 minutes');

-- name: DeleteOAuthTokensByProvider :exec
DELETE FROM oauth_tokens WHERE provider_id = ?;

-- name: DeleteOAuthTokensByUser :exec
DELETE FROM oauth_tokens WHERE user_id = ?;

-- name: DeleteOAuthTokensByUserAndProvider :exec
DELETE FROM oauth_tokens WHERE user_id = ? AND provider_id = ?;

-- name: ListOAuthTokensByKeyVersion :many
SELECT * FROM oauth_tokens WHERE key_version = ?;

-- name: CountOAuthTokensByKeyVersion :one
SELECT COUNT(*) FROM oauth_tokens WHERE key_version = ?;
