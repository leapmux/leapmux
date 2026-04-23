-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, email, email_verified, password_set, is_admin, prefs)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '{}');

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: GetUserByIDIncludeDeleted :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ? AND deleted_at IS NULL;

-- name: GetFirstAdmin :one
SELECT * FROM users WHERE is_admin = TRUE AND deleted_at IS NULL ORDER BY created_at LIMIT 1;

-- name: ExistsByUsername :one
SELECT EXISTS(SELECT 1 FROM users WHERE username = ? AND deleted_at IS NULL) AS exists_flag;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ? AND email != '' AND deleted_at IS NULL;

-- name: GetUserIDByEmail :one
SELECT id FROM users WHERE email = ? AND email != '' AND deleted_at IS NULL;

-- name: ExistsByEmail :one
SELECT EXISTS(
  SELECT 1
  FROM users
  WHERE email = sqlc.arg(email)
    AND email != ''
    AND deleted_at IS NULL
    AND id != sqlc.arg(exclude_user_id)
) AS exists_flag;

-- name: ListUsersByOrgID :many
SELECT * FROM users WHERE org_id = ? AND deleted_at IS NULL ORDER BY created_at;

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL
  AND (? IS NULL OR created_at < ?)
ORDER BY created_at DESC LIMIT ?;

-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (sqlc.narg(query) IS NULL
   OR LOWER(username) LIKE CONCAT(LOWER(sqlc.narg(query)), '%')
   OR LOWER(display_name) LIKE CONCAT(LOWER(sqlc.narg(query)), '%')
   OR LOWER(email) LIKE CONCAT(LOWER(sqlc.narg(query)), '%'))
  AND (? IS NULL OR created_at < ?)
ORDER BY created_at DESC
LIMIT ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, password_set = 1, updated_at = NOW(3)
WHERE id = ?;

-- name: UpdateUserProfile :exec
UPDATE users SET username = ?, display_name = ?, updated_at = NOW(3)
WHERE id = ?;

-- name: UpdateUserEmail :exec
UPDATE users SET email = ?, email_verified = ?, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW(3)
WHERE id = ?;

-- name: UpdateUserEmailVerified :exec
UPDATE users SET email_verified = ?, updated_at = NOW(3)
WHERE id = ?;

-- name: UpdateUserAdmin :exec
UPDATE users SET is_admin = ?, updated_at = NOW(3)
WHERE id = ?;

-- name: DeleteUser :exec
UPDATE users SET deleted_at = NOW(3) WHERE id = ?;

-- name: HardDeleteUsersBefore :execresult
DELETE FROM users WHERE id IN (SELECT u.id FROM (SELECT users.id FROM users WHERE users.deleted_at IS NOT NULL AND users.deleted_at < ? LIMIT 1000) u);

-- name: GetUserPrefs :one
SELECT prefs FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: UpdateUserPrefs :exec
UPDATE users SET prefs = ?, updated_at = NOW(3)
WHERE id = ?;

-- name: CountUsers :one
SELECT count(*) FROM users WHERE deleted_at IS NULL;

-- name: HasAnyUser :one
SELECT EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL LIMIT 1) AS has_any;

-- name: SetPendingEmail :exec
UPDATE users SET pending_email = ?, pending_email_token = ?, pending_email_expires_at = ?, updated_at = NOW(3)
WHERE id = ?;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW(3)
WHERE id = ?;

-- name: PromotePendingEmail :exec
UPDATE users SET email = pending_email, email_verified = 1, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW(3)
WHERE id = ? AND pending_email != '';

-- name: GetUserByPendingEmailToken :one
SELECT * FROM users WHERE pending_email_token = ? AND pending_email_token != '' AND deleted_at IS NULL;

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW(3)
WHERE pending_email = ? AND id != ?;
