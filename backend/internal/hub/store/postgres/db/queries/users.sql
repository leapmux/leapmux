-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, email, email_verified, password_set, is_admin)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: LockUserAuthState :one
SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL
FOR UPDATE;

-- LockUserRow acquires the row lock on a user WITHOUT the deleted_at filter
-- LockUserAuthState applies, so a user_info mutation can serialize its
-- before/after cached-field projection against a concurrent mutation on the same
-- user (including a soft-deleted one). A no-op self-assign: it touches no cached
-- field and not updated_at, and a missing row is a tolerated no-op.
-- name: LockUserRow :exec
UPDATE users SET auth_generation = auth_generation WHERE id = $1;

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

-- name: ListUsersByIDs :many
SELECT * FROM users
WHERE id = ANY(sqlc.arg(user_ids)::TEXT[])
  AND deleted_at IS NULL;

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

-- The profile/email/email_verified/admin updates all RETURN id, updated_at so
-- the store layer can atomically emit a user_info cache-invalidation event: each
-- mutates a field cached in UserInfo (username, email, email_verified -- an auth
-- gate -- and is_admin), so a stale cached UserInfo must be dropped cross-process
-- the same way. No row match -> no event.

-- name: UpdateUserProfile :one
UPDATE users SET username = $1, display_name = $2, updated_at = NOW()
WHERE id = $3
RETURNING id, updated_at;

-- name: UpdateUserEmail :one
UPDATE users SET email = $1, email_verified = $2, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = NOW()
WHERE id = $3
RETURNING id, updated_at;

-- name: UpdateUserEmailVerified :one
UPDATE users SET email_verified = $1, updated_at = NOW()
WHERE id = $2
RETURNING id, updated_at;

-- name: UpdateUserAdmin :one
UPDATE users SET is_admin = $1, updated_at = NOW()
WHERE id = $2
RETURNING id, updated_at;

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
UPDATE users SET pending_email = $1, pending_email_token = $2, pending_email_expires_at = $3, pending_email_attempts = 0, updated_at = NOW()
WHERE id = $4;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = NOW()
WHERE id = $1;

-- name: PromotePendingEmail :one
UPDATE users SET email = pending_email, email_verified = TRUE, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = NOW()
WHERE id = $1 AND pending_email != ''
RETURNING id, updated_at;

-- ConsumeVerificationAttempt atomically charges one attempt against the
-- user's pending verification, force-expiring on the 6th try, and
-- returns the post-update row. Returns no rows when there's no pending
-- verification -- callers map that to FailedPrecondition.
-- name: ConsumeVerificationAttempt :one
UPDATE users
SET pending_email_attempts = pending_email_attempts + 1,
    pending_email_expires_at = CASE
        WHEN pending_email_attempts + 1 > 5 THEN NOW()
        ELSE pending_email_expires_at END,
    updated_at = NOW()
WHERE id = $1 AND pending_email_token != ''
RETURNING *;

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = NOW()
WHERE pending_email = $1 AND id != $2;

-- name: ClearStalePendingEmails :execresult
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = NOW()
WHERE pending_email_token != '' AND pending_email_expires_at IS NOT NULL AND pending_email_expires_at < $1;

-- name: BumpUserTokensRevokedAt :one
-- The query itself has no deleted_at guard, so it would act on a
-- soft-deleted row -- but the only caller (RevokeAllUserCredentials) runs
-- inside RunInUserAuthTransaction, whose LockUserAuthState filters
-- deleted_at IS NULL, so revoking an already-soft-deleted user aborts
-- before this runs. Every revoke path revokes before soft-deleting, so
-- that ordering is not exercised today. Only a missing id is a no-op.
UPDATE users
SET tokens_revoked_at = revocation_clock.now_at,
    auth_generation   = auth_generation + 1,
    updated_at = revocation_clock.now_at
FROM (SELECT clock_timestamp() AS now_at) AS revocation_clock
WHERE id = $1
RETURNING id, tokens_revoked_at, auth_generation;
