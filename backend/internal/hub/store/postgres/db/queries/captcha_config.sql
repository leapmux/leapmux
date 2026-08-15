-- name: GetCaptchaConfig :one
SELECT * FROM captcha_config WHERE id = 1;

-- name: InsertCaptchaConfig :exec
INSERT INTO captcha_config (id, enabled, algorithm, cost, memory_cost, parallelism, challenge_expiry_seconds, secret)
VALUES (1, $1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO NOTHING;

-- The update never touches `secret`: provisioning is the secret's only
-- writer, so a configuration change cannot lose or corrupt the key.
-- name: UpdateCaptchaConfig :exec
UPDATE captcha_config
SET enabled = $1, algorithm = $2, cost = $3, memory_cost = $4, parallelism = $5, challenge_expiry_seconds = $6, updated_at = NOW()
WHERE id = 1;

-- name: DeleteCaptchaConfig :exec
DELETE FROM captcha_config WHERE id = 1;

-- name: GetRateLimitConfig :one
SELECT * FROM rate_limit_config WHERE operation = $1;

-- name: ListRateLimitConfigs :many
SELECT * FROM rate_limit_config ORDER BY operation;

-- name: UpsertRateLimitConfig :exec
INSERT INTO rate_limit_config (operation, enabled, max_attempts, window_seconds)
VALUES ($1, $2, $3, $4)
ON CONFLICT (operation) DO UPDATE
SET enabled = EXCLUDED.enabled,
    max_attempts = EXCLUDED.max_attempts,
    window_seconds = EXCLUDED.window_seconds,
    updated_at = NOW();

-- name: DeleteRateLimitConfig :exec
DELETE FROM rate_limit_config WHERE operation = $1;


-- ConsumeCaptchaSalt records a solved challenge's salt as used. The
-- conflict no-op makes the call the single-use decision: 1 row = first
-- use accepted, 0 rows = replay denied.
-- name: ConsumeCaptchaSalt :execrows
INSERT INTO captcha_used_salts (salt, expires_at) VALUES ($1, $2) ON CONFLICT (salt) DO NOTHING;

-- name: DeleteExpiredCaptchaSalts :execrows
DELETE FROM captcha_used_salts WHERE expires_at < $1;
