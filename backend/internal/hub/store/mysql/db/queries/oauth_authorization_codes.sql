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

-- name: GetActiveOAuthAuthorizationCode :one
SELECT * FROM oauth_authorization_codes
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ConsumeOAuthAuthorizationCode :execresult
UPDATE oauth_authorization_codes
SET consumed_at = NOW(3)
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: GetOAuthAuthorizationCode :one
-- The row WHATEVER its state, revoked and consumed included. MySQL has no
-- RETURNING, so Consume reads through here -- and the REPLAY path needs the
-- same shape: a consumed code must still name the credential it minted so
-- the replay can revoke it.
SELECT * FROM oauth_authorization_codes WHERE code = ?;

-- name: DeleteExpiredOAuthAuthorizationCodes :execresult
DELETE FROM oauth_authorization_codes
WHERE expires_at < ?;
