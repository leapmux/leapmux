-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

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
DELETE FROM oauth_states WHERE expires_at < sqlc.arg(now);
