-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateOAuthAuthorizationCode :exec
INSERT INTO oauth_authorization_codes (
    code, user_id, auth_generation, client_id, code_challenge, redirect_uri, granted_scopes,
    installation_name, expires_at, step_up_kind, step_up_ref_id, redemption_kind
) VALUES (
    sqlc.arg(code),
    sqlc.arg(user_id),
    sqlc.arg(auth_generation),
    sqlc.arg(client_id),
    sqlc.arg(code_challenge),
    sqlc.arg(redirect_uri),
    sqlc.arg(granted_scopes),
    sqlc.arg(installation_name),
    sqlc.arg(expires_at),
    sqlc.narg(step_up_kind),
    sqlc.arg(step_up_ref_id),
    sqlc.narg(redemption_kind)
);

-- name: MarkOAuthAuthorizationCodeMinted :execresult
-- Records WHICH credential this code produced -- a session or an app
-- credential, as the reference states -- so a replay of the same code can
-- revoke it. RFC 6749 section 4.1.2 requires exactly that, and without the
-- stamp there was nothing to name the credential by.
UPDATE oauth_authorization_codes
SET minted_kind = sqlc.narg(minted_kind),
    minted_ref_id = sqlc.arg(minted_ref_id)
WHERE code = sqlc.arg(code)
  AND consumed_at IS NOT NULL
  AND step_up_kind IS NULL
  AND minted_kind IS NULL
  AND redemption_kind = sqlc.arg(expected_redemption_kind);

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

-- name: ConsumeOAuthAuthorizationCode :execresult
UPDATE oauth_authorization_codes
SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE code = ? AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: DeleteExpiredOAuthAuthorizationCodes :execresult
-- Raw compare against a SQLiteTime instant (same canonical layout); see
-- DeleteExpiredDelegationTokensBefore for the pattern.
DELETE FROM oauth_authorization_codes
WHERE expires_at < sqlc.arg(now);

-- name: ConsumeActiveAuthorizationCodesForUserClient :execresult
-- The sibling of ConsumeApprovedDeviceAuthorizationsForUserClient: a
-- DISCONNECT spends this account's outstanding authorization codes, so a
-- consent granted seconds before it cannot be redeemed into a credential for
-- an app the account just cut off.
UPDATE oauth_authorization_codes
SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE client_id = sqlc.arg(client_id)
  AND user_id = sqlc.arg(user_id)
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);
