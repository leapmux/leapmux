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
SELECT * FROM delegation_tokens
WHERE user_id = ?
  AND revoked_at IS NULL
  AND expires_at > NOW(3)
ORDER BY created_at DESC;

-- name: RevokeDelegationTokensByUser :execresult
UPDATE delegation_tokens
SET revoked_at = NOW(3)
WHERE user_id = ? AND revoked_at IS NULL;

-- name: ListDelegationTokensRevokedSince :many
SELECT id, user_id, revoked_at FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND revoked_at > ?
ORDER BY revoked_at ASC;

-- name: MaxDelegationTokenRevokedAt :one
-- Mirror of MaxAPITokenRevokedAt for delegation_tokens. ORDER BY +
-- LIMIT 1 lets sqlc infer the return type from the underlying column.
SELECT revoked_at FROM delegation_tokens
WHERE revoked_at IS NOT NULL
ORDER BY revoked_at DESC
LIMIT 1;

-- name: TouchDelegationToken :exec
UPDATE delegation_tokens
SET last_used_at = NOW(3)
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
SET revoked_at = NOW(3)
WHERE id = ? AND revoked_at IS NULL;

-- name: DeleteRevokedDelegationTokensBefore :execresult
DELETE FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < ?;

-- name: DeleteExpiredDelegationTokensBefore :execresult
DELETE FROM delegation_tokens
WHERE expires_at < ? AND revoked_at IS NULL;
