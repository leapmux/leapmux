-- name: CreateOAuthState :exec
INSERT INTO oauth_states (state, provider_id, pkce_verifier, redirect_uri, expires_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetOAuthState :one
SELECT * FROM oauth_states WHERE state = ?;

-- name: DeleteOAuthState :exec
DELETE FROM oauth_states WHERE state = ?;

-- name: DeleteExpiredOAuthStates :exec
DELETE FROM oauth_states WHERE expires_at < datetime('now');
