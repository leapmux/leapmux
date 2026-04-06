-- name: CreatePendingOAuthSignup :exec
INSERT INTO pending_oauth_signups (token, provider_id, provider_subject, email, display_name, access_token, refresh_token, token_type, token_expires_at, key_version, redirect_uri, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetPendingOAuthSignup :one
SELECT * FROM pending_oauth_signups WHERE token = $1;

-- name: DeletePendingOAuthSignup :exec
DELETE FROM pending_oauth_signups WHERE token = $1;

-- name: DeleteExpiredPendingOAuthSignups :execresult
DELETE FROM pending_oauth_signups WHERE expires_at < NOW();
