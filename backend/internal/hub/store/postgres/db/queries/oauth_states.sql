-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateOAuthState :exec
-- purpose and session_id are written HERE, at the start of the flow, and the
-- callback reads them back. A reauth leg that took either from the callback
-- request could be aimed at a session of the caller's choosing.
INSERT INTO oauth_states (state, provider_id, pkce_verifier, nonce_hash, redirect_uri, purpose, session_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetOAuthState :one
SELECT * FROM oauth_states WHERE state = $1;

-- name: DeleteOAuthState :exec
DELETE FROM oauth_states WHERE state = $1;

-- name: DeleteExpiredOAuthStates :execresult
DELETE FROM oauth_states WHERE expires_at < sqlc.arg(now);
