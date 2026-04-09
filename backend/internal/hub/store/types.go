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
	DeletedAt       *time.Time
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

// WorkerRegistration represents a pending worker registration request.
type WorkerRegistration struct {
	ID              string
	Version         string
	PublicKey       []byte
	MlkemPublicKey  []byte
	SlhdsaPublicKey []byte
	Status          leapmuxv1.RegistrationStatus
	WorkerID        *string
	ApprovedBy      *string
	ExpiresAt       time.Time
	CreatedAt       time.Time
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

// WorkspaceTab represents a tab reference within a workspace.
type WorkspaceTab struct {
	WorkspaceID string
	WorkerID    string
	TabType     leapmuxv1.TabType
	TabID       string
	Position    string
	TileID      string
}

// WorkspaceLayout stores the JSON tiling layout for a workspace.
type WorkspaceLayout struct {
	WorkspaceID string
	LayoutJSON  string
	UpdatedAt   time.Time
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

type CreateRegistrationParams struct {
	ID              string
	Version         string
	PublicKey       []byte
	MlkemPublicKey  []byte
	SlhdsaPublicKey []byte
	ExpiresAt       time.Time
}

type ApproveRegistrationParams struct {
	ID         string
	WorkerID   *string
	ApprovedBy *string
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

type UpsertWorkspaceTabParams struct {
	WorkspaceID string
	WorkerID    string
	TabType     leapmuxv1.TabType
	TabID       string
	Position    string
	TileID      string
}

type DeleteWorkspaceTabParams struct {
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
}

type DeleteWorkerTabsForWorkspaceParams struct {
	WorkerID    string
	WorkspaceID string
}

type UpsertWorkspaceLayoutParams struct {
	WorkspaceID string
	LayoutJSON  string
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

type MoveWorkspaceSectionItemsToSectionParams struct {
	FromSectionID string
	ToSectionID   string
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
