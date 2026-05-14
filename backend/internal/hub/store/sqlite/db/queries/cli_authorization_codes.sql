-- name: CreateCLIAuthorizationCode :exec
INSERT INTO cli_authorization_codes (
    code, user_id, code_challenge, device_name, expires_at
) VALUES (?, ?, ?, ?, ?);

-- name: ConsumeCLIAuthorizationCode :one
-- expires_at is compared via datetime() so both operands are in
-- SQLite's canonical "YYYY-MM-DD HH:MM:SS" form. A bare lexicographic
-- comparison would silently misbehave when the row's stored timezone
-- offset string differs from strftime('now') by a single character.
UPDATE cli_authorization_codes
SET consumed_at = datetime('now')
WHERE code = ? AND consumed_at IS NULL AND datetime(expires_at) > datetime('now')
RETURNING *;

-- name: DeleteExpiredCLIAuthorizationCodes :execresult
DELETE FROM cli_authorization_codes
WHERE expires_at < ?;
