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

-- CountUsersOrphanedByProvider counts the live accounts whose ONLY login
-- method is a link to this provider: no password set, and no link to any
-- other provider. Removing the provider row cascades every link away, so
-- each of these accounts loses its last way in and only the recovery CLI
-- can restore it.
--
-- "the only link" is expressed as "exactly one link, and it is this
-- provider's" rather than as a NOT EXISTS over the other providers, so
-- the query takes the provider id as ONE parameter. The primary key
-- (user_id, provider_id) is what makes the two forms equal.
-- name: CountUsersOrphanedByProvider :one
SELECT COUNT(*) FROM users u
WHERE u.first_credential_exempt = FALSE
  AND u.deleted_at IS NULL
  AND (SELECT COUNT(*) FROM oauth_user_links l WHERE l.user_id = u.id) = 1
  AND EXISTS (
    SELECT 1 FROM oauth_user_links l2
    WHERE l2.user_id = u.id AND l2.provider_id = $1
  );
