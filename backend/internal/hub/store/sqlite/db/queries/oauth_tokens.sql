-- name: UpsertOAuthTokens :exec
INSERT INTO oauth_tokens (user_id, provider_id, access_token, refresh_token, token_type, expires_at, key_version)
VALUES (?, ?, ?, ?, ?, ?, ?)
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
