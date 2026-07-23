package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// SessionDuration is the lifetime of a user session.
const SessionDuration = 24 * time.Hour

type contextKey int

const userKey contextKey = iota

// UserInfo contains the authenticated user's information.
//
// Email and EmailVerified are loaded by ValidateToken from a JOIN on
// users; they're cached for sessionCacheTTL alongside the rest. Mutating
// either column on `users` (verify, change, admin reset) requires evicting the
// user's cached authentication contexts — see AuthContextRegistry.EvictByUserID.
// That is deliberately separate from credential revocation, which uses
// AuthContextRegistry.RevokeUserAuthContextAtGeneration.
//
// Credential.WorkspaceScopeID is set only when the request was authenticated
// by a delegation_tokens row (as opposed to a session cookie or an
// api_tokens bearer). It pins the request to the workspace the
// delegation was minted for; downstream authorization (notably
// ChannelService.OpenChannel) MUST narrow accessible-workspace
// reasoning to this single id rather than the user's full grant set,
// so a compromised delegation bearer cannot pivot beyond its scope.
//
// AuthenticatedAt is the timestamp of the stored session or bearer row that
// authenticated the request. It is retained for auditing and diagnostics;
// user-wide revocation correctness is based on UserAuthGeneration.
//
// UserAuthGeneration is the user's persisted credential epoch observed by the
// credential that authenticated this request. Long-lived channel registration
// records it so user-wide revocation events close only channels authorized by
// older credentials.
//
// AuthGeneration is the AuthContextRegistry revocation generation observed before
// validation. OpenChannel rejects a request whose session, bearer, or user
// identity was evicted after this generation, closing the race where a cache
// hit happens just before the watcher evicts and sweeps current channels.
type UserInfo struct {
	ID                  userid.UserID
	OrgID               string
	Username            string
	IsAdmin             bool
	Email               string
	EmailVerified       bool
	Credential          CredentialIdentity
	AuthenticatedAt     time.Time
	CredentialExpiresAt CredentialDeadline
	UserAuthGeneration  int64
	AuthGeneration      uint64
}

// CredentialCurrent reports whether the credential is still live at now. Nil-safe:
// a nil UserInfo is not current. The expiry semantics (NeverExpires is always
// live; At(t) requires now strictly before t) live on CredentialDeadline, the
// single source of truth shared by the auth cache and the channel service, so the
// two can never disagree at the exact expiry instant.
func (u *UserInfo) CredentialCurrent(now time.Time) bool {
	return u != nil && u.CredentialExpiresAt.IsCurrent(now)
}

// WithUser stores a UserInfo in the context.
func WithUser(ctx context.Context, u *UserInfo) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// GetUser retrieves UserInfo from the context. Returns nil if not authenticated.
func GetUser(ctx context.Context) *UserInfo {
	u, _ := ctx.Value(userKey).(*UserInfo)
	return u
}

// MustGetUser retrieves UserInfo from the context, returning an error if not
// authenticated.
func MustGetUser(ctx context.Context) (*UserInfo, error) {
	u := GetUser(ctx)
	if u == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}
	return u, nil
}

// RevokeAllUserCredentials revokes every active api_tokens and
// delegation_tokens row for userID and, via RevokeUserTokens, bumps
// users.tokens_revoked_at AND users.auth_generation, emitting the durable
// user_tokens revocation event that carries the new generation to other Hub
// processes (the backbone of cross-process teardown). Returns (apiCount,
// delegationCount) so admin handlers can report what was killed.
//
// Caller-side concerns:
//   - The bearer cache lives in the running hub process; admin CLI
//     callers don't need to evict it (the revocation watcher closes
//     the loop). In-process callers must invalidate AuthContextRegistry with
//     the committed user auth generation after this transaction commits.
//   - Pass a transaction-scoped Store to keep the multi-row update
//     atomic; admin handlers wrap this in `RunInTransaction`.
//
// Returns the first store error and leaves later steps unrun, so a
// failed api-token revoke aborts before the delegation-token revoke
// or the watermark bump.
func RevokeAllUserCredentials(ctx context.Context, st store.Store, userID userid.UserID) (int64, int64, error) {
	var apiCount, delegationCount int64
	err := st.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		var err error
		apiCount, err = tx.APITokens().RevokeByUser(ctx, userID)
		if err != nil {
			return fmt.Errorf("revoke api tokens: %w", err)
		}
		delegationCount, err = tx.DelegationTokens().RevokeByUser(ctx, userID)
		if err != nil {
			return fmt.Errorf("revoke delegation tokens: %w", err)
		}
		if _, err := tx.Users().RevokeUserTokens(ctx, userID); err != nil {
			return fmt.Errorf("revoke user tokens (bump generation): %w", err)
		}
		return nil
	})
	return apiCount, delegationCount, err
}

// LoadSoloUser looks up the bootstrapped solo user and maps it into a
// UserInfo suitable for synthetic authentication in solo mode. Returns
// store.ErrNotFound if the solo user has not been created yet.
func LoadSoloUser(ctx context.Context, st store.Store) (*UserInfo, error) {
	user, err := st.Users().GetByUsername(ctx, usernames.Solo)
	if err != nil {
		return nil, err
	}
	// A blank users.id is corrupt data, not a programmer error, so it is refused
	// rather than panicked on: MustNew's contract is "the caller already knows
	// this is non-empty", which holds for a literal but not for a column. The
	// row is rejected the same way a missing one is -- an identity we cannot
	// name cannot authenticate anything.
	id, ok := userid.New(user.ID)
	if !ok {
		return nil, fmt.Errorf("solo user row has a blank id")
	}
	return &UserInfo{
		ID:                 id,
		OrgID:              user.OrgID,
		Username:           user.Username,
		IsAdmin:            user.IsAdmin,
		AuthenticatedAt:    time.Now().UTC(),
		UserAuthGeneration: user.AuthGeneration,
	}, nil
}

// Login validates credentials and creates a new session token.
// Returns the session ID, user, session expiry time, and any error.
func Login(ctx context.Context, st store.Store, username, password string) (string, *store.User, time.Time, error) {
	var zero time.Time
	user, err := st.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, zero, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		return "", nil, zero, connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}

	// Mint once, here, from the row we just read. Everything below -- the auth
	// transaction's lock target and the session it creates -- takes the typed
	// id, so a blank users.id is refused before any of it rather than panicking
	// mid-transaction on the credential path.
	loginUID, mintOK := userid.New(user.ID)
	if !mintOK {
		return "", nil, zero, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	// Verify the password OUTSIDE the auth transaction. On the default
	// SQLite backend RunInUserAuthTransaction promotes to a write
	// transaction (LockUserAuthState), which serializes every other hub
	// write for as long as it is held; argon2 verification (~50-200ms) has
	// no need of that lock. Inside the transaction we recompute the
	// (expensive) hash only when the stored hash changed between this read
	// and the locked re-read, so a password rotated at the transaction
	// boundary is still verified against the committed hash -- preserving
	// the ordering guarantee of verifying against the locked row.
	matchPrelock, verifyErrPrelock := pwdhash.Verify(user.PasswordHash, password)
	prelockHash := user.PasswordHash

	var sessionID string
	var expiresAt time.Time
	err = st.RunInUserAuthTransaction(ctx, loginUID, func(tx store.Store) error {
		lockedUser, err := tx.Users().GetByID(ctx, user.ID)
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		if err != nil {
			return fmt.Errorf("query locked user: %w", err)
		}
		if lockedUser.Username != store.NormalizeUsername(username) {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		match, verifyErr := matchPrelock, verifyErrPrelock
		if lockedUser.PasswordHash != prelockHash {
			// The hash changed under the lock (concurrent rotation), so the
			// pre-lock result is stale: re-verify against the committed hash.
			match, verifyErr = pwdhash.Verify(lockedUser.PasswordHash, password)
		}
		if verifyErr != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("verify password: %w", verifyErr))
		}
		if !match {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		// Re-mint from the LOCKED row rather than reusing loginUID: the lock
		// re-read is the authoritative one, and this is a column, so MustNew's
		// contract ("the caller already knows this is non-empty") does not
		// hold. A panic here would tear the login connection on every retry
		// instead of failing closed, so a blank id is refused the same way a
		// bad password is.
		sessUID, sessMintOK := userid.New(lockedUser.ID)
		if !sessMintOK {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		sessionID, expiresAt, err = CreateSession(ctx, tx, sessUID)
		if err != nil {
			return err
		}
		user = lockedUser
		return nil
	})
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return "", nil, zero, connectErr
		}
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, zero, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		return "", nil, zero, connect.NewError(connect.CodeInternal, err)
	}
	return sessionID, user, expiresAt, nil
}

// SessionMeta holds optional metadata for session creation.
type SessionMeta struct {
	UserAgent string
	IPAddress string
}

// CreateSession creates a new user session and returns the session ID and
// expiry time.
func CreateSession(ctx context.Context, st store.Store, userID userid.UserID, meta ...SessionMeta) (string, time.Time, error) {
	// A session for nobody is worse than no session: it would authenticate as a
	// blank id, which every predicate then has to refuse individually. Refuse to
	// write the row at all.
	if userID.IsZero() {
		return "", time.Time{}, fmt.Errorf("create session: user id is required")
	}
	sessionID := id.Generate()
	expiresAt := time.Now().Add(SessionDuration).UTC()
	params := store.CreateSessionParams{
		ID:        sessionID,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}
	if len(meta) > 0 {
		params.UserAgent = meta[0].UserAgent
		params.IPAddress = meta[0].IPAddress
	}
	if err := st.Sessions().Create(ctx, params); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return sessionID, expiresAt, nil
}

// ValidateToken resolves a session token to a UserInfo. Returns an error if
// the token is invalid or expired. Uses a single joined query to avoid two
// sequential DB round-trips.
func ValidateToken(ctx context.Context, st store.Store, token string) (*UserInfo, error) {
	row, err := st.Sessions().ValidateWithUser(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid or expired token"))
		}
		return nil, fmt.Errorf("validate session: %w", err)
	}

	// Refuse rather than panic on a blank joined user_id. This is the highest-
	// traffic mint site (every cookie-authenticated RPC), and the row is store
	// data, so an orphaned or hand-edited sessions row must fail closed the same
	// way the not-found branch above does -- not take down the request with a
	// panic that reads to the client as a torn connection.
	id, ok := userid.New(row.UserID)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid or expired token"))
	}
	return &UserInfo{
		ID:                  id,
		Credential:          SessionCredential(token),
		OrgID:               row.OrgID,
		Username:            row.Username,
		IsAdmin:             row.IsAdmin,
		Email:               row.Email,
		EmailVerified:       row.EmailVerified,
		AuthenticatedAt:     row.CreatedAt.UTC(),
		CredentialExpiresAt: DeadlineAt(row.ExpiresAt.UTC()),
		UserAuthGeneration:  row.AuthGeneration,
	}, nil
}

// ResolveOrgID determines the effective org ID for a request. Every user
// belongs to exactly one (personal) org, so an empty requestedOrgID
// resolves to it and any other value must match it — anything else is
// NotFound, mirroring how an unknown org id read.
func ResolveOrgID(user *UserInfo, requestedOrgID string) (string, error) {
	if requestedOrgID == "" || requestedOrgID == user.OrgID {
		return user.OrgID, nil
	}
	return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("not a member of this organization"))
}
