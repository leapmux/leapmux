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

-- name: ListAllDelegationTokens :many
-- Admin listing across all users (LEFT JOIN users for the owner username so the
-- CLI does not fan out per user). Keyset on (created_at DESC, id DESC).
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

-- name: ListAllDelegationTokensIncludingRevoked :many
-- Forensics variant of ListAllDelegationTokens: includes revoked rows
-- (--include-revoked). No matching partial index serves this shape -- an
-- occasional admin forensics page may top-N sort, which is deliberate; the
-- live listings keep their partial-index seeks.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

-- name: ListAllDelegationTokensByUser :many
-- Per-user variant of ListAllDelegationTokens (the admin --user path): required
-- user_id equality on top of the same keyset + owner join.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.revoked_at IS NULL
  AND t.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

-- name: ListAllDelegationTokensByUserIncludingRevoked :many
-- Forensics variant of ListAllDelegationTokensByUser: includes revoked rows
-- (--include-revoked); see ListAllDelegationTokensIncludingRevoked for the
-- no-matching-index note.
SELECT sqlc.embed(t), COALESCE(u.username, '') AS owner_username, CAST(u.id IS NULL AS BOOLEAN) AS owner_deleted
FROM delegation_tokens t
LEFT JOIN users u ON t.user_id = u.id AND u.deleted_at IS NULL
WHERE t.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time) IS NULL
       OR t.created_at < sqlc.narg(cursor_time)
       OR (t.created_at = sqlc.narg(cursor_time) AND t.id < sqlc.narg(cursor_id)))
ORDER BY t.created_at DESC, t.id DESC
LIMIT sqlc.arg(limit);

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
-- Raw compare: every revoked_at write path stores the canonical strftime
-- layout (RevokeDelegationToken and RevokeDelegationTokensByUserFast both SET
-- strftime('%Y-%m-%dT%H:%M:%fZ','now')), and the Go side binds a
-- formatSQLiteTime-formatted cutoff (CAST AS TEXT -> string param), so the
-- lexicographic < is byte-exact. Unlike the previous datetime() wrap on the
-- column, this is sargable: the partial idx_delegation_tokens_revoked_at
-- serves an upper-bounded SEARCH of just the cutoff-eligible rows instead of
-- reading every revoked row on each hourly sweep of this high-churn table.
DELETE FROM delegation_tokens
WHERE revoked_at IS NOT NULL AND revoked_at < CAST(sqlc.arg(cutoff) AS TEXT);

-- name: DeleteExpiredDelegationTokensBefore :execresult
DELETE FROM delegation_tokens
WHERE expires_at < ? AND revoked_at IS NULL;
