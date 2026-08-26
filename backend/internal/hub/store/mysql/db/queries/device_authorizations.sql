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
UPDATE device_authorizations
SET approved = 1, user_id = sqlc.arg(user_id), admin_scope = sqlc.arg(admin_scope)
WHERE device_code = sqlc.arg(device_code) AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ApproveDeviceAuthorizationByUserCode :execresult
UPDATE device_authorizations
SET approved = 1, user_id = sqlc.arg(user_id), admin_scope = sqlc.arg(admin_scope)
WHERE user_code = sqlc.arg(user_code) AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: DenyDeviceAuthorization :execresult
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
