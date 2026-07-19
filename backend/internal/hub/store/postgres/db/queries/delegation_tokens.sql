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
SELECT * FROM delegation_tokens WHERE id = $1;

-- name: ListAllDelegationTokens :many
-- Admin listing across all users (LEFT JOIN users for the owner username).
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR t.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (t.created_at = sqlc.narg(cursor_time)::timestamptz AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg('limit');

-- name: ListAllDelegationTokensIncludingRevoked :many
-- Forensics variant of ListAllDelegationTokens: includes revoked rows
-- (--include-revoked). No matching partial index serves this shape -- an
-- occasional admin forensics page may top-N sort, which is deliberate; the
-- live listings keep their partial-index seeks.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR t.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (t.created_at = sqlc.narg(cursor_time)::timestamptz AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg('limit');

-- name: ListAllDelegationTokensByUser :many
-- Per-user variant of ListAllDelegationTokens (the admin --user path): required
-- user_id equality on top of the same keyset + owner join.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND t.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR t.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (t.created_at = sqlc.narg(cursor_time)::timestamptz AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg('limit');

-- name: ListAllDelegationTokensByUserIncludingRevoked :many
-- Forensics variant of ListAllDelegationTokensByUser: includes revoked rows
-- (--include-revoked); see ListAllDelegationTokensIncludingRevoked for the
-- no-matching-index note.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, (u.id IS NULL)::boolean AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR t.created_at < sqlc.narg(cursor_time)::timestamptz
       OR (t.created_at = sqlc.narg(cursor_time)::timestamptz AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg('limit');

-- name: ListActiveDelegationTokensByUser :many
SELECT * FROM delegation_tokens
WHERE user_id = $1
  AND revoked_at IS NULL
  AND expires_at > NOW()
ORDER BY created_at DESC;

-- name: RevokeDelegationTokensByUserFast :execrows
UPDATE delegation_tokens
SET revoked_at = clock_timestamp()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: TouchDelegationToken :exec
UPDATE delegation_tokens
SET last_used_at = NOW()
WHERE id = $1;

-- name: RevokeDelegationToken :one
UPDATE delegation_tokens
SET revoked_at = clock_timestamp()
WHERE id = $1 AND revoked_at IS NULL
RETURNING id, user_id, revoked_at;

-- name: DeleteRevokedDelegationTokensBefore :execrows
DELETE FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < $1;

-- name: DeleteExpiredDelegationTokensBefore :execrows
DELETE FROM delegation_tokens
WHERE expires_at < $1 AND revoked_at IS NULL;
