-- name: GetSelectedCaptchaConfig :one
SELECT * FROM captcha_config WHERE selected = TRUE;

-- name: GetCaptchaConfig :one
SELECT * FROM captcha_config WHERE provider = ?;

-- name: ListCaptchaProviders :many
SELECT * FROM captcha_config ORDER BY provider;

-- Provisioning inserts the row unselected; the caller activates it in a
-- separate statement, so a provision race can never leave two rows
-- selected. The conflict no-op makes racing first-use provisions a
-- one-winner race - the loser's fresh secret is simply discarded.
-- name: InsertCaptchaConfigIfAbsent :exec
INSERT INTO captcha_config (provider, selected, enabled, secret, settings)
VALUES (?, FALSE, TRUE, ?, ?)
ON DUPLICATE KEY UPDATE provider = provider;

-- UpdateCaptchaSettings rewrites one existing row's settings and never
-- touches its secret, so a settings change can never lose the key. It
-- matches zero rows when the provider has no row; the callers provision
-- first (or upsert with a secret below).
-- name: UpdateCaptchaSettings :exec
UPDATE captcha_config
SET settings = ?, updated_at = CURRENT_TIMESTAMP(3)
WHERE provider = ?;

-- The admin CLI's write path when a secret accompanies the settings
-- (first configuration of an external provider, or key rotation): both
-- are always written. The secret is deliberately required here - a
-- secret-less row would fail verification on every submission.
-- name: UpsertCaptchaConfig :exec
INSERT INTO captcha_config (provider, selected, enabled, secret, settings)
VALUES (?, FALSE, TRUE, ?, ?)
ON DUPLICATE KEY UPDATE
    settings = VALUES(settings),
    secret = VALUES(secret),
    updated_at = CURRENT_TIMESTAMP(3);

-- Activation is one statement: every row becomes selected and enabled
-- exactly when it is the named provider, so no interleaving of concurrent
-- activations can ever leave two rows selected or none. Selecting also
-- re-enables: choosing a provider means running it.
-- name: ActivateCaptchaConfig :exec
UPDATE captcha_config
SET selected = (captcha_config.provider = ?),
    enabled = (captcha_config.provider = ?),
    updated_at = CURRENT_TIMESTAMP(3);

-- The hub's first-use self-heal: activate the default provider only when
-- no row is selected, so a login resolving while an admin CLI switch
-- commits can never override the admin's selection. The extra derived
-- table works around MySQL 1093 (no target table in a FROM subquery) -
-- the wrapper materializes, so the read and the update stay one atomic
-- statement.
-- name: ActivateCaptchaConfigIfNoneSelected :exec
UPDATE captcha_config
SET selected = (captcha_config.provider = ?),
    enabled = (captcha_config.provider = ?),
    updated_at = CURRENT_TIMESTAMP(3)
WHERE NOT EXISTS (SELECT 1 FROM (SELECT 1 FROM captcha_config WHERE selected = TRUE) AS any_selected);

-- name: SetCaptchaEnabled :exec
UPDATE captcha_config
SET enabled = ?, updated_at = CURRENT_TIMESTAMP(3)
WHERE selected = TRUE;

-- name: DeleteCaptchaConfig :exec
DELETE FROM captcha_config;

-- name: DeleteCaptchaConfigProvider :exec
DELETE FROM captcha_config WHERE provider = ?;

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


-- ConsumeAltchaSalt records a solved challenge's salt as used: 1 row =
-- first use accepted; the duplicate-key error the wrapper maps to 0
-- rows = replay denied. An ON DUPLICATE KEY UPDATE no-op cannot carry
-- that decision here: this connection runs with clientFoundRows (rows
-- matched, not changed), under which the duplicate reports as 1.
-- name: ConsumeAltchaSalt :execrows
INSERT INTO altcha_used_salts (salt, expires_at) VALUES (?, ?);

-- name: DeleteExpiredAltchaSalts :execrows
DELETE FROM altcha_used_salts WHERE expires_at < ?;
