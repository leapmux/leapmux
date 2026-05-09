-- name: CreateAPIToken :exec
INSERT INTO api_tokens (
    id, user_id, client_type, client_name, secret_hash, refresh_hash,
    scope, expires_at, refresh_expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAPITokenByID :one
SELECT * FROM api_tokens WHERE id = ?;

-- name: ListAPITokensByUser :many
SELECT * FROM api_tokens
WHERE user_id = ?
  AND (sqlc.arg(client_type) = '' OR client_type = sqlc.arg(client_type))
  AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: TouchAPIToken :exec
UPDATE api_tokens
SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: RotateAPITokenRefresh :exec
-- Rotation rewrites BOTH secrets in place on the existing row: the
-- access secret_hash + access expires_at (so freshly-issued access
-- bearers validate against this row and use the new access TTL),
-- and the refresh_hash + refresh_expires_at (so the new refresh
-- replaces the rotated-out one). The previous refresh hash and its
-- grace window are preserved so the racing retry path can still
-- re-emit the cached pair from the in-process grace cache while
-- the on-disk row only carries the current refresh hash.
UPDATE api_tokens
SET secret_hash = sqlc.arg(new_secret_hash),
    expires_at = sqlc.arg(new_expires_at),
    refresh_hash = sqlc.arg(new_refresh_hash),
    refresh_expires_at = sqlc.arg(new_refresh_expires_at),
    previous_refresh_hash = sqlc.arg(prev_refresh_hash),
    previous_refresh_expires_at = sqlc.arg(prev_refresh_expires_at),
    last_rotated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = sqlc.arg(id);

-- name: RevokeAPIToken :execresult
UPDATE api_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND revoked_at IS NULL;

-- name: RevokeAPITokensByUser :execresult
-- Bulk-revokes every live api_tokens row for a user. Used when an
-- admin command kills the user's auth basis (delete, password
-- reset, force-logout-all). The hub's revocation watcher polls for
-- newly-revoked rows and fires `EvictBearer` +
-- `CloseChannelsByBearer` per token, so already-open channels die
-- in lock-step with the row revocation.
UPDATE api_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE user_id = ? AND revoked_at IS NULL;

-- name: ListAPITokensRevokedSince :many
-- Returns api_tokens revoked after the high-water mark passed in.
-- Comparison uses strftime on both sides because SQLite's plain
-- datetime() truncates to seconds, which would lose revocations
-- that landed in the same second as the watcher's previous
-- high-water mark. The %f specifier keeps milliseconds.
SELECT id, user_id, revoked_at FROM api_tokens
WHERE revoked_at IS NOT NULL
  AND strftime('%Y-%m-%dT%H:%M:%fZ', revoked_at) > strftime('%Y-%m-%dT%H:%M:%fZ', ?)
ORDER BY revoked_at ASC;

-- name: MaxAPITokenRevokedAt :one
-- Returns the most recent revoked_at across all revoked api_tokens
-- rows. The hub's revocation watcher calls this once at bootstrap to
-- seed its watermark past every historical revocation in O(log N)
-- (index seek) instead of materializing every row via
-- ListAPITokensRevokedSince. The query reads a real column (not an
-- aggregate) so sqlc can infer the return type from the schema; the
-- ORDER BY + LIMIT 1 turns the read into an index seek under the
-- existing revoked_at index.
--
-- ASCII-only on purpose: sqlc-sqlite's parser rejects multi-byte
-- characters (e.g. em-dash) inside the comment block above a query.
SELECT revoked_at FROM api_tokens
WHERE revoked_at IS NOT NULL
ORDER BY revoked_at DESC
LIMIT 1;

-- name: DeleteRevokedAPITokensBefore :execresult
-- See the matching delegation_tokens query for the rationale: wrap
-- both sides in datetime() so format mismatches between the strftime
-- write path and Go's bound time.Time don't silently skip rows.
DELETE FROM api_tokens
WHERE revoked_at IS NOT NULL AND datetime(revoked_at) < datetime(?);
