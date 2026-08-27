-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateOAuthState :exec
-- purpose and session_id are written HERE, at the start of the flow, and the
-- callback reads them back. A reauth leg that took either from the callback
-- request could target a session of the caller's choosing.
INSERT INTO oauth_states (state, provider_id, pkce_verifier, nonce_hash, redirect_uri, purpose, session_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetOAuthState :one
SELECT * FROM oauth_states WHERE state = $1;

-- name: DeleteOAuthState :execrows
-- The row count is the SINGLE USE of the flow, so the caller needs it.
-- Two callbacks that carry the same state and the same nonce cookie -- a
-- double-clicked callback, a browser prefetch of the Location, a retried
-- navigation -- both pass the nonce check and both reach here. Exactly one
-- deletes a row; the other must be refused. Without the count the property
-- rested on the identity provider rejecting the second use of its
-- authorization code, which is somebody else's guarantee.
DELETE FROM oauth_states WHERE state = $1;

-- name: DeleteExpiredOAuthStates :execresult
DELETE FROM oauth_states WHERE expires_at < sqlc.arg(now);
