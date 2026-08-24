-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, expires_at
) VALUES ($1, $2, $3, $4, $5);

-- name: GetActiveCLIAuthorizationCode :one
SELECT * FROM cli_authorization_codes
WHERE code = $1 AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ConsumeCLIAuthorizationCode :one
UPDATE cli_authorization_codes
SET consumed_at = NOW()
WHERE code = $1 AND consumed_at IS NULL AND expires_at > sqlc.arg(now)
RETURNING *;

-- name: DeleteExpiredCLIAuthorizationCodes :execrows
DELETE FROM cli_authorization_codes
WHERE expires_at < $1;
