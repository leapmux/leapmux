-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, admin_scope, expires_at
) VALUES (
    sqlc.arg(code),
    sqlc.arg(user_id),
    sqlc.arg(code_challenge),
    sqlc.arg(device_name),
    sqlc.arg(admin_scope),
    sqlc.arg(expires_at)
);

-- name: GetActiveCLIAuthorizationCode :one
-- Raw compare: expires_at is stored canonical (CreateCLIAuthorizationCode
-- binds a SQLiteTime), so the liveness guard is millisecond-exact against the
-- same canonical RHS layout.
SELECT * FROM cli_authorization_codes
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ConsumeCLIAuthorizationCode :one
UPDATE cli_authorization_codes
SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now)
RETURNING *;

-- name: DeleteExpiredCLIAuthorizationCodes :execresult
-- Raw compare against a SQLiteTime instant (same canonical layout); see
-- DeleteExpiredDelegationTokensBefore for the pattern.
DELETE FROM cli_authorization_codes
WHERE expires_at < sqlc.arg(now);
