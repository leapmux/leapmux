-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, expires_at
) VALUES (?, ?, ?, ?, ?);

-- name: GetActiveCLIAuthorizationCode :one
SELECT * FROM cli_authorization_codes
WHERE code = ? AND consumed_at IS NULL AND expires_at > NOW(3);

-- name: ConsumeCLIAuthorizationCode :execresult
UPDATE cli_authorization_codes
SET consumed_at = NOW(3)
WHERE code = ? AND consumed_at IS NULL AND expires_at > NOW(3);

-- name: GetCLIAuthorizationCode :one
SELECT * FROM cli_authorization_codes WHERE code = ?;

-- name: DeleteExpiredCLIAuthorizationCodes :execresult
DELETE FROM cli_authorization_codes
WHERE expires_at < ?;
