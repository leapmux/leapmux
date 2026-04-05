-- name: CreateOAuthProvider :exec
INSERT INTO oauth_providers (id, provider_type, name, issuer_url, client_id, client_secret, scopes, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetOAuthProviderByID :one
SELECT * FROM oauth_providers WHERE id = ?;

-- name: ListEnabledOAuthProviders :many
SELECT id, provider_type, name, issuer_url, client_id, scopes, enabled, created_at
FROM oauth_providers WHERE enabled = 1 ORDER BY created_at;

-- name: ListAllOAuthProviders :many
SELECT id, provider_type, name, issuer_url, client_id, scopes, enabled, created_at
FROM oauth_providers ORDER BY created_at;

-- name: UpdateOAuthProviderEnabled :exec
UPDATE oauth_providers SET enabled = ? WHERE id = ?;

-- name: ListAllOAuthProvidersWithSecrets :many
SELECT * FROM oauth_providers ORDER BY created_at;

-- name: DeleteOAuthProvider :exec
DELETE FROM oauth_providers WHERE id = ?;
