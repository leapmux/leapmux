package store

import (
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// NormalizeUsername returns a lowercased username for case-insensitive storage.
func NormalizeUsername(s string) string { return strings.ToLower(s) }

// NormalizeEmail returns a lowercased email for case-insensitive storage.
func NormalizeEmail(s string) string { return strings.ToLower(s) }

// --- Domain model types (backend-agnostic) ---

// Org represents an organization (tenant).
type Org struct {
	ID         string
	Name       string
	IsPersonal bool
	CreatedAt  time.Time
	DeletedAt  *time.Time
}

// User represents a user account.
type User struct {
	ID                    string
	OrgID                 string
	Username              string
	PasswordHash          string
	DisplayName           string
	Email                 string
	EmailVerified         bool
	PendingEmail          string
	PendingEmailToken     string
	PendingEmailExpiresAt *time.Time
	PendingEmailAttempts  int64
	PasswordSet           bool
	IsAdmin               bool
	Prefs                 string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	DeletedAt             *time.Time
}

// UserSession represents an authenticated session.
type UserSession struct {
	ID           string
	UserID       string
	ExpiresAt    time.Time
	CreatedAt    time.Time
	LastActiveAt time.Time
	UserAgent    string
	IPAddress    string
}

// SessionWithUser is the result of ValidateSessionWithUser (JOIN).
type SessionWithUser struct {
	UserID        string
	OrgID         string
	Username      string
	IsAdmin       bool
	EmailVerified bool
	Email         string
}

// ActiveSession is a session with the owning username (for admin listing).
type ActiveSession struct {
	ID           string
	UserID       string
	Username     string
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	IPAddress    string
	UserAgent    string
}

// OrgMember represents an org membership.
type OrgMember struct {
	OrgID    string
	UserID   string
	Role     leapmuxv1.OrgMemberRole
	JoinedAt time.Time
}

// OrgMemberWithUser is the result of listing org members (JOIN with users).
type OrgMemberWithUser struct {
	OrgMember
	Username    string
	DisplayName string
	Email       string
}

// Worker represents a registered worker node.
type Worker struct {
	ID              string
	AuthToken       string
	RegisteredBy    string
	Status          leapmuxv1.WorkerStatus
	CreatedAt       time.Time
	LastSeenAt      *time.Time
	PublicKey       []byte
	MlkemPublicKey  []byte
	SlhdsaPublicKey []byte
	// AutoRegistered marks rows created by Server.RegisterWorker (the
	// in-process bypass for the solo launcher's co-located worker).
	// DeregisterWorker refuses these to keep users from accidentally
	// tearing down the bundled desktop worker.
	AutoRegistered bool
	DeletedAt      *time.Time
}

// WorkerPublicKeys holds a worker's public key material.
type WorkerPublicKeys struct {
	PublicKey       []byte
	MlkemPublicKey  []byte
	SlhdsaPublicKey []byte
}

// WorkerWithOwner is the result of admin worker listing (JOIN with users).
type WorkerWithOwner struct {
	Worker
	OwnerUsername string
}

// WorkerAccessGrant represents cross-user worker access.
type WorkerAccessGrant struct {
	WorkerID  string
	UserID    string
	GrantedBy string
	CreatedAt time.Time
}

// WorkerNotification represents a queued notification for a worker.
type WorkerNotification struct {
	ID          string
	WorkerID    string
	Type        leapmuxv1.NotificationType
	Payload     string
	Status      leapmuxv1.NotificationStatus
	Attempts    int64
	MaxAttempts int64
	CreatedAt   time.Time
	DeliveredAt *time.Time
}

// WorkerRegistrationKey is a short-lived bearer credential the user mints
// from the frontend to authorize a single worker registration. The worker
// presents the row's ID on WorkerConnectorService.Register and the hub
// atomically consumes the row to create the workers entry.
//
// Soft-deletion is encoded by setting ExpiresAt to a past time; the
// cleanup loop hard-deletes rows whose ExpiresAt is older than the
// retention cutoff.
type WorkerRegistrationKey struct {
	ID        string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// WorkerRegistrationKeyWithCreator augments WorkerRegistrationKey with the
// creator's username (JOINed on users). Soft-deleted creators surface as
// "(deleted)" so admin listings remain readable after a user is purged.
type WorkerRegistrationKeyWithCreator struct {
	WorkerRegistrationKey
	CreatorUsername string
}

// Workspace represents a hub-owned workspace.
type Workspace struct {
	ID          string
	OrgID       string
	OwnerUserID string
	Title       string
	IsDeleted   bool
	CreatedAt   time.Time
	DeletedAt   *time.Time
}

// WorkspaceAccess represents a read-only sharing ACL entry.
type WorkspaceAccess struct {
	WorkspaceID string
	UserID      string
	CreatedAt   time.Time
}

// WorkspaceTabRow is a row from workspace_tab_owned or
// workspace_tab_rendered. The two views have the same shape; the
// distinction is *which* table they came from. Worker reconciliation
// reads from `_owned`; UI reads from `_rendered`.
type WorkspaceTabRow struct {
	OrgID       string
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
	WorkerID    string
	TileID      string
	Position    string
}

// OrgOpBatchRow is a single row of the CRDT op-batch journal.
type OrgOpBatchRow struct {
	OrgID        string
	PhysicalMs   int64
	Logical      int64
	LastLogical  int64
	OriginClient string
	PrincipalID  string
	BatchID      string
	BodyHash     []byte
	BatchPayload []byte
	OpCount      int64
	Epoch        int64
	CommittedAt  time.Time
}

// OrgStateRow is the materialized OrgCrdtState blob.
type OrgStateRow struct {
	OrgID          string
	StatePayload   []byte
	CurrentEpoch   int64
	EpochStartedAt time.Time
	UpdatedAt      time.Time
}

// OrgRecentBatchIDRow is a dedup-table row.
type OrgRecentBatchIDRow struct {
	OrgID               string
	BatchID             string
	BodyHash            []byte
	PrincipalID         string
	CanonicalPhysicalMs int64
	CanonicalLogical    int64
	CanonicalClient     string
	OpCount             int64
	Epoch               int64
	ExpiresAt           time.Time
}

// LifecycleOutboxRow is the persisted outbox payload.
type LifecycleOutboxRow struct {
	ID         int64
	OrgID      string
	OpType     string
	Payload    []byte
	EnqueuedAt time.Time
	ConsumedAt *time.Time
}

// WorkspaceSection represents a sidebar section for a user.
type WorkspaceSection struct {
	ID          string
	UserID      string
	Name        string
	Position    string
	SectionType leapmuxv1.SectionType
	Sidebar     leapmuxv1.Sidebar
	CreatedAt   time.Time
}

// WorkspaceSectionItem represents a workspace-to-section assignment.
type WorkspaceSectionItem struct {
	UserID      string
	WorkspaceID string
	SectionID   string
	Position    string
}

// OAuthProviderSummary holds all OAuth provider fields except the encrypted secret.
type OAuthProviderSummary struct {
	ID           string
	ProviderType string
	Name         string
	IssuerURL    string
	ClientID     string
	Scopes       string
	TrustEmail   bool
	Enabled      bool
	CreatedAt    time.Time
}

// OAuthProvider extends OAuthProviderSummary with the encrypted client secret.
type OAuthProvider struct {
	OAuthProviderSummary
	ClientSecret []byte
}

// OAuthState represents a short-lived CSRF + PKCE state during auth flow.
type OAuthState struct {
	State        string
	ProviderID   string
	PkceVerifier string
	RedirectURI  string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// OAuthToken stores encrypted OAuth tokens for a user+provider pair.
type OAuthToken struct {
	UserID       string
	ProviderID   string
	AccessToken  []byte
	RefreshToken []byte
	TokenType    string
	ExpiresAt    time.Time
	KeyVersion   int64
	UpdatedAt    time.Time
}

// OAuthUserLink represents a link between a local user and an OAuth identity.
type OAuthUserLink struct {
	UserID          string
	ProviderID      string
	ProviderSubject string
	CreatedAt       time.Time
}

// PendingOAuthSignup represents a new user in the middle of OAuth signup.
type PendingOAuthSignup struct {
	Token           string
	ProviderID      string
	ProviderSubject string
	Email           string
	DisplayName     string
	AccessToken     []byte
	RefreshToken    []byte
	TokenType       string
	TokenExpiresAt  time.Time
	KeyVersion      int64
	RedirectURI     string
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

// --- Parameter types for create/update operations ---

type CreateOrgParams struct {
	ID         string
	Name       string
	IsPersonal bool
}

type UpdateOrgNameParams struct {
	ID   string
	Name string
}

type ListAllOrgsParams struct {
	Cursor string // RFC3339Nano of created_at; empty = first page
	Limit  int64
}

type SearchOrgsParams struct {
	Query  *string // prefix match on org name
	Cursor string  // RFC3339Nano of created_at; empty = first page
	Limit  int64
}

type CreateUserParams struct {
	ID            string
	OrgID         string
	Username      string
	PasswordHash  string
	DisplayName   string
	Email         string
	EmailVerified bool
	PasswordSet   bool
	IsAdmin       bool
}

type UpdateUserProfileParams struct {
	ID          string
	Username    string
	DisplayName string
}

type UpdateUserPasswordParams struct {
	ID           string
	PasswordHash string
}

type UpdateUserEmailParams struct {
	ID            string
	Email         string
	EmailVerified bool
}

type UpdateUserEmailVerifiedParams struct {
	ID            string
	EmailVerified bool
}

type UpdateUserAdminParams struct {
	ID      string
	IsAdmin bool
}

type UpdateUserPrefsParams struct {
	ID    string
	Prefs string
}

type SetPendingEmailParams struct {
	ID                    string
	PendingEmail          string
	PendingEmailToken     string
	PendingEmailExpiresAt *time.Time
}

type ClearCompetingPendingEmailsParams struct {
	PendingEmail string
	ExcludeID    string
}

type SearchUsersParams struct {
	Query  *string
	Cursor string // RFC3339Nano of created_at; empty = first page
	Limit  int64
}

type ListAllUsersParams struct {
	Cursor string // RFC3339Nano of created_at; empty = first page
	Limit  int64
}

type CreateSessionParams struct {
	ID        string
	UserID    string
	ExpiresAt time.Time
	UserAgent string
	IPAddress string
}

type TouchSessionParams struct {
	ID           string
	ExpiresAt    time.Time
	LastActiveAt time.Time
}

type DeleteOtherSessionsParams struct {
	UserID string
	KeepID string
}

type ListAllActiveSessionsParams struct {
	Cursor string
	Limit  int64
}

type CreateOrgMemberParams struct {
	OrgID  string
	UserID string
	Role   leapmuxv1.OrgMemberRole
}

type UpdateOrgMemberRoleParams struct {
	OrgID  string
	UserID string
	Role   leapmuxv1.OrgMemberRole
}

type DeleteOrgMemberParams struct {
	OrgID  string
	UserID string
}

type CountOrgMembersByRoleParams struct {
	OrgID string
	Role  leapmuxv1.OrgMemberRole
}

type IsOrgMemberParams struct {
	OrgID  string
	UserID string
}

type CreateWorkerParams struct {
	ID              string
	AuthToken       string
	RegisteredBy    string
	PublicKey       []byte
	MlkemPublicKey  []byte
	SlhdsaPublicKey []byte
	// AutoRegistered must be true only on the solo launcher's
	// in-process bypass path (Server.RegisterWorker). All
	// registration-key driven Register RPCs leave it false.
	AutoRegistered bool
}

type SetWorkerStatusParams struct {
	ID     string
	Status leapmuxv1.WorkerStatus
}

type UpdateWorkerPublicKeyParams struct {
	ID              string
	PublicKey       []byte
	MlkemPublicKey  []byte
	SlhdsaPublicKey []byte
}

type DeregisterWorkerParams struct {
	ID           string
	RegisteredBy string
}

type ListWorkersByUserIDParams struct {
	RegisteredBy string
	Cursor       string // RFC3339Nano of created_at; empty = first page
	Limit        int64
}

type ListOwnedWorkersParams struct {
	UserID string
	Cursor string // RFC3339Nano of created_at; empty = first page
	Limit  int64
}

type GetOwnedWorkerParams struct {
	WorkerID string
	UserID   string
}

type ListWorkersAdminParams struct {
	UserID *string
	Status *leapmuxv1.WorkerStatus
	Cursor string // RFC3339Nano of created_at; empty = first page
	Limit  int64
}

type CreateWorkerNotificationParams struct {
	ID       string
	WorkerID string
	Type     leapmuxv1.NotificationType
	Payload  string
}

type GrantWorkerAccessParams struct {
	WorkerID  string
	UserID    string
	GrantedBy string
}

type RevokeWorkerAccessParams struct {
	WorkerID string
	UserID   string
}

type HasWorkerAccessParams struct {
	WorkerID string
	UserID   string
}

type DeleteWorkerAccessGrantsByUserInOrgParams struct {
	UserID string
	OrgID  string
}

type CreateRegistrationKeyParams struct {
	ID        string
	CreatedBy string
	ExpiresAt time.Time
}

type ExtendRegistrationKeyParams struct {
	ID        string
	CreatedBy string
	ExpiresAt time.Time
}

type SoftDeleteRegistrationKeyParams struct {
	ID        string
	CreatedBy string
}

type ListRegistrationKeysAdminParams struct {
	Cursor         string // RFC3339Nano of created_at; empty = first page
	Limit          int64
	IncludeExpired bool // true to surface revoked/expired rows for forensics
}

type CreateWorkspaceParams struct {
	ID          string
	OrgID       string
	OwnerUserID string
	Title       string
}

type ListAccessibleWorkspacesParams struct {
	UserID string
	OrgID  string
}

type RenameWorkspaceParams struct {
	ID          string
	OwnerUserID string
	Title       string
}

type SoftDeleteWorkspaceParams struct {
	ID          string
	OwnerUserID string
}

type GrantWorkspaceAccessParams struct {
	WorkspaceID string
	UserID      string
}

type RevokeWorkspaceAccessParams struct {
	WorkspaceID string
	UserID      string
}

type HasWorkspaceAccessParams struct {
	WorkspaceID string
	UserID      string
}

// UpsertOwnedTabParams / UpsertRenderedTabParams target the two
// derived tab-index views maintained by the CRDT manager. Both views
// carry identical column sets — alias rather than two parallel structs
// so the bulk-upsert helpers can take either type without an extra
// copy pass.
type UpsertOwnedTabParams struct {
	OrgID       string
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
	WorkerID    string
	TileID      string
	Position    string
}

// UpsertRenderedTabParams is an alias of UpsertOwnedTabParams; the two
// derived views share the same column set, so callers that build the
// "rendered" slice from already-typed "owned" data can pass it through
// directly.
type UpsertRenderedTabParams = UpsertOwnedTabParams

type GetRenderedTabParams struct {
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
}

// GetOwnedTabParams identifies a single owned-tab row.
// (workspace_id, tab_id) is sufficient: workspace_tab_owned is keyed
// on (org_id, tab_id), workspace_id is determined by org_id, and
// tab_id is globally unique within an org (the CRDT mints fresh ids
// per tab), so the pair lookups one row at most.
type GetOwnedTabParams struct {
	WorkspaceID string
	TabID       string
}

// TabIndexKey identifies a single row in workspace_tab_owned or
// workspace_tab_rendered for bulk-delete by (org_id, tab_id).
type TabIndexKey struct {
	OrgID string
	TabID string
}

// LocateAccessibleRenderedTabParams identifies a rendered tab without
// pre-scoping by workspace; the impl applies the user's accessibility
// filter so the lookup is safe across orgs.
type LocateAccessibleRenderedTabParams struct {
	UserID  string
	TabType leapmuxv1.TabType
	TabID   string
}

// InsertOrgOpBatchParams writes a single row to org_op_batches.
type InsertOrgOpBatchParams struct {
	OrgID        string
	PhysicalMs   int64
	Logical      int64
	LastLogical  int64
	OriginClient string
	PrincipalID  string
	BatchID      string
	BodyHash     []byte
	BatchPayload []byte
	OpCount      int64
	Epoch        int64
}

type ListOrgOpBatchesAfterParams struct {
	OrgID             string
	AfterPhysicalMs   int64
	AfterLogical      int64
	AfterOriginClient string
	// Limit caps the per-call row count so a far-behind subscriber
	// cannot OOM the broadcaster. Use CRDTBatchPageLimit for the
	// default page size.
	Limit int32
}

type DeleteOrgOpBatchesThroughParams struct {
	OrgID               string
	ThroughPhysicalMs   int64
	ThroughLogical      int64
	ThroughOriginClient string
}

// UpsertOrgStateParams writes a fresh state blob.
type UpsertOrgStateParams struct {
	OrgID          string
	StatePayload   []byte
	CurrentEpoch   int64
	EpochStartedAt time.Time
	UpdatedAt      time.Time
}

type AdvanceOrgEpochParams struct {
	OrgID          string
	Epoch          int64
	EpochStartedAt time.Time
	UpdatedAt      time.Time
}

type InsertOrgRecentBatchIDParams struct {
	OrgID               string
	BatchID             string
	BodyHash            []byte
	PrincipalID         string
	CanonicalPhysicalMs int64
	CanonicalLogical    int64
	CanonicalClient     string
	OpCount             int64
	Epoch               int64
	ExpiresAt           time.Time
}

type InsertLifecycleOutboxParams struct {
	OrgID   string
	OpType  string
	Payload []byte
}

type MarkLifecycleOutboxConsumedParams struct {
	ID         int64
	ConsumedAt time.Time
}

type CreateWorkspaceSectionParams struct {
	ID          string
	UserID      string
	Name        string
	Position    string
	SectionType leapmuxv1.SectionType
	Sidebar     leapmuxv1.Sidebar
}

type RenameWorkspaceSectionParams struct {
	ID     string
	UserID string
	Name   string
}

type UpdateWorkspaceSectionPositionParams struct {
	ID       string
	UserID   string
	Position string
}

type UpdateWorkspaceSectionSidebarPositionParams struct {
	ID       string
	UserID   string
	Sidebar  leapmuxv1.Sidebar
	Position string
}

type DeleteWorkspaceSectionParams struct {
	ID     string
	UserID string
}

type SetWorkspaceSectionItemParams struct {
	UserID      string
	WorkspaceID string
	SectionID   string
	Position    string
}

type DeleteWorkspaceSectionItemParams struct {
	UserID      string
	WorkspaceID string
}

type GetWorkspaceSectionItemParams struct {
	UserID      string
	WorkspaceID string
}

type IsWorkspaceInArchivedSectionParams struct {
	UserID      string
	WorkspaceID string
}

type CreateOAuthProviderParams struct {
	ID           string
	ProviderType string
	Name         string
	IssuerURL    string
	ClientID     string
	ClientSecret []byte
	Scopes       string
	TrustEmail   bool
	Enabled      bool
}

type UpdateOAuthProviderEnabledParams struct {
	ID      string
	Enabled bool
}

type CreateOAuthStateParams struct {
	State        string
	ProviderID   string
	PkceVerifier string
	RedirectURI  string
	ExpiresAt    time.Time
}

type UpsertOAuthTokensParams struct {
	UserID       string
	ProviderID   string
	AccessToken  []byte
	RefreshToken []byte
	TokenType    string
	ExpiresAt    time.Time
	KeyVersion   int64
}

type GetOAuthTokensParams struct {
	UserID     string
	ProviderID string
}

type DeleteOAuthTokensByUserAndProviderParams struct {
	UserID     string
	ProviderID string
}

type CreateOAuthUserLinkParams struct {
	UserID          string
	ProviderID      string
	ProviderSubject string
}

type GetOAuthUserLinkParams struct {
	ProviderID      string
	ProviderSubject string
}

type DeleteOAuthUserLinkParams struct {
	UserID     string
	ProviderID string
}

// --- API token types ---

// APIToken is a durable bearer credential issued to leapmux remote CLI
// (and future external clients). The exposed bearer is composed in code
// as "lmx_<id>_<secret>"; SecretHash stores HMAC-SHA256(secret, server
// pepper) so leaks of the snapshot alone don't allow forgery.
type APIToken struct {
	ID                       string
	UserID                   string
	ClientType               string
	ClientName               string
	SecretHash               []byte
	RefreshHash              []byte
	PreviousRefreshHash      []byte
	PreviousRefreshExpiresAt *time.Time
	Scope                    string
	CreatedAt                time.Time
	LastUsedAt               *time.Time
	LastRotatedAt            *time.Time
	ExpiresAt                *time.Time
	RefreshExpiresAt         *time.Time
	RevokedAt                *time.Time
}

// DelegationToken is a short-lived bearer minted by a worker so a
// spawned agent (or opt-in terminal) can act for the user against the
// hub or a sibling worker. Scope is (UserID, WorkspaceID); IssuedFor*
// fields are provenance only.
type DelegationToken struct {
	ID                       string
	UserID                   string
	WorkerID                 string
	WorkspaceID              string
	AgentID                  string
	TerminalID               string
	IssuedForTabID           string
	IssuedForTabType         int32
	SecretHash               []byte
	RefreshHash              []byte
	PreviousRefreshHash      []byte
	PreviousRefreshExpiresAt *time.Time
	CreatedAt                time.Time
	LastUsedAt               *time.Time
	ExpiresAt                time.Time
	RefreshExpiresAt         *time.Time
	RevokedAt                *time.Time
}

// DeviceAuthorization is an in-flight RFC 8628 device-code grant.
type DeviceAuthorization struct {
	DeviceCode      string
	UserCode        string
	DeviceName      string
	UserID          string
	Approved        int64 // 0 pending, 1 approved, 2 denied
	LastPolledAt    *time.Time
	IntervalSeconds int64
	CreatedAt       time.Time
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
}

// CLIAuthorizationCode is a one-shot OAuth-style code for the CLI's
// local-redirect login flow.
type CLIAuthorizationCode struct {
	Code          string
	UserID        string
	CodeChallenge string
	DeviceName    string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	ConsumedAt    *time.Time
}

type CreateAPITokenParams struct {
	ID               string
	UserID           string
	ClientType       string
	ClientName       string
	SecretHash       []byte
	RefreshHash      []byte
	Scope            string
	ExpiresAt        *time.Time
	RefreshExpiresAt *time.Time
}

type RotateAPITokenRefreshParams struct {
	ID                       string
	NewSecretHash            []byte
	NewExpiresAt             *time.Time
	NewRefreshHash           []byte
	NewRefreshExpiresAt      *time.Time
	PreviousRefreshHash      []byte
	PreviousRefreshExpiresAt *time.Time
}

type ListAPITokensByUserParams struct {
	UserID     string
	ClientType string // empty = all
}

type CreateDelegationTokenParams struct {
	ID               string
	UserID           string
	WorkerID         string
	WorkspaceID      string
	AgentID          string
	TerminalID       string
	IssuedForTabID   string
	IssuedForTabType int32
	SecretHash       []byte
	RefreshHash      []byte
	ExpiresAt        time.Time
	RefreshExpiresAt *time.Time
}

type RotateDelegationTokenRefreshParams struct {
	ID                       string
	NewRefreshHash           []byte
	NewRefreshExpiresAt      *time.Time
	PreviousRefreshHash      []byte
	PreviousRefreshExpiresAt *time.Time
}

type CreateDeviceAuthorizationParams struct {
	DeviceCode      string
	UserCode        string
	DeviceName      string
	IntervalSeconds int64
	ExpiresAt       time.Time
}

type ApproveDeviceAuthorizationParams struct {
	DeviceCode string
	UserID     string
}

type ApproveDeviceAuthorizationByUserCodeParams struct {
	UserCode string
	UserID   string
}

type CreateCLIAuthorizationCodeParams struct {
	Code          string
	UserID        string
	CodeChallenge string
	DeviceName    string
	ExpiresAt     time.Time
}

type CreatePendingOAuthSignupParams struct {
	Token           string
	ProviderID      string
	ProviderSubject string
	Email           string
	DisplayName     string
	AccessToken     []byte
	RefreshToken    []byte
	TokenType       string
	TokenExpiresAt  time.Time
	KeyVersion      int64
	RedirectURI     string
	ExpiresAt       time.Time
}
