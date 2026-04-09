-- name: CreateOAuthState :exec
INSERT INTO oauth_states (state, provider_id, pkce_verifier, redirect_uri, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetOAuthState :one
SELECT * FROM oauth_states WHERE state = $1;

-- name: DeleteOAuthState :exec
DELETE FROM oauth_states WHERE state = $1;

-- name: DeleteExpiredOAuthStates :execresult
DELETE FROM oauth_states WHERE expires_at < NOW();
