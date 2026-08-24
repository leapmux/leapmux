-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened keep the
-- database clock.

-- name: CreateDeviceAuthorization :exec
INSERT INTO device_authorizations (
    device_code, user_code, device_name, interval_seconds, expires_at
) VALUES ($1, $2, $3, $4, $5);

-- name: GetDeviceAuthorization :one
SELECT * FROM device_authorizations WHERE device_code = $1;

-- name: GetDeviceAuthorizationByUserCode :one
SELECT * FROM device_authorizations WHERE user_code = $1;

-- name: ApproveDeviceAuthorization :execrows
UPDATE device_authorizations
SET approved = 1, user_id = $1
WHERE device_code = $2 AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ApproveDeviceAuthorizationByUserCode :execrows
UPDATE device_authorizations
SET approved = 1, user_id = $1
WHERE user_code = $2 AND consumed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: DenyDeviceAuthorization :execrows
UPDATE device_authorizations
SET approved = 2
WHERE device_code = $1 AND consumed_at IS NULL;

-- name: ConsumeDeviceAuthorization :execrows
UPDATE device_authorizations
SET consumed_at = NOW()
WHERE device_code = $1 AND approved = 1 AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: TouchDeviceAuthorizationPoll :exec
UPDATE device_authorizations
SET last_polled_at = NOW()
WHERE device_code = $1;

-- name: DeleteExpiredDeviceAuthorizations :execrows
DELETE FROM device_authorizations
WHERE expires_at < $1;
