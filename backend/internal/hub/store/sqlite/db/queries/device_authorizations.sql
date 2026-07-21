-- name: CreateDeviceAuthorization :exec
INSERT INTO device_authorizations (
    device_code, user_code, device_name, interval_seconds, expires_at
) VALUES (
    sqlc.arg(device_code),
    sqlc.arg(user_code),
    sqlc.arg(device_name),
    sqlc.arg(interval_seconds),
    sqlc.arg(expires_at)
);

-- name: GetDeviceAuthorization :one
SELECT * FROM device_authorizations WHERE device_code = ?;

-- name: GetDeviceAuthorizationByUserCode :one
SELECT * FROM device_authorizations WHERE user_code = ?;

-- name: ApproveDeviceAuthorization :execresult
-- Raw compare: expires_at is stored canonical (CreateDeviceAuthorization
-- binds a SQLiteTime), so the liveness guard is millisecond-exact against the
-- same canonical RHS layout.
UPDATE device_authorizations
SET approved = 1, user_id = ?
WHERE device_code = ? AND consumed_at IS NULL AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: ApproveDeviceAuthorizationByUserCode :execresult
UPDATE device_authorizations
SET approved = 1, user_id = ?
WHERE user_code = ? AND consumed_at IS NULL AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: DenyDeviceAuthorization :execresult
UPDATE device_authorizations
SET approved = 2
WHERE device_code = ? AND consumed_at IS NULL;

-- name: ConsumeDeviceAuthorization :execresult
UPDATE device_authorizations
SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE device_code = ? AND approved = 1 AND consumed_at IS NULL
  AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: TouchDeviceAuthorizationPoll :exec
UPDATE device_authorizations
SET last_polled_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE device_code = ?;

-- name: DeleteExpiredDeviceAuthorizations :execresult
-- Raw compare against a SQLiteTime cutoff (same canonical layout); see
-- DeleteExpiredDelegationTokensBefore for the pattern.
DELETE FROM device_authorizations
WHERE expires_at < sqlc.arg(cutoff);
