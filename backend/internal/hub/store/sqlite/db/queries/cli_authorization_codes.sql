-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, expires_at
) VALUES (?, ?, ?, ?, ?);

-- name: GetActiveCLIAuthorizationCode :one
SELECT * FROM cli_authorization_codes
WHERE code = ? AND consumed_at IS NULL AND julianday(expires_at) > julianday('now');

-- name: ConsumeCLIAuthorizationCode :one
-- julianday() normalizes timezone representations without discarding the
-- fractional seconds needed at the exact expiry boundary.
UPDATE cli_authorization_codes
SET consumed_at = datetime('now')
WHERE code = ? AND consumed_at IS NULL AND julianday(expires_at) > julianday('now')
RETURNING *;

-- name: DeleteExpiredCLIAuthorizationCodes :execresult
DELETE FROM cli_authorization_codes
WHERE expires_at < ?;
