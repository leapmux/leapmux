package store

import (
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

// NormalizeUsername returns a lowercased username for case-insensitive storage.
func NormalizeUsername(s string) string { return strings.ToLower(s) }

// NormalizeEmail returns a lowercased email for case-insensitive storage.
func NormalizeEmail(s string) string { return strings.ToLower(s) }

// FoldSearchText returns the case-folded form of a searchable field used for the
// admin user search. Folding in Go (Unicode-aware strings.ToLower) and querying a
// pre-folded stored column with a plain LIKE makes the search match case-
// insensitively -- including for non-ASCII display names -- IDENTICALLY across
// SQLite, Postgres, and MySQL. Doing it in SQL instead would diverge: SQLite's
// built-in LOWER/LIKE/COLLATE NOCASE fold only ASCII, while Postgres ILIKE and
// MySQL LOWER fold by locale/collation. The write path stores FoldSearchText of the
// display name in display_name_folded, and the query folds the search term the same
// way, so both sides share this one rule and cannot drift.
func FoldSearchText(s string) string { return strings.ToLower(s) }

// likeEscaper backslash-escapes the LIKE metacharacters so a search term
// matches literally: \ itself first, then % (match-any) and _ (match-one).
// The dialects' SearchUsers queries declare ESCAPE '\' to match.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// SearchLikePattern builds the complete LIKE prefix pattern for an optional
// admin-search term, preserving nil (which SearchUsers reads as "no filter ->
// return all rows"). The term is case-folded via FoldSearchText (so it matches
// the pre-folded display_name_folded column and the lowercased username/email
// columns consistently across every dialect), its LIKE metacharacters are
// backslash-escaped so an operator's query matches literally -- `--query '%'`
// prefix-matches a literal percent sign instead of dumping every user, and a
// literal `_` in an email (legal in the local part) matches exactly rather
// than as a single-char wildcard -- and the match-anything `%` suffix is
// appended here, so the whole pattern is built at this one site and the SQL
// binds it directly (sqlc's SQLite grammar cannot parse `LIKE x || y ESCAPE`).
// Escaping lives here -- NOT in FoldSearchText, which the write path uses to
// store display_name_folded unescaped.
func SearchLikePattern(query *string) *string {
	if query == nil {
		return nil
	}
	pattern := likeEscaper.Replace(FoldSearchText(*query)) + "%"
	return &pattern
}

// --- Domain model types (backend-agnostic) ---

// Org represents a user's personal organization.
type Org struct {
	ID        string
	Name      string
	CreatedAt time.Time
	DeletedAt *time.Time
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
	TokensRevokedAt       *time.Time
	AuthGeneration        int64
	DeletedAt             *time.Time
}

// PageCursor returns the keyset position for user listings (ListAll/Search),
// which order by (created_at DESC, id DESC).
func (u User) PageCursor() (time.Time, string) { return u.CreatedAt, u.ID }

// UserSession represents an authenticated session.
type UserSession struct {
	ID             string
	UserID         string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	LastActiveAt   time.Time
	AuthGeneration int64
	UserAgent      string
	IPAddress      string
}

// PageCursor returns the keyset position for the per-user session listing
// (ListByUserID), which orders by (last_active_at DESC, id DESC) -- not
// created_at.
func (s UserSession) PageCursor() (time.Time, string) { return s.LastActiveAt, s.ID }

// SessionWithUser is the result of ValidateSessionWithUser (JOIN).
type SessionWithUser struct {
	UserID         string
	OrgID          string
	Username       string
	IsAdmin        bool
	EmailVerified  bool
	Email          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	AuthGeneration int64
}

// ActiveSession is a session with the owning username (for admin listing).
type ActiveSession struct {
	ID     string
	UserID string
	// Username is the owner's username, or "" when the owner is soft-deleted
	// (UserDeleted true). The store returns the raw state; presentation layers
	// decide how to render a deleted owner.
	Username     string
	UserDeleted  bool
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	IPAddress    string
	UserAgent    string
}

// PageCursor returns the keyset position for the active-session listing,
// which orders by (last_active_at DESC, id DESC) -- not created_at.
func (s ActiveSession) PageCursor() (time.Time, string) { return s.LastActiveAt, s.ID }

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

// PageCursor returns the keyset position for worker listings (ListByUserID
// and, via the WorkerWithOwner embedding, ListAdmin), which order by
// (created_at DESC, id DESC).
func (w Worker) PageCursor() (time.Time, string) { return w.CreatedAt, w.ID }

// WorkerPublicKeys holds a worker's public key material.
type WorkerPublicKeys struct {
	PublicKey       []byte
	MlkemPublicKey  []byte
	SlhdsaPublicKey []byte
}

// WorkerWithOwner is the result of admin worker listing (JOIN with users).
type WorkerWithOwner struct {
	Worker
	// OwnerUsername is "" when the owner is soft-deleted (OwnerDeleted true);
	// presentation layers decide how to render a deleted owner.
	OwnerUsername string
	OwnerDeleted  bool
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

// PageCursor returns the keyset position for the admin registration-key
// listing (via the WorkerRegistrationKeyWithCreator embedding), which orders
// by (created_at DESC, id DESC).
func (k WorkerRegistrationKey) PageCursor() (time.Time, string) { return k.CreatedAt, k.ID }

// WorkerRegistrationKeyWithCreator augments WorkerRegistrationKey with the
// creator's username (LEFT JOINed on users) for the admin listing.
type WorkerRegistrationKeyWithCreator struct {
	WorkerRegistrationKey
	// CreatorUsername is "" when the creator is soft-deleted (CreatorDeleted
	// true); presentation layers decide how to render a deleted creator.
	CreatorUsername string
	CreatorDeleted  bool
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
	ID   string
	Name string
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

// Validate enforces the same "username is always a routable slug" store-level
// invariant on the CREATE path that UpdateUserProfileParams.Validate enforces on
// rename. A user's username is created in the same transaction as its personal
// org and mirrored into orgs.name (CreateUserWithOrg / bootstrap), and it lands
// in the /o/{slug} URL space -- so a store-level caller that never routes through
// the service's SanitizeSlug (an admin seed, a sync tool, a test) must not be able
// to blank or corrupt the user row and its mirrored slug together. Validates the
// EXACT value the store persists -- NormalizeUsername(p.Username), which lowercases
// but does not trim -- against SanitizeSlug, so mixed case is accepted (the store
// lowercases it) while whitespace-only, "a b", or "Bad Name!" is refused before any
// query runs. Mirrors UpdateUserProfileParams.Validate; the service create paths
// already SanitizeSlug upstream, so this is a no-op for legitimate input.
func (p CreateUserParams) Validate() error {
	stored := NormalizeUsername(p.Username)
	if cleaned, err := validate.SanitizeSlug("username", stored); err != nil || cleaned != stored {
		return ErrInvalidArgument
	}
	return nil
}

type UpdateUserProfileParams struct {
	ID          string
	Username    string
	DisplayName string
}

// Validate enforces the store-level invariants on a profile update. The username
// mirrors the personal-org name under a partial unique index (RenameUserPersonalOrg
// runs in the same transaction), and it lands in the /o/{slug} URL space -- so the
// store refuses anything the service layer (validate.SanitizeSlug) would, making
// "username is always a routable slug" a property of the store rather than a step
// each caller must repeat. It validates the EXACT value the store persists --
// NormalizeUsername(p.Username), which lowercases but does not trim -- against
// SanitizeSlug: a whitespace-only or non-slug username ("  ", "Bad Name!", "a b")
// passes a bare non-empty check yet corrupts both users.username and the mirrored
// orgs.name. Mixed case is accepted (the store lowercases it, as its
// NormalizeUsername contract promises); surrounding whitespace is not, since the
// stored value keeps it and SanitizeSlug's trimmed output would then disagree with
// what is written. The guard runs before any query so a bad input cannot partially
// apply (the user row updated, the org not).
func (p UpdateUserProfileParams) Validate() error {
	stored := NormalizeUsername(p.Username)
	if cleaned, err := validate.SanitizeSlug("username", stored); err != nil || cleaned != stored {
		return ErrInvalidArgument
	}
	return nil
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

// PageParams is the shared keyset-pagination input embedded in every list
// param struct: the opaque composite cursor (empty = first page; produced by
// EncodeCursor / maybePrintNextCursor, validated by ParseCursor) and the page
// limit (normalized via ClampListLimit; 0 = no rows). Each embedding struct
// notes which ORDER BY tiebreak column its listing pages on -- created_at for
// most listings, last_active_at for the session listings -- since the cursor
// must be encoded from that column.
type PageParams struct {
	Cursor string
	Limit  int64
}

type SearchUsersParams struct {
	Query      *string
	PageParams // Keyset on (created_at DESC, id DESC).
}

type ListAllUsersParams struct {
	PageParams // Keyset on (created_at DESC, id DESC).
}

type CreateSessionParams struct {
	ID        string
	UserID    userid.UserID
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
	UserID userid.UserID
	KeepID string
}

type RefreshSessionAuthGenerationParams struct {
	SessionID string
	UserID    userid.UserID
}

type ListAllActiveSessionsParams struct {
	PageParams // Keyset on (last_active_at DESC, id DESC).
}

// ListUserSessionsParams pages a per-user session listing (ListByUserID),
// ordered by (last_active_at DESC, id DESC).
type ListUserSessionsParams struct {
	UserID     userid.UserID
	PageParams // Keyset on (last_active_at DESC, id DESC).
}

type CreateWorkerParams struct {
	ID              string
	AuthToken       string
	RegisteredBy    userid.UserID
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
	RegisteredBy userid.UserID
}

type ListWorkersByUserIDParams struct {
	RegisteredBy userid.UserID
	PageParams   // Keyset on (created_at DESC, id DESC).
}

type GetOwnedWorkerParams struct {
	WorkerID string
	UserID   userid.UserID
}

type ListWorkersAdminParams struct {
	UserID     *string
	Status     *leapmuxv1.WorkerStatus
	PageParams // Keyset on (created_at DESC, id DESC).
}

type CreateWorkerNotificationParams struct {
	ID       string
	WorkerID string
	Type     leapmuxv1.NotificationType
	Payload  string
}

type CreateRegistrationKeyParams struct {
	ID        string
	CreatedBy userid.UserID
	ExpiresAt time.Time
}

// GetOwnedRegistrationKeyParams names the ownership gate's two halves so the
// caller id cannot be an untyped positional string, mirroring
// GetOwnedWorkerParams.
type GetOwnedRegistrationKeyParams struct {
	ID        string
	CreatedBy userid.UserID
}

type ExtendRegistrationKeyParams struct {
	ID        string
	CreatedBy userid.UserID
	ExpiresAt time.Time
}

type SoftDeleteRegistrationKeyParams struct {
	ID        string
	CreatedBy userid.UserID
}

type ListRegistrationKeysAdminParams struct {
	PageParams          // Keyset on (created_at DESC, id DESC).
	IncludeExpired bool // true to surface revoked/expired rows for forensics
}

type CreateWorkspaceParams struct {
	ID          string
	OrgID       string
	OwnerUserID userid.UserID
	Title       string
}

type ListAccessibleWorkspacesParams struct {
	UserID userid.UserID
	OrgID  string
}

type RenameWorkspaceParams struct {
	ID          string
	OwnerUserID userid.UserID
	Title       string
}

type SoftDeleteWorkspaceParams struct {
	ID          string
	OwnerUserID userid.UserID
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
	UserID  userid.UserID
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
	UserID      userid.UserID
	Name        string
	Position    string
	SectionType leapmuxv1.SectionType
	Sidebar     leapmuxv1.Sidebar
}

type RenameWorkspaceSectionParams struct {
	ID     string
	UserID userid.UserID
	Name   string
}

type UpdateWorkspaceSectionPositionParams struct {
	ID       string
	UserID   userid.UserID
	Position string
}

type UpdateWorkspaceSectionSidebarPositionParams struct {
	ID       string
	UserID   userid.UserID
	Sidebar  leapmuxv1.Sidebar
	Position string
}

type DeleteWorkspaceSectionParams struct {
	ID     string
	UserID userid.UserID
}

type SetWorkspaceSectionItemParams struct {
	UserID      userid.UserID
	WorkspaceID string
	SectionID   string
	Position    string
}

type DeleteWorkspaceSectionItemParams struct {
	UserID      userid.UserID
	WorkspaceID string
}

type GetWorkspaceSectionItemParams struct {
	UserID      userid.UserID
	WorkspaceID string
}

type IsWorkspaceInArchivedSectionParams struct {
	UserID      userid.UserID
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
	UserID       userid.UserID
	ProviderID   string
	AccessToken  []byte
	RefreshToken []byte
	TokenType    string
	ExpiresAt    time.Time
	KeyVersion   int64
}

type GetOAuthTokensParams struct {
	UserID     userid.UserID
	ProviderID string
}

type DeleteOAuthTokensByUserAndProviderParams struct {
	UserID     userid.UserID
	ProviderID string
}

type CreateOAuthUserLinkParams struct {
	UserID          userid.UserID
	ProviderID      string
	ProviderSubject string
}

type GetOAuthUserLinkParams struct {
	ProviderID      string
	ProviderSubject string
}

type DeleteOAuthUserLinkParams struct {
	UserID     userid.UserID
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
	AuthGeneration           int64
	LastUsedAt               *time.Time
	LastRotatedAt            *time.Time
	ExpiresAt                *time.Time
	RefreshExpiresAt         *time.Time
	RevokedAt                *time.Time
}

// APITokenWithOwner augments APIToken with the owner's username (LEFT JOINed
// on users) for the admin listing. A soft-deleted owner surfaces as
// OwnerUsername "" + OwnerDeleted true; presentation layers decide how to
// render a deleted owner.
type APITokenWithOwner struct {
	APIToken
	OwnerUsername string
	OwnerDeleted  bool
}

// PageCursor returns the keyset position for the admin api-token listing
// (ListAllAPITokens), which orders by (created_at DESC, id DESC).
func (t APITokenWithOwner) PageCursor() (time.Time, string) { return t.CreatedAt, t.ID }

// DelegationToken is a short-lived bearer minted by a worker so a
// spawned agent (or opt-in terminal) can act for the user against the
// hub or a sibling worker. Scope is (UserID, WorkspaceID); IssuedFor*
// fields are provenance only.
type DelegationToken struct {
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
	CreatedAt        time.Time
	AuthGeneration   int64
	LastUsedAt       *time.Time
	ExpiresAt        time.Time
	RefreshExpiresAt *time.Time
	RevokedAt        *time.Time
}

// DelegationTokenWithOwner augments DelegationToken with the owner's username
// for the admin listing. A soft-deleted owner surfaces as OwnerUsername "" +
// OwnerDeleted true; presentation layers decide how to render a deleted owner.
type DelegationTokenWithOwner struct {
	DelegationToken
	OwnerUsername string
	OwnerDeleted  bool
}

// PageCursor returns the keyset position for the admin delegation-token listing
// (ListAllDelegationTokens), which orders by (created_at DESC, id DESC).
func (t DelegationTokenWithOwner) PageCursor() (time.Time, string) { return t.CreatedAt, t.ID }

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
	UserID           userid.UserID
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
	UserID     userid.UserID
	ClientType string // empty = all
}

// ListAllAPITokensParams pages the admin api-token listing (ListAllAPITokens),
// ordered by (created_at DESC, id DESC) and LEFT JOINed with users so the owner
// username rides each row (no per-user fanout).
type ListAllAPITokensParams struct {
	UserID     *string // nil = all users; non-nil dispatches to the ByUser query twin
	ClientType string  // empty = all
	PageParams         // Keyset on (created_at DESC, id DESC).
	// IncludeRevoked adds revoked rows to the listing (forensics); the default
	// lists live tokens only and rides the partial keyset indexes.
	IncludeRevoked bool
}

// ListAllDelegationTokensParams pages the admin delegation-token listing
// (ListAllDelegationTokens), ordered by (created_at DESC, id DESC).
type ListAllDelegationTokensParams struct {
	UserID     *string // nil = all users; non-nil dispatches to the ByUser query twin
	PageParams         // Keyset on (created_at DESC, id DESC).
	// IncludeRevoked adds revoked rows to the listing (forensics); the default
	// lists live tokens only and rides the partial keyset indexes.
	IncludeRevoked bool
}

type CreateDelegationTokenParams struct {
	ID               string
	UserID           userid.UserID
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

type CreateDeviceAuthorizationParams struct {
	DeviceCode      string
	UserCode        string
	DeviceName      string
	IntervalSeconds int64
	ExpiresAt       time.Time
}

type ApproveDeviceAuthorizationParams struct {
	DeviceCode string
	UserID     userid.UserID
}

type ApproveDeviceAuthorizationByUserCodeParams struct {
	UserCode string
	UserID   userid.UserID
}

type CreateCLIAuthorizationCodeParams struct {
	Code          string
	UserID        userid.UserID
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
