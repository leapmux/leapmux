-- The whole settings snapshot in one read: the manager resolves every
-- registered key against these rows and applies code defaults for the
-- keys without one. (`key` is a MySQL reserved word, hence the backticks.)
-- name: GetAllSettings :many
SELECT * FROM hub_settings ORDER BY `key`;

-- UpsertSetting rewrites BOTH halves of one key's row; a NULL clears that
-- half. The caller (settings.Manager) merges with the existing row inside
-- a transaction, so an unchanging half is passed back rather than nulled,
-- and at least one half is always non-NULL to satisfy the CHECK.
-- name: UpsertSetting :exec
INSERT INTO hub_settings (`key`, value, secret)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE
    value = VALUES(value),
    secret = VALUES(secret),
    updated_at = CURRENT_TIMESTAMP(3);

-- Reset-to-default: the key's absence IS the default.
-- name: DeleteSetting :exec
DELETE FROM hub_settings WHERE `key` = ?;
-- name: GetSetting :one
SELECT * FROM hub_settings WHERE `key` = ?;
