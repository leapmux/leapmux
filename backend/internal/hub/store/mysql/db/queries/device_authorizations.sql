-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateDeviceAuthorization :exec
INSERT INTO device_authorizations (
    device_code, user_code, device_name, interval_seconds, expires_at, elevate_token_id
) VALUES (?, ?, ?, ?, ?, sqlc.narg(elevate_token_id));

-- name: GetDeviceAuthorization :one
SELECT * FROM device_authorizations WHERE device_code = ?;

-- name: GetDeviceAuthorizationByUserCode :one
SELECT * FROM device_authorizations WHERE user_code = ?;

-- name: ApproveDeviceAuthorization :execresult
-- admin_scope is written at APPROVAL, because approval is where the human
-- consents. The device that started the flow only asks for the scope; the
-- browser decides it, and /auth/cli/token reads it back from this row.
--
-- The row must still be PENDING. Without `approved = 0` a second POST
-- re-approves a live grant and overwrites user_id and admin_scope, so the
-- credential reaches whoever approved LAST while the first approver read
-- "Device authorized" -- a double click or a re-submitted form is enough.
-- The same guard makes a DENIAL final: approved = 2 can never return to 1.
UPDATE device_authorizations
SET approved = 1, user_id = sqlc.arg(user_id), admin_scope = sqlc.arg(admin_scope)
WHERE device_code = sqlc.arg(device_code) AND approved = 0 AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: ApproveDeviceAuthorizationByUserCode :execresult
UPDATE device_authorizations
SET approved = 1, user_id = sqlc.arg(user_id), admin_scope = sqlc.arg(admin_scope)
WHERE user_code = sqlc.arg(user_code) AND approved = 0 AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: DenyDeviceAuthorization :execresult
-- Deny is FINAL, and the approve statements above are what make it so: they
-- match a pending row only, so a denied grant can never become approved
-- again. Deny writes no consumed_at, because the poll must keep answering
-- access_denied rather than "device_code already used".
UPDATE device_authorizations
SET approved = 2
WHERE device_code = ? AND consumed_at IS NULL;

-- name: ConsumeDeviceAuthorization :execresult
UPDATE device_authorizations
SET consumed_at = NOW(3)
WHERE device_code = ? AND approved = 1 AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: TouchDeviceAuthorizationPoll :exec
UPDATE device_authorizations
SET last_polled_at = NOW(3)
WHERE device_code = ?;

-- name: DeleteExpiredDeviceAuthorizations :execresult
DELETE FROM device_authorizations
WHERE expires_at < ?;
