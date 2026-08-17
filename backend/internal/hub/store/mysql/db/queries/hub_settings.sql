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

-- InsertSettingIfAbsent writes the row only when the key has no row: the
-- first-use provisioning primitive. Exactly one winner across processes
-- and hub instances sharing the database -- the insert that lands first
-- is the row that stays; a racing writer's value is discarded, never
-- applied over the winner's. A plain INSERT (not ON DUPLICATE KEY): the
-- connection runs with clientFoundRows, under which the no-op-update form
-- reports a duplicate as 1, so the duplicate arrives as error 1062 and
-- the wrapper reads it as "not inserted". (`key` is a MySQL reserved
-- word, hence the backticks.)
-- name: InsertSettingIfAbsent :execrows
INSERT INTO hub_settings (`key`, value, secret)
VALUES (?, ?, ?);

-- The write path's cross-key validation reads every row under this table
-- lock, so a rule that spans keys cannot be checked against a sibling row
-- another writer is about to change. (`key` is a MySQL reserved word,
-- hence the backticks.)
-- name: GetAllSettingsForUpdate :many
SELECT * FROM hub_settings ORDER BY `key` FOR UPDATE;
