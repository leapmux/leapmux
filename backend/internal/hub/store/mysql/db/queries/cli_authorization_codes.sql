-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, admin_scope, expires_at
) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetActiveCLIAuthorizationCode :one
SELECT * FROM cli_authorization_codes
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ConsumeCLIAuthorizationCode :execresult
UPDATE cli_authorization_codes
SET consumed_at = NOW(3)
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: GetCLIAuthorizationCode :one
SELECT * FROM cli_authorization_codes WHERE code = ?;

-- name: DeleteExpiredCLIAuthorizationCodes :execresult
DELETE FROM cli_authorization_codes
WHERE expires_at < ?;
