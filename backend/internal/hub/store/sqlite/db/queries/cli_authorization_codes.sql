-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, expires_at
) VALUES (
    sqlc.arg(code),
    sqlc.arg(user_id),
    sqlc.arg(code_challenge),
    sqlc.arg(device_name),
    strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(expires_at))
);

-- name: GetActiveCLIAuthorizationCode :one
-- Raw compare: expires_at is stored canonical (CreateCLIAuthorizationCode
-- wraps the bound instant in strftime), so the liveness guard is
-- millisecond-exact against the same canonical RHS layout.
SELECT * FROM cli_authorization_codes
WHERE code = ? AND consumed_at IS NULL AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: ConsumeCLIAuthorizationCode :one
UPDATE cli_authorization_codes
SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE code = ? AND consumed_at IS NULL AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
RETURNING *;

-- name: DeleteExpiredCLIAuthorizationCodes :execresult
-- Raw compare against a formatSQLiteTime-formatted cutoff (CAST AS TEXT ->
-- string param); see DeleteExpiredDelegationTokensBefore for the pattern.
DELETE FROM cli_authorization_codes
WHERE expires_at < CAST(sqlc.arg(cutoff) AS TEXT);
