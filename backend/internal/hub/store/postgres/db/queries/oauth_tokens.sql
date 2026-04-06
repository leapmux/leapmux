-- name: UpsertOAuthTokens :exec
INSERT INTO oauth_tokens (user_id, provider_id, access_token, refresh_token, token_type, expires_at, key_version)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, provider_id) DO UPDATE SET
    access_token = EXCLUDED.access_token,
    refresh_token = EXCLUDED.refresh_token,
    token_type = EXCLUDED.token_type,
    expires_at = EXCLUDED.expires_at,
    key_version = EXCLUDED.key_version,
    updated_at = NOW();

-- name: GetOAuthTokens :one
SELECT * FROM oauth_tokens
WHERE user_id = $1 AND provider_id = $2;

-- name: ListExpiringOAuthTokens :many
SELECT * FROM oauth_tokens
WHERE expires_at <= NOW() + INTERVAL '5 minutes';

-- name: DeleteOAuthTokensByProvider :exec
DELETE FROM oauth_tokens WHERE provider_id = $1;

-- name: DeleteOAuthTokensByUser :exec
DELETE FROM oauth_tokens WHERE user_id = $1;

-- name: DeleteOAuthTokensByUserAndProvider :exec
DELETE FROM oauth_tokens WHERE user_id = $1 AND provider_id = $2;

-- name: ListOAuthTokensByKeyVersion :many
SELECT * FROM oauth_tokens WHERE key_version = $1;

-- name: CountOAuthTokensByKeyVersion :one
SELECT COUNT(*) FROM oauth_tokens WHERE key_version = $1;
