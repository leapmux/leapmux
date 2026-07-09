-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, email, email_verified, password_set, is_admin)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ? AND deleted_at IS NULL;

-- SQLite has no row-level SELECT FOR UPDATE. This no-op write acquires the
-- database writer lock before any credential table is touched.
-- name: LockUserAuthState :one
UPDATE users SET auth_generation = auth_generation
WHERE id = ? AND deleted_at IS NULL
RETURNING id;

-- LockUserRow acquires the writer lock on a user row WITHOUT the deleted_at
-- filter LockUserAuthState applies, so a user_info mutation can serialize its
-- before/after cached-field projection against a concurrent mutation on the same
-- user (including a soft-deleted one). A no-op self-assign: it touches no cached
-- field and not updated_at, and a missing row is a tolerated no-op.
-- name: LockUserRow :exec
UPDATE users SET auth_generation = auth_generation WHERE id = ?;

-- name: GetUserByIDIncludeDeleted :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ? AND deleted_at IS NULL;

-- name: GetFirstAdmin :one
SELECT * FROM users WHERE is_admin = 1 AND deleted_at IS NULL ORDER BY created_at LIMIT 1;

-- name: ExistsByUsername :one
SELECT EXISTS(SELECT 1 FROM users WHERE username = ? AND deleted_at IS NULL);

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
);

-- name: ListUsersByOrgID :many
SELECT * FROM users WHERE org_id = ? AND deleted_at IS NULL ORDER BY created_at;

-- name: ListUsersByIDs :many
SELECT * FROM users
WHERE id IN (sqlc.slice('user_ids'))
  AND deleted_at IS NULL;

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL
  AND (sqlc.narg(cursor) IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC LIMIT sqlc.arg(limit);

-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (sqlc.narg(query) IS NULL
   OR username LIKE sqlc.narg(query) || '%'
   OR display_name LIKE sqlc.narg(query) || '%'
   OR email LIKE sqlc.narg(query) || '%')
  AND (sqlc.narg(cursor) IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC
LIMIT sqlc.arg(limit);

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, password_set = 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- The profile/email/email_verified/admin updates all RETURN id, updated_at so
-- the store layer can atomically emit a user_info cache-invalidation event: each
-- mutates a field cached in UserInfo (username, email, email_verified -- an auth
-- gate -- and is_admin), so a stale cached UserInfo must be dropped cross-process
-- the same way. No row match -> no event.

-- name: UpdateUserProfile :one
UPDATE users SET username = ?, display_name = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?
RETURNING id, updated_at;

-- name: UpdateUserEmail :one
UPDATE users SET email = ?, email_verified = ?, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?
RETURNING id, updated_at;

-- name: UpdateUserEmailVerified :one
UPDATE users SET email_verified = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?
RETURNING id, updated_at;

-- name: UpdateUserAdmin :one
UPDATE users SET is_admin = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?
RETURNING id, updated_at;

-- name: DeleteUser :exec
UPDATE users SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: HardDeleteUsersBefore :execresult
DELETE FROM users WHERE rowid IN (SELECT u.rowid FROM users u WHERE u.deleted_at IS NOT NULL AND u.deleted_at < ? LIMIT 1000);

-- name: GetUserPrefs :one
SELECT prefs FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: UpdateUserPrefs :exec
UPDATE users SET prefs = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: CountUsers :one
SELECT count(*) FROM users WHERE deleted_at IS NULL;

-- name: HasAnyUser :one
SELECT EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL LIMIT 1);

-- name: SetPendingEmail :exec
UPDATE users SET pending_email = ?, pending_email_token = ?, pending_email_expires_at = ?, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: PromotePendingEmail :one
UPDATE users SET email = pending_email, email_verified = 1, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND pending_email != ''
RETURNING id, updated_at;

-- name: ConsumeVerificationAttempt :one
UPDATE users
SET pending_email_attempts = pending_email_attempts + 1,
    pending_email_expires_at = CASE
        WHEN pending_email_attempts + 1 > 5 THEN strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
        ELSE pending_email_expires_at END,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND pending_email_token != ''
RETURNING *;

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE pending_email = ? AND id != ?;

-- name: ClearStalePendingEmails :execresult
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE pending_email_token != '' AND pending_email_expires_at IS NOT NULL AND pending_email_expires_at < ?;

-- name: BumpUserTokensRevokedAt :one
-- Bumps the user-wide revocation timestamp and credential epoch, then
-- returns the row that moved so the store layer can emit a durable
-- revocation event carrying the new epoch. The query itself has no
-- deleted_at guard, so it would act on a soft-deleted row -- but the only
-- caller (RevokeAllUserCredentials) runs inside RunInUserAuthTransaction,
-- whose LockUserAuthState filters deleted_at IS NULL, so revoking an
-- already-soft-deleted user aborts before this runs. Every revoke path
-- revokes before soft-deleting, so that ordering is not exercised today.
-- Only a truly missing row (no id match) is a no-op.
UPDATE users
SET tokens_revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    auth_generation   = auth_generation + 1,
    updated_at        = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?
RETURNING id, tokens_revoked_at, auth_generation;
