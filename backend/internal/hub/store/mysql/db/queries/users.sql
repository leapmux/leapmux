-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, display_name_folded, email, email_verified, password_set, is_admin, prefs)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}');

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: LockUserAuthState :one
SELECT id FROM users WHERE id = ? AND deleted_at IS NULL
FOR UPDATE;

-- LockUserRow acquires the row lock on a user WITHOUT the deleted_at filter
-- LockUserAuthState applies, so a user_info mutation can serialize its
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

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL
  AND (? IS NULL OR created_at < ?)
ORDER BY created_at DESC LIMIT ?;

-- The query arg is pre-folded (store.FoldSearchText) by the Go glue, and username
-- and email are already stored lowercased, so a plain LIKE against the pre-folded
-- display_name_folded column matches case-insensitively -- identically to
-- SQLite/Postgres, which fold the same way in Go rather than in the DB's collation.
-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (sqlc.narg(query) IS NULL
   OR username LIKE CONCAT(sqlc.narg(query), '%')
   OR display_name_folded LIKE CONCAT(sqlc.narg(query), '%')
   OR email LIKE CONCAT(sqlc.narg(query), '%'))
  AND (? IS NULL OR created_at < ?)
ORDER BY created_at DESC
LIMIT ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, password_set = 1, updated_at = NOW(3)
WHERE id = ?;

-- The profile/email/email_verified/admin updates take an explicit updated_at
-- (read once via GetUserForUpdate below) so the store layer can atomically emit
-- a user_info cache-invalidation event under the same clock reading: each mutates
-- a field cached in UserInfo (username, email, email_verified -- an auth gate --
-- and is_admin), so a stale cached UserInfo must be dropped cross-process the
-- same way. No locked row -> no event.

-- name: UpdateUserProfile :execresult
UPDATE users SET username = sqlc.arg(username), display_name = sqlc.arg(display_name), display_name_folded = sqlc.arg(display_name_folded), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdateUserEmail :execresult
UPDATE users SET email = sqlc.arg(email), email_verified = sqlc.arg(email_verified), pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdateUserEmailVerified :execresult
UPDATE users SET email_verified = sqlc.arg(email_verified), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: GetUserForUpdate :one
-- Locks the user row (matched by id only, like the RETURNING form used by
-- SQLite/PostgreSQL) so the profile/email/email_verified/admin updates can
-- atomically emit a user_info cache-invalidation event under the same clock
-- reading. MySQL has no RETURNING, so the store layer follows this locked read
-- with the UPDATE.
SELECT id, NOW(3) AS now_at FROM users
WHERE id = ?
FOR UPDATE;

-- name: UpdateUserAdmin :execresult
UPDATE users SET is_admin = sqlc.arg(is_admin), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: DeleteUser :exec
UPDATE users SET deleted_at = NOW(3) WHERE id = ?;

-- name: SoftDeleteUserPersonalOrg :exec
-- Soft-delete the personal org whose id is the given user's org_id. Paired with
-- DeleteUser inside userStore.Delete so a user soft-delete can never leave the org
-- name occupying the partial unique index idx_orgs_name -- which would fail a later
-- re-signup of the freed username. The subquery reads users (a different table from
-- the updated orgs, so MySQL permits it) with no deleted_at guard, so it resolves
-- the org_id whether or not the user row is already soft-deleted.
UPDATE orgs SET deleted_at = NOW(3)
WHERE orgs.id = (SELECT users.org_id FROM users WHERE users.id = ?);

-- name: RenameUserPersonalOrg :exec
-- Rename the personal org whose id is the given user's org_id to mirror a
-- username change. Paired with UpdateUserProfile inside userStore.UpdateProfile
-- so a username change can never leave the org name (and thus the /o/ slug)
-- stale: the org name mirrors the username under idx_orgs_name, and this makes
-- the pairing a property of the store rather than a step each caller must
-- repeat -- mirroring SoftDeleteUserPersonalOrg's pairing with DeleteUser. The
-- subquery reads users (a different table from the updated orgs, so MySQL
-- permits it) with no deleted_at guard, matching SoftDeleteUserPersonalOrg.
-- Idempotent for a display-name-only edit: the org name already equals the
-- (unchanged, normalized) username, so this sets it to the same value.
UPDATE orgs SET name = sqlc.arg(org_name)
WHERE orgs.id = (SELECT users.org_id FROM users WHERE users.id = sqlc.arg(user_id));

-- name: HardDeleteUsersBefore :execresult
-- A user is hard-deletable only once nothing references it via a no-ON-DELETE
-- foreign key. workspaces.owner_user_id and workers.registered_by both REFERENCE
-- users(id) with no ON DELETE, so a user still referenced by a (possibly
-- soft-deleted, not-yet-hard-deleted) workspace or worker would abort this whole
-- DELETE on a foreign-key violation -- poisoning every FK-free user in the same
-- LIMIT 1000 chunk. Gating keeps the workspaces/workers -> users delete order
-- correct under bulk deletes; the user is reaped on a later pass once its
-- stragglers drain. Mirrors the NOT EXISTS users gate on HardDeleteOrgsBefore.
DELETE FROM users WHERE id IN (
    SELECT u.id FROM (
        SELECT users.id FROM users
        WHERE users.deleted_at IS NOT NULL AND users.deleted_at < ?
          AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.owner_user_id = users.id)
          AND NOT EXISTS (SELECT 1 FROM workers wk WHERE wk.registered_by = users.id)
        LIMIT 1000
    ) u
);

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
UPDATE users SET pending_email = ?, pending_email_token = ?, pending_email_expires_at = ?, pending_email_attempts = 0, updated_at = NOW(3)
WHERE id = ?;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = NOW(3)
WHERE id = ?;

-- name: PromotePendingEmail :execresult
UPDATE users SET email = pending_email, email_verified = 1, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND pending_email != '';

-- ConsumeVerificationAttempt atomically charges one attempt against
-- the user's pending verification, force-expiring on the 6th try.
-- MySQL has no RETURNING -- the Go store layer follows up with a
-- GetUserByID under the row lock taken by this UPDATE.
-- name: ConsumeVerificationAttempt :execresult
UPDATE users
SET pending_email_attempts = pending_email_attempts + 1,
    pending_email_expires_at = CASE
        WHEN pending_email_attempts + 1 > 5 THEN NOW(3)
        ELSE pending_email_expires_at END,
    updated_at = NOW(3)
WHERE id = ? AND pending_email_token != '';

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = NOW(3)
WHERE pending_email = ? AND id != ?;

-- name: ClearStalePendingEmails :execresult
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_attempts = 0, updated_at = NOW(3)
WHERE pending_email_token != '' AND pending_email_expires_at IS NOT NULL AND pending_email_expires_at < ?;

-- The token-revocation lock/update pair has no deleted_at guard, so it
-- would act on a soft-deleted row -- but the only caller
-- (RevokeAllUserCredentials) runs inside RunInUserAuthTransaction, whose
-- LockUserAuthState filters deleted_at IS NULL, so revoking an
-- already-soft-deleted user aborts before this lock runs. Every revoke
-- path revokes before soft-deleting, so that ordering is not exercised
-- today. Only a missing id is a no-op (ErrNoRows on the lock).
-- name: GetUserTokensRevocationForUpdate :one
SELECT id, tokens_revoked_at, auth_generation, NOW(3) AS now_at FROM users
WHERE id = ?
FOR UPDATE;

-- name: SetUserTokensRevokedAt :execresult
UPDATE users
SET tokens_revoked_at = sqlc.arg(tokens_revoked_at),
    auth_generation = sqlc.arg(auth_generation),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);
