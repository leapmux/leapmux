// Package store defines the Hub storage abstraction layer.
//
// The Store interface provides all database operations needed by the Hub,
// grouped into domain-specific sub-stores. Implementations exist for
// SQLite (default), PostgreSQL, and MySQL-compatible backends.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"
)

// Store is the top-level storage abstraction for the Hub.
type Store interface {
	Users() UserStore
	Sessions() SessionStore
	Workers() WorkerStore
	WorkerNotifications() WorkerNotificationStore
	RegistrationKeys() RegistrationKeyStore
	Workspaces() WorkspaceStore
	WorkspaceTabIndex() WorkspaceTabIndexStore
	UserOpBatches() UserOpBatchesStore
	UserState() UserStateStore
	UserRecentBatchIDs() UserRecentBatchIDStore
	LifecycleOutbox() LifecycleOutboxStore
	WorkspaceSections() WorkspaceSectionStore
	WorkspaceSectionItems() WorkspaceSectionItemStore
	OAuthProviders() OAuthProviderStore
	OAuthStates() OAuthStateStore
	OAuthTokens() OAuthTokenStore
	OAuthUserLinks() OAuthUserLinkStore
	PendingOAuthSignups() PendingOAuthSignupStore
	PasskeyCredentials() PasskeyCredentialStore
	WebAuthnSessions() WebAuthnSessionStore
	Settings() SettingsStore
	AltchaSalts() AltchaSaltsStore
	APITokens() APITokenStore
	DelegationTokens() DelegationTokenStore
	RevocationEvents() RevocationEventStore
	DeviceAuthorizations() DeviceAuthorizationStore
	OAuthAuthorizationCodes() OAuthAuthorizationCodeStore
	OAuthClients() OAuthClientStore
	Cleanup() CleanupStore

	// Migrator returns the schema migration manager for this backend.
	Migrator() Migrator

	// RunInTransaction executes fn within a transaction. The provided
	// Store is bound to the transaction.
	//
	// FN MAY RUN MORE THAN ONCE. A distributed backend resolves a
	// write-write conflict by aborting one transaction with a retryable
	// SQLSTATE and expects the client to run the whole unit of work again,
	// so an implementation may do exactly that. Two rules follow, and they
	// bind every caller:
	//
	//   - A result reported through a captured variable is FINE. A re-run
	//     overwrites it, so the caller reads the successful attempt's value.
	//   - State that ACCUMULATES is not: an append to a captured slice, a
	//     counter, a send on a channel, a lifecycle effect, a metric. The
	//     aborted attempt's contribution stays and the caller double-counts
	//     it. Do that work AFTER the transaction returns, from the values
	//     the callback assigned -- which is what every caller here does.
	//
	// A re-run repeats every read the callback performs, so a callback must
	// not assume it observes the database only once.
	RunInTransaction(ctx context.Context, fn func(tx Store) error) error

	// RunInUserAuthTransaction executes fn in a transaction after locking the
	// user's auth-state row. Credential creation, password rotation, and
	// user-wide revocation must use this boundary so their commit order is the
	// credential validity order. Nested calls reuse the current transaction.
	//
	// fn may run more than once, under the same rules RunInTransaction states.
	RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx Store) error) error

	// Close releases any resources (connection pools, etc.).
	Close() error
}

// Migrator handles schema evolution for the storage backend.
type Migrator interface {
	// CurrentVersion returns the currently applied schema version.
	CurrentVersion(ctx context.Context) (int64, error)

	// LatestVersion returns the highest available migration version.
	LatestVersion() int64

	// Migrate applies all pending migrations up to the latest version.
	Migrate(ctx context.Context) error

	// MigrateTo applies or rolls back migrations to reach the target
	// version. Rollback support depends on the backend.
	MigrateTo(ctx context.Context, version int64) error
}

type UserStore interface {
	Create(ctx context.Context, p CreateUserParams) error
	GetByID(ctx context.Context, id string) (*User, error)
	GetByIDIncludeDeleted(ctx context.Context, id string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetFirstAdmin(ctx context.Context) (*User, error)
	ExistsByUsername(ctx context.Context, username string) (bool, error)
	ExistsByEmail(ctx context.Context, email, excludeUserID string) (bool, error)
	// ConsumeVerificationAttempt atomically charges one attempt against
	// the user's pending verification (force-expiring on the 6th try)
	// and returns the post-update row. Returns ErrNotFound when there
	// is no pending verification to charge — callers should map that
	// to FailedPrecondition. The returned row is the source of truth
	// for the constant-time code comparison that follows.
	ConsumeVerificationAttempt(ctx context.Context, id string, now time.Time, maxAttempts int64) (*User, error)
	GetPrefs(ctx context.Context, id string) (string, error)
	// GetPrefsForUpdate reads the prefs blob locked against concurrent
	// writers (SELECT ... FOR UPDATE, or the SQLite self-assign write that
	// takes the writer lock). The lock is what makes the per-key
	// read-modify-write merge safe: two overlapping updates to different
	// keys would otherwise both read the same base blob and the second
	// commit would erase the first's key. Callers must hold a transaction.
	GetPrefsForUpdate(ctx context.Context, id string) (string, error)
	HasAny(ctx context.Context) (bool, error)
	Count(ctx context.Context) (int64, error)
	ListAll(ctx context.Context, p ListAllUsersParams) (Page[User], error)
	Search(ctx context.Context, p SearchUsersParams) (Page[User], error)
	UpdateProfile(ctx context.Context, p UpdateUserProfileParams) error
	UpdatePassword(ctx context.Context, p UpdateUserPasswordParams) error
	UpdateEmail(ctx context.Context, p UpdateUserEmailParams) error
	UpdateEmailVerified(ctx context.Context, p UpdateUserEmailVerifiedParams) error
	UpdateAdmin(ctx context.Context, p UpdateUserAdminParams) error
	UpdatePrefs(ctx context.Context, p UpdateUserPrefsParams) error
	// SetPendingEmail conditionally mints a verification code. It reports
	// false when a previous code is still inside its resend cooldown, so
	// the caller neither sends nor overwrites the live code.
	SetPendingEmail(ctx context.Context, p SetPendingEmailParams) (bool, error)
	PromotePendingEmail(ctx context.Context, id string) error
	ClearPendingEmail(ctx context.Context, id string) error
	// ClearPendingEmailCode drops an undelivered code and keeps the
	// pending address, so a failed send does not lose the only record of
	// what the account verifies. UnblockedAt is the failure-window deadline
	// instant the clear leaves behind: the mint gate and the reported
	// countdown then agree that one more short window (the failure
	// cooldown) must pass before the retry a failed send invites.
	ClearPendingEmailCode(ctx context.Context, p ClearPendingEmailCodeParams) error
	ClearCompetingPendingEmails(ctx context.Context, p ClearCompetingPendingEmailsParams) error
	SetPendingRecovery(ctx context.Context, p SetPendingRecoveryParams) (bool, error)
	// ClearPendingRecovery clears an uncompleted recovery link; see
	// ClearPendingEmailCode for the stamp's meaning.
	ClearPendingRecovery(ctx context.Context, p ClearPendingRecoveryParams) error
	ConsumeRecoveryAttemptByToken(ctx context.Context, tokenHash string, now time.Time, maxAttempts int64) (*User, error)
	CompleteRecovery(ctx context.Context, p CompleteRecoveryParams) (*RecoveryRevocation, error)
	// Delete soft-deletes the user.
	Delete(ctx context.Context, id string) error
	// RevokeUserTokens advances the user's tokens_revoked_at marker
	// plus auth_generation epoch and emits a durable user-token
	// revocation event in the same transaction. Returns the number of
	// rows affected (0 when no user row matches the id). The UPDATE has no
	// deleted_at guard, but the sole production caller
	// (RevokeAllUserCredentials) runs inside RunInUserAuthTransaction, whose
	// LockUserAuthState filters `deleted_at IS NULL` -- so a
	// revoke-after-soft-delete aborts the transaction before this runs rather
	// than firing teardown. Every revoke path revokes BEFORE soft-deleting, so
	// that ordering is not exercised; a delete flow must not be reordered to
	// soft-delete-then-revoke or the cross-process teardown is lost.
	// Idempotent with respect to missing rows, but each successful revoke is
	// a fresh revocation event because channels opened after an earlier
	// revoke still need the newer epoch.
	RevokeUserTokens(ctx context.Context, userID userid.UserID) (int64, error)
}

type SessionStore interface {
	Create(ctx context.Context, p CreateSessionParams) error
	GetByID(ctx context.Context, id string, now time.Time) (*UserSession, error)
	// Touch conditionally slides a session's expiry forward and returns the
	// number of rows updated. The UPDATE is guarded by last_active_at so a
	// recently-touched session matches zero rows; callers must extend an
	// in-memory lifecycle only when rowsAffected > 0, so cached deadlines
	// never advance past the un-updated DB expiry.
	Touch(ctx context.Context, p TouchSessionParams, now time.Time) (int64, error)
	// Delete ends one session because its OWNER ended it -- Logout, or a
	// sweep that a password rotation drives. It emits
	// RevocationEventKindSession.
	Delete(ctx context.Context, id string) (int64, error)
	// Revoke ends one session because an ADMINISTRATOR took it away. It
	// deletes the same row Delete does and emits
	// RevocationEventKindSessionRevoked instead, which is the only durable
	// record that separates the two -- see that constant.
	Revoke(ctx context.Context, id string) (int64, error)
	// DeleteByUser ends EVERY session of one account. It emits no event, and
	// that is correct rather than an omission: each caller pairs it with
	// auth.RevokeAllUserCredentials in the same transaction, which advances
	// the account's credential epoch. An epoch bump is strictly stronger than
	// a per-session event -- it invalidates every credential the account
	// holds, sessions included -- so a second event would say less and cost
	// another row.
	DeleteByUser(ctx context.Context, userID userid.UserID) error
	// DeleteOthers ends every session of one account EXCEPT KeepID, for a
	// password change that keeps the caller signed in. It emits no event for
	// the same reason DeleteByUser does not: its caller bumps the account's
	// credential epoch, then restamps the kept session onto the new epoch.
	DeleteOthers(ctx context.Context, p DeleteOtherSessionsParams) error
	// RefreshAuthGeneration moves the kept current session onto the
	// user's latest auth_generation after a password change. Other
	// sessions remain deleted or stale.
	RefreshAuthGeneration(ctx context.Context, p RefreshSessionAuthGenerationParams) (int64, error)
	ListByUserID(ctx context.Context, p ListUserSessionsParams, now time.Time) (Page[UserSession], error)
	ListAllActive(ctx context.Context, p ListAllActiveSessionsParams, now time.Time) (Page[ActiveSession], error)
	ValidateWithUser(ctx context.Context, id string, now time.Time) (*SessionWithUser, error)
	// Elevate grants the session a fresh step-up window, replacing whatever
	// window it held. It returns the number of rows updated: zero when the
	// session is gone, expired, or owned by another user. It emits a
	// user_info event so every hub drops the cached UserInfo and re-reads
	// the new deadline without logging the user out.
	//
	// It writes ElevateSessionParams.ClampedExpiresAt, not the requested
	// deadline, so a grant longer than ElevationMaxTotal is unwritable.
	Elevate(ctx context.Context, p ElevateSessionParams, now time.Time) (int64, error)
	// SlideElevation extends a LIVE elevation and returns the rows updated.
	// The statement clamps the new deadline to the stored
	// elevation_proven_at plus ElevationMaxTotal, which the store binds
	// itself, so no caller can extend one past its cap.
	//
	// It deliberately emits no event: a cache still holding the shorter
	// deadline fails closed, and it expires on its own within the cache TTL.
	SlideElevation(ctx context.Context, p SlideSessionElevationParams, now time.Time) (int64, error)
	// DropElevation ends the session's elevation now, and emits the same
	// user_info event Elevate does -- here it must, because a cached longer
	// deadline would fail OPEN.
	DropElevation(ctx context.Context, p DropSessionElevationParams, now time.Time) (int64, error)
}

type WorkerStore interface {
	Create(ctx context.Context, p CreateWorkerParams) error
	GetByID(ctx context.Context, id string) (*Worker, error)
	// GetByIDIncludeDeleted returns the worker row even if it is
	// soft-deleted. Use this only for admin tooling / audit paths that need
	// to inspect deleted records; normal business logic should use GetByID.
	GetByIDIncludeDeleted(ctx context.Context, id string) (*Worker, error)
	GetByAuthToken(ctx context.Context, token string) (*Worker, error)
	GetPublicKey(ctx context.Context, id string) (*WorkerPublicKeys, error)
	GetOwned(ctx context.Context, p GetOwnedWorkerParams) (*Worker, error)
	ListByUserID(ctx context.Context, p ListWorkersByUserIDParams) (Page[Worker], error)
	ListAdmin(ctx context.Context, p ListWorkersAdminParams) (Page[WorkerWithOwner], error)
	// GetAdmin is the single-row form of ListAdmin: the worker plus the
	// owner projection, resolved by the SAME join. A caller that reads the
	// worker and then looks the owner up separately re-derives that
	// projection in Go and gets it wrong -- it cannot report OwnerDeleted
	// at all, and it reports a deleted owner's username where the listing
	// reports "". Soft-deleted workers are included, as ListAdmin includes
	// them for status=WORKER_STATUS_DELETED.
	GetAdmin(ctx context.Context, id string) (*WorkerWithOwner, error)
	SetStatus(ctx context.Context, p SetWorkerStatusParams) error
	UpdateLastSeen(ctx context.Context, id string) error
	UpdatePublicKey(ctx context.Context, p UpdateWorkerPublicKeyParams) error
	Deregister(ctx context.Context, p DeregisterWorkerParams) (int64, error)
	ForceDeregister(ctx context.Context, id string) (int64, error)
	MarkDeleted(ctx context.Context, id string) error
	// MarkAllDeletedByUser soft-deletes every worker registered by
	// registeredBy. A zero id is ErrInvalidArgument, never a silent no-op:
	// binding "" would address every blank-registrant row for deletion, and
	// reporting success when it deleted nothing is the worse half of the same
	// mistake.
	MarkAllDeletedByUser(ctx context.Context, registeredBy userid.UserID) error
}

type WorkerNotificationStore interface {
	Create(ctx context.Context, p CreateWorkerNotificationParams) error
	ListPendingByWorker(ctx context.Context, workerID string) ([]WorkerNotification, error)
	MarkDelivered(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id string) error
	IncrementAttempts(ctx context.Context, id string) error
}

// RegistrationKeySoftDeleteOffset is how far into the past SoftDelete
// pushes a key's expires_at. One second is enough to fail liveness
// checks while staying well within the cleanup loop's retention window.
const RegistrationKeySoftDeleteOffset = -time.Second

// RegistrationKeyStore manages short-lived worker registration keys.
type RegistrationKeyStore interface {
	Create(ctx context.Context, p CreateRegistrationKeyParams) error
	// GetByID returns the row regardless of expiry; callers that want
	// liveness must check ExpiresAt themselves. Returns ErrNotFound when
	// no row exists with the given id.
	GetByID(ctx context.Context, id string) (*WorkerRegistrationKey, error)
	// GetOwned returns the row only if it exists AND was created by
	// p.CreatedBy. Returns ErrNotFound for both "no such id" and "id is
	// someone else's" — collapsing them avoids leaking an oracle on
	// other users' keys. A zero CreatedBy is ErrNotFound too: it is an
	// ownership gate, so it must never bind a blank owner (see OwnerFilter).
	GetOwned(ctx context.Context, p GetOwnedRegistrationKeyParams) (*WorkerRegistrationKey, error)
	// Extend atomically rewrites ExpiresAt iff the row is owned by
	// CreatedBy and still live (current expires_at > now). Returns
	// rows-affected: 0 means the row is missing, not owned, or was
	// concurrently consumed/expired — closing the resurrection race
	// against a concurrent Consume. The caller still owns the
	// service-level anti-spam buffer check.
	Extend(ctx context.Context, p ExtendRegistrationKeyParams) (int64, error)
	// SoftDelete pushes ExpiresAt into the past for a row owned by
	// CreatedBy. Returns rows-affected: 0 means missing or not owned
	// (callers map to NotFound). Idempotent on already-dead rows.
	SoftDelete(ctx context.Context, p SoftDeleteRegistrationKeyParams) (int64, error)
	// AdminSoftDelete is the operator-driven counterpart to SoftDelete:
	// it pushes ExpiresAt into the past without an ownership check.
	// Returns rows-affected: 0 means missing. Used by `admin worker
	// reg-key revoke` to defuse a leaked key regardless of its creator.
	AdminSoftDelete(ctx context.Context, id string) (int64, error)
	// Consume atomically marks a *live* row as soft-deleted and returns
	// it. Returns ErrNotFound if the row is missing or already expired
	// (so callers can map the result to Unauthenticated).
	Consume(ctx context.Context, id string) (*WorkerRegistrationKey, error)
	// ListAdmin returns registration keys for `admin worker reg-key list`.
	// IncludeExpired=false is the default and hides revoked/expired rows;
	// IncludeExpired=true surfaces the full table for forensics within the
	// cleanup retention window.
	ListAdmin(ctx context.Context, p ListRegistrationKeysAdminParams) (Page[WorkerRegistrationKeyWithCreator], error)
}

type WorkspaceStore interface {
	Create(ctx context.Context, p CreateWorkspaceParams) error
	GetByID(ctx context.Context, id string) (*Workspace, error)
	GetByIDIncludeDeleted(ctx context.Context, id string) (*Workspace, error)
	// ListByIDs returns the non-deleted workspace rows whose id is in
	// `ids`. Missing or deleted ids are silently dropped from the
	// result. Empty `ids` returns nil with no DB call. The CLI's
	// requested-workspace paths (`tab list`, `/ws/userevents`
	// subscribe) use this to verify a batch of refs in a single query.
	ListByIDs(ctx context.Context, ids []string) ([]Workspace, error)
	// ListAccessible returns every non-deleted workspace the user owns,
	// newest first.
	ListAccessible(ctx context.Context, p ListAccessibleWorkspacesParams) ([]Workspace, error)
	Rename(ctx context.Context, p RenameWorkspaceParams) (int64, error)
	SoftDelete(ctx context.Context, p SoftDeleteWorkspaceParams) (int64, error)
	SoftDeleteAllByUser(ctx context.Context, ownerUserID userid.UserID) error
}

// WorkspaceTabIndexStore is the materialized derived view of every
// non-tombstoned tab in the user doc. The CRDT manager keeps it in
// sync with UserCrdtState; UI / worker reconciliation consume it via
// _rendered (UI) or _owned (worker reconciliation).
type WorkspaceTabIndexStore interface {
	UpsertOwned(ctx context.Context, p UpsertOwnedTabParams) error
	// BulkUpsertOwned applies every row in `rows` as a single bulk
	// upsert. Empty slice is a no-op. Implementations chunk internally
	// when the backend's parameter limit would be exceeded, but the
	// operation as a whole is not atomic across chunks — callers that
	// need atomicity must run inside a transaction.
	BulkUpsertOwned(ctx context.Context, rows []UpsertOwnedTabParams) error
	// BulkDeleteOwned deletes every row identified by `keys` as a
	// single bulk delete. Empty slice is a no-op. Same chunking /
	// atomicity notes as BulkUpsertOwned.
	//
	// A key with a zero/blank owner is SKIPPED rather than refusing the
	// whole call (store.FilterTabIndexKeys): the bad key is one of many, so
	// refusing would silently drop the deletes queued for its valid
	// neighbours. Skipped is not silent -- every dialect logs the drop count
	// at WARN, because reaching a zero owner means an upstream tenancy
	// invariant broke.
	BulkDeleteOwned(ctx context.Context, keys []TabIndexKey) error
	// ListOwnedByWorker returns p.UserID's owned tabs hosted by p.WorkerID.
	// The result is authoritative ONLY for that owner: the worker's orphan
	// reconciler infers "absent here means orphaned" from it, so a response
	// must declare the owner it covers (see leapmuxv1.
	// ListOwnedTabsForWorkerResponse.owner_user_id) rather than let a
	// narrower scope read as a wider absence. A zero UserID returns nil.
	ListOwnedByWorker(ctx context.Context, p ListOwnedTabsByWorkerParams) ([]WorkspaceTabRow, error)
	// ListOwnedTabsByWorkspace returns every tab p.UserID holds in
	// p.WorkspaceID, as (worker_id, tab_type, tab_id). A zero UserID returns
	// nil. Owner-scoped because the caller cannot filter a foreign owner's rows
	// back out of the result.
	//
	// Called INSIDE the workspace-delete transaction, which is the point: the
	// worker fan-out and the per-worker tab list come from one atomic read of
	// the authoritative projection, so no tab can slip in between them and no
	// caller has to resolve its own list beforehand.
	ListOwnedTabsByWorkspace(ctx context.Context, p ListOwnedTabsByWorkspaceParams) ([]OwnedTabRef, error)
	// GetOwned returns the single workspace_tab_owned row identified
	// by (user_id, tab_id) -- the table's primary key -- or ErrNotFound. It takes
	// no workspace: the mint-time check asks "does this user own this tab on this
	// worker", and which workspace holds it is not part of that question. The
	// indexed point-lookup mirrors GetRendered and lets the delegation
	// handler's mint-time propagation wait poll a single row instead
	// of materializing every owned tab the user has. A zero
	// p.UserID is ErrNotFound: it is an ownership gate, so it must
	// never bind a blank owner (see OwnerFilter).
	GetOwned(ctx context.Context, p GetOwnedTabParams) (*WorkspaceTabRow, error)

	UpsertRendered(ctx context.Context, p UpsertRenderedTabParams) error
	// BulkUpsertRendered is the rendered-view counterpart to
	// BulkUpsertOwned.
	BulkUpsertRendered(ctx context.Context, rows []UpsertRenderedTabParams) error
	// BulkDeleteRendered is the rendered-view counterpart to
	// BulkDeleteOwned.
	BulkDeleteRendered(ctx context.Context, keys []TabIndexKey) error
	// ListRenderedByWorkspaceIDs returns p.UserID's rendered tabs across
	// every workspace_id in `p.WorkspaceIDs`. The result is ordered by
	// (workspace_id, position) so callers iterating the slice get a
	// stable per-workspace grouping without a secondary sort. Empty
	// `p.WorkspaceIDs` returns nil with no DB call, and so does a zero
	// p.UserID: it is an ownership gate, so it must never bind a blank
	// owner (see userid.OwnerFilter).
	ListRenderedByWorkspaceIDs(ctx context.Context, p ListRenderedTabsByWorkspaceIDsParams) ([]WorkspaceTabRow, error)
	// GetRendered returns the single workspace_tab_rendered row identified
	// by (user_id, workspace_id, tab_type, tab_id), or ErrNotFound. A zero
	// p.UserID is ErrNotFound, for the same reason as GetOwned.
	GetRendered(ctx context.Context, p GetRenderedTabParams) (*WorkspaceTabRow, error)
	// LocateAccessibleRendered returns the rendered-tab row matching
	// (tab_type, tab_id) across every workspace the user owns.
	// Returns ErrNotFound when no owned workspace contains the tab.
	// Backs WorkspaceService.LocateTab so the CLI can resolve a tab's
	// full context (user / workspace / tile / worker) from just the id.
	LocateAccessibleRendered(ctx context.Context, p LocateAccessibleRenderedTabParams) (*WorkspaceTabRow, error)
}

// UserOpBatchesStore manages the CRDT op-batch journal.
type UserOpBatchesStore interface {
	Insert(ctx context.Context, p InsertUserOpBatchParams) error
	// ListAfter pages through batches strictly after the given HLC
	// cursor. `limit` caps the per-call row count so a far-behind
	// subscriber cannot OOM the broadcaster; pass a large value
	// (CRDTBatchPageLimit) for "drain everything available now".
	ListAfter(ctx context.Context, p ListUserOpBatchesAfterParams) ([]UserOpBatchRow, error)
	DeleteThrough(ctx context.Context, p DeleteUserOpBatchesThroughParams) error
	Count(ctx context.Context, userID userid.UserID) (int64, error)
}

// UserStateStore manages the per-user materialized state blob.
type UserStateStore interface {
	Get(ctx context.Context, userID userid.UserID) (*UserStateRow, error)
	Upsert(ctx context.Context, p UpsertUserStateParams) error
	AdvanceEpoch(ctx context.Context, p AdvanceUserEpochParams) error
}

// UserRecentBatchIDStore manages the dedup table.
type UserRecentBatchIDStore interface {
	Get(ctx context.Context, userID userid.UserID, batchID string) (*UserRecentBatchIDRow, error)
	Insert(ctx context.Context, p InsertUserRecentBatchIDParams) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// LifecycleOutboxStore manages the workspace-lifecycle transactional outbox.
type LifecycleOutboxStore interface {
	Insert(ctx context.Context, p InsertLifecycleOutboxParams) error
	// ListPending pages through unconsumed rows in id order. `limit`
	// caps the per-call row count so a wedged outbox cannot OOM the
	// dispatcher; callers iterate to drain.
	ListPending(ctx context.Context, p ListPendingLifecycleOutboxParams) ([]LifecycleOutboxRow, error)
	MarkConsumed(ctx context.Context, p MarkLifecycleOutboxConsumedParams) error
	DeleteConsumedBefore(ctx context.Context, before time.Time) (int64, error)
}

// CRDTBatchPageLimit is the default per-page row cap when a caller has
// no specific paging preference. Big enough that practical drains see
// one round trip; small enough to limit memory on a far-behind path.
const CRDTBatchPageLimit = 1024

// ListPendingLifecycleOutboxParams pages a ListPending call.
type ListPendingLifecycleOutboxParams struct {
	UserID userid.UserID
	Limit  int32
}

// APITokenStore manages durable bearer tokens (CLI / future external).
type APITokenStore interface {
	Create(ctx context.Context, p CreateAPITokenParams) error
	GetByID(ctx context.Context, id string) (*APIToken, error)
	// Elevate grants the credential a fresh step-up window, replacing
	// whatever it held, and emits a user_info event so the watcher's replay
	// re-reads the deadline. Returns the rows updated: 0 means the row is
	// gone, revoked, expired, or owned by somebody else.
	//
	// A command-line credential can elevate AT ALL because the gate that
	// protects hub settings, the user surface and the mint would otherwise
	// admit it unconditionally -- it had no row to stamp, so possession of
	// the credential file was the whole of the check. The factor is proven
	// in a browser (the /oauth/step-up ceremony), which is the only
	// place a person can answer a password or a passkey prompt.
	//
	// A DELEGATION token deliberately has no equivalent. It is minted by a
	// worker for an agent that reads untrusted input, and there is nobody to
	// prompt.
	//
	// It writes ElevateAPITokenParams.ClampedExpiresAt, not the requested
	// deadline, so a grant longer than ElevationMaxTotal is unwritable.
	Elevate(ctx context.Context, p ElevateAPITokenParams, now time.Time) (int64, error)
	// SlideElevation extends a LIVE elevation and returns the rows updated.
	// The statement clamps the new deadline to elevation_proven_at plus
	// ElevationMaxTotal, which the store binds itself, so no caller can
	// extend one past its cap. It emits no event: a stale SHORTER deadline
	// fails closed.
	SlideElevation(ctx context.Context, p SlideAPITokenElevationParams, now time.Time) (int64, error)
	// DropElevation ends the credential's elevation now, and emits the same
	// user_info event Elevate does -- here it must, because a cached longer
	// deadline would otherwise keep granting.
	DropElevation(ctx context.Context, p DropAPITokenElevationParams, now time.Time) (int64, error)
	// ListByUser pages one user's own live tokens, keyset on created_at.
	ListByUser(ctx context.Context, p ListAPITokensByUserParams) (Page[APIToken], error)
	// ListAll pages every live api_token across users with the owner username
	// (LEFT JOIN users), replacing the admin CLI's per-user fanout. Keyset on
	// created_at.
	ListAll(ctx context.Context, p ListAllAPITokensParams) (Page[APITokenWithOwner], error)
	Touch(ctx context.Context, id string) error
	// RotateRefresh atomically replaces the access/refresh secrets and emits a
	// cache-only rotation event when its compare-and-swap succeeds.
	RotateRefresh(ctx context.Context, p RotateAPITokenRefreshParams) (int64, error)
	Revoke(ctx context.Context, id string) (int64, error)
	// RevokeOwned revokes one token only when userID owns it, with the owner
	// equality inside the statement. Returns 0 when the row is missing,
	// already revoked, or owned by somebody else -- the three cases a
	// self-service caller must not be able to tell apart.
	RevokeOwned(ctx context.Context, p RevokeOwnedAPITokenParams) (int64, error)
	// RevokeByUser bulk-revokes every live api_tokens row for userID and
	// returns the count of rows affected. Hooked from admin commands that
	// kill the user's auth basis (delete, password reset,
	// force-logout-all) so api bearers die alongside delegation tokens.
	// It emits no per-token events: the user-wide RevokeUserTokens event
	// (generation-bearing) invalidates every credential atomically, so
	// per-row events would be redundant.
	//
	// It is RevokeOthers with no exclusion, and it stays a method of its own
	// for the reason SessionStore keeps DeleteByUser beside DeleteOthers: a
	// caller that means "every row" says so, rather than binding an empty
	// field that a reader must recognise as the whole-set case.
	RevokeByUser(ctx context.Context, userID userid.UserID) (int64, error)
	// RevokeOthers bulk-revokes every live api_tokens row for p.UserID
	// EXCEPT p.KeepID, and returns the count of rows affected. The twin of
	// SessionStore.DeleteOthers, and the self-service password change is the
	// caller both exist for: it revokes every other credential the account
	// holds and keeps the one that asked, whether that is a browser session
	// or a command-line credential.
	//
	// It emits no per-token events, for the reason RevokeByUser gives.
	// The caller pairs it with RefreshAuthGeneration, because the epoch bump
	// that follows would otherwise refuse the kept row on its next request.
	RevokeOthers(ctx context.Context, p RevokeOtherAPITokensParams) (int64, error)
	// RefreshAuthGeneration moves the kept command-line credential onto the
	// user's latest auth_generation after a password change, and returns the
	// rows affected. Zero means the row is gone or already revoked. The twin
	// of SessionStore.RefreshAuthGeneration.
	RefreshAuthGeneration(ctx context.Context, p RefreshAPITokenAuthGenerationParams) (int64, error)
}

// DelegationTokenStore manages worker-minted ephemeral tokens.
type DelegationTokenStore interface {
	Create(ctx context.Context, p CreateDelegationTokenParams) error
	GetByID(ctx context.Context, id string) (*DelegationToken, error)
	// ListAll pages every delegation token across users with the owner username
	// (LEFT JOIN users), replacing the admin CLI's per-user fanout. Keyset on
	// created_at.
	ListAll(ctx context.Context, p ListAllDelegationTokensParams) (Page[DelegationTokenWithOwner], error)
	ListActiveByUser(ctx context.Context, userID userid.UserID, now time.Time) ([]DelegationToken, error)
	Touch(ctx context.Context, id string) error
	Revoke(ctx context.Context, id string) (int64, error)
	// RevokeByUser bulk-revokes every non-revoked delegation token for
	// the given user (already-expired but not-yet-revoked rows are
	// revoked too -- harmless, since an expired token cannot
	// authenticate). Returns the count of rows affected. Hooked from
	// auth flows (logout, password change,
	// account deactivation) so the plan's "user-session revocation
	// propagated by hub" requirement holds: the spawned-agent
	// bearers tied to that user die at the hub the moment the user's
	// auth basis goes away. Like the api-token counterpart it emits no
	// per-token events; the user-wide RevokeUserTokens event carries the
	// generation-bearing signal.
	RevokeByUser(ctx context.Context, userID userid.UserID) (int64, error)
}

// Credential lifecycle event kinds persisted in revocation_events.kind.
const (
	// RevocationEventKindSession is a session that ENDED. The user signed
	// out, or a password rotation swept the account. It closes the
	// session's streams and drops its cached UserInfo.
	RevocationEventKindSession = "session"
	// RevocationEventKindSessionRevoked is a session an ADMINISTRATOR took
	// away. It carries the same subject and the same consumer handling as
	// RevocationEventKindSession -- the two differ only in what they say
	// about WHO ended the session, and one reader needs that.
	//
	// A per-session revoke does not move the account's auth generation,
	// deliberately: it must not sign the user's other sessions out. So the
	// credential epoch cannot separate it from a plain sign-out, and the
	// deleted row cannot either, because both paths delete the row. A
	// step-up mutation that waits on the user-auth lock TOLERATES an absent
	// acting session, or a legitimate sign-out in another tab would roll
	// the change back -- and that tolerance covered the administrator's
	// revoke as well, so a queued passkey deletion committed on the
	// authority of a session the administrator already took away. This
	// kind is the fact that separates the two. See
	// recheckActingSessionUnderLock.
	RevocationEventKindSessionRevoked   = "session_revoked"
	RevocationEventKindAPIToken         = "api_token"
	RevocationEventKindAPITokenRotation = "api_token_rotation"
	RevocationEventKindDelegationToken  = "delegation_token"
	RevocationEventKindUserTokens       = "user_tokens"
	// RevocationEventKindUserInfo is a cache-invalidation signal rather
	// than a credential revocation: an admin changed a user's cached
	// profile state (e.g. IsAdmin), so consumers must drop their cached
	// UserInfo without logging the user out. It carries
	// SubjectID=UserID=the user id and generation 0 (not generation-bearing).
	RevocationEventKindUserInfo = "user_info"
)

// RevocationEvent is a durable credential lifecycle fact before publication.
// IDs are generated by application code; watcher cursors never use this id.
type RevocationEvent struct {
	ID                 string
	Kind               string
	SubjectID          string
	UserID             string
	RevokedAt          time.Time
	UserAuthGeneration int64
	CreatedAt          time.Time
}

// PublishedRevocationEvent is the watcher-facing stream record. Seq is
// assigned gaplessly when pending events are published.
type PublishedRevocationEvent struct {
	Seq         int64
	Event       RevocationEvent
	PublishedAt time.Time
}

type AcquireHubRuntimeLeaseParams struct {
	HolderID     string
	PublishLimit int32
	// LeaseDuration is applied relative to the database's current time.
	LeaseDuration time.Duration
}

type RenewHubRuntimeLeaseParams struct {
	HolderID  string
	CursorSeq int64
	// LeaseDuration is applied relative to the database's current time.
	LeaseDuration time.Duration
}

type ReacquireHubRuntimeLeaseParams struct {
	HolderID string
	// CursorSeq is the position the holder already reached. Reacquisition
	// keeps it, where acquisition fences to the current head of the stream.
	CursorSeq int64
	// LeaseDuration is applied relative to the database's current time.
	LeaseDuration time.Duration
}

type CompactRevocationEventsParams struct {
	Cutoff time.Time
}

// RevocationEventStore manages durable pending revocation events and their
// published sequence numbers. PublishPending atomically assigns gapless seq
// values under the singleton sequence row lock.
type RevocationEventStore interface {
	PublishPending(ctx context.Context, limit int32) (int64, error)
	// AcquireHubRuntimeLease publishes at most PublishLimit pending events and
	// records the resulting sequence fence while acquiring the singleton Hub
	// lease. It returns ErrHubAlreadyRunning while another live holder exists.
	AcquireHubRuntimeLease(ctx context.Context, p AcquireHubRuntimeLeaseParams) (int64, error)
	// RenewHubRuntimeLease atomically advances the cursor and renews the live
	// singleton lease. It returns false after expiry, takeover, or removal.
	RenewHubRuntimeLease(ctx context.Context, p RenewHubRuntimeLeaseParams) (bool, error)
	// ReacquireHubRuntimeLease re-takes the singleton lease for a holder whose
	// process could not renew -- a suspended laptop, a paused VM, a long stall,
	// or a local clock that reported a lapse while the database row was still
	// live. It keeps CursorSeq instead of fencing to the head of the stream, so
	// no revocation published during the stall is skipped. It returns
	// ErrHubAlreadyRunning while ANY other holder's row is present, live or
	// expired: that holder possibly consumed and compacted past this cursor.
	// The caller's own row is released first, so a still-live own row becomes a
	// force-renewal rather than a false rival.
	ReacquireHubRuntimeLease(ctx context.Context, p ReacquireHubRuntimeLeaseParams) error
	ReleaseHubRuntimeLease(ctx context.Context, holderID string) (int64, error)
	ListPublishedAfter(ctx context.Context, afterSeq int64, limit int32) ([]PublishedRevocationEvent, error)
	MaxPublishedSeq(ctx context.Context) (int64, error)
	// SessionWasRevoked reports whether an administrator took the given
	// session away, by looking for a RevocationEventKindSessionRevoked row.
	// Pending and published events both count: the fact is the insert, and
	// the sequence number only orders the broadcast.
	//
	// The read is by subject_id, and idx_revocation_events_session_revoked
	// serves it in every dialect. sqlite and postgres declare it PARTIAL on
	// kind = 'session_revoked', so an insert of any OTHER kind -- which is
	// every kind but the rarest -- writes no index entry at all. mysql has no
	// partial index, so it leads with kind instead: the one kind this asks
	// about occupies a contiguous range, and every event pays one entry.
	//
	// TWO callers reach it, and both from a rare path: the
	// absent-acting-session case of recheckActingSessionUnderLock, and the
	// same case in refuseIfActingAuthorityMovedFrom. A request gets there only
	// when its own session row disappeared while it waited on the user-auth
	// lock, and it still HOLDS that lock while this runs -- which is why the
	// read must not degrade to a scan.
	//
	// The cleanup pass compacts published events on a seven-day retention,
	// which is longer than any lock wait by many orders, so the pass can
	// never compact a revoke away while a request waits on it.
	SessionWasRevoked(ctx context.Context, sessionID string) (bool, error)
}

// DeviceAuthorizationStore manages RFC 8628 device-code grants.
type DeviceAuthorizationStore interface {
	Create(ctx context.Context, p CreateDeviceAuthorizationParams) error
	Get(ctx context.Context, deviceCode string) (*DeviceAuthorization, error)
	GetByUserCode(ctx context.Context, userCode string) (*DeviceAuthorization, error)
	Approve(ctx context.Context, p ApproveDeviceAuthorizationParams, now time.Time) (int64, error)
	ApproveByUserCode(ctx context.Context, p ApproveDeviceAuthorizationByUserCodeParams, now time.Time) (int64, error)
	// DenyByUserCode is the deny the BROWSER runs: the activation page holds
	// the user code and never the device code. It matches a pending row only,
	// so an answered grant keeps its answer.
	DenyByUserCode(ctx context.Context, userCode string) (int64, error)
	Consume(ctx context.Context, deviceCode string, now time.Time) (int64, error)
	// ConsumeApprovedForUserClient spends the approved-but-unpolled grants of
	// one account's authorization of an app -- the DISCONNECT path, which ends
	// the authorization and must not leave a redeemable grant behind.
	ConsumeApprovedForUserClient(ctx context.Context, clientID string, user userid.UserID, now time.Time) (int64, error)
	// TouchPoll records a poll with the HUB's clock: the slow_down throttle
	// compares the value it writes against the same clock, and a database
	// clock that drifts ahead of the hub's would stall the flow.
	TouchPoll(ctx context.Context, deviceCode string, now time.Time) error
}

// OAuthAuthorizationCodeStore manages RFC 6749 section 4.1 one-shot codes.
type OAuthAuthorizationCodeStore interface {
	Create(ctx context.Context, p CreateOAuthAuthorizationCodeParams) error
	// Get returns the row WHATEVER its state, consumed and expired included.
	// The REPLAY path needs it: RFC 6749 section 4.1.2 requires the server to
	// revoke the credential a replayed code already produced, and only a
	// consumed row names that credential.
	Get(ctx context.Context, code string) (*OAuthAuthorizationCode, error)
	GetActive(ctx context.Context, code string, now time.Time) (*OAuthAuthorizationCode, error)
	Consume(ctx context.Context, code string, now time.Time) (*OAuthAuthorizationCode, error)
	// MarkMinted records which credential the exchange produced, so a later
	// replay of the same code can revoke it.
	MarkMinted(ctx context.Context, code, tokenID string) error
	// ConsumeActiveForUserClient spends this account's outstanding codes for
	// an app -- the DISCONNECT path, which must not leave a consent granted
	// seconds before it redeemable into a fresh credential.
	ConsumeActiveForUserClient(ctx context.Context, clientID string, user userid.UserID, now time.Time) (int64, error)
}

// OAuthClientStore manages registered apps.
//
// Every write carries the CALLER into the statement rather than trusting a
// check the service ran first: a read-then-write pair leaves a window in which
// the row changes hands between the two. See UpdateOAuthClientParams.
type OAuthClientStore interface {
	// Create inserts the row and returns it AS THE DATABASE WROTE IT -- the
	// DB-defaulted created_at/updated_at and the projected icon presence --
	// so a registration response is the row's truth rather than a Go-side
	// projection of the INSERT parameters. MySQL has no RETURNING; its
	// adapter pays one follow-up read.
	Create(ctx context.Context, p CreateOAuthClientParams) (*OAuthClient, error)
	// Get returns the app WHATEVER its state, revoked included. A revoked app
	// must still be readable: bearer validation joins it to refuse a live
	// credential on a retired app, and the disconnect surface has to identify
	// what it revoked.
	Get(ctx context.Context, clientID string) (*OAuthClient, error)
	// GetIcon reads the icon bytes, their media type, and the facts that gate
	// serving them, for the /oauth/apps/<id>/icon asset endpoint. It is a
	// separate read because the full-row queries carry only whether an icon
	// exists -- the bytes would put 64 KiB on every token exchange otherwise.
	GetIcon(ctx context.Context, clientID string) (*OAuthClientIcon, error)
	// UpsertBuiltIn inserts one built-in registration on a fresh database and
	// reconciles the build's constants on an existing one. The conflict
	// branch rewrites ONLY the constant columns, so elevation_allowed, the
	// vouch, revocation and the row's own history survive every boot. See
	// SeedBuiltIns, which is the only caller.
	UpsertBuiltIn(ctx context.Context, p UpsertBuiltInClientParams) error
	// List pages apps: the user's own registrations, plus the whole hub-wide
	// catalogue when p.IncludeHubWide says the reader is an administrator --
	// one statement for both questions, so the two listings cannot drift in
	// what they select or how they order.
	List(ctx context.Context, p ListOAuthClientsParams) (Page[OAuthClient], error)
	Update(ctx context.Context, p UpdateOAuthClientParams) (int64, error)
	// SetElevationAllowed toggles the one field the app list changes inline,
	// and the ONE field a built-in registration may still change: an operator
	// who does not want `leapmux control admin ...` to elevate must be able to
	// say so.
	SetElevationAllowed(ctx context.Context, p SetOAuthClientElevationAllowedParams) (int64, error)
	SetIcon(ctx context.Context, p SetOAuthClientIconParams) (int64, error)
	SetVerified(ctx context.Context, p SetOAuthClientVerifiedParams) (int64, error)
	// Revoke retires the app. It is the verb the surface offers, because a
	// hard delete of an app with live credentials is refused by the RESTRICT
	// foreign key -- and because revocation is what the caller can cascade
	// with each credential's lifecycle effects.
	Revoke(ctx context.Context, p OAuthClientOwnershipParams) (int64, error)
	// Delete hard-deletes an app that never held a credential. The foreign key
	// refuses it otherwise, which is the point: a delete that silently
	// orphaned credentials would be worse than a refusal.
	//
	// It clears the app's DEVICE GRANTS and AUTHORIZATION CODES first, in the
	// same transaction. Those reference the app under the same RESTRICT key but
	// are one-shot artifacts of a flow -- ten minutes and one minute of life --
	// so an app that ran a single abandoned device flow would otherwise be
	// undeletable, with a foreign-key error naming a table the operator has no
	// surface for. api_tokens is not cleared: a revoked credential is history,
	// and the delete refuses while one exists.
	Delete(ctx context.Context, p OAuthClientOwnershipParams) (int64, error)
	// ListTokenRefs reads the credentials RevokeTokens will revoke, before it
	// runs, so the caller can apply each row's lifecycle effects after the
	// transaction commits.
	ListTokenRefs(ctx context.Context, clientID string) ([]APITokenRef, error)
	// RevokeTokens is the cascade of an APP REVOCATION: every user's
	// credentials for the app.
	RevokeTokens(ctx context.Context, clientID string) (int64, error)
	// ListUserTokenRefs is ListTokenRefs for a DISCONNECT: this one account's
	// credentials. The user argument is a typed id rather than an optional
	// column value, so a caller cannot widen one account's disconnect to
	// everybody by forgetting a field.
	ListUserTokenRefs(ctx context.Context, clientID string, user userid.UserID) ([]APITokenRef, error)
	// RevokeUserTokens is the per-user cascade a DISCONNECT runs.
	RevokeUserTokens(ctx context.Context, clientID string, user userid.UserID) (int64, error)
	// CountLiveTokens reports how many LIVE credentials the app holds across
	// every user. It answers "what can this app still do", which is what the
	// app listing shows.
	CountLiveTokens(ctx context.Context, clientID string) (int64, error)
	// CountLiveTokensByClients is CountLiveTokens batched over a page's worth
	// of apps: one GROUP BY round trip instead of one query per row, for the
	// listing surfaces. Ids with no live credential are absent from the map.
	CountLiveTokensByClients(ctx context.Context, clientIDs []string) (map[string]int64, error)
	// CountTokens reports EVERY credential row, revoked ones included, which is
	// what the RESTRICT foreign key counts -- so it is the honest delete
	// precondition. Using the live count there told an operator to revoke and
	// then refused the delete anyway.
	CountTokens(ctx context.Context, clientID string) (int64, error)
}

type WorkspaceSectionStore interface {
	Create(ctx context.Context, p CreateWorkspaceSectionParams) error
	GetByID(ctx context.Context, id string) (*WorkspaceSection, error)
	ListByUserID(ctx context.Context, userID userid.UserID) ([]WorkspaceSection, error)
	Rename(ctx context.Context, p RenameWorkspaceSectionParams) (int64, error)
	UpdatePosition(ctx context.Context, p UpdateWorkspaceSectionPositionParams) error
	UpdateSidebarPosition(ctx context.Context, p UpdateWorkspaceSectionSidebarPositionParams) error
	Delete(ctx context.Context, p DeleteWorkspaceSectionParams) (int64, error)
	HasDefaultForUser(ctx context.Context, userID userid.UserID) (bool, error)
}

type WorkspaceSectionItemStore interface {
	Set(ctx context.Context, p SetWorkspaceSectionItemParams) error
	Get(ctx context.Context, p GetWorkspaceSectionItemParams) (*WorkspaceSectionItem, error)
	ListByUser(ctx context.Context, userID userid.UserID) ([]WorkspaceSectionItem, error)
	Delete(ctx context.Context, p DeleteWorkspaceSectionItemParams) error
	DeleteBySection(ctx context.Context, sectionID string) error
	HasItemsBySection(ctx context.Context, sectionID string) (bool, error)
	IsInArchivedSection(ctx context.Context, p IsWorkspaceInArchivedSectionParams) (bool, error)
}

type OAuthProviderStore interface {
	Create(ctx context.Context, p CreateOAuthProviderParams) error
	GetByID(ctx context.Context, id string) (*OAuthProvider, error)
	ListEnabled(ctx context.Context) ([]OAuthProviderSummary, error)
	ListAll(ctx context.Context) ([]OAuthProviderSummary, error)
	ListAllWithSecrets(ctx context.Context) ([]OAuthProvider, error)
	UpdateEnabled(ctx context.Context, p UpdateOAuthProviderEnabledParams) error
	UpdateClientSecret(ctx context.Context, id string, clientSecret []byte) error
	Delete(ctx context.Context, id string) error
}

type OAuthStateStore interface {
	Create(ctx context.Context, p CreateOAuthStateParams) error
	Get(ctx context.Context, state string) (*OAuthState, error)
	// Delete removes one state row and reports how many rows it removed.
	//
	// The count IS the single use of the flow, so the caller must read it
	// and refuse a 0. Two callbacks that carry the same state and the same
	// browser nonce both pass every earlier check and both arrive here;
	// exactly one of them deletes a row. Without the count, "a state is
	// spent once" rested on the identity provider rejecting the second use
	// of its authorization code, which this hub does not control.
	Delete(ctx context.Context, state string) (int64, error)
}

type OAuthTokenStore interface {
	Upsert(ctx context.Context, p UpsertOAuthTokensParams) error
	Get(ctx context.Context, p GetOAuthTokensParams) (*OAuthToken, error)
	ListExpiring(ctx context.Context, refreshDueAt time.Time) ([]OAuthToken, error)
	ListByKeyVersion(ctx context.Context, keyVersion int64) ([]OAuthToken, error)
	CountByKeyVersion(ctx context.Context, keyVersion int64) (int64, error)
	DeleteByProvider(ctx context.Context, providerID string) error
	DeleteByUser(ctx context.Context, userID userid.UserID) error
	DeleteByUserAndProvider(ctx context.Context, p DeleteOAuthTokensByUserAndProviderParams) error
}

type OAuthUserLinkStore interface {
	Create(ctx context.Context, p CreateOAuthUserLinkParams) error
	Get(ctx context.Context, p GetOAuthUserLinkParams) (*OAuthUserLink, error)
	ListByUser(ctx context.Context, userID userid.UserID) ([]OAuthUserLink, error)
	Delete(ctx context.Context, p DeleteOAuthUserLinkParams) error
	DeleteByProvider(ctx context.Context, providerID string) error
	// CountUsersOrphanedByProvider counts the live accounts whose only
	// login method is a link to this provider: no password set, and no
	// link to any other provider. Removing the provider row cascades every
	// link away, so each of these accounts loses its last way in.
	CountUsersOrphanedByProvider(ctx context.Context, providerID string) (int64, error)
}

type PendingOAuthSignupStore interface {
	Create(ctx context.Context, p CreatePendingOAuthSignupParams) error
	Get(ctx context.Context, token string) (*PendingOAuthSignup, error)
	Delete(ctx context.Context, token string) error
}

type PasskeyCredentialStore interface {
	Create(ctx context.Context, p CreatePasskeyCredentialParams) error
	GetByID(ctx context.Context, id string) (*PasskeyCredential, error)
	GetByCredentialID(ctx context.Context, credentialID []byte) (*PasskeyCredential, error)
	ListByUser(ctx context.Context, userID string) ([]PasskeyCredential, error)
	CountByUser(ctx context.Context, userID string) (int64, error)
	UpdateSignCount(ctx context.Context, p UpdatePasskeySignCountParams) error
	UpdateFriendlyName(ctx context.Context, id, userID, friendlyName string) error
	UpdatePublicKey(ctx context.Context, p UpdatePasskeyPublicKeyParams) error
	Delete(ctx context.Context, id, userID string) error
	DeleteAllByUser(ctx context.Context, userID string) error
	ListByKeyVersion(ctx context.Context, keyVersion int64) ([]PasskeyCredential, error)
	CountByKeyVersion(ctx context.Context, keyVersion int64) (int64, error)
}

type WebAuthnSessionStore interface {
	Create(ctx context.Context, p CreateWebAuthnSessionParams) error
	Get(ctx context.Context, id string) (*WebAuthnSession, error)
	Delete(ctx context.Context, id string) error
	// ConsumeCeremony deletes one unexpired ceremony row of the given kind.
	// Returns rows affected (0 when missing, wrong kind, expired, or already consumed).
	ConsumeCeremony(ctx context.Context, id, kind string, now time.Time) (int64, error)
	DeleteAllByUser(ctx context.Context, userID string) error
	// DeleteByUserAndKind removes open ceremony rows for one user and kind
	// (for example, replacing a prior elevation Begin).
	DeleteByUserAndKind(ctx context.Context, userID, kind string) error
}

// SettingsStore manages the hub_settings table: one row per setting key,
// carrying a public JSON half and, for secret-bearing keys, a
// keystore-encrypted secret half. Absent keys mean code defaults. The
// typed interpretation, validation, caching, and secret handling live in
// internal/hub/settings, the sole caller of this store.
type SettingsStore interface {
	// GetAll returns every stored row, ordered by key. Keys without rows
	// are simply absent; the caller applies code defaults for them.
	GetAll(ctx context.Context) ([]SettingRow, error)
	// Get returns one key's row, or ErrNotFound when the key has no row
	// (meaning it sits at its code default).
	Get(ctx context.Context, key string) (*SettingRow, error)
	// Upsert rewrites both halves of one key's row. The caller merges with
	// the existing row inside a transaction, so a nil half is an explicit
	// clear rather than an accident; at least one half must be non-nil to
	// satisfy the table's CHECK.
	Upsert(ctx context.Context, p UpsertSettingParams) error
	// InsertIfAbsent writes the row only when the key has no row, making
	// first-use provisioning a one-winner race that holds across processes
	// under every dialect's isolation. It reports whether this call
	// inserted the row.
	InsertIfAbsent(ctx context.Context, p UpsertSettingParams) (bool, error)
	// GetAllForUpdate is GetAll under the settings table's writer lock. It
	// is the ONLY lock the write path takes, and it serves both halves of
	// that path: the read-modify-write merge of the keys being written, and
	// the cross-key validation over every other key.
	//
	// One lock, not one per key plus this one. A plain GetAll for the
	// cross-key read would see rows a concurrent writer is about to change,
	// so a rule that spans keys ("email verification needs an SMTP host")
	// could pass against a sibling row that no longer holds by the time
	// both transactions commit. A per-key row lock TAKEN FIRST and this one
	// second would let two writers form a cycle, which Postgres and MySQL
	// end by aborting one of them.
	//
	// Callers must hold a transaction. Rows are locked in key order.
	GetAllForUpdate(ctx context.Context) ([]SettingRow, error)
	// Delete removes one key's row, returning the key to its code default.
	Delete(ctx context.Context, key string) error
}

// AltchaSaltsStore is the consumed-salt ledger backing ALTCHA's
// single-use challenge enforcement; rows are operational data (purged by
// the cleanup loop), not configuration.
type AltchaSaltsStore interface {
	// ConsumeAltchaSalt marks a solved ALTCHA challenge's salt as used; it
	// returns 1 when the salt was unused (first use accepted) and 0 when
	// a row already exists (replay denied).
	ConsumeAltchaSalt(ctx context.Context, p ConsumeAltchaSaltParams) (int64, error)
	// HasAltchaSalt reports, read-only, whether a salt row exists. The
	// verifier consults it before the memory-hard solution check so a
	// replay costs one indexed read; ConsumeAltchaSalt stays the
	// single-use authority.
	HasAltchaSalt(ctx context.Context, salt string) (bool, error)
}

// CleanupStore provides methods for hard-deleting soft-deleted records
// and expired ephemeral data. Backends may augment these with native
// mechanisms but must implement all methods for consistent cross-backend
// behavior.
type CleanupStore interface {
	HardDeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)
	HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteExpiredRegistrationKeysBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// DeleteUserOpBatchesBeforePhysical hard-deletes CRDT op batches whose HLC
	// physical is below cutoffPhysicalMs, across all users, in capped passes
	// (the caller drains until a pass deletes nothing). It is the retention
	// backstop for crdt.OpRetentionTTL: Manager.maybeCompact deletes by HLC but
	// only runs while a user commits, so a dormant account would otherwise
	// retain its final retention window of batches indefinitely.
	//
	// The cutoff is an HLC physical rather than a wall clock so this deletes
	// EXACTLY the set Manager.decideResume refuses. Sweeping the committed_at
	// column instead would put the two floors in different time domains -- the
	// hub's monotonically-clamped HLC versus the DB server's wall clock -- and
	// any skew between them lets this delete rows a live cursor still passes,
	// which ListUserOpBatchesAfter reports as a short tail rather than an error.
	DeleteUserOpBatchesBeforePhysical(ctx context.Context, cutoffPhysicalMs int64) (int64, error)
	// ClearStalePendingEmails wipes pending_email columns for users whose
	// pending_email_expires_at is older than cutoff. Frees up index slots
	// and ensures stale codes don't leak into future lookups.
	ClearStalePendingEmails(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredOAuthStates(ctx context.Context, now time.Time) (int64, error)
	DeleteExpiredPendingOAuthSignups(ctx context.Context, now time.Time) (int64, error)
	DeleteExpiredWebAuthnSessions(ctx context.Context, now time.Time) (int64, error)
	DeleteExpiredDeviceAuthorizations(ctx context.Context, now time.Time) (int64, error)
	DeleteExpiredOAuthAuthorizationCodes(ctx context.Context, now time.Time) (int64, error)
	// DeleteExpiredAPITokensBefore hard-deletes a live api_tokens row only
	// when BOTH of its deadlines closed before cutoff: the access expiry AND
	// the refresh window. The caller sets cutoff behind now by a retention
	// margin, so a user who asks why their CLI stopped working can still be
	// shown the row.
	//
	// BOTH, because bearer validation reads expires_at alone: an
	// administrator issues a token with a year of access and ninety days of
	// refresh, and a sweep on the refresh column alone deleted that
	// credential on day ninety-seven while it still authenticated. A NULL
	// expires_at is a token that never expires and is never swept here; a
	// NULL refresh_expires_at is a row with no refresh deadline, which counts
	// as a closed one.
	DeleteExpiredAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteRevokedAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteRevokedDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredDelegationTokensBefore(ctx context.Context, now time.Time) (int64, error)
	// DeleteExpiredAltchaSalts purges consumed ALTCHA salts whose
	// challenge window passed; the salt can no longer verify after
	// expiry, so the row only caps table growth until this sweep drops
	// it. External captcha providers enforce single use at their
	// siteverify endpoint and contribute no rows.
	DeleteExpiredAltchaSalts(ctx context.Context) (int64, error)
	// CompactPublishedRevocationEvents removes an expired Hub runtime lease,
	// then deletes retained events only through the live Hub cursor.
	CompactPublishedRevocationEvents(ctx context.Context, p CompactRevocationEventsParams) (int64, error)
}

// TestEntity identifies a table/collection for test helper operations.
type TestEntity string

const (
	EntityUsers                  TestEntity = "users"
	EntitySessions               TestEntity = "user_sessions"
	EntityWorkers                TestEntity = "workers"
	EntityWorkerRegistrationKeys TestEntity = "worker_registration_keys"
	EntityWorkspaces             TestEntity = "workspaces"
	EntityAPITokens              TestEntity = "api_tokens"
	EntityDelegationTokens       TestEntity = "delegation_tokens"
)

// validEntities is the set of known TestEntity values, used by
// ValidateEntity to prevent SQL injection in test helpers.
var validEntities = map[TestEntity]bool{
	EntityUsers:                  true,
	EntitySessions:               true,
	EntityWorkers:                true,
	EntityWorkerRegistrationKeys: true,
	EntityWorkspaces:             true,
	EntityAPITokens:              true,
	EntityDelegationTokens:       true,
}

// ValidateEntity returns an error if entity is not a known TestEntity value.
func ValidateEntity(entity TestEntity) error {
	if !validEntities[entity] {
		return fmt.Errorf("unknown entity %q", entity)
	}
	return nil
}

// TestHelper provides test-only operations for backends. It is not
// part of the production Store interface but is used by the conformance
// test suite to perform operations like backdating deleted_at timestamps.
type TestHelper interface {
	// SetDeletedAt backdates the deleted_at timestamp for a record.
	SetDeletedAt(ctx context.Context, entity TestEntity, id string, deletedAt time.Time) error

	// SetCreatedAt backdates the created_at timestamp for a record.
	SetCreatedAt(ctx context.Context, entity TestEntity, id string, createdAt time.Time) error

	// SetLastActiveAt writes an exact last_active_at timestamp for a session
	// row. Only the user_sessions table carries this column, so the table is
	// pinned in the implementations rather than caller-supplied -- a
	// wrong-entity call is inexpressible.
	SetLastActiveAt(ctx context.Context, id string, lastActiveAt time.Time) error

	// SetRevocationEventRevokedAt writes an exact revocation_events.revoked_at timestamp.
	SetRevocationEventRevokedAt(ctx context.Context, id string, revokedAt time.Time) error

	// ListAllOwnedTabs returns every workspace_tab_owned row, unfiltered by
	// owner, ordered by (user_id, tab_id).
	//
	// This is test-only ON PURPOSE and must never gain a production caller:
	// every production read of that table binds user_id, because
	// (workspace_id, tab_id) is not a key (see GetOwnedTabParams). The suite
	// still needs an owner-blind view to assert what a write or delete left
	// behind for an owner it cannot identify -- notably the blank-owner rows
	// the FilterTabIndexKeys guard exists to protect, which no owner-scoped
	// read can observe.
	ListAllOwnedTabs(ctx context.Context) ([]WorkspaceTabRow, error)

	// TruncateAll deletes all data from all tables, preserving the schema.
	// Metadata tables (e.g. goose_db_version, schema_version, meta) are
	// not touched so that the migrator remains functional.
	TruncateAll(ctx context.Context) error
}

// TestableStore is a Store that also provides test helper operations.
// Backend implementations should implement this in test code only.
type TestableStore interface {
	Store
	TestHelper() TestHelper
}
