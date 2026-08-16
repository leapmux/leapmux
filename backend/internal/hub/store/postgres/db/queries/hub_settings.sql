-- The whole settings snapshot in one read: the manager resolves every
-- registered key against these rows and applies code defaults for the
-- keys without one.
-- name: GetAllSettings :many
SELECT * FROM hub_settings ORDER BY key;

-- UpsertSetting rewrites BOTH halves of one key's row; a NULL clears that
-- half. The caller (settings.Manager) merges with the existing row inside
-- a transaction, so an unchanging half is passed back rather than nulled,
-- and at least one half is always non-NULL to satisfy the CHECK.
-- name: UpsertSetting :exec
INSERT INTO hub_settings (key, value, secret)
VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE
SET value = excluded.value,
    secret = excluded.secret,
    updated_at = NOW();

-- Reset-to-default: the key's absence IS the default.
-- name: DeleteSetting :exec
DELETE FROM hub_settings WHERE key = $1;
-- name: GetSetting :one
SELECT * FROM hub_settings WHERE key = $1;

-- InsertSettingIfAbsent writes the row only when the key has no row: the
-- first-use provisioning primitive. Exactly one winner across processes
-- and hub instances sharing the database -- the insert that lands first
-- is the row that stays; a racing writer's value is discarded, never
-- applied over the winner's.
-- name: InsertSettingIfAbsent :execrows
INSERT INTO hub_settings (key, value, secret)
VALUES ($1, $2, $3)
ON CONFLICT (key) DO NOTHING;

-- The settings write path's read-modify-write merge reads under this row
-- lock, so two overlapping partial writes to one key serialize instead of
-- both merging onto the same stale base.
-- name: GetSettingForUpdate :one
SELECT * FROM hub_settings WHERE key = $1 FOR UPDATE;
