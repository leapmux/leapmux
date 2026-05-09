-- name: CreateDelegationToken :exec
INSERT INTO delegation_tokens (
    id, user_id, worker_id, workspace_id, agent_id, terminal_id,
    issued_for_tab_id, issued_for_tab_type, secret_hash, refresh_hash,
    expires_at, refresh_expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetDelegationTokenByID :one
SELECT * FROM delegation_tokens WHERE id = $1;

-- name: ListDelegationTokensByUser :many
SELECT * FROM delegation_tokens
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: ListActiveDelegationTokensByUser :many
SELECT * FROM delegation_tokens
WHERE user_id = $1
  AND revoked_at IS NULL
  AND expires_at > NOW()
ORDER BY created_at DESC;

-- name: RevokeDelegationTokensByUser :execrows
UPDATE delegation_tokens
SET revoked_at = NOW()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: ListDelegationTokensRevokedSince :many
SELECT id, user_id, revoked_at FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND revoked_at > $1
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
SET last_used_at = NOW()
WHERE id = $1;

-- name: RotateDelegationTokenRefresh :exec
UPDATE delegation_tokens
SET refresh_hash = sqlc.arg(new_refresh_hash),
    refresh_expires_at = sqlc.arg(new_refresh_expires_at),
    previous_refresh_hash = sqlc.arg(prev_refresh_hash),
    previous_refresh_expires_at = sqlc.arg(prev_refresh_expires_at)
WHERE id = sqlc.arg(id);

-- name: RevokeDelegationToken :execrows
UPDATE delegation_tokens
SET revoked_at = NOW()
WHERE id = $1 AND revoked_at IS NULL;

-- name: DeleteRevokedDelegationTokensBefore :execrows
DELETE FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < $1;

-- name: DeleteExpiredDelegationTokensBefore :execrows
DELETE FROM delegation_tokens
WHERE expires_at < $1 AND revoked_at IS NULL;
