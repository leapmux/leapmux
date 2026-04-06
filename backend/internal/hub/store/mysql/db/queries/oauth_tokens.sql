-- name: UpsertOAuthTokens :exec
INSERT INTO oauth_tokens (user_id, provider_id, access_token, refresh_token, token_type, expires_at, key_version)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    access_token = VALUES(access_token),
    refresh_token = VALUES(refresh_token),
    token_type = VALUES(token_type),
    expires_at = VALUES(expires_at),
    key_version = VALUES(key_version),
    updated_at = NOW(3);

-- name: GetOAuthTokens :one
SELECT * FROM oauth_tokens
WHERE user_id = ? AND provider_id = ?;

-- name: ListExpiringOAuthTokens :many
SELECT * FROM oauth_tokens
WHERE expires_at <= DATE_ADD(NOW(3), INTERVAL 5 MINUTE);

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
