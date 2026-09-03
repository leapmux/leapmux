package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/authscope"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// DefaultSessionDuration is the minimum time a session stays valid after the
// user's last request, when the operator configures no other value. The
// session_duration flag registers it, so the flag default and this one cannot
// drift.
//
// CreateSession sets the first expiry through sessionExpiry, and each later
// authenticated request slides that expiry forward (see touchSession in
// interceptor.go). A session therefore ends one lifetime after the last
// request, not one lifetime after the login.
const DefaultSessionDuration = 7 * 24 * time.Hour

// MinSessionDuration is the shortest session the Hub can honestly enforce.
//
// Two mechanisms keep a session alive past its written expiry, and the floor
// caps what they cost together:
//
//   - The Hub serves a validated session from an in-memory cache for
//     sessionCacheTTL, so a request succeeds up to that long after the row
//     expired. That window is a constant, so it is the term that decides the
//     floor.
//   - A slide is throttled, so the row can carry up to one throttle window more
//     than the configured duration. touchThreshold keeps that window at a
//     tenth of the session, so it scales instead of dominating.
//
// At touchSlideDivisor times the cache TTL, the two together are at most a
// fifth of the configured duration. Below that, "expired" stops describing when
// the user is signed out. ValidateSessionDuration rejects a shorter
// configuration rather than accept a policy the Hub cannot keep.
const MinSessionDuration = touchSlideDivisor * sessionCacheTTL

// ValidateSessionDuration reports whether an operator's session lifetime is one
// the Hub can enforce. The rule lives here, next to the two constants that set
// the floor, so a change to either cannot leave the check behind. The caller
// adds the name of the setting.
//
// There is no upper limit: a long session is a policy an operator may hold, and
// the slide makes it an idle timeout rather than an unlimited credential.
func ValidateSessionDuration(d time.Duration) error {
	switch {
	case d < 0:
		return fmt.Errorf("must not be negative, got %s", d)
	case d < MinSessionDuration:
		return fmt.Errorf(
			"must be at least %s, got %s: the Hub serves a validated session from "+
				"an in-memory cache for %s, so a shorter session stays usable for a "+
				"large share of its own life",
			MinSessionDuration, d, sessionCacheTTL)
	}
	return nil
}

// sessionLifetime resolves a configured session lifetime. Zero or less falls
// back to DefaultSessionDuration. One home for that fallback, so the first
// expiry that CreateSession writes and the slide that the interceptor writes
// cannot disagree about how long an unconfigured session lives.
func sessionLifetime(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultSessionDuration
	}
	return configured
}

// touchSlideDivisor sets how much of a session's life may pass before the Hub
// slides it again. A tenth keeps the DB write rate low on a long session and
// still slides a short one often enough that the configured duration describes
// it.
const touchSlideDivisor = 10

// touchThreshold returns how long the Hub waits before it slides a session
// again. It is a tenth of the session, and never more than
// sessionTouchThreshold, which caps the write rate for a long session.
//
// The fixed ceiling alone was wrong for a short session: at a five-minute
// throttle a session shorter than five minutes expired before its first slide
// could land, so it ended at the login deadline however active the user was --
// the absolute timeout that the slide exists to remove.
func touchThreshold(lifetime time.Duration) time.Duration {
	return min(sessionTouchThreshold, sessionLifetime(lifetime)/touchSlideDivisor)
}

// sessionExpiry returns the expiry to write for a session whose lifetime is
// lifetime. Both writers use it -- CreateSession at the login and touchSession
// at each slide -- so the row always carries the same promise.
//
// The extra touchThreshold is what keeps that promise. A request inside the
// throttle window writes no row, so an expiry of exactly now + lifetime would
// end the session up to one throttle window before the lifetime passed since
// the last request.
func sessionExpiry(now time.Time, lifetime time.Duration) time.Time {
	return now.Add(sessionLifetime(lifetime) + touchThreshold(lifetime)).UTC()
}

type contextKey int

const userKey contextKey = iota

// UserInfo contains the authenticated user's information.
//
// ValidateToken loads Email and EmailVerified from a JOIN on users; the
// cache holds them for sessionCacheTTL alongside the rest. Mutating
// either column on `users` (verify, change, admin reset) requires evicting the
// user's cached authentication contexts — see AuthContextRegistry.EvictByUserID.
// That is deliberately separate from credential revocation, which uses
// AuthContextRegistry.RevokeUserAuthContextAtGeneration.
//
// Credential.WorkerScopeID carries a value only when a delegation_tokens row
// authenticated the request (as opposed to a session cookie or an
// api_tokens bearer). It identifies the worker that MINTED the token, which is the
// one axis a delegation bearer is narrowed on: downstream authorization
// (ChannelService.verifyDelegationWorkerScope, crdt's worker-scoped auth
// checker) refuses a bearer aimed at a different machine of the same user, so
// a leaked token cannot pivot off the worker it was issued on.
//
// AuthenticatedAt is the timestamp of the stored session or bearer row that
// authenticated the request. User-wide revocation correctness rests on
// UserAuthGeneration, not on this field.
//
// It is also an authorization input, so it must stay the instant of the
// authentication and must never track a slide. Every producer that specifies a
// stored credential reads that row's created_at, and the session slide
// rewrites last_active_at and expires_at alone. LoadSoloUser stamps the
// process clock instead, which is safe only because it carries no
// Credential and the admission below refuses on the credential kind first.
// service.assertFirstCredentialAuthIsFresh refuses to attach an account's
// FIRST password or passkey on a session older than a short window, and a
// slid value would make a stolen cookie permanently fresh for that
// admission.
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
//
// Elevation is the credential's step-up state. A session cookie and a
// command-line credential both carry one; a delegation bearer and a solo
// request carry the zero value. Read it through Elevated(now) rather than
// directly -- see Elevation on why the deadline stays raw here and the
// predicate takes the current instant.
//
// Scopes is what the CREDENTIAL was granted, which is never wider than what
// the user themself may do. It lives on UserInfo rather than on
// CredentialIdentity because that value is compared by value on the hot path
// (channel and lease indexes key on it), and a second field would change what
// "same credential" means there.
//
// It must be constructed EXPLICITLY at every producer. The zero ScopeSet
// reaches nothing, so a path that forgets to fill it denies rather than
// grants: ValidateToken (a session cookie), LoadSoloUser (solo mode) and
// validateRow (a bearer) each state their own answer, and a fourth producer
// cannot inherit one by accident.
type UserInfo struct {
	ID                  userid.UserID
	Username            string
	IsAdmin             bool
	Email               string
	EmailVerified       bool
	Credential          CredentialIdentity
	Scopes              authscope.ScopeSet
	AuthenticatedAt     time.Time
	CredentialExpiresAt CredentialDeadline
	Elevation           Elevation
	UserAuthGeneration  int64
	AuthGeneration      uint64
	// Solo marks solo mode's synthetic user. LoadSoloUser is the only
	// producer; every authenticated path leaves it false.
	Solo bool
}

// SoloAuthenticated reports whether this is solo mode's synthetic user.
//
// Solo mode authenticates every request as one bootstrapped account with no
// session row and no credential row, so the questions the credential-shaped
// rules ask have no answer for it. The elevation rule is the one that matters:
// there is no sign-in, so there is no ceremony to prove a factor with, and a
// gate that demanded one would refuse the whole hub-administration surface for
// ever. The predicate lives here, on the identity itself, so a rule states its
// solo answer once rather than each gate testing a credential kind that solo
// mode never fills.
func (u *UserInfo) SoloAuthenticated() bool {
	return u != nil && u.Solo
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

// ErrInvalidCurrentPassword wraps a rejected password on the ELEVATION path.
// verifyElevationPassword is its sole producer, for ElevateSession: an
// account with no password, and a password that does not match, both report
// it. ChangePassword raised it too until the elevation window replaced its
// current-password check -- that verb now presents no secret of its own.
//
// The rate limiter keys on it via errors.Is so that only genuine credential
// failures count toward a user's attempt budget -- weak-new-password
// validation and transient internal errors must never lock anyone out.
var ErrInvalidCurrentPassword = errors.New("current password is incorrect")

// ErrInvalidElevationAssertion wraps a failed passkey assertion on the
// step-up path (FinishPasskeyElevation). The rate limiter keys on it the same
// way it keys on ErrInvalidCurrentPassword, so the two factors a user can
// present to elevate share one attempt budget.
var ErrInvalidElevationAssertion = errors.New("passkey verification failed")

// RevokeAllUserCredentials revokes every active api_tokens and
// delegation_tokens row for userID and, via RevokeUserTokens, bumps
// users.tokens_revoked_at AND users.auth_generation, emitting the durable
// user_tokens revocation event that carries the new generation through the
// stream (the backbone of out-of-band teardown). Returns (apiCount,
// delegationCount) so admin handlers can report what it revoked.
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
//
// "All" is the whole of the promise: this spares NOTHING, and an
// administrator's ResetPassword and DeleteUser depend on that.
// RevokeUserCredentialsExceptAPIToken below is the one variant that keeps a
// row, and only the self-service step-up path calls it.
func RevokeAllUserCredentials(ctx context.Context, st store.Store, userID userid.UserID) (int64, int64, error) {
	return RevokeUserCredentialsExceptAPIToken(ctx, st, userID, "")
}

// RevokeUserCredentialsExceptAPIToken is the same revocation, minus ONE
// command-line credential.
//
// The self-service step-up path is its only caller, and it needs the
// exclusion for the same reason Sessions().DeleteOthers takes KeepID. A user
// who changes their password from a browser keeps the session that asked; a
// user who changes it from the command line must keep the credential that
// asked. Without the exclusion that credential destroyed itself as a side
// effect of its own success: the change committed, and every later call
// answered Unauthenticated with the credential file still on disk.
//
// The exclusion is narrow on purpose. It spares ONE api_tokens row. Every
// delegation token still dies -- a worker mints those for an agent, and no
// agent is the person who changed the password -- and the account's
// credential epoch still moves, which is what ends every session and every
// cached authentication context.
//
// keepAPITokenID EMPTY revokes every row, which is what an administrator's
// ResetPassword and DeleteUser need: RevokeAllUserCredentials above is the
// name for that intent, and no caller of it changed.
//
// The kept row needs one more write that this does NOT do: the epoch bump
// leaves its api_tokens.auth_generation behind users.auth_generation, and
// TokenValidator refuses a row in that state. The caller pairs this with
// APITokens().RefreshAuthGeneration, exactly as the session path pairs the
// delete with Sessions().RefreshAuthGeneration. It stays the caller's write
// because the caller is the one place that knows WHICH row it kept.
func RevokeUserCredentialsExceptAPIToken(
	ctx context.Context, st store.Store, userID userid.UserID, keepAPITokenID string,
) (int64, int64, error) {
	var apiCount, delegationCount int64
	err := st.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		var err error
		apiCount, err = tx.APITokens().RevokeOthers(ctx, store.RevokeOtherAPITokensParams{
			UserID: userID,
			KeepID: keepAPITokenID,
		})
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
// store.ErrNotFound when nothing created the solo user yet.
func LoadSoloUser(ctx context.Context, st store.Store) (*UserInfo, error) {
	user, err := st.Users().GetByUsername(ctx, usernames.Solo)
	if err != nil {
		return nil, err
	}
	// A blank users.id is corrupt data, not a programmer error, so LoadSoloUser
	// refuses it rather than panicking on it: MustNew's contract is "the caller
	// already knows this is non-empty", which holds for a literal but not for a
	// column. LoadSoloUser rejects the row the same way it rejects a missing one
	// -- an identity the hub cannot resolve cannot authenticate anything.
	id, ok := userid.New(user.ID)
	if !ok {
		return nil, fmt.Errorf("solo user row has a blank id")
	}
	return &UserInfo{
		ID:       id,
		Username: user.Username,
		IsAdmin:  user.IsAdmin,
		// Solo mode's synthetic user is UNSCOPED, spelled out for the same
		// reason a session is: whoever reaches the port already has full
		// authority, so there is nothing here for a scope to subtract. A solo
		// hub still honours a PRESENTED bearer's grant -- the solo rung yields
		// to one; see authenticate.
		Scopes:             authscope.UnscopedGrant(),
		AuthenticatedAt:    time.Now().UTC(),
		UserAuthGeneration: user.AuthGeneration,
		Solo:               true,
	}, nil
}

// Login validates credentials and creates a new session token. lifetime is the
// session's minimum life past the user's last request; see CreateSession.
// Returns the session ID, user, session expiry time, and any error.
func Login(ctx context.Context, st store.Store, username, password string, lifetime time.Duration) (string, *store.User, time.Time, error) {
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

	// Passkey-only / OAuth-only accounts store PlaceholderHash. Verify would
	// return a parse error mapped to CodeInternal and leak that the username
	// exists without a password. Treat "no password" like a wrong password.
	if !user.FirstCredentialExempt {
		return "", nil, zero, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	// Verify the password OUTSIDE the auth transaction. On the default
	// SQLite backend RunInUserAuthTransaction promotes to a write
	// transaction (LockUserAuthState), which serializes every other
	// writer for as long as it is held; argon2 verification (~50-200ms) has
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
		if !lockedUser.FirstCredentialExempt {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		match, verifyErr := matchPrelock, verifyErrPrelock
		if lockedUser.PasswordHash != prelockHash {
			// The hash changed under the lock (concurrent rotation), so the
			// pre-lock result is stale: re-verify against the committed hash.
			match, verifyErr = pwdhash.Verify(lockedUser.PasswordHash, password)
		}
		if verifyErr != nil {
			// Malformed hashes (including PlaceholderHash) must not become
			// CodeInternal — that distinguishes "exists without password".
			// Log the parse cause server-side: a corrupted stored hash is
			// otherwise indistinguishable from a wrong password, with no
			// operator signal at all.
			slog.WarnContext(ctx, "stored password hash failed to parse",
				"user_id", user.ID, "err", verifyErr)
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
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
		sessionID, expiresAt, err = CreateSession(ctx, tx, sessUID, lifetime)
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
//
// lifetime is the session's minimum life past the user's last request. Zero or
// less falls back to DefaultSessionDuration: a caller that has no configured
// value must still issue a usable session, not one that expired before the
// response left the Hub.
func CreateSession(ctx context.Context, st store.Store, userID userid.UserID, lifetime time.Duration, meta ...SessionMeta) (string, time.Time, error) {
	// A session for nobody is worse than no session: it would authenticate as a
	// blank id, which every predicate then has to refuse individually. Refuse to
	// write the row at all.
	if userID.IsZero() {
		return "", time.Time{}, fmt.Errorf("create session: user id is required")
	}
	sessionID := id.Generate()
	expiresAt := sessionExpiry(time.Now(), lifetime)
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
	row, err := st.Sessions().ValidateWithUser(ctx, token, time.Now().UTC())
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
		ID:         id,
		Credential: SessionCredential(token),
		// A browser session is UNSCOPED, and it says so explicitly. A session
		// is the account itself signed in, not an app the account authorized,
		// so no scope subtracts from it -- and stating that here rather than
		// leaving the zero value is what keeps a dropped grant fail-closed
		// everywhere else.
		Scopes:              authscope.UnscopedGrant(),
		Username:            row.Username,
		IsAdmin:             row.IsAdmin,
		Email:               row.Email,
		EmailVerified:       row.EmailVerified,
		AuthenticatedAt:     row.CreatedAt.UTC(),
		CredentialExpiresAt: DeadlineAt(row.ExpiresAt.UTC()),
		// One of the TWO producers that fill Elevation. The other is
		// loadBearer's api_tokens branch, because a command-line credential
		// proves its factor in a browser and carries a window of its own.
		// Every other path leaves the zero value, so a delegation bearer and
		// solo mode read as un-elevated by construction rather than by a
		// check somebody must remember. CredentialIdentity.ElevatableRow is
		// the statement of which credentials may carry a window at all.
		Elevation:          NewElevation(row.ElevationProvenAt, row.ElevationExpiresAt),
		UserAuthGeneration: row.AuthGeneration,
	}, nil
}
