-- name: CreateUser :exec
INSERT INTO users (id, username, password_hash, display_name, display_name_folded, email, email_verified, first_credential_exempt, is_admin)
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

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR created_at < sqlc.narg(cursor_time)::timestamptz
       OR (created_at = sqlc.narg(cursor_time)::timestamptz AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC LIMIT sqlc.arg('limit');

-- The query arg is pre-folded (store.FoldSearchText) by the Go glue, and username
-- and email are already stored lowercased, so a plain LIKE (not ILIKE) against the
-- pre-folded display_name_folded column matches case-insensitively -- identically to
-- SQLite/MySQL, which fold the same way in Go rather than in the DB's collation.
-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
-- The query arg arrives as a complete LIKE prefix pattern built by
-- store.SearchLikePattern (folded + backslash-escaped + trailing '%');
-- backslash is Postgres's default LIKE escape character, so the escaped
-- metacharacters match literally without an ESCAPE clause.
  AND (sqlc.narg(query)::text IS NULL
   OR username LIKE sqlc.narg(query)
   OR display_name_folded LIKE sqlc.narg(query)
   OR email LIKE sqlc.narg(query))
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR created_at < sqlc.narg(cursor_time)::timestamptz
       OR (created_at = sqlc.narg(cursor_time)::timestamptz AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit');

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $1, first_credential_exempt = TRUE, updated_at = NOW()
WHERE id = $2;

-- The profile/email/email_verified/admin updates all RETURN id, updated_at so
-- the store layer can atomically emit a user_info cache-invalidation event: each
-- mutates a field cached in UserInfo (username, email, email_verified -- an auth
-- gate -- and is_admin), so a stale cached UserInfo must be dropped cross-process
-- the same way. No row match -> no event.

-- name: UpdateUserProfile :one
UPDATE users SET username = $1, display_name = $2, display_name_folded = $3, updated_at = NOW()
WHERE id = $4
RETURNING id, updated_at;

-- name: UpdateUserEmail :one
UPDATE users SET email = $1, email_verified = $2, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW()
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
-- A user is hard-deletable only once nothing references it via a no-ON-DELETE
-- foreign key. workspaces.owner_user_id and workers.registered_by both REFERENCE
-- users(id) with no ON DELETE, so a user still referenced by a (possibly
-- soft-deleted, not-yet-hard-deleted) workspace or worker would abort this whole
-- DELETE on a foreign-key violation -- poisoning every FK-free user in the same
-- LIMIT 1000 chunk. Gating keeps the workspaces/workers -> users delete order
-- correct under bulk deletes; the user is reaped on a later pass once its
-- stragglers drain.
WITH to_delete AS (
    SELECT u.id FROM users u
    WHERE u.deleted_at IS NOT NULL AND u.deleted_at < $1
      AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.owner_user_id = u.id)
      AND NOT EXISTS (SELECT 1 FROM workers wk WHERE wk.registered_by = u.id)
    LIMIT 1000
)
DELETE FROM users WHERE id IN (SELECT id FROM to_delete);

-- name: GetUserPrefs :one
SELECT prefs FROM users WHERE id = $1 AND deleted_at IS NULL;

-- Locks the row for the user-preferences write path's read-modify-write
-- merge; without it two concurrent per-key updates both read the same
-- base and the second commit erases the first's key. Callers must hold
-- a transaction.
-- name: GetUserPrefsForUpdate :one
SELECT prefs FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: UpdateUserPrefs :exec
UPDATE users SET prefs = $1, updated_at = NOW()
WHERE id = $2;

-- name: CountUsers :one
SELECT count(*) FROM users WHERE deleted_at IS NULL;

-- name: HasAnyUser :one
SELECT EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL LIMIT 1);

-- name: SetPendingEmail :execresult
-- Conditional mint: the write lands only when the row carries no live
-- blockade -- unblocked_at IS NULL, or it elapsed by now -- so two
-- concurrent resends for one account cannot both mint and both send (the
-- loser matches no row), and RequestEmailChange cannot mint on every
-- request. The mint arms the next blockade itself: unblocked_at = now +
-- the resend cooldown. A failed-send clear writes now + the failure
-- window; every other clear (address-level moves, promotions, rotation
-- teardowns) writes NULL, which leaves no blockade at all. Its twin
-- SetPendingRecovery carries the same predicate. The OR is parenthesized
-- because AND binds tighter: without that, a mint for one account whose
-- cooldown elapsed would update every other elapsed row.
--
-- The gate reads pending_email_unblocked_at and never the expiry:
-- ConsumeVerificationAttempt force-expires a burned code by moving the
-- expiry to now, so an expiry-derived gate would read a five-second-old
-- burned code as minted a full lifetime ago and re-mint inside the
-- cooldown. The comparison stays on the app clock, the clock that wrote
-- the blockade.
UPDATE users SET pending_email = sqlc.arg(pending_email), pending_email_token = sqlc.arg(pending_email_token), pending_email_expires_at = sqlc.narg(pending_email_expires_at), pending_email_unblocked_at = sqlc.arg(pending_email_unblocked_at), pending_email_attempts = 0, updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (pending_email_unblocked_at IS NULL
       OR pending_email_unblocked_at <= sqlc.arg(now));

-- name: ClearPendingEmailCode :exec
-- ClearPendingEmailCode drops an undelivered code and KEEPS the pending
-- address. An empty token is the "no live code, address still pending"
-- state: ConsumeVerificationAttempt and ClearStalePendingEmails both
-- filter pending_email_token != '', so neither acts on it. The clear
-- writes the caller's unblocked_at -- the failure window a refused send
-- leaves -- so the gate above and the reported countdown agree that the
-- retry a failed send invites waits out that window instead of landing at
-- request speed; a NULL (the zero instant the caller binds) leaves no
-- blockade at all. Unconditional on purpose: it undoes a mint that the
-- relay refused.
UPDATE users SET pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = sqlc.narg(unblocked_at), pending_email_attempts = 0, updated_at = NOW()
WHERE id = $1;

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW()
WHERE id = $1;

-- name: PromotePendingEmail :one
UPDATE users SET email = pending_email, email_verified = TRUE, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW()
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
        WHEN pending_email_attempts + 1 > sqlc.arg(max_attempts) THEN sqlc.arg(now)
        ELSE pending_email_expires_at END,
    updated_at = NOW()
WHERE id = $1 AND pending_email_token != ''
RETURNING *;

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW()
WHERE pending_email = $1 AND id != $2;

-- name: ClearStalePendingEmails :execresult
-- The second arm reaps a codeless row: a send the relay refused leaves the
-- ADDRESS with no token and no expiry (ClearPendingEmailCode), so the
-- expiry compare can never see it and an abandoned address would otherwise
-- outlive the database. updated_at is the only instant such a row carries,
-- and ClearPendingEmailCode stamps it.
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = NOW()
WHERE pending_email != ''
  AND ((pending_email_token != '' AND pending_email_expires_at IS NOT NULL AND pending_email_expires_at < sqlc.arg(cutoff))
    OR (pending_email_token = '' AND updated_at < sqlc.arg(codeless_cutoff)));

-- name: BumpUserTokensRevokedAt :one
-- The query itself has no deleted_at guard, so it would act on a
-- soft-deleted row -- but neither caller can reach one: RevokeUserTokens
-- (via RevokeAllUserCredentials) runs inside RunInUserAuthTransaction, whose
-- LockUserAuthState filters deleted_at IS NULL; fenceUserTokensLocked (the
-- auth-gate reduction fence) runs only in RunUserInfoMutation's
-- existedBefore && existedAfter branch, and both existence reads use
-- GetUserByID, which also filters deleted_at IS NULL. Every revoke path
-- revokes before soft-deleting, so that ordering is not exercised today.
-- Only a missing id is a no-op.
UPDATE users
SET tokens_revoked_at = revocation_clock.now_at,
    auth_generation   = auth_generation + 1,
    updated_at = revocation_clock.now_at
FROM (SELECT clock_timestamp() AS now_at) AS revocation_clock
WHERE id = $1
RETURNING id, tokens_revoked_at, auth_generation;

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
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND (pending_recovery_unblocked_at IS NULL
       OR pending_recovery_unblocked_at <= sqlc.arg(now));


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
    updated_at = NOW()
WHERE id = $1;

-- name: ConsumeRecoveryAttemptByToken :one
-- Charges one attempt against the row that holds this exact token. Keying
-- the charge by the token (not the user id) makes the find, the charge, and
-- the ownership re-check one statement: a token cleared between a caller's
-- read and this update simply matches no row.
UPDATE users
SET pending_recovery_expires_at = CASE
        WHEN pending_recovery_attempts + 1 > sqlc.arg(max_attempts) THEN sqlc.arg(now)
        ELSE pending_recovery_expires_at END,
    pending_recovery_attempts = pending_recovery_attempts + 1,
    updated_at = NOW()
WHERE pending_recovery_token = sqlc.arg(token) AND pending_recovery_token != ''
  AND pending_recovery_expires_at > sqlc.arg(now)
  AND deleted_at IS NULL
RETURNING *;

-- name: GetUserByLiveRecoveryToken :one
-- Finish reads this before it consumes the WebAuthn ceremony, so a
-- force-expired or reminted token cannot spend a session minted under a
-- previous token. The liveness predicate matches ConsumeRecoveryAttemptByToken.
SELECT * FROM users
WHERE pending_recovery_token = sqlc.arg(token) AND pending_recovery_token != ''
  AND pending_recovery_expires_at > sqlc.arg(now)
  AND deleted_at IS NULL;

-- name: CompleteRecovery :one
-- Spends the recovery token on a replacement factor. first_credential_exempt is true
-- for a new password and false for a passkey spend (caller binds the
-- not-set placeholder hash). The WHERE re-checks the token at write time,
-- so a link spent on a concurrent completion matches no row.
UPDATE users
SET password_hash = sqlc.arg(password_hash),
    first_credential_exempt = sqlc.arg(first_credential_exempt),
    pending_recovery_token = '',
    pending_recovery_expires_at = NULL,
    pending_recovery_unblocked_at = NULL,
    pending_recovery_attempts = 0,
    tokens_revoked_at = clock_timestamp(),
    auth_generation = auth_generation + 1,
    updated_at = NOW()
WHERE id = sqlc.arg(id) AND pending_recovery_token = sqlc.arg(pending_recovery_token)
RETURNING id, tokens_revoked_at, auth_generation;

