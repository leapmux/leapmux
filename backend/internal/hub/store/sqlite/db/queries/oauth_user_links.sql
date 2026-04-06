-- name: CreateOAuthUserLink :exec
INSERT INTO oauth_user_links (user_id, provider_id, provider_subject)
VALUES (?, ?, ?);

-- name: GetOAuthUserLink :one
SELECT * FROM oauth_user_links
WHERE provider_id = ? AND provider_subject = ?;

-- name: ListOAuthUserLinksByUser :many
SELECT * FROM oauth_user_links WHERE user_id = ?;

-- name: DeleteOAuthUserLink :exec
DELETE FROM oauth_user_links WHERE user_id = ? AND provider_id = ?;

-- name: DeleteOAuthUserLinksByProvider :exec
DELETE FROM oauth_user_links WHERE provider_id = ?;
