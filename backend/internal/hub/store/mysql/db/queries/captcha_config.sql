-- name: GetCaptchaConfig :one
SELECT * FROM captcha_config WHERE id = 1;

-- name: InsertCaptchaConfig :exec
INSERT INTO captcha_config (id, enabled, algorithm, cost, memory_cost, parallelism, challenge_expiry_seconds, secret)
VALUES (1, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE id = id;

-- The update never touches `secret`: provisioning is the secret's only
-- writer, so a configuration change cannot lose or corrupt the key.
-- name: UpdateCaptchaConfig :exec
UPDATE captcha_config
SET enabled = ?, algorithm = ?, cost = ?, memory_cost = ?, parallelism = ?, challenge_expiry_seconds = ?, updated_at = CURRENT_TIMESTAMP(3)
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
ON DUPLICATE KEY UPDATE
    enabled = VALUES(enabled),
    max_attempts = VALUES(max_attempts),
    window_seconds = VALUES(window_seconds),
    updated_at = CURRENT_TIMESTAMP(3);

-- name: DeleteRateLimitConfig :exec
DELETE FROM rate_limit_config WHERE operation = ?;


-- ConsumeCaptchaSalt records a solved challenge's salt as used. The
-- conflict no-op makes the call the single-use decision: 1 row = first
-- use accepted, 0 rows = replay denied.
-- name: ConsumeCaptchaSalt :execrows
INSERT INTO captcha_used_salts (salt, expires_at) VALUES (?, ?) ON DUPLICATE KEY UPDATE salt = salt;

-- name: DeleteExpiredCaptchaSalts :execrows
DELETE FROM captcha_used_salts WHERE expires_at < ?;
