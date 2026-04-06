-- name: CreateOAuthUserLink :exec
INSERT INTO oauth_user_links (user_id, provider_id, provider_subject)
VALUES ($1, $2, $3);

-- name: GetOAuthUserLink :one
SELECT * FROM oauth_user_links
WHERE provider_id = $1 AND provider_subject = $2;

-- name: ListOAuthUserLinksByUser :many
SELECT * FROM oauth_user_links WHERE user_id = $1;

-- name: DeleteOAuthUserLink :exec
DELETE FROM oauth_user_links WHERE user_id = $1 AND provider_id = $2;

-- name: DeleteOAuthUserLinksByProvider :exec
DELETE FROM oauth_user_links WHERE provider_id = $1;
