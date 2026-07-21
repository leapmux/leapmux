-- name: CreateOAuthState :exec
INSERT INTO oauth_states (state, provider_id, pkce_verifier, redirect_uri, expires_at)
VALUES (
    sqlc.arg(state),
    sqlc.arg(provider_id),
    sqlc.arg(pkce_verifier),
    sqlc.arg(redirect_uri),
    sqlc.arg(expires_at)
);

-- name: GetOAuthState :one
SELECT * FROM oauth_states WHERE state = ?;

-- name: DeleteOAuthState :exec
DELETE FROM oauth_states WHERE state = ?;

-- name: DeleteExpiredOAuthStates :execresult
-- Raw compare: expires_at is stored canonical (CreateOAuthState binds a
-- SQLiteTime), so the sweep is millisecond-exact against the same canonical RHS
-- layout.
DELETE FROM oauth_states WHERE expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
