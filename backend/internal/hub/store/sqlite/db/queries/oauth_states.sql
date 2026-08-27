-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateOAuthState :exec
-- purpose and session_id are written HERE, at the start of the flow, and the
-- callback reads them back. A reauth leg that took either from the callback
-- request could target a session of the caller's choosing.
INSERT INTO oauth_states (state, provider_id, pkce_verifier, nonce_hash, redirect_uri, purpose, session_id, expires_at)
VALUES (
    sqlc.arg(state),
    sqlc.arg(provider_id),
    sqlc.arg(pkce_verifier),
    sqlc.arg(nonce_hash),
    sqlc.arg(redirect_uri),
    sqlc.arg(purpose),
    sqlc.arg(session_id),
    sqlc.arg(expires_at)
);

-- name: GetOAuthState :one
SELECT * FROM oauth_states WHERE state = ?;

-- name: DeleteOAuthState :execresult
-- The row count is the SINGLE USE of the flow, so the caller needs it.
-- Two callbacks that carry the same state and the same nonce cookie -- a
-- double-clicked callback, a browser prefetch of the Location, a retried
-- navigation -- both pass the nonce check and both reach here. Exactly one
-- deletes a row; the other must be refused. Without the count the property
-- rested on the identity provider rejecting the second use of its
-- authorization code, which is somebody else's guarantee.
DELETE FROM oauth_states WHERE state = ?;

-- name: DeleteExpiredOAuthStates :execresult
-- Raw compare: expires_at is stored canonical (CreateOAuthState binds a
-- SQLiteTime), so the sweep is millisecond-exact against the same canonical RHS
-- layout.
DELETE FROM oauth_states WHERE expires_at < sqlc.arg(now);
