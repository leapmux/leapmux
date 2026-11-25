-- name: CreateOAuthProvider :exec
INSERT INTO oauth_providers (id, provider_type, name, issuer_url, client_id, client_secret, scopes, trust_email, response_mode, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: GetOAuthProviderByID :one
SELECT * FROM oauth_providers WHERE id = $1;

-- name: ListEnabledOAuthProviders :many
SELECT id, provider_type, name, issuer_url, client_id, scopes, trust_email, response_mode, enabled, created_at
FROM oauth_providers WHERE enabled = TRUE ORDER BY created_at;

-- name: ListAllOAuthProviders :many
SELECT id, provider_type, name, issuer_url, client_id, scopes, trust_email, response_mode, enabled, created_at
FROM oauth_providers ORDER BY created_at;

-- name: UpdateOAuthProviderEnabled :exec
UPDATE oauth_providers SET enabled = $1 WHERE id = $2;

-- name: UpdateOAuthProviderClientSecret :execrows
UPDATE oauth_providers SET client_secret = $1 WHERE id = $2;

-- name: DeleteOAuthProvider :exec
DELETE FROM oauth_providers WHERE id = $1;
