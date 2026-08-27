-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreatePendingOAuthSignup :exec
INSERT INTO pending_oauth_signups (token, provider_id, provider_subject, nonce_hash, email, display_name, access_token, refresh_token, token_type, token_expires_at, key_version, redirect_uri, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetPendingOAuthSignup :one
SELECT * FROM pending_oauth_signups WHERE token = $1;

-- name: DeletePendingOAuthSignup :exec
DELETE FROM pending_oauth_signups WHERE token = $1;

-- name: DeleteExpiredPendingOAuthSignups :execresult
DELETE FROM pending_oauth_signups WHERE expires_at < sqlc.arg(now);
