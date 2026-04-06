-- name: CreatePendingOAuthSignup :exec
INSERT INTO pending_oauth_signups (token, provider_id, provider_subject, email, display_name, access_token, refresh_token, token_type, token_expires_at, key_version, redirect_uri, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetPendingOAuthSignup :one
SELECT * FROM pending_oauth_signups WHERE token = ?;

-- name: DeletePendingOAuthSignup :exec
DELETE FROM pending_oauth_signups WHERE token = ?;

-- name: DeleteExpiredPendingOAuthSignups :execresult
DELETE FROM pending_oauth_signups WHERE expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
