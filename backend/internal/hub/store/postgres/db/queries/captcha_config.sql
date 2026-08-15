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

-- Activation is one statement: every row becomes selected and enabled
-- exactly when it is the named provider, so no interleaving of concurrent
-- activations can ever leave two rows selected or none. Selecting also
-- re-enables: choosing a provider means running it.
-- name: ActivateCaptchaConfig :exec
UPDATE captcha_config
SET selected = (captcha_config.provider = $1),
    enabled = (captcha_config.provider = $1),
    updated_at = NOW();

-- The hub's first-use self-heal: activate the default provider only when
-- no row is selected, so a login resolving while an admin CLI switch
-- commits can never override the admin's selection.
-- name: ActivateCaptchaConfigIfNoneSelected :exec
UPDATE captcha_config
SET selected = (captcha_config.provider = $1),
    enabled = (captcha_config.provider = $1),
    updated_at = NOW()
WHERE NOT EXISTS (SELECT 1 FROM captcha_config WHERE selected = TRUE);
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
