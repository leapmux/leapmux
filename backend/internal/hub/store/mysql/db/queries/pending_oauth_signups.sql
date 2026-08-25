-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreatePendingOAuthSignup :exec
INSERT INTO pending_oauth_signups (token, provider_id, provider_subject, email, display_name, access_token, refresh_token, token_type, token_expires_at, key_version, redirect_uri, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetPendingOAuthSignup :one
SELECT * FROM pending_oauth_signups WHERE token = ?;

-- name: DeletePendingOAuthSignup :exec
DELETE FROM pending_oauth_signups WHERE token = ?;

-- name: DeleteExpiredPendingOAuthSignups :execresult
DELETE FROM pending_oauth_signups WHERE expires_at < sqlc.arg(now);
