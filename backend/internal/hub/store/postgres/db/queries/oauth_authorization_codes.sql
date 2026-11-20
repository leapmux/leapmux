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
-- Records WHICH credential id this code produced. redemption_kind supplies
-- the kind, so a replay can revoke the exact credential without a second copy.
UPDATE oauth_authorization_codes
SET minted_ref_id = sqlc.arg(minted_ref_id)
WHERE code = sqlc.arg(code)
  AND consumed_at IS NOT NULL
  AND step_up_kind IS NULL
  AND minted_ref_id = ''
  AND redemption_kind = sqlc.arg(expected_redemption_kind);

-- name: GetOAuthAuthorizationCode :one
-- The row WHATEVER its state, for the replay path: a consumed code must still
-- name the credential it minted so the replay can revoke it.
SELECT * FROM oauth_authorization_codes WHERE code = sqlc.arg(code);

-- name: GetActiveOAuthAuthorizationCode :one
SELECT * FROM oauth_authorization_codes
WHERE code = $1 AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ConsumeOAuthAuthorizationCode :execresult
UPDATE oauth_authorization_codes
SET consumed_at = NOW()
WHERE code = $1 AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: DeleteExpiredOAuthAuthorizationCodes :execrows
DELETE FROM oauth_authorization_codes
WHERE expires_at < $1;

-- name: ConsumeActiveAuthorizationCodesForUserClient :execresult
-- The sibling of ConsumeApprovedDeviceAuthorizationsForUserClient: a
-- DISCONNECT spends this account's outstanding authorization codes, so a
-- consent granted seconds before it cannot be redeemed into a credential for
-- an app the account just cut off.
UPDATE oauth_authorization_codes
SET consumed_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND user_id = sqlc.arg(user_id)
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);
