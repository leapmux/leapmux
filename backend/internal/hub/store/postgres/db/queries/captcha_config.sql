-- name: GetSelectedCaptchaConfig :one
SELECT * FROM captcha_config WHERE selected = TRUE;

-- name: GetCaptchaConfig :one
SELECT * FROM captcha_config WHERE provider = $1;

-- name: ListCaptchaProviders :many
SELECT * FROM captcha_config ORDER BY provider;

-- Provisioning inserts the row unselected; the caller activates it in a
-- separate statement, so a provision race can never leave two rows
-- selected. The conflict no-op makes racing first-use provisions a
-- one-winner race - the loser's fresh secret is simply discarded.
-- name: InsertCaptchaConfigIfAbsent :exec
INSERT INTO captcha_config (provider, selected, enabled, secret, settings)
VALUES ($1, FALSE, TRUE, $2, $3)
ON CONFLICT (provider) DO NOTHING;

-- UpdateCaptchaSettings rewrites one existing row's settings and never
-- touches its secret, so a settings change can never lose the key. It
-- matches zero rows when the provider has no row; the callers provision
-- first (or upsert with a secret below).
-- name: UpdateCaptchaSettings :exec
UPDATE captcha_config
SET settings = $1, updated_at = NOW()
WHERE provider = $2;

-- The admin CLI's write path when a secret accompanies the settings
-- (first configuration of an external provider, or key rotation): both
-- are always written. The secret is deliberately required here - a
-- secret-less row would fail verification on every submission.
-- name: UpsertCaptchaConfig :exec
INSERT INTO captcha_config (provider, selected, enabled, secret, settings)
VALUES ($1, FALSE, TRUE, $2, $3)
ON CONFLICT (provider) DO UPDATE
SET settings = EXCLUDED.settings,
    secret = EXCLUDED.secret,
    updated_at = NOW();

-- Activation is a deselect-all followed by select-target. Two statements,
-- not one: a reader racing between them sees "no selected provider",
-- which the hub's provisioning self-heals, so the switch can never strand
-- the hub. Selecting also re-enables: choosing a provider means running
-- it.
-- name: DeselectCaptchaConfigs :exec
UPDATE captcha_config
SET selected = FALSE, enabled = FALSE,
    updated_at = NOW();

-- name: SelectCaptchaConfig :exec
UPDATE captcha_config
SET selected = TRUE, enabled = TRUE,
    updated_at = NOW()
WHERE provider = $1;
-- name: SetCaptchaEnabled :exec
UPDATE captcha_config
SET enabled = $1, updated_at = NOW()
WHERE selected = TRUE;

-- name: DeleteCaptchaConfig :exec
DELETE FROM captcha_config;

-- name: DeleteCaptchaConfigProvider :exec
DELETE FROM captcha_config WHERE provider = $1;

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


-- ConsumeAltchaSalt records a solved challenge's salt as used. The
-- conflict no-op makes the call the single-use decision: 1 row = first
-- use accepted, 0 rows = replay denied.
-- name: ConsumeAltchaSalt :execrows
INSERT INTO altcha_used_salts (salt, expires_at) VALUES ($1, $2) ON CONFLICT (salt) DO NOTHING;

-- name: DeleteExpiredAltchaSalts :execrows
DELETE FROM altcha_used_salts WHERE expires_at < $1;
