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
SELECT * FROM delegation_tokens
WHERE user_id = ?
  AND revoked_at IS NULL
  AND expires_at > NOW(3)
ORDER BY created_at DESC;

-- name: RevokeDelegationTokensByUserFast :execresult
UPDATE delegation_tokens
SET revoked_at = CURRENT_TIMESTAMP(6)
WHERE user_id = ? AND revoked_at IS NULL;

-- name: TouchDelegationToken :exec
UPDATE delegation_tokens
SET last_used_at = NOW(3)
WHERE id = ?;

-- name: GetLiveDelegationTokenForUpdate :one
SELECT id, user_id FROM delegation_tokens
WHERE id = ? AND revoked_at IS NULL
FOR UPDATE;

-- name: RevokeDelegationTokenAt :execresult
UPDATE delegation_tokens
SET revoked_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: DeleteRevokedDelegationTokensBefore :execresult
DELETE FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < ?;

-- name: DeleteExpiredDelegationTokensBefore :execresult
DELETE FROM delegation_tokens
WHERE expires_at < ? AND revoked_at IS NULL;
