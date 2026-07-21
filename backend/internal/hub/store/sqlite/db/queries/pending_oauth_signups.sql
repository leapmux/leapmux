-- name: CreatePendingOAuthSignup :exec
INSERT INTO pending_oauth_signups (token, provider_id, provider_subject, email, display_name, access_token, refresh_token, token_type, token_expires_at, key_version, redirect_uri, expires_at)
VALUES (
    sqlc.arg(token),
    sqlc.arg(provider_id),
    sqlc.arg(provider_subject),
    sqlc.arg(email),
    sqlc.arg(display_name),
    sqlc.arg(access_token),
    sqlc.arg(refresh_token),
    sqlc.arg(token_type),
    sqlc.arg(token_expires_at),
    sqlc.arg(key_version),
    sqlc.arg(redirect_uri),
    sqlc.arg(expires_at)
);

-- name: GetPendingOAuthSignup :one
SELECT * FROM pending_oauth_signups WHERE token = ?;

-- name: DeletePendingOAuthSignup :exec
DELETE FROM pending_oauth_signups WHERE token = ?;

-- name: DeleteExpiredPendingOAuthSignups :execresult
-- Raw compare: expires_at is stored canonical (CreatePendingOAuthSignup binds
-- a SQLiteTime), so the sweep is millisecond-exact against the same canonical
-- RHS layout.
DELETE FROM pending_oauth_signups WHERE expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
