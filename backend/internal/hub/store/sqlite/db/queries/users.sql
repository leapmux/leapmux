-- name: CreateUser :exec
INSERT INTO users (id, org_id, username, password_hash, display_name, display_name_folded, email, email_verified, password_set, is_admin)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

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

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL
  AND (sqlc.narg(cursor) IS NULL OR created_at < sqlc.narg(cursor))
ORDER BY created_at DESC LIMIT sqlc.arg(limit);

-- The query arg is pre-folded (store.FoldSearchText) by the Go glue, and username
-- and email are already stored lowercased, so a plain LIKE against the pre-folded
-- display_name_folded column matches case-insensitively for non-ASCII names -- and
-- identically to Postgres/MySQL, which fold the same way. (A bare LIKE on the raw
-- display_name would fold only ASCII on SQLite; see FoldSearchText.)
-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (sqlc.narg(query) IS NULL
   OR username LIKE sqlc.narg(query) || '%'
   OR display_name_folded LIKE sqlc.narg(query) || '%'
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
UPDATE users SET username = ?, display_name = ?, display_name_folded = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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

-- name: SoftDeleteUserPersonalOrg :exec
-- Soft-delete the personal org whose id is the given user's org_id. Paired with
-- DeleteUser inside userStore.Delete so a user soft-delete can never leave the org
-- name occupying the partial unique index idx_orgs_name -- which would fail a later
-- re-signup of the freed username. The subquery has no deleted_at guard, so it
-- resolves the org_id whether or not the user row is already soft-deleted.
UPDATE orgs SET deleted_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE orgs.id = (SELECT users.org_id FROM users WHERE users.id = ?);

-- name: RenameUserPersonalOrg :exec
-- Rename the personal org whose id is the given user's org_id to mirror a
-- username change. Paired with UpdateUserProfile inside userStore.UpdateProfile
-- so a username change can never leave the org name (and thus the /o/ slug)
-- stale: the org name mirrors the username under idx_orgs_name, and this makes
-- the pairing a property of the store rather than a step each caller must
-- repeat -- mirroring SoftDeleteUserPersonalOrg's pairing with DeleteUser. The
-- subquery has no deleted_at guard, matching SoftDeleteUserPersonalOrg.
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
-- LIMIT 1000 chunk. Deleting a user soft-deletes its workspaces and workers too,
-- but the chunked HardDeleteWorkspacesBefore/HardDeleteWorkersBefore (LIMIT 1000,
-- run earlier in the same sweep) can leave stragglers whose owner then lands here.
-- Gating keeps the workspaces/workers -> users delete order correct under bulk
-- deletes; the user is reaped on a later pass once its stragglers drain. Mirrors
-- the NOT EXISTS users gate on HardDeleteOrgsBefore. idx_workspaces_owner_user_id
-- and idx_workers_registered_by make each NOT EXISTS an indexed point probe.
DELETE FROM users WHERE rowid IN (
    SELECT u.rowid FROM users u
    WHERE u.deleted_at IS NOT NULL AND u.deleted_at < ?
      AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.owner_user_id = u.id)
      AND NOT EXISTS (SELECT 1 FROM workers wk WHERE wk.registered_by = u.id)
    LIMIT 1000
);

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
