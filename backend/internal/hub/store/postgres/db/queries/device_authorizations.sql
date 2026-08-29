-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateDeviceAuthorization :exec
INSERT INTO device_authorizations (
    device_code, user_code, device_name, client_id, requested_scopes,
    granted_scopes, interval_seconds, expires_at, elevate_token_id
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,

    -- The grant starts EMPTY and the approval writes it. A pending row that
    -- already carried the ask in the grant column would be indistinguishable
    -- from an approved one at the token leg.
    '',
    $6,
    $7,
    sqlc.narg(elevate_token_id)
);

-- name: GetDeviceAuthorization :one
SELECT * FROM device_authorizations WHERE device_code = $1;

-- name: GetDeviceAuthorizationByUserCode :one
SELECT * FROM device_authorizations WHERE user_code = $1;

-- name: ApproveDeviceAuthorization :execrows
-- granted_scopes is written at APPROVAL, because approval is where the human
-- consents. The device that started the flow only ASKS (requested_scopes);
-- the browser decides, and /oauth/token reads the grant back from this row.
--
-- The row must still be PENDING. Without `approved = 0` a second POST
-- re-approves a live grant and overwrites user_id and granted_scopes, so the
-- credential reaches whoever approved LAST while the first approver read
-- "Device authorized" -- a double click or a re-submitted form is enough.
-- The same guard makes a DENIAL final: approved = 2 can never return to 1.
UPDATE device_authorizations
SET approved = 1, user_id = sqlc.arg(user_id), granted_scopes = sqlc.arg(granted_scopes)
WHERE device_code = sqlc.arg(device_code) AND approved = 0 AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: ApproveDeviceAuthorizationByUserCode :execrows
UPDATE device_authorizations
SET approved = 1, user_id = sqlc.arg(user_id), granted_scopes = sqlc.arg(granted_scopes)
WHERE user_code = sqlc.arg(user_code) AND approved = 0 AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: DenyDeviceAuthorizationByUserCode :execrows
-- Deny by user code. The browser holds the user
-- code and never the device code, so without this the activation page had no
-- way to refuse and the poller waited out the whole expiry.
--
-- It matches a PENDING row only, exactly as the approve statements do, so a
-- grant that was already answered keeps its answer.
UPDATE device_authorizations
SET approved = 2
WHERE user_code = sqlc.arg(user_code) AND approved = 0 AND consumed_at IS NULL;

-- name: ConsumeDeviceAuthorization :execrows
UPDATE device_authorizations
SET consumed_at = NOW()
WHERE device_code = $1 AND approved = 1 AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: TouchDeviceAuthorizationPoll :exec
-- last_polled_at binds the HUB's clock, one deliberate exception to the
-- header rule: the slow_down throttle subtracts this value from the hub's
-- clock, and a database clock ahead of the hub's by more than a fudge factor
-- answered every on-time poll with slow_down, stalling the flow for the skew.
UPDATE device_authorizations
SET last_polled_at = sqlc.arg(now)
WHERE device_code = sqlc.arg(device_code);

-- name: ConsumeApprovedDeviceAuthorizationsForUserClient :execresult
-- A DISCONNECT ends the authorization this account gave the app, so the
-- approved-but-unpolled grants that authorization produced are spent here:
-- without this, a code approved seconds before the disconnect stayed
-- redeemable into a fresh credential for its whole TTL.
UPDATE device_authorizations
SET consumed_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND user_id = sqlc.arg(user_id)
  AND approved = 1
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: DeleteExpiredDeviceAuthorizations :execrows
DELETE FROM device_authorizations
WHERE expires_at < $1;
