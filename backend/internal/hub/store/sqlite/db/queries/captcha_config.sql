-- name: GetCaptchaConfig :one
SELECT * FROM captcha_config WHERE id = 1;

-- name: InsertCaptchaConfig :exec
INSERT INTO captcha_config (id, enabled, algorithm, cost, memory_cost, parallelism, challenge_expiry_seconds, secret)
VALUES (1, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- The update never touches `secret`: provisioning is the secret's only
-- writer, so a configuration change cannot lose or corrupt the key.
-- name: UpdateCaptchaConfig :exec
UPDATE captcha_config
SET enabled = ?, algorithm = ?, cost = ?, memory_cost = ?, parallelism = ?, challenge_expiry_seconds = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE id = 1;

-- name: DeleteCaptchaConfig :exec
DELETE FROM captcha_config WHERE id = 1;

-- name: GetRateLimitConfig :one
SELECT * FROM rate_limit_config WHERE operation = ?;

-- name: ListRateLimitConfigs :many
SELECT * FROM rate_limit_config ORDER BY operation;

-- name: UpsertRateLimitConfig :exec
INSERT INTO rate_limit_config (operation, enabled, max_attempts, window_seconds)
VALUES (?, ?, ?, ?)
ON CONFLICT (operation) DO UPDATE
SET enabled = excluded.enabled,
    max_attempts = excluded.max_attempts,
    window_seconds = excluded.window_seconds,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now');

-- name: DeleteRateLimitConfig :exec
DELETE FROM rate_limit_config WHERE operation = ?;


-- ConsumeCaptchaSalt records a solved challenge's salt as used. The
-- conflict no-op makes the call the single-use decision: 1 row = first
-- use accepted, 0 rows = replay denied.
-- name: ConsumeCaptchaSalt :execrows
INSERT INTO captcha_used_salts (salt, expires_at) VALUES (?, ?) ON CONFLICT (salt) DO NOTHING;

-- name: DeleteExpiredCaptchaSalts :execrows
DELETE FROM captcha_used_salts WHERE expires_at < ?;
