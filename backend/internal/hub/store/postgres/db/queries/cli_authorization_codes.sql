-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, expires_at
) VALUES ($1, $2, $3, $4, $5);

-- name: ConsumeCLIAuthorizationCode :one
UPDATE cli_authorization_codes
SET consumed_at = NOW()
WHERE code = $1 AND consumed_at IS NULL AND expires_at > NOW()
RETURNING *;

-- name: DeleteExpiredCLIAuthorizationCodes :execrows
DELETE FROM cli_authorization_codes
WHERE expires_at < $1;
