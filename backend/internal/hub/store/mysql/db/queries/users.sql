-- name: CreateUser :exec
INSERT INTO users (id, username, password_hash, display_name, display_name_folded, email, email_verified, password_set, is_admin, prefs)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '{}');

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
  AND (sqlc.narg(cursor_time) IS NULL OR created_at < sqlc.narg(cursor_time) OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC LIMIT ?;

-- The query arg is pre-folded (store.FoldSearchText) by the Go glue, and username
-- and email are already stored lowercased, so a plain LIKE against the pre-folded
-- display_name_folded column matches case-insensitively -- identically to
-- SQLite/Postgres, which fold the same way in Go rather than in the DB's collation.
-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
-- The query arg arrives as a complete LIKE prefix pattern built by
-- store.SearchLikePattern (folded + backslash-escaped + trailing '%');
-- backslash is MySQL's default LIKE escape character, so the escaped
-- metacharacters match literally without an ESCAPE clause.
  AND (sqlc.narg(query) IS NULL
   OR username LIKE sqlc.narg(query)
   OR display_name_folded LIKE sqlc.narg(query)
   OR email LIKE sqlc.narg(query))
  AND (sqlc.narg(cursor_time) IS NULL OR created_at < sqlc.narg(cursor_time) OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
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
UPDATE users SET email = sqlc.arg(email), email_verified = sqlc.arg(email_verified), pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = sqlc.arg(updated_at)
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



-- name: HardDeleteUsersBefore :execresult
-- A user is hard-deletable only once nothing references it via a no-ON-DELETE
-- foreign key. workspaces.owner_user_id and workers.registered_by both REFERENCE
-- users(id) with no ON DELETE, so a user still referenced by a (possibly
-- soft-deleted, not-yet-hard-deleted) workspace or worker would abort this whole
-- DELETE on a foreign-key violation -- poisoning every FK-free user in the same
-- LIMIT 1000 chunk. Gating keeps the workspaces/workers -> users delete order
-- correct under bulk deletes; the user is reaped on a later pass once its
-- stragglers drain.
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

-- Locks the row for the user-preferences write path's read-modify-write
-- merge; without it two concurrent per-key updates both read the same
-- base and the second commit erases the first's key. Callers must hold
-- a transaction.
-- name: GetUserPrefsForUpdate :one
SELECT prefs FROM users WHERE id = ? AND deleted_at IS NULL FOR UPDATE;

-- name: UpdateUserPrefs :exec
UPDATE users SET prefs = ?, updated_at = NOW(3)
WHERE id = ?;

-- name: CountUsers :one
SELECT count(*) FROM users WHERE deleted_at IS NULL;

-- name: HasAnyUser :one
SELECT EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL LIMIT 1) AS has_any;

-- name: SetPendingEmail :execresult
-- Conditional mint: the write lands only when the row carries no live
-- blockade -- unblocked_at IS NULL, or it elapsed by now -- so two
-- concurrent resends for one account cannot both mint and both send (the
-- loser matches no row), and RequestEmailChange cannot mint on every
-- request. The mint arms the next blockade itself: unblocked_at = now +
-- the resend cooldown. A failed-send clear writes now + the failure
-- window; every other clear (address-level moves, promotions, rotation
-- teardowns) writes NULL, which leaves no blockade at all. Its twin
-- SetPendingRecovery carries the same predicate.
--
-- The gate reads pending_email_unblocked_at and never the expiry:
-- ConsumeVerificationAttempt force-expires a burned code by moving the
-- expiry to now, so an expiry-derived gate would read a five-second-old
-- burned code as minted a full lifetime ago and re-mint inside the
-- cooldown. The comparison stays on the app clock, the clock that wrote
-- the blockade.
UPDATE users SET pending_email = sqlc.arg(pending_email), pending_email_token = sqlc.arg(pending_email_token), pending_email_expires_at = sqlc.narg(pending_email_expires_at), pending_email_unblocked_at = sqlc.arg(pending_email_unblocked_at), pending_email_attempts = 0, updated_at = NOW(3)
WHERE id = sqlc.arg(id)
  AND pending_email_unblocked_at IS NULL
       OR pending_email_unblocked_at <= sqlc.arg(now);

-- name: ClearPendingEmailCode :exec
-- ClearPendingEmailCode drops an undelivered code and KEEPS the pending
-- address. An empty token is the "no live code, address still pending"
-- state: ConsumeVerificationAttempt and ClearStalePendingEmails both
-- filter pending_email_token != '', so neither acts on it. The clear
-- writes the caller's unblocked_at -- the failure window a refused send
-- leaves -- so the gate and the reported countdown agree that the retry a
-- failed send invites waits out that window instead of landing at request
-- speed; a NULL (the zero instant the caller binds) leaves no blockade at
-- all. Unconditional on purpose: it undoes a mint that the relay
-- refused.
UPDATE users SET pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = sqlc.narg(unblocked_at), pending_email_attempts = 0, updated_at = NOW(3)
WHERE id = ?;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW(3)
WHERE id = ?;

-- name: PromotePendingEmail :execresult
UPDATE users SET email = pending_email, email_verified = 1, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND pending_email != '';

-- ConsumeVerificationAttempt atomically charges one attempt against
-- the user's pending verification, force-expiring on the 6th try.
-- MySQL has no RETURNING -- the Go store layer follows up with a
-- GetUserByID under the row lock taken by this UPDATE.
-- The expiry CASE is assigned BEFORE the attempt increment: MySQL
-- evaluates SET clauses left-to-right and a later clause would read the
-- already-incremented counter, firing the force-expiry one charge early.
-- name: ConsumeVerificationAttempt :execresult
UPDATE users
SET pending_email_expires_at = CASE
        WHEN pending_email_attempts + 1 > sqlc.arg(max_attempts) THEN sqlc.arg(now)
        ELSE pending_email_expires_at END,
    pending_email_attempts = pending_email_attempts + 1,
    updated_at = NOW(3)
WHERE id = ? AND pending_email_token != '';

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW(3)
WHERE pending_email = ? AND id != ?;

-- name: ClearStalePendingEmails :execresult
-- The second arm reaps a codeless row: a send the relay refused leaves the
-- ADDRESS with no token and no expiry (ClearPendingEmailCode), so the
-- expiry compare can never see it and an abandoned address would otherwise
-- outlive the database. updated_at is the only instant such a row carries,
-- and ClearPendingEmailCode stamps it.
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW(3)
WHERE pending_email != ''
  AND ((pending_email_token != '' AND pending_email_expires_at IS NOT NULL AND pending_email_expires_at < sqlc.arg(cutoff))
    OR (pending_email_token = '' AND updated_at < sqlc.arg(codeless_cutoff)));

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

-- name: SetPendingRecovery :execresult
-- Conditional mint, recovery twin of SetPendingEmail: the write lands
-- only when the row carries no live blockade (unblocked_at IS NULL, or
-- it elapsed by now), so two concurrent requests for one account cannot
-- both mint and both send (the loser matches no row). The gate reads
-- pending_recovery_unblocked_at, not the expiry, for the reason
-- SetPendingEmail states: the attempt consumer force-expires a burned
-- link by moving the expiry to now.
UPDATE users
SET pending_recovery_token = sqlc.arg(pending_recovery_token),
    pending_recovery_expires_at = sqlc.arg(pending_recovery_expires_at),
    pending_recovery_unblocked_at = sqlc.arg(pending_recovery_unblocked_at),
    pending_recovery_attempts = 0,
    updated_at = NOW(3)
WHERE id = sqlc.arg(id)
  AND pending_recovery_unblocked_at IS NULL
       OR pending_recovery_unblocked_at <= sqlc.arg(now);


-- name: ClearPendingRecovery :exec
-- The failed-send clear, recovery twin: the unblocked_at the caller binds
-- leaves a short failure window on the next mint (see
-- ClearPendingEmailCode); a NULL (the zero instant) leaves none, which is
-- the rotation teardown's shape.
UPDATE users
SET pending_recovery_token = '',
    pending_recovery_expires_at = NULL,
    pending_recovery_unblocked_at = sqlc.narg(unblocked_at),
    pending_recovery_attempts = 0,
    updated_at = NOW(3)
WHERE id = ?;

-- name: ConsumeRecoveryAttemptByToken :execresult
-- Charges one attempt against the row that holds this exact token. Keying
-- the charge by the token (not the user id) makes the find, the charge, and
-- the ownership re-check one statement: a token cleared between a caller's
-- read and this update simply matches no row. MySQL has no RETURNING, so a
-- matched update is followed by one select of the same token.
-- The expiry CASE is assigned BEFORE the attempt increment: MySQL
-- evaluates SET clauses left-to-right and a later clause would read the
-- already-incremented counter, firing the force-expiry one charge early.
UPDATE users
SET pending_recovery_expires_at = CASE
        WHEN pending_recovery_attempts + 1 > sqlc.arg(max_attempts) THEN sqlc.arg(now)
        ELSE pending_recovery_expires_at END,
    pending_recovery_attempts = pending_recovery_attempts + 1,
    updated_at = NOW(3)
WHERE pending_recovery_token = ? AND pending_recovery_token != ''
  AND pending_recovery_expires_at > sqlc.arg(now)
  AND deleted_at IS NULL;

-- name: GetUserAfterRecoveryAttemptByToken :one
-- No liveness predicate here: the paired UPDATE already enforced it, and a
-- force-expired row (expires_at = NOW(3) in the same statement) must still
-- be readable in the same transaction.
SELECT * FROM users
WHERE pending_recovery_token = ? AND deleted_at IS NULL;

-- name: CompleteRecovery :execresult
UPDATE users
SET password_hash = sqlc.arg(password_hash),
    password_set = TRUE,
    pending_recovery_token = '',
    pending_recovery_expires_at = NULL,
    pending_recovery_unblocked_at = NULL,
    pending_recovery_attempts = 0,
    tokens_revoked_at = sqlc.arg(tokens_revoked_at),
    auth_generation = sqlc.arg(auth_generation),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND pending_recovery_token = sqlc.arg(pending_recovery_token);
