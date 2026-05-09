-- name: CreateDeviceAuthorization :exec
INSERT INTO device_authorizations (
    device_code, user_code, device_name, interval_seconds, expires_at
) VALUES (?, ?, ?, ?, ?);

-- name: GetDeviceAuthorization :one
SELECT * FROM device_authorizations WHERE device_code = ?;

-- name: GetDeviceAuthorizationByUserCode :one
SELECT * FROM device_authorizations WHERE user_code = ?;

-- name: ApproveDeviceAuthorization :execresult
UPDATE device_authorizations
SET approved = 1, user_id = ?
WHERE device_code = ? AND consumed_at IS NULL AND expires_at > NOW(3);

-- name: ApproveDeviceAuthorizationByUserCode :execresult
UPDATE device_authorizations
SET approved = 1, user_id = ?
WHERE user_code = ? AND consumed_at IS NULL AND expires_at > NOW(3);

-- name: DenyDeviceAuthorization :execresult
UPDATE device_authorizations
SET approved = 2
WHERE device_code = ? AND consumed_at IS NULL;

-- name: ConsumeDeviceAuthorization :execresult
UPDATE device_authorizations
SET consumed_at = NOW(3)
WHERE device_code = ? AND approved = 1 AND consumed_at IS NULL;

-- name: TouchDeviceAuthorizationPoll :exec
UPDATE device_authorizations
SET last_polled_at = NOW(3)
WHERE device_code = ?;

-- name: DeleteExpiredDeviceAuthorizations :execresult
DELETE FROM device_authorizations
WHERE expires_at < ?;
