-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateOAuthAuthorizationCode :exec
INSERT INTO oauth_authorization_codes (
    code, user_id, client_id, code_challenge, redirect_uri, granted_scopes,
    installation_name, expires_at
) VALUES (
    sqlc.arg(code),
    sqlc.arg(user_id),
    sqlc.arg(client_id),
    sqlc.arg(code_challenge),
    sqlc.arg(redirect_uri),
    sqlc.arg(granted_scopes),
    sqlc.arg(installation_name),
    sqlc.arg(expires_at)
);

-- name: MarkOAuthAuthorizationCodeMinted :exec
-- Records WHICH credential this code produced, so a replay of the same code
-- can revoke it. RFC 6749 section 4.1.2 requires exactly that, and without the
-- column there was nothing to name the token by.
UPDATE oauth_authorization_codes
SET minted_token_id = sqlc.arg(minted_token_id)
WHERE code = sqlc.arg(code);

-- name: GetOAuthAuthorizationCode :one
-- The row WHATEVER its state, for the replay path: a consumed code must still
-- name the credential it minted so the replay can revoke it.
SELECT * FROM oauth_authorization_codes WHERE code = sqlc.arg(code);

-- name: GetActiveOAuthAuthorizationCode :one
-- Raw compare: expires_at is stored canonical (CreateOAuthAuthorizationCode
-- binds a SQLiteTime), so the liveness guard is millisecond-exact against the
-- same canonical RHS layout.
SELECT * FROM oauth_authorization_codes
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ConsumeOAuthAuthorizationCode :one
UPDATE oauth_authorization_codes
SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now)
RETURNING *;

-- name: DeleteExpiredOAuthAuthorizationCodes :execresult
-- Raw compare against a SQLiteTime instant (same canonical layout); see
-- DeleteExpiredDelegationTokensBefore for the pattern.
DELETE FROM oauth_authorization_codes
WHERE expires_at < sqlc.arg(now);
