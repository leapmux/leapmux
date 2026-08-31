-- name: CreateUser :exec
INSERT INTO users (id, username, password_hash, display_name, display_name_folded, email, email_verified, password_set, is_admin)
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

-- name: ListAllUsers :many
SELECT * FROM users WHERE deleted_at IS NULL
  AND (sqlc.narg(cursor_time) IS NULL
       OR created_at < sqlc.narg(cursor_time)
       OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC LIMIT sqlc.arg(limit);

-- The query arg arrives as a complete LIKE prefix pattern built by
-- store.SearchLikePattern: pre-folded (so the match against the pre-folded
-- display_name_folded column and the lowercased username/email columns is
-- case-insensitive for non-ASCII names, identically to Postgres/MySQL),
-- backslash-escaped (so an operator's literal '%'/'_' cannot act as a
-- wildcard), with the trailing match-anything '%' already appended. (A bare
-- LIKE on the raw display_name would fold only ASCII on SQLite; see
-- FoldSearchText.) like(pattern, col, '\') is the LIKE operator's function
-- form with an explicit escape character: a bare `col LIKE ?` has NO escape
-- char on SQLite (unlike Postgres/MySQL, whose default is backslash), and
-- sqlc's SQLite grammar cannot parse the `LIKE ? ESCAPE '\'` clause form.
-- name: SearchUsers :many
SELECT * FROM users
WHERE deleted_at IS NULL
  AND (sqlc.narg(query) IS NULL
   OR like(sqlc.narg(query), username, '\')
   OR like(sqlc.narg(query), display_name_folded, '\')
   OR like(sqlc.narg(query), email, '\'))
  AND (sqlc.narg(cursor_time) IS NULL
       OR created_at < sqlc.narg(cursor_time)
       OR (created_at = sqlc.narg(cursor_time) AND id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, id DESC
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
UPDATE users SET email = ?, email_verified = ?, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
-- A user is hard-deletable only once nothing references it via a no-ON-DELETE
-- foreign key. workspaces.owner_user_id and workers.registered_by both REFERENCE
-- users(id) with no ON DELETE, so a user still referenced by a (possibly
-- soft-deleted, not-yet-hard-deleted) workspace or worker would abort this whole
-- DELETE on a foreign-key violation -- poisoning every FK-free user in the same
-- LIMIT 1000 chunk. Deleting a user soft-deletes its workspaces and workers too,
-- but the chunked HardDeleteWorkspacesBefore/HardDeleteWorkersBefore (LIMIT 1000,
-- run earlier in the same sweep) can leave stragglers whose owner then lands here.
-- Gating keeps the workspaces/workers -> users delete order correct under bulk
-- deletes; the user is reaped on a later pass once its stragglers drain. Each
-- NOT EXISTS is an indexed point probe: idx_workspaces_owner_user_id covers
-- workspaces.owner_user_id, and the leading column of
-- idx_workers_registered_by_status_created covers workers.registered_by.
DELETE FROM users WHERE rowid IN (
    SELECT u.rowid FROM users u
    -- Raw compare: deleted_at (canonical on every write) against the SQLiteTime
    -- cutoff (same canonical layout; see HardDeleteWorkersBefore).
    WHERE u.deleted_at IS NOT NULL AND u.deleted_at < sqlc.arg(cutoff)
      AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.owner_user_id = u.id)
      AND NOT EXISTS (SELECT 1 FROM workers wk WHERE wk.registered_by = u.id)
    LIMIT 1000
);

-- name: GetUserPrefs :one
SELECT prefs FROM users WHERE id = ? AND deleted_at IS NULL;

-- SQLite has no row-level SELECT FOR UPDATE. This self-assign write takes
-- the database writer lock before the user-preferences write path's
-- read-modify-write merge reads the blob, exactly as LockAllSettings
-- does for hub_settings; without it two concurrent per-key updates both
-- read the same base and the second commit erases the first's key.
-- Callers must hold a transaction.
-- name: GetUserPrefsForUpdate :one
UPDATE users SET id = id WHERE id = ? AND deleted_at IS NULL RETURNING prefs;

-- name: UpdateUserPrefs :exec
UPDATE users SET prefs = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

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
--
-- pending_email_expires_at and pending_email_unblocked_at MUST land in the
-- canonical strftime layout, so the bound instants are a SQLiteNullTime
-- rather than the modernc driver layout a raw time.Time bind would
-- produce: ConsumeVerificationAttempt's lockout branch writes the expiry
-- via strftime('now'), ClearStalePendingEmails compares it raw against a
-- canonical cutoff, and the WHERE above compares unblocked_at raw against
-- a canonical cutoff -- mixing layouts breaks those lexicographic compares
-- at the ' ' vs 'T' separator byte (byte 10). An invalid SQLiteNullTime
-- binds NULL.
UPDATE users SET pending_email = sqlc.arg(pending_email), pending_email_token = sqlc.arg(pending_email_token), pending_email_expires_at = sqlc.narg(pending_email_expires_at), pending_email_unblocked_at = sqlc.arg(pending_email_unblocked_at), pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
UPDATE users SET pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = sqlc.narg(unblocked_at), pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = sqlc.arg(id);

-- name: ClearPendingEmail :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: PromotePendingEmail :one
UPDATE users SET email = pending_email, email_verified = 1, pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ? AND pending_email != ''
RETURNING id, updated_at;

-- name: ConsumeVerificationAttempt :one
UPDATE users
SET pending_email_attempts = pending_email_attempts + 1,
    pending_email_expires_at = CASE
        WHEN pending_email_attempts + 1 > sqlc.arg(max_attempts) THEN sqlc.arg(now)
        ELSE pending_email_expires_at END,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = sqlc.arg(id) AND pending_email_token != ''
RETURNING *;

-- name: ClearCompetingPendingEmails :exec
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE pending_email = ? AND id != ?;

-- name: ClearStalePendingEmails :execresult
-- The second arm reaps a codeless row: a send the relay refused leaves the
-- ADDRESS with no token and no expiry (ClearPendingEmailCode), so the
-- expiry compare can never see it and an abandoned address would otherwise
-- outlive the database. updated_at is the only instant such a row carries,
-- and ClearPendingEmailCode stamps it.
-- Raw compare: pending_email_expires_at is stored canonical on every write path
-- (SetPendingEmail binds a SQLiteNullTime; ConsumeVerificationAttempt's lockout
-- branch writes strftime('now')), so comparing it raw against the SQLiteTime
-- cutoff (same canonical layout) is byte-exact; see HardDeleteUsersBefore.
UPDATE users SET pending_email = '', pending_email_token = '', pending_email_expires_at = NULL, pending_email_unblocked_at = NULL, pending_email_attempts = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE pending_email != ''
  AND ((pending_email_token != '' AND pending_email_expires_at IS NOT NULL AND pending_email_expires_at < sqlc.arg(cutoff))
    OR (pending_email_token = '' AND updated_at < sqlc.arg(codeless_cutoff)));

-- name: BumpUserTokensRevokedAt :one
-- Bumps the user-wide revocation timestamp and credential epoch, then
-- returns the row that moved so the store layer can emit a durable
-- revocation event carrying the new epoch. The query itself has no
-- deleted_at guard, so it would act on a soft-deleted row -- but neither
-- caller can reach one: RevokeUserTokens (via RevokeAllUserCredentials)
-- runs inside RunInUserAuthTransaction, whose LockUserAuthState filters
-- deleted_at IS NULL; fenceUserTokensLocked (the auth-gate reduction fence)
-- runs only in RunUserInfoMutation's existedBefore && existedAfter branch,
-- and both existence reads use GetUserByID, which also filters
-- deleted_at IS NULL. Every revoke path revokes before soft-deleting, so
-- that ordering is not exercised today. Only a truly missing row (no id
-- match) is a no-op.
UPDATE users
SET tokens_revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    auth_generation   = auth_generation + 1,
    updated_at        = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?
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
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = sqlc.arg(id);

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
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
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
-- Spends the recovery token on a replacement factor. password_set is 1 for
-- a new password and 0 for a passkey spend (caller binds the not-set
-- placeholder hash). The WHERE re-checks the token at write time, so a
-- link spent on a concurrent completion matches no row.
UPDATE users
SET password_hash = sqlc.arg(password_hash),
    password_set = sqlc.arg(password_set),
    pending_recovery_token = '',
    pending_recovery_expires_at = NULL,
    pending_recovery_unblocked_at = NULL,
    pending_recovery_attempts = 0,
    tokens_revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
    auth_generation = auth_generation + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = sqlc.arg(id) AND pending_recovery_token = sqlc.arg(pending_recovery_token)
RETURNING id, tokens_revoked_at, auth_generation;

