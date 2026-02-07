-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, email, is_admin)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ?;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ? AND email != '';

-- name: ListUsersByOrgID :many
SELECT * FROM users WHERE org_id = ? ORDER BY created_at;

-- name: ListAllUsers :many
SELECT * FROM users ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: SearchUsers :many
SELECT * FROM users
WHERE username LIKE '%' || sqlc.arg(query) || '%'
   OR display_name LIKE '%' || sqlc.arg(query) || '%'
   OR email LIKE '%' || sqlc.arg(query) || '%'
ORDER BY created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: UpdateUserProfile :exec
UPDATE users SET username = ?, display_name = ?, email = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: UpdateUserEmailVerified :exec
UPDATE users SET email_verified = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: UpdateUserAdmin :exec
UPDATE users SET is_admin = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?;

-- name: CountUsers :one
SELECT count(*) FROM users;
