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
	Registrations() RegistrationStore
	Workspaces() WorkspaceStore
	WorkspaceAccess() WorkspaceAccessStore
	WorkspaceTabs() WorkspaceTabStore
	WorkspaceLayouts() WorkspaceLayoutStore
	WorkspaceSections() WorkspaceSectionStore
	WorkspaceSectionItems() WorkspaceSectionItemStore
	OAuthProviders() OAuthProviderStore
	OAuthStates() OAuthStateStore
	OAuthTokens() OAuthTokenStore
	OAuthUserLinks() OAuthUserLinkStore
	PendingOAuthSignups() PendingOAuthSignupStore
	Cleanup() CleanupStore

	// Migrator returns the schema migration manager for this backend.
	Migrator() Migrator

	// RunInTransaction executes fn within a transaction. The provided
	// Store is bound to the transaction.
	RunInTransaction(ctx context.Context, fn func(tx Store) error) error

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
	ExistsByUsername(ctx context.Context, username string) (bool, error)
	ExistsByEmail(ctx context.Context, email, excludeUserID string) (bool, error)
	GetByPendingEmailToken(ctx context.Context, token string) (*User, error)
	GetPrefs(ctx context.Context, id string) (string, error)
	HasAny(ctx context.Context) (bool, error)
	Count(ctx context.Context) (int64, error)
	ListByOrgID(ctx context.Context, orgID string) ([]User, error)
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
}

type SessionStore interface {
	Create(ctx context.Context, p CreateSessionParams) error
	GetByID(ctx context.Context, id string) (*UserSession, error)
	Touch(ctx context.Context, p TouchSessionParams) error
	Delete(ctx context.Context, id string) (int64, error)
	DeleteByUser(ctx context.Context, userID string) error
	DeleteOthers(ctx context.Context, p DeleteOtherSessionsParams) error
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

type RegistrationStore interface {
	Create(ctx context.Context, p CreateRegistrationParams) error
	GetByID(ctx context.Context, id string) (*WorkerRegistration, error)
	Approve(ctx context.Context, p ApproveRegistrationParams) error
	ExpirePending(ctx context.Context) error
}

type WorkspaceStore interface {
	Create(ctx context.Context, p CreateWorkspaceParams) error
	GetByID(ctx context.Context, id string) (*Workspace, error)
	GetByIDIncludeDeleted(ctx context.Context, id string) (*Workspace, error)
	ListAccessible(ctx context.Context, p ListAccessibleWorkspacesParams) ([]Workspace, error)
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
	Clear(ctx context.Context, workspaceID string) error
}

type WorkspaceTabStore interface {
	Upsert(ctx context.Context, p UpsertWorkspaceTabParams) error
	BulkUpsert(ctx context.Context, params []UpsertWorkspaceTabParams) error
	Delete(ctx context.Context, p DeleteWorkspaceTabParams) error
	DeleteByWorker(ctx context.Context, workerID string) error
	DeleteByWorkspace(ctx context.Context, workspaceID string) error
	DeleteWorkerTabsForWorkspace(ctx context.Context, p DeleteWorkerTabsForWorkspaceParams) error
	ListByWorkspace(ctx context.Context, workspaceID string) ([]WorkspaceTab, error)
	ListByWorker(ctx context.Context, workerID string) ([]WorkspaceTab, error)
	ListDistinctWorkersByWorkspace(ctx context.Context, workspaceID string) ([]string, error)
	GetMaxPosition(ctx context.Context, workspaceID string) (string, error)
}

type WorkspaceLayoutStore interface {
	Get(ctx context.Context, workspaceID string) (*WorkspaceLayout, error)
	Upsert(ctx context.Context, p UpsertWorkspaceLayoutParams) error
	Delete(ctx context.Context, workspaceID string) error
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
	MoveToSection(ctx context.Context, p MoveWorkspaceSectionItemsToSectionParams) error
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
	HardDeleteExpiredRegistrationsBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error)
	HardDeleteOrgsBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredOAuthStates(ctx context.Context) (int64, error)
	DeleteExpiredPendingOAuthSignups(ctx context.Context) (int64, error)
}

// TestEntity identifies a table/collection for test helper operations.
type TestEntity string

const (
	EntityOrgs                TestEntity = "orgs"
	EntityUsers               TestEntity = "users"
	EntitySessions            TestEntity = "user_sessions"
	EntityWorkers             TestEntity = "workers"
	EntityWorkerRegistrations TestEntity = "worker_registrations"
	EntityWorkspaces          TestEntity = "workspaces"
)

// validEntities is the set of known TestEntity values, used by
// ValidateEntity to prevent SQL injection in test helpers.
var validEntities = map[TestEntity]bool{
	EntityOrgs:                true,
	EntityUsers:               true,
	EntitySessions:            true,
	EntityWorkers:             true,
	EntityWorkerRegistrations: true,
	EntityWorkspaces:          true,
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
