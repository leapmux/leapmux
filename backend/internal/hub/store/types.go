package store

import (
	"errors"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/password"
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

// User represents a user account.
type User struct {
	ID                         string
	Username                   string
	PasswordHash               string
	DisplayName                string
	Email                      string
	EmailVerified              bool
	PendingEmail               string
	PendingEmailToken          string
	PendingEmailExpiresAt      *time.Time
	PendingEmailUnblockedAt    *time.Time
	PendingEmailAttempts       int64
	PendingRecoveryToken       string
	PendingRecoveryExpiresAt   *time.Time
	PendingRecoveryUnblockedAt *time.Time
	PendingRecoveryAttempts    int64
	// FirstCredentialExempt is a CLAIM the creating flow makes, not a fact about
	// the stored hash: the solo bootstrap sets it true with an EMPTY hash, which
	// is what routes solo past a first-credential rule no solo account could
	// satisfy. Ask HasUsablePassword for "can this account sign in with a
	// password?" -- the two answer differently on exactly that account.
	FirstCredentialExempt bool
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

// HasUsablePassword reports whether a password can actually sign this account
// in, which is a question about the stored HASH and not about
// FirstCredentialExempt.
//
// One spelling for one fact. It is what every projection reports as
// `password_set` on the wire, and what the solo gate asks; the exempt flag
// answers a different question and reads differently on the bootstrap account.
func (u User) HasUsablePassword() bool { return password.IsUsable(u.PasswordHash) }

// UserSession represents an authenticated session.
//
// ElevationProvenAt and ElevationExpiresAt carry the session's step-up ("sudo mode")
// state: the non-sliding instant a factor was proven, and the sliding
// deadline. Both nil means the session was never elevated. See
// auth.Elevation, which is the one place the pair is interpreted.
type UserSession struct {
	ID                 string
	UserID             string
	ExpiresAt          time.Time
	CreatedAt          time.Time
	LastActiveAt       time.Time
	AuthGeneration     int64
	ElevationProvenAt  *time.Time
	ElevationExpiresAt *time.Time
	UserAgent          string
	IPAddress          string
}

// PageCursor returns the keyset position for the per-user session listing
// (ListByUserID), which orders by (last_active_at DESC, id DESC) -- not
// created_at.
func (s UserSession) PageCursor() (time.Time, string) { return s.LastActiveAt, s.ID }

// SessionWithUser is the result of ValidateSessionWithUser (JOIN).
//
// ElevationProvenAt / ElevationExpiresAt ride the hot auth path so a request can decide
// step-up admission without a second query. See UserSession for the pair's
// meaning.
type SessionWithUser struct {
	UserID             string
	Username           string
	IsAdmin            bool
	EmailVerified      bool
	Email              string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	AuthGeneration     int64
	ElevationProvenAt  *time.Time
	ElevationExpiresAt *time.Time
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
// SoftDelete encodes the deletion by setting ExpiresAt to a past time; the
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
	UserID      string
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
	WorkerID    string
	TileID      string
	Position    string
}

// UserOpBatchRow is a single row of the CRDT op-batch journal.
type UserOpBatchRow struct {
	UserID       string
	PhysicalMs   int64
	Logical      int64
	LastLogical  int64
	OriginClient string
	PrincipalID  string
	BatchID      string
	BodyHash     []byte
	BatchPayload []byte
	// TransitionsPayload is the proto-marshalled BatchTransitions the resume
	// path replays as visibility-transition frames (see crdt.ResumeBatch).
	TransitionsPayload []byte
	OpCount            int64
	Epoch              int64
	CommittedAt        time.Time
}

// UserStateRow is the materialized UserCrdtState blob.
type UserStateRow struct {
	UserID         string
	StatePayload   []byte
	CurrentEpoch   int64
	EpochStartedAt time.Time
	UpdatedAt      time.Time
}

// UserRecentBatchIDRow is a dedup-table row.
type UserRecentBatchIDRow struct {
	UserID              string
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
	UserID     string
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

// SettingRow is one hub_settings row: a setting key's stored halves.
// Value is the public JSON document; Secret is the keystore-encrypted
// JSON secret half. Either half may be nil (stored NULL), never both.
type SettingRow struct {
	Key       string
	Value     *string
	Secret    []byte
	UpdatedAt time.Time
}

// UpsertSettingParams rewrites one setting key's row. A nil half clears
// that half; the settings.Manager merges with the existing row first so
// only an intentional clear passes nil.
type UpsertSettingParams struct {
	Key    string
	Value  *string
	Secret []byte
}

// OAuthState represents a short-lived CSRF + PKCE state during auth flow.
type OAuthState struct {
	State        string
	ProviderID   string
	PkceVerifier string
	// NonceHash is the SHA-256 hex of the nonce held in the browser's
	// short-lived OAuth cookie. It binds this flow to the browser that
	// started it, so an attacker cannot complete a (code, state) pair they
	// captured in somebody else's browser. Empty means the hub minted no
	// browser cookie for this row.
	NonceHash   string
	RedirectURI string
	// Purpose is OAuthStatePurposeLogin or OAuthStatePurposeReauth. The
	// callback branches on it: a reauth row elevates SessionID and must never
	// create a session or link an identity.
	Purpose string
	// SessionID is the session a reauth row elevates. Empty for a login row.
	SessionID string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// OAuth state purposes. The value is persisted, so the strings are the wire
// form and must stay stable.
const (
	// OAuthStatePurposeLogin starts a sign-in and may create a session.
	OAuthStatePurposeLogin = "login"
	// OAuthStatePurposeReauth proves the identity again for a session that is
	// ALREADY signed in, to elevate it.
	OAuthStatePurposeReauth = "reauth"
)

// PasskeyCredential stores one WebAuthn credential for a user. PublicKey is
// keystore-encrypted at the service layer before persistence.
type PasskeyCredential struct {
	ID             string
	UserID         string
	CredentialID   []byte
	PublicKey      []byte
	SignCount      int64
	AAGUID         []byte
	BackupEligible bool
	BackupState    bool
	Transports     string
	FriendlyName   string
	KeyVersion     int64
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

// WebAuthnSession holds ephemeral ceremony state. SessionData is
// keystore-encrypted at the service layer before persistence.
type WebAuthnSession struct {
	ID          string
	Kind        string
	UserID      string
	PayloadJSON string
	SessionData []byte
	ExpiresAt   time.Time
	CreatedAt   time.Time
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
	Token      string
	ProviderID string
	// NonceHash is the SHA-256 hex of the nonce in the browser's
	// pending-signup cookie. It carries the OAuth flow's browser binding
	// ACROSS the hand-off from the callback to CompleteOAuthSignup, which
	// would otherwise create the account for whoever presents the token.
	// Empty means the hub minted no browser cookie for this row.
	NonceHash       string
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

type CreateUserParams struct {
	ID                    string
	Username              string
	PasswordHash          string
	DisplayName           string
	Email                 string
	EmailVerified         bool
	FirstCredentialExempt bool
	IsAdmin               bool
}

// Validate enforces two store-level invariants on the CREATE path.
//
// The id must be one an ownership predicate could later bind. users.id is the
// parent key every owner-keyed child row hangs off, and SQLite accepts "" as a
// TEXT primary key -- so a blank-id user is what makes a blank-OWNER tab or
// CRDT row satisfy `user_id REFERENCES users(id)` and become storable. No
// predicate can then identify that row: binding "" matches every blank-owner row
// rather than none, which is the fail-open userid.OwnerFilter exists to refuse.
// Checking through userid.New rather than a local `p.ID == ""` is what keeps
// the two ends from drifting -- the id create accepts is by construction
// exactly the id a predicate can bind.
//
// This closes the store API as a route to that shape; it does not make the
// shape unrepresentable in the database, which still permits a blank TEXT key
// through raw SQL. The bind guards stay load-bearing for that reason.
//
// The username must be a routable slug -- see validateUsernameSlug, which
// create and rename share so the rule cannot drift between them.
func (p CreateUserParams) Validate() error {
	if _, ok := userid.New(p.ID); !ok {
		return ErrInvalidArgument
	}
	return validateUsernameSlug(p.Username)
}

// validateUsernameSlug enforces "a stored username is always a routable slug",
// the one rule CreateUserParams.Validate and UpdateUserProfileParams.Validate
// both apply. A store-level caller that never routes through the service's
// SanitizeSlug (an admin seed, a sync tool, a test) must not be able to blank
// or corrupt users.username, and a value that is creatable but not renameable
// (or the reverse) would be a bug in itself -- so the two share one body rather
// than two independently-worded copies of it.
//
// Validates the EXACT value the store persists -- NormalizeUsername(username),
// which lowercases but does not trim -- against SanitizeSlug. Mixed case is
// therefore accepted (the store lowercases it, as NormalizeUsername's contract
// promises), while whitespace-only, "a b", or "Bad Name!" is refused before any
// query runs. Surrounding whitespace is refused too: the stored value would
// keep it, and SanitizeSlug's trimmed output would then disagree with the
// value the store persists.
func validateUsernameSlug(username string) error {
	stored := NormalizeUsername(username)
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

// Validate enforces the store-level invariants on a profile update: the store
// refuses anything the service layer would, making "username is always a
// routable slug" a property of the store rather than a step each caller must
// repeat. Shares validateUsernameSlug with create.
func (p UpdateUserProfileParams) Validate() error {
	return validateUsernameSlug(p.Username)
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
	// PendingEmailUnblockedAt is the blockade this mint itself arms: now
	// plus the resend cooldown, so the row's gate stays closed for one
	// cooldown after the code lands. Required, not a pointer, and the
	// store adapters refuse the zero time with
	// ErrPendingUnblockedAtRequired: a forgotten deadline would store a
	// live token the gate can never cool down, and a zero time binds as
	// year 1 -- eternally open -- which fails the gate OPEN, so the value
	// must be a real deadline.
	PendingEmailUnblockedAt time.Time
	// Now is the instant the gate compares UnblockedAt against; the mint
	// derives it from the same clock read as the deadline, so the two
	// cannot disagree. Its twin is SetPendingRecoveryParams.Now.
	Now time.Time
}

// ErrPendingUnblockedAtRequired reports a mint whose blockade deadline is
// the zero time. The gate compares the blocked-until column, and a zero
// time binds as year 1 -- eternally open -- so a live token minted under
// it never cools down and the gate fails open. The required time.Time
// field removed the nil case; the adapters' guard
// (ValidatePendingUnblockedAt) removes the zero case, so the store
// boundary refuses both.
var ErrPendingUnblockedAtRequired = errors.New("pending blocked-until is required: the mint gate reads it, and a zero deadline disables the cooldown")

// ValidatePendingUnblockedAt enforces ErrPendingUnblockedAtRequired for
// every dialect's SetPendingEmail and SetPendingRecovery.
func ValidatePendingUnblockedAt(blockedUntil time.Time) error {
	if blockedUntil.IsZero() {
		return ErrPendingUnblockedAtRequired
	}
	return nil
}

// UnblockedAtPtr maps a clear's deadline onto the nullable bind the SQL
// statements take: the zero time means "no blockade" and binds NULL, which
// is the rotation teardown's shape; any other instant binds itself.
func UnblockedAtPtr(blockedUntil time.Time) *time.Time {
	if blockedUntil.IsZero() {
		return nil
	}
	return &blockedUntil
}

type ClearCompetingPendingEmailsParams struct {
	PendingEmail string
	ExcludeID    string
}

// ClearPendingEmailCodeParams clears an undelivered code while keeping the
// pending address. UnblockedAt is the deadline this clear leaves: the
// failure window a refused send arms (now plus the failure cooldown), so
// the retry a failed send invites waits out that window instead of landing
// at request speed. The zero time writes NULL -- no blockade at all.
type ClearPendingEmailCodeParams struct {
	ID          string
	UnblockedAt time.Time
}

// ClearPendingRecoveryParams clears an uncompleted recovery link;
// UnblockedAt carries the same meaning as
// ClearPendingEmailCodeParams.UnblockedAt. The rotation teardowns pass the
// zero time: the credential the link rode just died, so the account's next
// request must mint freely.
type ClearPendingRecoveryParams struct {
	ID          string
	UnblockedAt time.Time
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

// ElevationMaxTotal caps the total life of ONE elevation, measured from the
// instant the factor was proven rather than from the last slide.
//
// The cap is what stops a sliding window from becoming a permanent privilege:
// without it a user -- or a stolen cookie that acts for them -- who performs
// one sensitive action every two hours stays elevated for ever. Eight hours is
// a working day, so a genuine all-day session reaches the ceiling and nothing
// shorter does.
//
// The constant lives in the STORE because the store is what enforces it. The
// auth package holds the rest of the elevation policy (auth.ElevationWindow)
// and cannot hold this one: auth imports the store, so the dependency runs one
// way only. auth.ElevationMaxTotal must therefore be an ALIAS of this constant
// rather than a second literal, or the two drift apart in silence.
//
// Both writers bind this constant and nothing else. Elevate clamps the granted
// deadline through ClampedExpiresAt below, and SlideElevation binds it into
// the SQL clamp. No parameter struct carries a ceiling, so no call site can
// widen one, and none can pass 0 and turn the clamp into a no-op.
const ElevationMaxTotal = 8 * time.Hour

// clampElevationExpiry returns the deadline an elevation GRANT may write: the
// requested deadline, or provenAt + ElevationMaxTotal when the request reaches
// past that ceiling.
//
// The grant clamps in Go where the slide clamps in SQL, and the difference is
// structural. The slide measures the ceiling from the STORED anchor, which Go
// never reads, so only the statement can apply it. The grant writes the anchor
// itself, so both instants are already in hand here and one Go expression
// serves all three dialects. Three SQL expressions would each carry a
// dialect's own datetime arithmetic, and two of the three also lose the sqlc
// parameter type that makes a raw time.Time bind a compile error: sqlite drops
// the deadline to interface{} inside min(), and mysql infers two incompatible
// types for the anchor because DATE_ADD reads it a second time.
func clampElevationExpiry(provenAt, requested time.Time) time.Time {
	ceiling := provenAt.Add(ElevationMaxTotal)
	if requested.After(ceiling) {
		return ceiling
	}
	return requested
}

// ElevateSessionParams grants a session a fresh elevation window. Both
// instants come from the caller's clock, so a test that moves that clock
// moves the whole window with it.
//
// ElevationExpiresAt is the deadline the caller WANTS. Read it through
// ClampedExpiresAt, never directly: the field holds the request, and the
// method holds what the store agrees to write.
type ElevateSessionParams struct {
	SessionID          string
	UserID             userid.UserID
	ElevationProvenAt  time.Time
	ElevationExpiresAt time.Time
}

// ClampedExpiresAt returns the deadline this grant writes: the requested one,
// capped at ElevationProvenAt + ElevationMaxTotal. Every dialect binds this
// and not the raw field.
func (p ElevateSessionParams) ClampedExpiresAt() time.Time {
	return clampElevationExpiry(p.ElevationProvenAt, p.ElevationExpiresAt)
}

// SlideSessionElevationParams extends a live elevation. WindowDeadline is the
// deadline the caller WANTS; the statement clamps it against the session's
// stored elevation_proven_at plus ElevationMaxTotal, which the store binds
// itself. There is no ceiling field, so a caller cannot extend an elevation
// past the cap however it is called.
type SlideSessionElevationParams struct {
	SessionID      string
	UserID         userid.UserID
	WindowDeadline time.Time
}

// DropSessionElevationParams ends a session's elevation immediately.
type DropSessionElevationParams struct {
	SessionID string
	UserID    userid.UserID
}

// The api_tokens half of the same three shapes. A command-line credential
// elevates exactly as a session does -- see APITokenStore.Elevate for why it
// must be able to at all -- so the parameters differ only in which row they
// identify.

// ElevateAPITokenParams grants a command-line credential a fresh elevation
// window. Both instants come from the caller's clock. ElevationExpiresAt is
// the deadline the caller WANTS; read it through ClampedExpiresAt.
type ElevateAPITokenParams struct {
	TokenID            string
	UserID             userid.UserID
	ElevationProvenAt  time.Time
	ElevationExpiresAt time.Time
}

// ClampedExpiresAt returns the deadline this grant writes: the requested one,
// capped at ElevationProvenAt + ElevationMaxTotal. Every dialect binds this
// and not the raw field.
func (p ElevateAPITokenParams) ClampedExpiresAt() time.Time {
	return clampElevationExpiry(p.ElevationProvenAt, p.ElevationExpiresAt)
}

// SlideAPITokenElevationParams extends a live elevation. WindowDeadline is the
// deadline the caller WANTS; the statement clamps it against the row's stored
// elevation_proven_at plus ElevationMaxTotal, which the store binds itself.
type SlideAPITokenElevationParams struct {
	TokenID        string
	UserID         userid.UserID
	WindowDeadline time.Time
}

// DropAPITokenElevationParams ends a command-line credential's elevation
// immediately.
type DropAPITokenElevationParams struct {
	TokenID string
	UserID  userid.UserID
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

// GetOwnedRegistrationKeyParams gives a name to each half of the ownership
// gate, so the caller id cannot be an untyped positional string, mirroring
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
	OwnerUserID userid.UserID
	Title       string
}

type ListAccessibleWorkspacesParams struct {
	UserID userid.UserID
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
//
// UserID is userid.UserID like every other owner field on a *Params struct in
// this file: the value is minted once at the store call (hub/service's
// crdtJournal.CommitBatch mints the committing tenant and hands it to
// txTabIndexWriter), not per row inside the bulk loop. The row STRUCTS above
// (WorkspaceTabRow, UserOpBatchRow, ...) stay string-keyed -- their user_id is
// a column read back out of the database, and typing it would force a mint at
// the read boundary on data the process never vouched for.
type UpsertOwnedTabParams struct {
	UserID      userid.UserID
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

// GetRenderedTabParams identifies a single rendered-tab row.
//
// UserID is the tenancy axis and is REQUIRED, for the reason spelled out on
// GetOwnedTabParams: workspace_tab_rendered has the same (user_id, tab_id) key
// and the same plain workspace_id FK, so any user's row may refer to any existing
// workspace and tab ids are unique only within one user. Verifying that the
// CALLER owns the workspace does not establish that the ROW does.
type GetRenderedTabParams struct {
	UserID      userid.UserID
	WorkspaceID string
	TabType     leapmuxv1.TabType
	TabID       string
}

// ListRenderedTabsByWorkspaceIDsParams scopes the rendered-tab listing to one
// owner across a set of workspaces. See GetRenderedTabParams for why the
// workspace set alone is not a tenancy scope.
type ListRenderedTabsByWorkspaceIDsParams struct {
	UserID       userid.UserID
	WorkspaceIDs []string
}

// GetOwnedTabParams identifies a single owned-tab row by its PRIMARY KEY.
//
// UserID is the tenancy axis and is REQUIRED: workspace_tab_owned is keyed on
// (user_id, tab_id), and tab ids are client-minted and unique only within one
// user (a FILE tab id is `file-<millis>-<counter>` from a per-module-load
// counter, so two clients collide readily). An owner-blind :one on tab_id
// alone returns whichever tenant's row the index visited first.
//
// There is no workspace predicate: the caller that needs this -- the
// delegation mint -- asks "does this worker host this tab for this
// user?", which the (user, tab, worker) triple answers on its own. The row's
// workspace_id was never the tenancy axis and pinning it added nothing the
// key does not already give.
type GetOwnedTabParams struct {
	UserID userid.UserID
	TabID  string
}

// ListOwnedTabsByWorkerParams scopes the worker-reconciliation snapshot to one
// worker AND one owner. See GetOwnedTabParams for why worker_id alone is not a
// tenancy scope: nothing in the schema ties a row's user_id to the registrant
// of the worker it identifies.
type ListOwnedTabsByWorkerParams struct {
	UserID   userid.UserID
	WorkerID string
}

// ListOwnedTabsByWorkspaceParams scopes the worker fan-out of a workspace
// deletion to one owner. A workspace's owner does not constrain the user_id of
// rows written against that workspace_id, so the predicate has to be in the
// query rather than applied by the caller.
type ListOwnedTabsByWorkspaceParams struct {
	UserID      userid.UserID
	WorkspaceID string
}

// OwnedTabRef is one tab a workspace holds, as the two facts a teardown needs:
// which machine hosts it, and how to address it there.
//
// Deliberately narrower than WorkspaceTabRow -- the delete path has no use for
// tile or position, and returning them would invite a caller to act on layout
// state read from inside a delete transaction.
type OwnedTabRef struct {
	WorkerID string
	TabType  leapmuxv1.TabType
	TabID    string
}

// TabIndexKey identifies a single row in workspace_tab_owned or
// workspace_tab_rendered for bulk-delete by (user_id, tab_id).
//
// A zero UserID is representable (Go cannot forbid userid.UserID{}) and is how
// an unminted CRDT key arrives here; FilterTabIndexKeys drops those rather than
// binding them, so one unusable key never cancels its neighbours' deletes.
type TabIndexKey struct {
	UserID userid.UserID
	TabID  string
}

// LocateAccessibleRenderedTabParams identifies a rendered tab without
// pre-scoping by workspace; the impl applies the user's accessibility
// filter so the lookup is safe across users.
type LocateAccessibleRenderedTabParams struct {
	UserID  userid.UserID
	TabType leapmuxv1.TabType
	TabID   string
}

// InsertUserOpBatchParams writes a single row to user_op_batches.
type InsertUserOpBatchParams struct {
	UserID       userid.UserID
	PhysicalMs   int64
	Logical      int64
	LastLogical  int64
	OriginClient string
	PrincipalID  string
	BatchID      string
	BodyHash     []byte
	BatchPayload []byte
	// TransitionsPayload is the proto-marshalled BatchTransitions (per-entity
	// {pre,post} workspace) the resume path replays as visibility-transition
	// frames.
	TransitionsPayload []byte
	OpCount            int64
	Epoch              int64
}

type ListUserOpBatchesAfterParams struct {
	UserID            userid.UserID
	AfterPhysicalMs   int64
	AfterLogical      int64
	AfterOriginClient string
	// Limit caps the per-call row count so a far-behind subscriber
	// cannot OOM the broadcaster. Use CRDTBatchPageLimit for the
	// default page size.
	Limit int32
}

type DeleteUserOpBatchesThroughParams struct {
	UserID              userid.UserID
	ThroughPhysicalMs   int64
	ThroughLogical      int64
	ThroughOriginClient string
}

// UpsertUserStateParams writes a fresh state blob.
type UpsertUserStateParams struct {
	UserID       userid.UserID
	StatePayload []byte
	// CompactionPhysicalMs is StatePayload's own compaction_watermark.physical,
	// projected into a column so SQL can filter on it. Callers MUST derive it
	// from the very state they marshal into StatePayload -- it is the limit
	// the cross-user retention sweep trusts to decide which op batches are
	// already absorbed, and a value that outran the blob would license deleting
	// ops no Bootstrap could then replay.
	CompactionPhysicalMs int64
	CurrentEpoch         int64
	EpochStartedAt       time.Time
	UpdatedAt            time.Time
}

type AdvanceUserEpochParams struct {
	UserID         userid.UserID
	Epoch          int64
	EpochStartedAt time.Time
	UpdatedAt      time.Time
}

type InsertUserRecentBatchIDParams struct {
	UserID              userid.UserID
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
	UserID  userid.UserID
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

// ConsumeAltchaSaltParams records a solved ALTCHA challenge's salt as
// used until its expiry, so single-use enforcement survives restarts and
// holds across hub instances sharing the database.
type ConsumeAltchaSaltParams struct {
	Salt      string
	ExpiresAt time.Time
}

type CreateOAuthStateParams struct {
	State        string
	ProviderID   string
	PkceVerifier string
	NonceHash    string
	RedirectURI  string
	Purpose      string
	SessionID    string
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

type CreatePasskeyCredentialParams struct {
	ID             string
	UserID         string
	CredentialID   []byte
	PublicKey      []byte
	SignCount      int64
	AAGUID         []byte
	BackupEligible bool
	BackupState    bool
	Transports     string
	FriendlyName   string
	KeyVersion     int64
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

type UpdatePasskeySignCountParams struct {
	CredentialID []byte
	UserID       string
	SignCount    int64
	LastUsedAt   time.Time
}

type UpdatePasskeyPublicKeyParams struct {
	ID         string
	UserID     string
	PublicKey  []byte
	KeyVersion int64
}

type CreateWebAuthnSessionParams struct {
	ID          string
	Kind        string
	UserID      string
	PayloadJSON string
	SessionData []byte
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

type SetPendingRecoveryParams struct {
	ID                       string
	PendingRecoveryToken     string
	PendingRecoveryExpiresAt time.Time
	// PendingRecoveryUnblockedAt is the blockade this mint arms: now plus
	// the resend cooldown. Required, not a pointer, for the reason
	// SetPendingEmailParams states.
	PendingRecoveryUnblockedAt time.Time
	// Now is the instant the gate compares UnblockedAt against.
	Now time.Time
}

type CompleteRecoveryParams struct {
	ID                    string
	PasswordHash          string
	FirstCredentialExempt bool
	PendingRecoveryToken  string
}

// RecoveryRevocation is returned by CompleteRecovery and carries the auth
// generation the completion committed, so the caller's post-commit
// lifecycle eviction targets exactly the epoch this transaction produced.
type RecoveryRevocation struct {
	AuthGeneration int64
}

// --- API token types ---

// APIToken is a durable bearer credential issued to leapmux control CLI
// (and future external clients). The exposed bearer is composed in code
// as "lmx_<id>_<secret>"; SecretHash stores HMAC-SHA256(secret, server
// pepper) so leaks of the snapshot alone don't allow forgery.
type APIToken struct {
	ID     string
	UserID string
	// ClientID is WHICH APP holds this credential, and InstallationName is
	// which copy of that app ("trustin's MacBook"). The two used to be one
	// column (client_name), which fused an identity the hub registered with a
	// label the caller asserted about itself.
	ClientID                 string
	InstallationName         string
	SecretHash               []byte
	RefreshHash              []byte
	PreviousRefreshHash      []byte
	PreviousRefreshExpiresAt *time.Time
	CreatedAt                time.Time
	AuthGeneration           int64
	LastUsedAt               *time.Time
	LastRotatedAt            *time.Time
	ExpiresAt                *time.Time
	RefreshExpiresAt         *time.Time
	RevokedAt                *time.Time
	// GrantedScopes is the canonical RFC 6749 section 3.3 scope string the
	// consent granted. Read it through authscope.Parse, which refuses an
	// unknown token outright rather than dropping it -- a grant that quietly
	// lost a member would keep working as a narrower app and nobody would
	// notice the vocabulary drifted.
	GrantedScopes string
	// The step-up window, the same pair a session row carries. Nil means the
	// credential never proved a factor, and the two are written and cleared
	// together -- read them through auth.NewElevation, which refuses half a
	// pair.
	ElevationProvenAt  *time.Time
	ElevationExpiresAt *time.Time

	// The four fields below come from the oauth_clients JOIN, which is total:
	// client_id is NOT NULL with a foreign key, so every credential names a
	// registered app.
	//
	// Two of them are CEILINGS the registration holds over this credential,
	// and both are read at every validation rather than only at the mint --
	// so an owner's edit takes effect on the next request instead of on the
	// next mint. See loadBearer.

	// ClientName is the app's display name, so a listing can group by app
	// rather than by installation.
	ClientName string
	// ClientScopes is the app's REGISTERED permission ceiling, as the
	// canonical RFC 6749 section 3.3 string. Validation narrows the stored
	// grant to it, so removing a permission from a registration takes it from
	// every credential the app already holds.
	ClientScopes string
	// ClientElevationAllowed is the app's permission to run the step-up
	// ceremony.
	// Validation re-reads it, so turning it off closes every live window on
	// the next request rather than at the next write.
	ClientElevationAllowed bool
	// ClientRevokedAt is the APP's retirement, not this credential's. It is
	// joined so a hub that died part-way through a disconnect cannot leave a
	// live credential on a retired app.
	ClientRevokedAt *time.Time
	// ClientVerifiedAt and ClientRegistrationSource answer whether somebody
	// vouched for the app, through store.ClientIsVerified. The connected-app
	// list labels every row with the answer, exactly as the consent screen
	// does; only the per-user listing joins them.
	ClientVerifiedAt         *time.Time
	ClientRegistrationSource string
}

// PageCursor returns the keyset position for the per-user api-token listing
// (ListAPITokensByUser), which orders by (created_at DESC, id DESC).
func (t APIToken) PageCursor() (time.Time, string) { return t.CreatedAt, t.ID }

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
	AgentID          string
	TerminalID       string
	IssuedForTabID   string
	IssuedForTabType int32
	// GrantedScopes is what the minting worker delegated, already narrowed to
	// auth.CeilingFor(BearerKindDelegation) at the mint -- and narrowed again
	// when the row is READ, so a hand-edited row cannot widen it.
	GrantedScopes    string
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
	// ClientID is the app that started the flow.
	ClientID string
	// RequestedScopes is what the APP asked for, written at creation.
	// GrantedScopes is what the APPROVAL bound, written by the browser stage
	// and read back by the token stage. The two are separate columns because the
	// request and the consent happen on different machines: taken from the
	// activation form instead, whoever types the code could widen the ask.
	RequestedScopes string
	GrantedScopes   string
	CreatedAt       time.Time
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
	// ElevateTokenID identifies the command-line credential this grant ELEVATES,
	// when it elevates one rather than minting one. Empty for a login.
	//
	// The two flows share the row, and with it the TTL, the poll throttle,
	// the expiry sweep and the activation page -- they differ only in what
	// the approval DOES. A second table is a second copy of every one of
	// those rules.
	ElevateTokenID string
}

// OAuthClient is one registered app: everything that may ask an account for
// access, including the two LeapMux ships with.
//
// The VISIBILITY rule is one column. A non-NULL OwnerUserID makes the app that
// user's private one -- visible and authorizable only to them -- and a NULL one
// makes it hub-wide. No second flag exists to disagree with it.
//
// The CLIENT TYPE is one column too. A non-nil SecretHash is a confidential
// client and a nil one is public, so "a public client with a secret" is not a
// state this type can hold.
type OAuthClient struct {
	ClientID    string
	OwnerUserID string // empty = hub-wide
	CreatedBy   string
	// SecretHash is nil for a PUBLIC client. It is never returned to a caller;
	// the secret itself crosses once, at registration.
	SecretHash []byte
	ClientName string
	// HasIcon reports whether an icon exists, served from
	// /oauth/apps/<client_id>/icon same-origin, so the consent page's img-src
	// stays 'self'. A remote logo URL would be a beacon that reports to the
	// app operator when the consent page rendered and from which IP, and its
	// bytes would be chosen by the registrant.
	//
	// The BYTES themselves live behind OAuthClientStore.GetIcon, because the
	// full-row reads (the token endpoint, every consent page, every device
	// poll) need only this boolean, and the column holds up to 64 KiB.
	HasIcon   bool
	ClientURI string
	// RedirectURIs is the newline-delimited exact-match list, and Scopes and
	// GrantTypes are space-delimited. The service layer owns the one spelling
	// of each split: ParseRedirectURIs for the redirect list, authscope.Parse
	// for scopes, and appAllowsGrantType over strings.Fields for grant types --
	// read them through those rather than splitting by hand at a new site.
	RedirectURIs string
	Scopes       string
	GrantTypes   string
	// ElevationAllowed is whether this app may run the step-up ceremony. It is
	// a trust tier and NOT a scope: the elevation window is orthogonal to the
	// scope set and MULTIPLIES it, so no grant field could express it and mean
	// anything.
	ElevationAllowed bool
	// RegistrationSource is one of builtin, admin, user, dynamic. The CHECK
	// constraint is the closed set.
	RegistrationSource string
	// VerifiedAt and VerifiedBy move together (a CHECK enforces it). Nil is the
	// unverified marker the consent page renders.
	VerifiedAt *time.Time
	VerifiedBy string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	RevokedAt  *time.Time
}

// PageCursor returns the keyset position for the app listings, which order by
// (created_at DESC, client_id DESC).
func (c OAuthClient) PageCursor() (time.Time, string) { return c.CreatedAt, c.ClientID }

// IsConfidential reports whether this app holds a secret. See SecretHash: the
// presence of the hash IS the client type, so the two can never disagree.
func (c OAuthClient) IsConfidential() bool { return len(c.SecretHash) > 0 }

// IsHubWide reports whether every account can see and authorize this app.
func (c OAuthClient) IsHubWide() bool { return c.OwnerUserID == "" }

// IsVerified reports whether an administrator vouched for this app.
//
// A BUILT-IN registration is verified by construction: it ships with the
// build, so its author is known. ClientIsVerified states the rule once, for
// the JOIN-carrying readers that do not hold this struct.
func (c OAuthClient) IsVerified() bool {
	return ClientIsVerified(c.RegistrationSource, c.VerifiedAt)
}

// ClientIsVerified is the one rule for "did somebody vouch for this app": an
// administrator's timestamp, or a source the build itself stands behind.
//
// It exists as a function because two kinds of reader ask -- the Go struct
// above, and the columns a JOIN carries onto an api_tokens listing -- and a
// second spelling of "builtin means verified" is exactly the drift a fifth
// surface would inherit.
func ClientIsVerified(registrationSource string, verifiedAt *time.Time) bool {
	return verifiedAt != nil || registrationSource == OAuthClientSourceBuiltin
}

// Registration sources, the closed set the CHECK constraint enforces.
const (
	// OAuthClientSourceBuiltin is an app this build ships with. Its fields are
	// constants of the build, so the surface refuses to edit, revoke or delete
	// one -- see internal/hub/oauthapp.
	OAuthClientSourceBuiltin = "builtin"
	// OAuthClientSourceAdmin is an app an administrator registered. It is
	// hub-wide.
	OAuthClientSourceAdmin = "admin"
	// OAuthClientSourceUser is an app one user registered for themself.
	OAuthClientSourceUser = "user"
	// OAuthClientSourceDynamic is an app that self-registered through RFC 7591,
	// which an administrator must turn on. It is hub-wide and unverified.
	OAuthClientSourceDynamic = "dynamic"
)

// CreateOAuthClientParams registers one app.
type CreateOAuthClientParams struct {
	ClientID string
	// OwnerUserID empty registers a HUB-WIDE app. Only an administrator or
	// dynamic registration may leave it empty; the calling service binds it.
	OwnerUserID        string
	CreatedBy          string
	SecretHash         []byte
	ClientName         string
	IconBlob           []byte
	IconMediaType      string
	ClientURI          string
	RedirectURIs       string
	Scopes             string
	GrantTypes         string
	ElevationAllowed   bool
	RegistrationSource string
	VerifiedAt         *time.Time
	VerifiedBy         string
}

// UpsertBuiltInClientParams seeds or reconciles one built-in registration.
//
// The fields it carries are exactly the columns that are constants of the
// build; the upsert's conflict branch rewrites only these, so an operator's
// elevation decision, a vouch, a revocation and the row's own dates are never
// the seed's to touch. See SeedBuiltIns.
type UpsertBuiltInClientParams struct {
	ClientID           string
	ClientName         string
	ClientURI          string
	RedirectURIs       string
	Scopes             string
	GrantTypes         string
	ElevationAllowed   bool
	RegistrationSource string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// UpdateOAuthClientParams rewrites the editable half of a registration.
//
// CallerUserID and CallerIsAdmin are part of the STATEMENT, not a check the
// caller runs first: a read-then-write pair would leave a window in which the
// row changes hands between the two.
type UpdateOAuthClientParams struct {
	ClientID         string
	ClientName       string
	ClientURI        string
	RedirectURIs     string
	Scopes           string
	GrantTypes       string
	ElevationAllowed bool
	CallerUserID     userid.UserID
	CallerIsAdmin    bool
}

// ApplyTo writes the editable columns onto a row, so a response projected
// from the params a handler just wrote states the same field list the write
// did. The projection and the SQL SET list cannot drift apart when a column
// is added: adding it to this type makes both carry it.
func (p UpdateOAuthClientParams) ApplyTo(row *OAuthClient) {
	row.ClientName = p.ClientName
	row.ClientURI = p.ClientURI
	row.RedirectURIs = p.RedirectURIs
	row.Scopes = p.Scopes
	row.GrantTypes = p.GrantTypes
	row.ElevationAllowed = p.ElevationAllowed
}

// SetOAuthClientElevationAllowedParams toggles the one field the app list
// changes inline, and the one field a BUILT-IN registration may still change.
type SetOAuthClientElevationAllowedParams struct {
	ClientID         string
	ElevationAllowed bool
	CallerUserID     userid.UserID
	CallerIsAdmin    bool
}

// SetOAuthClientIconParams replaces the stored icon. A nil blob clears it, and
// the consent page then renders a monogram, which fetches nothing.
type SetOAuthClientIconParams struct {
	ClientID      string
	IconBlob      []byte
	IconMediaType string
	CallerUserID  userid.UserID
	CallerIsAdmin bool
}

// SetOAuthClientVerifiedParams records or withdraws an administrator's vouch.
// Both fields move together, so the half-vouch CHECK can never be violated.
type SetOAuthClientVerifiedParams struct {
	ClientID   string
	VerifiedAt *time.Time
	VerifiedBy string
	// CallerIsAdmin carries the vouch's authorization into the statement,
	// beside every other write's caller bind on this table.
	CallerIsAdmin bool
}

// OAuthClientOwnershipParams addresses one app for a caller-authorized write
// (revoke, delete). See UpdateOAuthClientParams on why the caller travels into
// the statement.
type OAuthClientOwnershipParams struct {
	ClientID      string
	CallerUserID  userid.UserID
	CallerIsAdmin bool
}

// ListOAuthClientsParams pages an app listing, keyset on
// (created_at DESC, client_id DESC).
type ListOAuthClientsParams struct {
	// UserID selects whose apps. It is required for every listing shape.
	UserID userid.UserID
	// IncludeHubWide widens the page with the hub-wide catalogue, which is
	// what an administrator's listing reads; the default keeps the page to
	// the user's own registrations.
	IncludeHubWide bool
	// HubWideOnly narrows the page to the hub-wide catalogue alone,
	// dropping the user's own registrations. It is the administration
	// panel's listing: an administrator's own rows must not ride along only
	// for the caller to discard them. The caller owns the administrator
	// check -- the store does not repeat it.
	HubWideOnly bool
	// IncludeRevoked widens the page to retired rows, which the "include
	// retired" listing asks for. The default keeps the live-only shape every
	// authorize surface reads.
	IncludeRevoked bool
	PageParams
}

// OAuthClientIcon is what the icon asset endpoint serves: the bytes, their
// media type, and the three facts that gate serving them. It is a separate
// read because the full-row queries carry only whether an icon exists -- the
// bytes would put 64 KiB on every token exchange otherwise.
type OAuthClientIcon struct {
	IconBlob           []byte
	IconMediaType      string
	VerifiedAt         *time.Time
	RegistrationSource string
	RevokedAt          *time.Time
}

// APITokenRef names one credential an app holds. The caller reads these BEFORE
// a cascade so it can apply each row's lifecycle effects after the transaction
// commits -- lifecycle effects ACCUMULATE, so they must not run inside a
// transaction the store may retry.
type APITokenRef struct {
	ID     string
	UserID string
	// GrantedScopes is what this credential was consented, as the canonical
	// RFC 6749 section 3.3 string.
	//
	// It is here for the caller that NARROWS the app's registered ceiling
	// rather than retiring the app: only the credentials that actually held
	// the removed permission need their channels torn down, and closing every
	// one of a hub-wide app's channels would be an outage for accounts whose
	// grant never reached it. A caller that revokes the row outright ignores
	// this field.
	GrantedScopes string
}

// OAuthAuthorizationCode is a one-shot RFC 6749 section 4.1 code.
type OAuthAuthorizationCode struct {
	Code   string
	UserID string
	// ClientID is the app the code was issued TO. The token stage refuses a
	// code presented by a different one (RFC 6749 section 4.1.3).
	ClientID      string
	CodeChallenge string
	// RedirectURI is the address the authorization used. The token stage
	// compares it, so a code intercepted at one registered redirect cannot be
	// redeemed as though it came from another.
	RedirectURI string
	// GrantedScopes is what the user consented to. The token exchange reads it
	// from HERE and never from its own form, so holding the code does not let
	// a caller widen the grant.
	GrantedScopes    string
	InstallationName string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	ConsumedAt       *time.Time
	// MintedTokenID is the credential this code produced, stamped at
	// consumption. A REPLAY revokes it, which is what RFC 6749 section 4.1.2
	// requires and what nothing could express while the column was absent.
	MintedTokenID string
}

type CreateAPITokenParams struct {
	ID     string
	UserID userid.UserID
	// ClientID must name a registered app; the foreign key refuses anything
	// else. There is no NULL: "an administrator issued this out of band" is
	// an ANSWER to which app holds the credential, and
	// oauthapp.ServiceAccountClientID is that answer.
	ClientID         string
	InstallationName string
	// GrantedScopes is the canonical scope string. The column has no default,
	// so a caller that omits it fails the INSERT rather than storing a silent
	// empty grant. Build it with authscope.ScopeSet.Storable, which refuses
	// the unscoped grant -- no stored credential carries one.
	GrantedScopes    string
	SecretHash       []byte
	RefreshHash      []byte
	ExpiresAt        *time.Time
	RefreshExpiresAt *time.Time
}

type RotateAPITokenRefreshParams struct {
	ID string
	// NewGrantedScopes rides the rotation because RFC 6749 section 6 lets a
	// refresh NARROW its grant, and a narrowing must land atomically with the
	// new secrets: written separately, a hub that died between the two would
	// leave the old grant on a credential whose owner had just given part of
	// it up. A refresh that narrows nothing binds the current value.
	NewGrantedScopes         string
	NewSecretHash            []byte
	NewExpiresAt             *time.Time
	NewRefreshHash           []byte
	NewRefreshExpiresAt      *time.Time
	PreviousRefreshHash      []byte
	PreviousRefreshExpiresAt *time.Time
}

// ListAPITokensByUserParams pages one user's OWN live tokens (the account
// settings device list), ordered by (created_at DESC, id DESC).
type ListAPITokensByUserParams struct {
	UserID userid.UserID
	// ClientID narrows the listing to ONE app; empty lists every app.
	ClientID   string
	PageParams // Keyset on (created_at DESC, id DESC).
}

// RevokeOwnedAPITokenParams revokes one of the caller's own tokens. The owner
// is part of the statement, not a check the caller runs first, so a token id
// belonging to somebody else matches no row.
type RevokeOwnedAPITokenParams struct {
	ID     string
	UserID userid.UserID
}

// RevokeOtherAPITokensParams revokes every live command-line credential one
// account holds EXCEPT KeepID. It is the twin of DeleteOtherSessionsParams,
// and the two carry the same field names for that reason: a password change
// keeps the credential that asked for it, whichever kind that credential is.
//
// An EMPTY KeepID keeps nothing and revokes the whole set. Every
// administrator path binds it that way; see APITokenStore.RevokeByUser, which
// is the name for that intent.
type RevokeOtherAPITokensParams struct {
	UserID userid.UserID
	KeepID string
}

// RefreshAPITokenAuthGenerationParams moves one kept command-line credential
// onto the account's current auth_generation. The twin of
// RefreshSessionAuthGenerationParams.
type RefreshAPITokenAuthGenerationParams struct {
	TokenID string
	UserID  userid.UserID
}

// ListAllAPITokensParams pages the admin api-token listing (ListAllAPITokens),
// ordered by (created_at DESC, id DESC) and LEFT JOINed with users so the owner
// username rides each row (no per-user fanout).
type ListAllAPITokensParams struct {
	UserID     *string // nil = all users; non-nil dispatches to the ByUser query twin
	ClientID   string  // empty = all apps
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
	AgentID          string
	TerminalID       string
	IssuedForTabID   string
	IssuedForTabType int32
	// GrantedScopes is the canonical scope string; see DelegationToken. The
	// column has no default, so an unstated grant fails the INSERT.
	GrantedScopes    string
	SecretHash       []byte
	RefreshHash      []byte
	ExpiresAt        time.Time
	RefreshExpiresAt *time.Time
}

type CreateDeviceAuthorizationParams struct {
	DeviceCode      string
	UserCode        string
	DeviceName      string
	ClientID        string
	RequestedScopes string
	IntervalSeconds int64
	ExpiresAt       time.Time
	// ElevateTokenID makes this an ELEVATION grant for that api_tokens row
	// rather than a login. Empty mints a credential, as it always did.
	ElevateTokenID string
}

type ApproveDeviceAuthorizationParams struct {
	DeviceCode string
	UserID     userid.UserID
	// GrantedScopes is what the browser consented to, canonical form.
	GrantedScopes string
}

type ApproveDeviceAuthorizationByUserCodeParams struct {
	UserCode string
	UserID   userid.UserID
	// GrantedScopes is what the browser consented to, canonical form.
	GrantedScopes string
}

type CreateOAuthAuthorizationCodeParams struct {
	Code             string
	UserID           userid.UserID
	ClientID         string
	CodeChallenge    string
	RedirectURI      string
	GrantedScopes    string
	InstallationName string
	ExpiresAt        time.Time
}

type CreatePendingOAuthSignupParams struct {
	Token           string
	ProviderID      string
	NonceHash       string
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
