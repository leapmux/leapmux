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
)

// Store is the top-level storage abstraction for the Hub.
type Store interface {
	Orgs() OrgStore
	Users() UserStore
	Sessions() SessionStore
	OrgMembers() OrgMemberStore
	Workers() WorkerStore
	WorkerAccessGrants() WorkerAccessGrantStore
	WorkerNotifications() WorkerNotificationStore
	RegistrationKeys() RegistrationKeyStore
	Workspaces() WorkspaceStore
	WorkspaceAccess() WorkspaceAccessStore
	WorkspaceTabIndex() WorkspaceTabIndexStore
	OrgOpBatches() OrgOpBatchesStore
	OrgState() OrgStateStore
	OrgRecentBatchIDs() OrgRecentBatchIDStore
	LifecycleOutbox() LifecycleOutboxStore
	WorkspaceSections() WorkspaceSectionStore
	WorkspaceSectionItems() WorkspaceSectionItemStore
	OAuthProviders() OAuthProviderStore
	OAuthStates() OAuthStateStore
	OAuthTokens() OAuthTokenStore
	OAuthUserLinks() OAuthUserLinkStore
	PendingOAuthSignups() PendingOAuthSignupStore
	APITokens() APITokenStore
	DelegationTokens() DelegationTokenStore
	RevocationEvents() RevocationEventStore
	DeviceAuthorizations() DeviceAuthorizationStore
	CLIAuthorizationCodes() CLIAuthorizationCodeStore
	Cleanup() CleanupStore

	// Migrator returns the schema migration manager for this backend.
	Migrator() Migrator

	// RunInTransaction executes fn within a transaction. The provided
	// Store is bound to the transaction.
	RunInTransaction(ctx context.Context, fn func(tx Store) error) error

	// RunInUserAuthTransaction executes fn in a transaction after locking the
	// user's auth-state row. Credential creation, password rotation, and
	// user-wide revocation must use this boundary so their commit order is the
	// credential validity order. Nested calls reuse the current transaction.
	RunInUserAuthTransaction(ctx context.Context, userID string, fn func(tx Store) error) error

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

type OrgStore interface {
	Create(ctx context.Context, p CreateOrgParams) error
	GetByID(ctx context.Context, id string) (*Org, error)
	GetByIDIncludeDeleted(ctx context.Context, id string) (*Org, error)
	GetByName(ctx context.Context, name string) (*Org, error)
	HasAny(ctx context.Context) (bool, error)
	ListAll(ctx context.Context, p ListAllOrgsParams) ([]Org, error)
	Search(ctx context.Context, p SearchOrgsParams) ([]Org, error)
	UpdateName(ctx context.Context, p UpdateOrgNameParams) error
	SoftDelete(ctx context.Context, id string) error
	SoftDeleteNonPersonal(ctx context.Context, id string) error
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
	ConsumeVerificationAttempt(ctx context.Context, id string) (*User, error)
	GetPrefs(ctx context.Context, id string) (string, error)
	HasAny(ctx context.Context) (bool, error)
	Count(ctx context.Context) (int64, error)
	ListByOrgID(ctx context.Context, orgID string) ([]User, error)
	// ListByIDs returns the live (non-deleted) user rows whose id is
	// in `ids`. Missing or deleted ids are silently dropped from the
	// result — callers diff against the input slice when they need to
	// detect absence. Empty `ids` returns nil with no DB call. Used by
	// share-flow validators that need to verify a batch of user refs
	// without paying N round-trips.
	ListByIDs(ctx context.Context, ids []string) ([]User, error)
	ListAll(ctx context.Context, p ListAllUsersParams) ([]User, error)
	Search(ctx context.Context, p SearchUsersParams) ([]User, error)
	UpdateProfile(ctx context.Context, p UpdateUserProfileParams) error
	UpdatePassword(ctx context.Context, p UpdateUserPasswordParams) error
	UpdateEmail(ctx context.Context, p UpdateUserEmailParams) error
	UpdateEmailVerified(ctx context.Context, p UpdateUserEmailVerifiedParams) error
	UpdateAdmin(ctx context.Context, p UpdateUserAdminParams) error
	UpdatePrefs(ctx context.Context, p UpdateUserPrefsParams) error
	SetPendingEmail(ctx context.Context, p SetPendingEmailParams) error
	PromotePendingEmail(ctx context.Context, id string) error
	ClearPendingEmail(ctx context.Context, id string) error
	ClearCompetingPendingEmails(ctx context.Context, p ClearCompetingPendingEmailsParams) error
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
	RevokeUserTokens(ctx context.Context, userID string) (int64, error)
}

type SessionStore interface {
	Create(ctx context.Context, p CreateSessionParams) error
	GetByID(ctx context.Context, id string) (*UserSession, error)
	// Touch conditionally slides a session's expiry forward and returns the
	// number of rows updated. The UPDATE is guarded by last_active_at so a
	// recently-touched session matches zero rows; callers must gate any
	// in-memory lifecycle extension on rowsAffected > 0 so cached deadlines
	// never advance past the un-updated DB expiry.
	Touch(ctx context.Context, p TouchSessionParams) (int64, error)
	Delete(ctx context.Context, id string) (int64, error)
	DeleteByUser(ctx context.Context, userID string) error
	DeleteOthers(ctx context.Context, p DeleteOtherSessionsParams) error
	// RefreshAuthGeneration moves the kept current session onto the
	// user's latest auth_generation after a password change. Other
	// sessions remain deleted or stale.
	RefreshAuthGeneration(ctx context.Context, p RefreshSessionAuthGenerationParams) (int64, error)
	ListByUserID(ctx context.Context, userID string) ([]UserSession, error)
	ListAllActive(ctx context.Context, p ListAllActiveSessionsParams) ([]ActiveSession, error)
	ValidateWithUser(ctx context.Context, id string) (*SessionWithUser, error)
}

type OrgMemberStore interface {
	Create(ctx context.Context, p CreateOrgMemberParams) error
	GetByOrgAndUser(ctx context.Context, orgID, userID string) (*OrgMember, error)
	ListByOrgID(ctx context.Context, orgID string) ([]OrgMemberWithUser, error)
	ListOrgsByUserID(ctx context.Context, userID string) ([]Org, error)
	UpdateRole(ctx context.Context, p UpdateOrgMemberRoleParams) error
	Delete(ctx context.Context, p DeleteOrgMemberParams) error
	CountByRole(ctx context.Context, p CountOrgMembersByRoleParams) (int64, error)
	IsMember(ctx context.Context, p IsOrgMemberParams) (bool, error)
}

type WorkerStore interface {
	Create(ctx context.Context, p CreateWorkerParams) error
	GetByID(ctx context.Context, id string) (*Worker, error)
	// GetByIDIncludeDeleted returns the worker row even if it has been
	// soft-deleted. Use this only for admin tooling / audit paths that need
	// to inspect deleted records; normal business logic should use GetByID.
	GetByIDIncludeDeleted(ctx context.Context, id string) (*Worker, error)
	GetByAuthToken(ctx context.Context, token string) (*Worker, error)
	GetPublicKey(ctx context.Context, id string) (*WorkerPublicKeys, error)
	GetOwned(ctx context.Context, p GetOwnedWorkerParams) (*Worker, error)
	ListByUserID(ctx context.Context, p ListWorkersByUserIDParams) ([]Worker, error)
	ListOwned(ctx context.Context, p ListOwnedWorkersParams) ([]Worker, error)
	ListAdmin(ctx context.Context, p ListWorkersAdminParams) ([]WorkerWithOwner, error)
	SetStatus(ctx context.Context, p SetWorkerStatusParams) error
	UpdateLastSeen(ctx context.Context, id string) error
	UpdatePublicKey(ctx context.Context, p UpdateWorkerPublicKeyParams) error
	Deregister(ctx context.Context, p DeregisterWorkerParams) (int64, error)
	ForceDeregister(ctx context.Context, id string) (int64, error)
	MarkDeleted(ctx context.Context, id string) error
	MarkAllDeletedByUser(ctx context.Context, registeredBy string) error
}

type WorkerAccessGrantStore interface {
	Grant(ctx context.Context, p GrantWorkerAccessParams) error
	Revoke(ctx context.Context, p RevokeWorkerAccessParams) error
	List(ctx context.Context, workerID string) ([]WorkerAccessGrant, error)
	HasAccess(ctx context.Context, p HasWorkerAccessParams) (bool, error)
	DeleteByWorker(ctx context.Context, workerID string) error
	DeleteByUser(ctx context.Context, userID string) error
	DeleteByUserInOrg(ctx context.Context, p DeleteWorkerAccessGrantsByUserInOrgParams) error
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
	// createdBy. Returns ErrNotFound for both "no such id" and "id is
	// someone else's" — collapsing them avoids leaking an oracle on
	// other users' keys.
	GetOwned(ctx context.Context, id, createdBy string) (*WorkerRegistrationKey, error)
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
	ListAdmin(ctx context.Context, p ListRegistrationKeysAdminParams) ([]WorkerRegistrationKeyWithCreator, error)
}

type WorkspaceStore interface {
	Create(ctx context.Context, p CreateWorkspaceParams) error
	GetByID(ctx context.Context, id string) (*Workspace, error)
	GetByIDIncludeDeleted(ctx context.Context, id string) (*Workspace, error)
	// ListByIDs returns the non-deleted workspace rows whose id is in
	// `ids`. Missing or deleted ids are silently dropped from the
	// result. Empty `ids` returns nil with no DB call. The CLI's
	// requested-workspace paths (`tab list`, `/ws/orgevents`
	// subscribe) use this to verify a batch of refs in a single query.
	ListByIDs(ctx context.Context, ids []string) ([]Workspace, error)
	ListAccessible(ctx context.Context, p ListAccessibleWorkspacesParams) ([]Workspace, error)
	// ListAllAccessible returns every non-deleted workspace the user can read
	// (owner OR explicit grant) across ALL orgs -- the org-unfiltered
	// counterpart of ListAccessible. It surfaces workspaces shared with a user
	// who is not a member of the owning org (cross-org collaboration) alongside
	// the user's own workspaces in every org.
	ListAllAccessible(ctx context.Context, userID string) ([]Workspace, error)
	Rename(ctx context.Context, p RenameWorkspaceParams) (int64, error)
	SoftDelete(ctx context.Context, p SoftDeleteWorkspaceParams) (int64, error)
	SoftDeleteAllByUser(ctx context.Context, ownerUserID string) error
}

type WorkspaceAccessStore interface {
	Grant(ctx context.Context, p GrantWorkspaceAccessParams) error
	BulkGrant(ctx context.Context, params []GrantWorkspaceAccessParams) error
	Revoke(ctx context.Context, p RevokeWorkspaceAccessParams) error
	ListByWorkspaceID(ctx context.Context, workspaceID string) ([]WorkspaceAccess, error)
	HasAccess(ctx context.Context, p HasWorkspaceAccessParams) (bool, error)
	// ListForUserIn returns the subset of `workspaceIDs` for which
	// userID holds a workspace_access grant. Used by the
	// /ws/orgevents subscribe path to filter a batch of requested
	// workspaces without one HasAccess call per id. Empty
	// `workspaceIDs` returns an empty slice with no DB call.
	ListForUserIn(ctx context.Context, userID string, workspaceIDs []string) ([]string, error)
	Clear(ctx context.Context, workspaceID string) error
}

// WorkspaceTabIndexStore is the materialized derived view of every
// non-tombstoned tab in the org doc. The CRDT manager keeps it in
// sync with OrgCrdtState; UI / worker reconciliation consume it via
// _rendered (UI) or _owned (worker reconciliation).
type WorkspaceTabIndexStore interface {
	UpsertOwned(ctx context.Context, p UpsertOwnedTabParams) error
	// BulkUpsertOwned applies every row in `rows` as a single bulk
	// upsert. Empty slice is a no-op. Implementations chunk internally
	// when the backend's parameter limit would be exceeded, but the
	// operation as a whole is not atomic across chunks — callers that
	// need atomicity must run inside a transaction.
	BulkUpsertOwned(ctx context.Context, rows []UpsertOwnedTabParams) error
	DeleteOwned(ctx context.Context, orgID, tabID string) error
	// BulkDeleteOwned deletes every row identified by `keys` as a
	// single bulk delete. Empty slice is a no-op. Same chunking /
	// atomicity notes as BulkUpsertOwned.
	BulkDeleteOwned(ctx context.Context, keys []TabIndexKey) error
	DeleteOwnedByOrg(ctx context.Context, orgID string) error
	ListOwnedByWorkspace(ctx context.Context, workspaceID string) ([]WorkspaceTabRow, error)
	ListOwnedByWorker(ctx context.Context, workerID string) ([]WorkspaceTabRow, error)
	ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error)
	// GetOwned returns the single workspace_tab_owned row identified
	// by (workspace_id, tab_type, tab_id), or ErrNotFound. The
	// indexed point-lookup mirrors GetRendered and lets the
	// delegation handler's mint-time propagation wait poll a single
	// row instead of materializing every owned tab in the workspace.
	GetOwned(ctx context.Context, p GetOwnedTabParams) (*WorkspaceTabRow, error)

	UpsertRendered(ctx context.Context, p UpsertRenderedTabParams) error
	// BulkUpsertRendered is the rendered-view counterpart to
	// BulkUpsertOwned.
	BulkUpsertRendered(ctx context.Context, rows []UpsertRenderedTabParams) error
	DeleteRendered(ctx context.Context, orgID, tabID string) error
	// BulkDeleteRendered is the rendered-view counterpart to
	// BulkDeleteOwned.
	BulkDeleteRendered(ctx context.Context, keys []TabIndexKey) error
	DeleteRenderedByOrg(ctx context.Context, orgID string) error
	ListRenderedByWorkspace(ctx context.Context, workspaceID string) ([]WorkspaceTabRow, error)
	// ListRenderedByWorkspaceIDs returns rendered tabs across every
	// workspace_id in `workspaceIDs`. The result is ordered by
	// (workspace_id, position) so callers iterating the slice get a
	// stable per-workspace grouping without a secondary sort. Empty
	// `workspaceIDs` returns nil with no DB call.
	ListRenderedByWorkspaceIDs(ctx context.Context, workspaceIDs []string) ([]WorkspaceTabRow, error)
	GetRendered(ctx context.Context, p GetRenderedTabParams) (*WorkspaceTabRow, error)
	// LocateAccessibleRendered returns the rendered-tab row matching
	// (tab_type, tab_id) across every workspace the user can access
	// (owner or share grant). Returns ErrNotFound when no accessible
	// workspace contains the tab. Backs WorkspaceService.LocateTab so
	// the CLI can resolve a tab's full context (org / workspace /
	// tile / worker) from just the id.
	LocateAccessibleRendered(ctx context.Context, p LocateAccessibleRenderedTabParams) (*WorkspaceTabRow, error)
}

// OrgOpBatchesStore manages the CRDT op-batch journal.
type OrgOpBatchesStore interface {
	Insert(ctx context.Context, p InsertOrgOpBatchParams) error
	// ListAfter pages through batches strictly after the given HLC
	// cursor. `limit` caps the per-call row count so a far-behind
	// subscriber cannot OOM the broadcaster; pass a large value
	// (CRDTBatchPageLimit) for "drain everything available now".
	ListAfter(ctx context.Context, p ListOrgOpBatchesAfterParams) ([]OrgOpBatchRow, error)
	DeleteThrough(ctx context.Context, p DeleteOrgOpBatchesThroughParams) error
	Count(ctx context.Context, orgID string) (int64, error)
}

// OrgStateStore manages the per-org materialized state blob.
type OrgStateStore interface {
	Get(ctx context.Context, orgID string) (*OrgStateRow, error)
	Upsert(ctx context.Context, p UpsertOrgStateParams) error
	AdvanceEpoch(ctx context.Context, p AdvanceOrgEpochParams) error
}

// OrgRecentBatchIDStore manages the dedup table.
type OrgRecentBatchIDStore interface {
	Get(ctx context.Context, orgID, batchID string) (*OrgRecentBatchIDRow, error)
	Insert(ctx context.Context, p InsertOrgRecentBatchIDParams) error
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
// one round trip; small enough to bound memory on a far-behind path.
const CRDTBatchPageLimit = 1024

// ListPendingLifecycleOutboxParams pages a ListPending call.
type ListPendingLifecycleOutboxParams struct {
	OrgID string
	Limit int32
}

// APITokenStore manages durable bearer tokens (CLI / future external).
type APITokenStore interface {
	Create(ctx context.Context, p CreateAPITokenParams) error
	GetByID(ctx context.Context, id string) (*APIToken, error)
	ListByUser(ctx context.Context, p ListAPITokensByUserParams) ([]APIToken, error)
	Touch(ctx context.Context, id string) error
	// RotateRefresh atomically replaces the access/refresh secrets and emits a
	// cache-only rotation event when its compare-and-swap succeeds.
	RotateRefresh(ctx context.Context, p RotateAPITokenRefreshParams) (int64, error)
	Revoke(ctx context.Context, id string) (int64, error)
	// RevokeByUser bulk-revokes every live api_tokens row for userID and
	// returns the count of rows affected. Hooked from admin commands that
	// kill the user's auth basis (delete, password reset,
	// force-logout-all) so api bearers die alongside delegation tokens.
	// It emits no per-token events: the user-wide RevokeUserTokens event
	// (generation-bearing) invalidates every credential atomically, so
	// per-row events would be redundant.
	RevokeByUser(ctx context.Context, userID string) (int64, error)
}

// DelegationTokenStore manages worker-minted ephemeral tokens.
type DelegationTokenStore interface {
	Create(ctx context.Context, p CreateDelegationTokenParams) error
	GetByID(ctx context.Context, id string) (*DelegationToken, error)
	ListByUser(ctx context.Context, userID string) ([]DelegationToken, error)
	ListActiveByUser(ctx context.Context, userID string) ([]DelegationToken, error)
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
	RevokeByUser(ctx context.Context, userID string) (int64, error)
}

// Credential lifecycle event kinds persisted in revocation_events.kind.
const (
	RevocationEventKindSession          = "session"
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
	ReleaseHubRuntimeLease(ctx context.Context, holderID string) (int64, error)
	ListPublishedAfter(ctx context.Context, afterSeq int64, limit int32) ([]PublishedRevocationEvent, error)
	MaxPublishedSeq(ctx context.Context) (int64, error)
}

// DeviceAuthorizationStore manages RFC 8628 device-code grants.
type DeviceAuthorizationStore interface {
	Create(ctx context.Context, p CreateDeviceAuthorizationParams) error
	Get(ctx context.Context, deviceCode string) (*DeviceAuthorization, error)
	GetByUserCode(ctx context.Context, userCode string) (*DeviceAuthorization, error)
	Approve(ctx context.Context, p ApproveDeviceAuthorizationParams) (int64, error)
	ApproveByUserCode(ctx context.Context, p ApproveDeviceAuthorizationByUserCodeParams) (int64, error)
	Deny(ctx context.Context, deviceCode string) (int64, error)
	Consume(ctx context.Context, deviceCode string) (int64, error)
	TouchPoll(ctx context.Context, deviceCode string) error
}

// CLIAuthorizationCodeStore manages local-redirect one-shot codes.
type CLIAuthorizationCodeStore interface {
	Create(ctx context.Context, p CreateCLIAuthorizationCodeParams) error
	GetActive(ctx context.Context, code string) (*CLIAuthorizationCode, error)
	Consume(ctx context.Context, code string) (*CLIAuthorizationCode, error)
}

type WorkspaceSectionStore interface {
	Create(ctx context.Context, p CreateWorkspaceSectionParams) error
	GetByID(ctx context.Context, id string) (*WorkspaceSection, error)
	ListByUserID(ctx context.Context, userID string) ([]WorkspaceSection, error)
	Rename(ctx context.Context, p RenameWorkspaceSectionParams) (int64, error)
	UpdatePosition(ctx context.Context, p UpdateWorkspaceSectionPositionParams) error
	UpdateSidebarPosition(ctx context.Context, p UpdateWorkspaceSectionSidebarPositionParams) error
	Delete(ctx context.Context, p DeleteWorkspaceSectionParams) (int64, error)
	HasDefaultForUser(ctx context.Context, userID string) (bool, error)
}

type WorkspaceSectionItemStore interface {
	Set(ctx context.Context, p SetWorkspaceSectionItemParams) error
	Get(ctx context.Context, p GetWorkspaceSectionItemParams) (*WorkspaceSectionItem, error)
	ListByUser(ctx context.Context, userID string) ([]WorkspaceSectionItem, error)
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
	Delete(ctx context.Context, state string) error
}

type OAuthTokenStore interface {
	Upsert(ctx context.Context, p UpsertOAuthTokensParams) error
	Get(ctx context.Context, p GetOAuthTokensParams) (*OAuthToken, error)
	ListExpiring(ctx context.Context) ([]OAuthToken, error)
	ListByKeyVersion(ctx context.Context, keyVersion int64) ([]OAuthToken, error)
	CountByKeyVersion(ctx context.Context, keyVersion int64) (int64, error)
	DeleteByProvider(ctx context.Context, providerID string) error
	DeleteByUser(ctx context.Context, userID string) error
	DeleteByUserAndProvider(ctx context.Context, p DeleteOAuthTokensByUserAndProviderParams) error
}

type OAuthUserLinkStore interface {
	Create(ctx context.Context, p CreateOAuthUserLinkParams) error
	Get(ctx context.Context, p GetOAuthUserLinkParams) (*OAuthUserLink, error)
	ListByUser(ctx context.Context, userID string) ([]OAuthUserLink, error)
	Delete(ctx context.Context, p DeleteOAuthUserLinkParams) error
	DeleteByProvider(ctx context.Context, providerID string) error
}

type PendingOAuthSignupStore interface {
	Create(ctx context.Context, p CreatePendingOAuthSignupParams) error
	Get(ctx context.Context, token string) (*PendingOAuthSignup, error)
	Delete(ctx context.Context, token string) error
}

// CleanupStore provides methods for hard-deleting soft-deleted records
// and expired ephemeral data. Backends may augment these with native
// mechanisms but must implement all methods for consistent cross-backend
// behavior.
type CleanupStore interface {
	HardDeleteExpiredSessions(ctx context.Context) (int64, error)
	HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteExpiredRegistrationKeysBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// ClearStalePendingEmails wipes pending_email columns for users whose
	// pending_email_expires_at is older than cutoff. Frees up index slots
	// and ensures stale codes don't leak into future lookups.
	ClearStalePendingEmails(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteOrgsBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredOAuthStates(ctx context.Context) (int64, error)
	DeleteExpiredPendingOAuthSignups(ctx context.Context) (int64, error)
	DeleteExpiredDeviceAuthorizations(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredCLIAuthorizationCodes(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteRevokedAPITokensBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteRevokedDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredDelegationTokensBefore(ctx context.Context, cutoff time.Time) (int64, error)
	// CompactPublishedRevocationEvents removes an expired Hub runtime lease,
	// then deletes retained events only through the live Hub cursor.
	CompactPublishedRevocationEvents(ctx context.Context, p CompactRevocationEventsParams) (int64, error)
}

// TestEntity identifies a table/collection for test helper operations.
type TestEntity string

const (
	EntityOrgs                   TestEntity = "orgs"
	EntityUsers                  TestEntity = "users"
	EntitySessions               TestEntity = "user_sessions"
	EntityWorkers                TestEntity = "workers"
	EntityWorkerRegistrationKeys TestEntity = "worker_registration_keys"
	EntityWorkspaces             TestEntity = "workspaces"
)

// validEntities is the set of known TestEntity values, used by
// ValidateEntity to prevent SQL injection in test helpers.
var validEntities = map[TestEntity]bool{
	EntityOrgs:                   true,
	EntityUsers:                  true,
	EntitySessions:               true,
	EntityWorkers:                true,
	EntityWorkerRegistrationKeys: true,
	EntityWorkspaces:             true,
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

	// SetRevocationEventRevokedAt writes an exact revocation_events.revoked_at timestamp.
	SetRevocationEventRevokedAt(ctx context.Context, id string, revokedAt time.Time) error

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
