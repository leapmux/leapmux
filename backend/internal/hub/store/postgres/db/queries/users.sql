-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, email, email_verified, password_set, is_admin)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByIDIncludeDeleted :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1 AND deleted_at IS NULL;

-- name: GetFirstAdmin :one
SELECT * FROM users WHERE is_admin = TRUE AND deleted_at IS NULL ORDER BY created_at LIMIT 1;

-- name: ExistsByUsername :one
SELECT EXISTS(SELECT 1 FROM users WHERE username = $1 AND deleted_at IS NULL);

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 AND email != '' AND deleted_at IS NULL;

-- name: GetUserIDByEmail :one
SELECT id FROM users WHERE email = $1 AND email != '' AND deleted_at IS NULL;

-- name: ExistsByEmail :one
SELECT EXISTS(
  SELECT 1
  FROM users
  WHERE email = sqlc.arg(email)
    AND email != ''
    AND deleted_at IS NULL
    AND id != sqlc.arg(exclude_user_id)
);

-- name: ListUsersByOrgID :many
SELECT * FROM users WHERE org_id = $1 AND deleted_at IS NULL ORDER BY created_at;

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC LIMIT sqlc.arg('limit');

-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (sqlc.narg(query)::text IS NULL
   OR username ILIKE sqlc.narg(query) || '%'
   OR display_name ILIKE sqlc.narg(query) || '%'
   OR email ILIKE sqlc.narg(query) || '%')
  AND (sqlc.narg(cursor)::timestamptz IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $1, password_set = TRUE, updated_at = NOW()
WHERE id = $2;

-- name: UpdateUserProfile :exec
UPDATE users SET username = $1, display_name = $2, updated_at = NOW()
WHERE id = $3;

-- name: UpdateUserEmail :exec
UPDATE users SET email = $1, email_verified = $2, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW()
WHERE id = $3;

-- name: UpdateUserEmailVerified :exec
UPDATE users SET email_verified = $1, updated_at = NOW()
WHERE id = $2;

-- name: UpdateUserAdmin :exec
UPDATE users SET is_admin = $1, updated_at = NOW()
WHERE id = $2;

-- name: DeleteUser :exec
UPDATE users SET deleted_at = NOW() WHERE id = $1;

-- name: HardDeleteUsersBefore :execresult
-- NOTE: Use CTE form (not LIMIT in subquery) for CockroachDB compatibility.
WITH to_delete AS (
    SELECT u.id FROM users u WHERE u.deleted_at IS NOT NULL AND u.deleted_at < $1 LIMIT 1000
)
DELETE FROM users WHERE id IN (SELECT id FROM to_delete);

-- name: GetUserPrefs :one
SELECT prefs FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: UpdateUserPrefs :exec
UPDATE users SET prefs = $1, updated_at = NOW()
WHERE id = $2;

-- name: CountUsers :one
SELECT count(*) FROM users WHERE deleted_at IS NULL;

-- name: HasAnyUser :one
SELECT EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL LIMIT 1);

-- name: SetPendingEmail :exec
UPDATE users SET pending_email = $1, pending_email_token = $2, pending_email_expires_at = $3, updated_at = NOW()
WHERE id = $4;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW()
WHERE id = $1;

-- name: PromotePendingEmail :exec
UPDATE users SET email = pending_email, email_verified = TRUE, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW()
WHERE id = $1 AND pending_email != '';

-- name: GetUserByPendingEmailToken :one
SELECT * FROM users WHERE pending_email_token = $1 AND pending_email_token != '' AND deleted_at IS NULL;

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW()
WHERE pending_email = $1 AND id != $2;
