-- name: CreateDelegationToken :exec
INSERT INTO delegation_tokens (
    id, user_id, worker_id, workspace_id, agent_id, terminal_id,
    issued_for_tab_id, issued_for_tab_type, secret_hash, refresh_hash,
    expires_at, refresh_expires_at, auth_generation
) VALUES (
    sqlc.arg(id),
    sqlc.arg(user_id),
    sqlc.arg(worker_id),
    sqlc.arg(workspace_id),
    sqlc.arg(agent_id),
    sqlc.arg(terminal_id),
    sqlc.arg(issued_for_tab_id),
    sqlc.arg(issued_for_tab_type),
    sqlc.arg(secret_hash),
    sqlc.arg(refresh_hash),
    sqlc.arg(expires_at),
    sqlc.arg(refresh_expires_at),
    (SELECT auth_generation FROM users WHERE users.id = sqlc.arg(user_id))
);

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

-- name: RevokeDelegationTokensByUserFast :execresult
UPDATE delegation_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE user_id = ? AND revoked_at IS NULL;

-- name: TouchDelegationToken :exec
UPDATE delegation_tokens
SET last_used_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: RevokeDelegationToken :one
UPDATE delegation_tokens
SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND revoked_at IS NULL
RETURNING id, user_id, revoked_at;

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
