-- name: CreateAPIToken :exec
INSERT INTO api_tokens (
    id, user_id, client_type, client_name, secret_hash, refresh_hash,
    scope, expires_at, refresh_expires_at, auth_generation
) VALUES (
    sqlc.arg(id),
    sqlc.arg(user_id),
    sqlc.arg(client_type),
    sqlc.arg(client_name),
    sqlc.arg(secret_hash),
    sqlc.arg(refresh_hash),
    sqlc.arg(scope),
    strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(expires_at)),
    strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(refresh_expires_at)),
    (SELECT auth_generation FROM users WHERE users.id = sqlc.arg(user_id))
);

-- name: GetAPITokenByID :one
SELECT * FROM api_tokens WHERE id = ?;

-- name: ListAPITokensByUser :many
SELECT * FROM api_tokens
WHERE user_id = ?
  AND (sqlc.arg(client_type) = '' OR client_type = sqlc.arg(client_type))
  AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: ListAllAPITokens :many
-- Admin listing across all users (LEFT JOIN users for the owner username so the
-- CLI does not fan out per user). Keyset on (created_at DESC, id DESC).
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

-- name: ListAllAPITokensIncludingRevoked :many
-- Forensics variant of ListAllAPITokens: includes revoked rows
-- (--include-revoked). No matching partial index serves this shape -- an
-- occasional admin forensics page may top-N sort, which is deliberate; the
-- live listings keep their partial-index seeks.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

-- name: ListAllAPITokensByUser :many
-- Per-user variant of ListAllAPITokens (the admin --user path): required
-- user_id equality on top of the same keyset + owner join.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND t.user_id = sqlc.arg(user_id)
  AND (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

-- name: ListAllAPITokensByUserIncludingRevoked :many
-- Forensics variant of ListAllAPITokensByUser: includes revoked rows
-- (--include-revoked); see ListAllAPITokensIncludingRevoked for the
-- no-matching-index note.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM api_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.user_id = sqlc.arg(user_id)
  AND (sqlc.arg(client_type) = '' OR t.client_type = sqlc.arg(client_type))
  AND (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

-- name: TouchAPIToken :exec
UPDATE api_tokens
SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: RotateAPITokenRefresh :execresult
-- Rotation rewrites BOTH secrets in place on the existing row: the
-- access secret_hash + access expires_at (so freshly-issued access
-- bearers validate against this row and use the new access TTL),
-- and the refresh_hash + refresh_expires_at (so the new refresh
-- replaces the rotated-out one). The previous refresh hash and its
-- grace window are preserved so any Hub can recognize a racing retry
-- and deterministically derive the same replacement pair.
UPDATE api_tokens
SET secret_hash = sqlc.arg(new_secret_hash),
    expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(new_expires_at)),
    refresh_hash = sqlc.arg(new_refresh_hash),
    refresh_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(new_refresh_expires_at)),
    previous_refresh_hash = sqlc.arg(prev_refresh_hash),
    previous_refresh_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(prev_refresh_expires_at)),
    last_rotated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = sqlc.arg(id)
  AND revoked_at IS NULL
  AND refresh_hash = sqlc.arg(prev_refresh_hash);

-- name: RevokeAPIToken :one
UPDATE api_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND revoked_at IS NULL
RETURNING id, user_id, revoked_at;

-- name: RevokeAPITokensByUserFast :execresult
UPDATE api_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE user_id = ? AND revoked_at IS NULL;

-- name: DeleteRevokedAPITokensBefore :execresult
-- See the matching delegation_tokens query for the rationale: revoked_at is
-- written canonical on every path, so the raw compare against the canonical
-- CAST AS TEXT cutoff is byte-exact and sargable for idx_api_tokens_revoked_at.
DELETE FROM api_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < CAST(sqlc.arg(cutoff) AS TEXT);
