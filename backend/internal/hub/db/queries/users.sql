-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, email, email_verified, password_set, is_admin)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ? AND deleted_at IS NULL;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ? AND email != '' AND deleted_at IS NULL;

-- name: ListUsersByOrgID :many
SELECT * FROM users WHERE org_id = ? AND deleted_at IS NULL ORDER BY created_at;

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (username LIKE '%' || sqlc.arg(query) || '%'
   OR display_name LIKE '%' || sqlc.arg(query) || '%'
   OR email LIKE '%' || sqlc.arg(query) || '%')
ORDER BY created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, password_set = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: UpdateUserProfile :exec
UPDATE users SET username = ?, display_name = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: UpdateUserEmail :exec
UPDATE users SET email = ?, email_verified = ?, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: UpdateUserEmailVerified :exec
UPDATE users SET email_verified = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: UpdateUserAdmin :exec
UPDATE users SET is_admin = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: DeleteUser :exec
UPDATE users SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: HardDeleteUsersBefore :execresult
DELETE FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?;

-- name: GetUserPrefs :one
SELECT prefs FROM users WHERE id = ?;

-- name: UpdateUserPrefs :exec
UPDATE users SET prefs = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: CountUsers :one
SELECT count(*) FROM users WHERE deleted_at IS NULL;

-- name: HasAnyUser :one
SELECT EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL LIMIT 1);

-- name: SetPendingEmail :exec
UPDATE users SET pending_email = ?, pending_email_token = ?, pending_email_expires_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: PromotePendingEmail :exec
UPDATE users SET email = pending_email, email_verified = 1, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND pending_email != '';

-- name: GetUserByPendingEmailToken :one
SELECT * FROM users WHERE pending_email_token = ? AND pending_email_token != '' AND deleted_at IS NULL;

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE pending_email = ? AND id != ?;
