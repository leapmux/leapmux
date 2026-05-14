-- name: CreateDelegationToken :exec
INSERT INTO delegation_tokens (
    id, user_id, worker_id, workspace_id, agent_id, terminal_id,
    issued_for_tab_id, issued_for_tab_type, secret_hash, refresh_hash,
    expires_at, refresh_expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDelegationTokenByID :one
SELECT * FROM delegation_tokens WHERE id = ?;

-- name: ListDelegationTokensByUser :many
SELECT * FROM delegation_tokens
WHERE user_id = ? AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: ListActiveDelegationTokensByUser :many
-- Returns rows that are still usable: not revoked AND not yet expired.
-- Used by lifecycle hooks (logout, password change, account deactivation)
-- that want to enumerate live tokens before bulk-revoking them.
SELECT * FROM delegation_tokens
WHERE user_id = ?
  AND revoked_at IS NULL
  AND datetime(expires_at) > datetime('now')
ORDER BY created_at DESC;

-- name: RevokeDelegationTokensByUser :execresult
-- Bulk-revokes every live delegation token for a user. Used when a
-- user's auth basis is invalidated (logout, password change, account
-- deactivation) so spawned-agent bearers tied to that user die at the
-- hub. Already-revoked rows are left untouched so revoked_at remains
-- the original revocation timestamp.
UPDATE delegation_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE user_id = ? AND revoked_at IS NULL;

-- name: ListDelegationTokensRevokedSince :many
-- Returns delegation_tokens revoked after the watcher's high-water
-- mark. Comparison uses strftime so millisecond resolution is
-- preserved (see ListAPITokensRevokedSince for the rationale).
SELECT id, user_id, revoked_at FROM delegation_tokens
WHERE revoked_at IS NOT NULL
  AND strftime('%Y-%m-%dT%H:%M:%fZ', revoked_at) > strftime('%Y-%m-%dT%H:%M:%fZ', ?)
ORDER BY revoked_at ASC;

-- name: MaxDelegationTokenRevokedAt :one
-- Mirror of MaxAPITokenRevokedAt for the delegation_tokens table.
-- ORDER BY + LIMIT 1 reads a real column so sqlc can infer the
-- return type and the index seek stays O(log N).
SELECT revoked_at FROM delegation_tokens
WHERE revoked_at IS NOT NULL
ORDER BY revoked_at DESC
LIMIT 1;

-- name: TouchDelegationToken :exec
UPDATE delegation_tokens
SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: RotateDelegationTokenRefresh :exec
UPDATE delegation_tokens
SET refresh_hash = sqlc.arg(new_refresh_hash),
    refresh_expires_at = sqlc.arg(new_refresh_expires_at),
    previous_refresh_hash = sqlc.arg(prev_refresh_hash),
    previous_refresh_expires_at = sqlc.arg(prev_refresh_expires_at)
WHERE id = sqlc.arg(id);

-- name: RevokeDelegationToken :execresult
UPDATE delegation_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND revoked_at IS NULL;

-- name: DeleteRevokedDelegationTokensBefore :execresult
-- Both revoked_at and the bound cutoff go through datetime() so the
-- comparison is tolerant of format differences: revoked_at is stored
-- as ISO-8601 with 'T'/'Z' via strftime, while Go's database/sql
-- binds time.Time in a driver-native form. Without the datetime()
-- wrap the < comparison falls back to a lexicographic test that
-- silently fails on legitimate cutoffs (the 'T' separator on the
-- stored side sorts after the ' ' separator on the bound side).
DELETE FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND datetime(revoked_at) < datetime(?);

-- name: DeleteExpiredDelegationTokensBefore :execresult
DELETE FROM delegation_tokens
WHERE expires_at < ? AND revoked_at IS NULL;
