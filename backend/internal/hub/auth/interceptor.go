package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/singleflight"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/periodic"
)

// publicProcedures lists RPC procedures that do not require a session
// cookie. Worker registration (Register) is public from the
// session-cookie perspective because the worker process has no hub
// session — the handler validates an Authorization: Bearer <key> header
// itself. Connect is similarly authenticated by the long-lived
// auth_token in its own header.
//
// "Public" here means "the handler authenticates itself", NOT "anyone may
// call it". Each worker-facing entry below refuses without a credential of its
// own; the exemption only keeps the interceptor from rejecting that credential
// as neither a session cookie nor an `lmx_` API token. Adding a procedure that
// does NOT authenticate itself is an auth bypass.
//
// The credential differs by entry, so do not assume one mechanism: Register
// consumes a registration KEY, Connect resolves a worker auth_token directly
// through Workers().GetByAuthToken, and ListOwnedTabsForWorker goes through
// AuthenticateWorkerBearer. The rationale map in the package's test specifies
// the mechanism per entry, which is the check that keeps this list honest.
//
// Procedure names come from the generated `leapmuxv1connect` constants
// so a rename in the proto definition turns a typo here into a build
// error instead of a silent auth bypass.
var publicProcedures = map[string]bool{
	leapmuxv1connect.AuthServiceLoginProcedure:               true,
	leapmuxv1connect.AuthServiceSignUpProcedure:              true,
	leapmuxv1connect.AuthServiceGetSystemInfoProcedure:       true,
	leapmuxv1connect.WorkerConnectorServiceRegisterProcedure: true,
	leapmuxv1connect.WorkerConnectorServiceConnectProcedure:  true,
	// Same worker auth_token as Connect, on a plain unary RPC rather than the
	// stream. The worker's orphan reconciler is the only caller, and it is what
	// converges a worker after a close or a cross-workspace move that landed
	// while the worker was offline -- so without this entry the interceptor
	// answers `unauthenticated` and the worker never learns a tab is gone.
	leapmuxv1connect.WorkerReconcilerServiceListOwnedTabsForWorkerProcedure: true,
	leapmuxv1connect.AuthServiceGetOAuthProvidersProcedure:                  true,
	leapmuxv1connect.AuthServiceGetPendingOAuthSignupProcedure:              true,
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure:                true,
	// Public by design: ALTCHA challenges carry no secret and must be
	// fetchable before login. The captcha interceptor guards the
	// procedures that consume solutions, not the one that issues
	// challenges.
	leapmuxv1connect.AuthServiceGetAltchaChallengeProcedure:    true,
	leapmuxv1connect.AuthServiceBeginPasskeyLoginProcedure:     true,
	leapmuxv1connect.AuthServiceFinishPasskeyLoginProcedure:    true,
	leapmuxv1connect.AuthServiceBeginPasskeySignUpProcedure:    true,
	leapmuxv1connect.AuthServiceFinishPasskeySignUpProcedure:   true,
	leapmuxv1connect.AuthServiceRequestPasswordResetProcedure:  true,
	leapmuxv1connect.AuthServiceCompletePasswordResetProcedure: true,
}

// PublicProcedures lists every procedure the auth interceptor waives, so
// sibling packages can tripwire against the exact set (the captcha
// package's classification test uses it to keep its protected subset
// honest). Sorted for stable iteration.
func PublicProcedures() []string {
	out := make([]string, 0, len(publicProcedures))
	for p := range publicProcedures {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// The delegation ALLOWLIST that used to live here is gone. Its guarantee -- a
// worker-minted bearer for a process that reads untrusted input can never
// administer the hub -- now rides on CeilingFor(BearerKindDelegation), which
// loadBearer applies when it READS the row. That is strictly stronger: an
// allowlist limited the procedures a delegation bearer could call and left the
// grant on the row unbounded, so a mint bug or a hand-edited row produced an
// over-scoped credential that still authenticated. The ceiling limits the
// GRANT, at every validation, so there is no such row.
//
// delegation_procedures_test.go pins the new ceiling against the exact
// procedure set the old allowlist named.

// adminProcedurePrefix is what MAKES a procedure admin-only: every
// Connect procedure path is /leapmux.v1.<Service>/<Method>, and the four
// Admin*Service services are the only ones whose name begins with
// "Admin". enforceAdmin denies on this prefix, so a procedure that
// admin.proto declares is restricted the moment it is declared.
//
// A hand-maintained allowlist cannot carry that property: it fails OPEN.
// A new Admin*Service method that nobody remembers to add mounts
// unrestricted, and the only thing standing between it and any
// authenticated user is whether somebody runs the suite.
// TestNoAdminServiceOutsideAdminProto already treats the "Admin" name as
// authoritative, so the prefix rests on an invariant the suite enforces.
const adminProcedurePrefix = "/leapmux.v1.Admin"

// adminProcedures lists the procedures of the four Admin*Service
// services. It is the RECORD, not the enforcement: enforceAdmin denies on
// adminProcedurePrefix, and the descriptor-walk tests keep this map, the
// rationale map in admin_procedures_test.go, and the proto in lockstep,
// so every restricted procedure still carries a stated reason.
//
// Ordering note: the admin gate runs AFTER the delegation refusal, so a
// delegation bearer cannot reach an admin procedure even though its
// underlying user is an admin (loadUser resolves the full row) — the
// gate is the only thing standing between a worker-spawned agent and
// full hub administration. It also runs after the email gate, where
// admins are exempt by design: an admin who cannot receive mail must
// still be able to configure SMTP. The solo short-circuit above both
// returns before enforceAdmin ever runs, so a solo hub serves every
// admin procedure to its synthetic user whatever IsAdmin holds; bootstrap
// creates that user as an administrator, and the reserved username rule
// (usernames.IsReservedSystem) is what keeps a stored row from taking
// that identity.
var adminProcedures = map[string]bool{
	leapmuxv1connect.AdminSettingsServiceListSettingsProcedure:        true,
	leapmuxv1connect.AdminSettingsServiceUpdateSettingProcedure:       true,
	leapmuxv1connect.AdminSettingsServiceUpdateSettingSecretProcedure: true,
	leapmuxv1connect.AdminSettingsServiceUpdateSettingsProcedure:      true,
	leapmuxv1connect.AdminSettingsServiceResetSettingProcedure:        true,
	leapmuxv1connect.AdminSettingsServiceResetSettingsProcedure:       true,

	leapmuxv1connect.AdminUserServiceListUsersProcedure:             true,
	leapmuxv1connect.AdminUserServiceGetUserProcedure:               true,
	leapmuxv1connect.AdminUserServiceCreateUserProcedure:            true,
	leapmuxv1connect.AdminUserServiceUpdateUserProcedure:            true,
	leapmuxv1connect.AdminUserServiceDeleteUserProcedure:            true,
	leapmuxv1connect.AdminUserServiceSetUserAdminProcedure:          true,
	leapmuxv1connect.AdminUserServiceResetPasswordProcedure:         true,
	leapmuxv1connect.AdminUserServiceListUserSessionsProcedure:      true,
	leapmuxv1connect.AdminUserServiceListSessionsProcedure:          true,
	leapmuxv1connect.AdminUserServiceRevokeSessionProcedure:         true,
	leapmuxv1connect.AdminUserServiceRevokeUserSessionsProcedure:    true,
	leapmuxv1connect.AdminUserServicePurgeExpiredSessionsProcedure:  true,
	leapmuxv1connect.AdminUserServiceListAPITokensProcedure:         true,
	leapmuxv1connect.AdminUserServiceIssueAPITokenProcedure:         true,
	leapmuxv1connect.AdminUserServiceRevokeAPITokenProcedure:        true,
	leapmuxv1connect.AdminUserServiceListDelegationTokensProcedure:  true,
	leapmuxv1connect.AdminUserServiceRevokeDelegationTokenProcedure: true,

	leapmuxv1connect.AdminWorkerServiceListWorkersProcedure:                  true,
	leapmuxv1connect.AdminWorkerServiceGetWorkerProcedure:                    true,
	leapmuxv1connect.AdminWorkerServiceDeregisterWorkerProcedure:             true,
	leapmuxv1connect.AdminWorkerServiceListRegistrationKeysProcedure:         true,
	leapmuxv1connect.AdminWorkerServiceRevokeRegistrationKeyProcedure:        true,
	leapmuxv1connect.AdminWorkerServicePurgeExpiredRegistrationKeysProcedure: true,

	leapmuxv1connect.AdminIdPServiceAddOAuthProviderProcedure:        true,
	leapmuxv1connect.AdminIdPServiceListOAuthProvidersProcedure:      true,
	leapmuxv1connect.AdminIdPServiceRemoveOAuthProviderProcedure:     true,
	leapmuxv1connect.AdminIdPServiceSetOAuthProviderEnabledProcedure: true,
}

// No exported accessor for adminProcedures: unlike PublicProcedures,
// which the captcha package's classification test consumes, every admin
// tripwire lives in this package and reads the map directly. Export one
// when a cross-package test needs it, not before.

// unverifiedAllowedProcedures lists RPC procedures that authenticated
// users may call before their email is verified. The verify endpoint
// itself must be in this list — otherwise an unverified user couldn't
// complete verification.
//
// The list must be CLOSED under the preconditions of its own members. A
// procedure admitted here can still be refused by another rung, and the
// procedure that clears THAT refusal has to be admitted too. The three
// elevation procedures are here for exactly that reason: RequestEmailChange
// requires an elevated credential, no sign-in grants one, and an unverified
// account reaches nothing else. Without them a user who mistypes their
// address at sign-up is refused the change AND refused the elevation the
// change needs, so the account is unusable until an administrator repairs
// it. The three verify a factor rather than spend one, so admitting them
// before verification widens nothing.
var unverifiedAllowedProcedures = map[string]bool{
	leapmuxv1connect.AuthServiceGetCurrentUserProcedure:          true,
	leapmuxv1connect.AuthServiceLogoutProcedure:                  true,
	leapmuxv1connect.UserServiceRequestEmailChangeProcedure:      true,
	leapmuxv1connect.UserServiceVerifyEmailProcedure:             true,
	leapmuxv1connect.UserServiceResendVerificationEmailProcedure: true,
	leapmuxv1connect.UserServiceElevateSessionProcedure:          true,
	leapmuxv1connect.UserServiceBeginPasskeyElevationProcedure:   true,
	leapmuxv1connect.UserServiceFinishPasskeyElevationProcedure:  true,
}

// sessionCacheTTL controls how long a validated session is cached in memory.
// During this window, repeated requests skip the DB query entirely. Session
// revocation (logout) evicts the entry immediately via AuthContextRegistry.Evict.
const sessionCacheTTL = 30 * time.Second

// cachedSession holds a validated UserInfo with its cache time and the
// revocation sequence observed before validation. Cache reads compare that
// sequence only with marks for the same session, bearer, or user, so unrelated
// revocations do not turn otherwise-fresh entries into misses.
type cachedSession struct {
	user     *UserInfo
	cachedAt time.Time
	gen      uint64
}

type credentialCaches struct {
	lastTouch         sync.Map // sessionID -> time.Time of last DB touch
	sessions          sync.Map // sessionID -> cachedSession
	userSessions      sync.Map // userID -> *sync.Map (sessionID -> struct{})
	bearers           sync.Map // bearer kind + tokenID + secret hash -> cachedSession
	bearerKeysByToken sync.Map // bearer kind + tokenID -> *sync.Map(cache key -> struct{})
	userBearerKeys    sync.Map // userID -> *sync.Map(cache key -> struct{})
	// bearerExpiries records the most permissive known teardown deadline of a
	// bearer whose lifetime was extended by an in-process rotation (BearerRef ->
	// CredentialDeadline). Unlike a session slide -- which advances its cache row by
	// storing a fresh copy, so CurrentCredentialExpiry reads the new deadline -- a
	// bearer rotation EVICTS the bearer cache, leaving no row to carry the new
	// deadline. This map is that carrier, so a channel opening in the window between
	// validation and channel indexing is armed at the extended deadline, not the
	// stale connect-time one. Swept when the recorded deadline passes or the bearer
	// is evicted.
	bearerExpiries sync.Map
}

type authState struct {
	credentialCaches
	revocationLedger
	authenticatedLeaseRegistry

	// Revocation and validation must share one lock so a validation snapshot
	// cannot be published between recording a revocation and draining leases.
	// Cache entries and their reverse indexes also change under this lock so an
	// eviction cannot remove an index entry belonging to a fresh replacement.
	revocationMu sync.Mutex

	// channelRescheduler extends the expiry of channels tied to a sliding session
	// (set once at startup; nil until wired). Kept off revocationMu so a
	// session touch does not hold the auth lock across the channel-manager lock.
	channelRescheduler atomic.Pointer[ChannelExpiryRescheduler]

	// maxConnectionsPerUser limits the long-lived connections one user may hold
	// at once; zero is unlimited. Atomic rather than revocationMu-guarded because
	// it is written once at startup and read on every registration -- taking the
	// lock to read a constant would put startup configuration on the hot path.
	// The READ still happens under revocationMu, alongside the count it is
	// compared against; see RegisterAuthenticatedLease.
	maxConnectionsPerUser atomic.Int64
}

func cloneUserInfoWithGeneration(u *UserInfo, gen uint64) *UserInfo {
	if u == nil {
		return nil
	}
	out := *u
	out.AuthGeneration = gen
	return &out
}

// Policy is the auth-relevant settings snapshot, supplied as a closure so
// the interceptor re-resolves it per use: these three values are
// DB-backed runtime settings now, and a change must apply to the next
// request without a restart.
type Policy struct {
	// SecureCookies selects the __Host- prefixed cookie name, which the
	// Hub uses behind TLS.
	SecureCookies bool
	// EmailVerificationRequired refuses an unverified, non-admin user
	// every procedure outside the pre-verification allowlist.
	EmailVerificationRequired bool
	// SessionDuration is the minimum time a session stays valid after the
	// user's last request. Zero or less falls back to
	// DefaultSessionDuration.
	SessionDuration time.Duration
}

// PolicyFromSettings resolves the settings-backed Policy fields from a
// snapshot. The hub's interceptor closure and the service tests share
// this one mapping, so a new Policy field is wired in exactly one place
// and a test cannot enforce a policy the hub computes differently.
func PolicyFromSettings(snap *settings.Snapshot) Policy {
	return Policy{
		SecureCookies:             settings.KeySecureCookies.Of(snap),
		EmailVerificationRequired: settings.EmailVerificationEffective(snap),
		SessionDuration:           settings.SessionDuration(snap),
	}
}

// authInterceptor implements connect.Interceptor to validate session cookies
// on both unary and streaming RPCs.
type authInterceptor struct {
	store          store.Store
	policy         func() Policy
	soloUser       *UserInfo
	tokenValidator *TokenValidator
	// state holds the shared caches, lease registry, and revocation ledger.
	// Its revocationGen is the monotonic ordering clock shared by validation
	// snapshots and identity-scoped revocation/invalidation marks.
	state *authState
	// bearerFlight collapses concurrent ValidateBearer calls for the
	// same bearer into one DB hit. Without it, a long-lived agent
	// firing N concurrent RPCs right after a cache miss pays N DB
	// round-trips for the same bearer; the followers wait on the first
	// flight's result and write the cache once. Matters most for
	// delegation tokens that authenticate dozens of concurrent inner-
	// RPCs over a fresh Noise session.
	bearerFlight singleflight.Group
}

// InterceptorOptions carries what the auth interceptor needs. Store is what a
// working interceptor requires; every other field is optional, and the rest of
// the zero value gives cookie-only multi-user auth with the default session
// duration.
type InterceptorOptions struct {
	// Store validates a session cookie and a bearer credential. A nil Store
	// gives an interceptor that can do neither, which is useful only for
	// the AuthContextRegistry that the constructor returns beside it -- a
	// request that presents a cookie reaches the store and panics.
	Store store.Store
	// SoloUser enables solo mode, in which every request is automatically
	// authenticated as that user. Nil gives normal multi-user auth.
	SoloUser *UserInfo
	// TokenValidator accepts Authorization: Bearer lmx_... credentials alongside
	// the cookie path. Nil keeps cookie-only behavior.
	TokenValidator *TokenValidator
	// Policy supplies the DB-backed auth settings per use (cookie name,
	// verification gate, session duration). Nil keeps the zero Policy:
	// plain cookie names, no verification requirement, and the default
	// session duration.
	Policy func() Policy
}

// NewInterceptor creates a ConnectRPC interceptor that validates session cookies
// and attaches user info to the context. Public procedures (login, worker
// registration) are exempt from auth checks.
//
// Callers use the returned AuthContextRegistry to evict entries from the
// in-memory touch throttle (e.g. on logout). Call AuthContextRegistry.Stop to terminate
// the background sweep goroutine.
func NewInterceptor(opts InterceptorOptions) (connect.Interceptor, *AuthContextRegistry) {
	st := opts.Store
	state := &authState{}
	a := &authInterceptor{
		store:          st,
		policy:         opts.Policy,
		soloUser:       opts.SoloUser,
		tokenValidator: opts.TokenValidator,
		state:          state,
	}
	sweepCtx, cancel := context.WithCancel(context.Background())
	sc := &AuthContextRegistry{
		state:  state,
		cancel: cancel,
	}
	if st != nil {
		// Cache-miss fallback for CurrentCredentialExpiry: the authoritative DB
		// expiry of a live session, so a slid deadline survives an eviction of the
		// session's cache row. ValidateWithUser returns ErrNotFound for a gone or
		// expired session, which the caller degrades to the connect-time value.
		sc.sessionExpiry = func(ctx context.Context, sessionID string) (time.Time, bool, error) {
			sess, err := st.Sessions().ValidateWithUser(ctx, sessionID, time.Now().UTC())
			if errors.Is(err, store.ErrNotFound) {
				return time.Time{}, false, nil
			}
			if err != nil {
				return time.Time{}, false, err
			}
			return sess.ExpiresAt, true, nil
		}
	}
	periodic.Start(sweepCtx, periodic.Schedule{Interval: touchSweepInterval, SkipFirstRun: true}, func(context.Context) {
		a.sweepCachesOnce()
	})
	return a, sc
}

// AuthContextRegistry provides eviction access to the interceptor's in-memory
// session caches.
type AuthContextRegistry struct {
	state  *authState
	cancel context.CancelFunc
	// sessionExpiry reads the authoritative current expiry of a live session by id
	// as the CurrentCredentialExpiry cache-miss fallback. Nil in tests that
	// construct the registry directly; a nil reader degrades to the connect-time
	// value (the pre-fallback behavior).
	sessionExpiry func(ctx context.Context, sessionID string) (time.Time, bool, error)
}

// EvictBearer drops a cached bearer-token validation by bearer reference.
// Required so token revocation is immediate, not lagged by the cache TTL.
func (c *AuthContextRegistry) EvictBearer(ref BearerRef) {
	if c == nil || c.state == nil || !ref.IsValid() {
		return
	}
	c.state.revocationMu.Lock()
	c.state.deleteBearerCacheEntries(ref)
	gen := c.state.bumpGeneration()
	recordBearerRevocation(&c.state.bearerRevocations, ref, gen)
	leases := c.state.removeIndexedLeasesLocked(func(lease *authenticatedLease) bool {
		leaseRef, ok := lease.user.Credential.BearerRef()
		return ok && leaseRef == ref
	}, c.state.leasesByBearer[ref])
	c.state.revocationMu.Unlock()
	c.state.bearerExpiries.Delete(ref)
	c.state.cancelLeases(leases)
}

// InvalidateBearer drops every cached secret for a bearer row without
// revoking the credential or canceling authenticated leases. Refresh rotation
// uses this path because the row remains valid under a newly derived secret.
func (c *AuthContextRegistry) InvalidateBearer(ref BearerRef) {
	if c == nil || c.state == nil || !ref.IsValid() {
		return
	}
	c.state.revocationMu.Lock()
	c.state.deleteBearerCacheEntries(ref)
	gen := c.state.bumpGeneration()
	recordBearerRevocation(&c.state.bearerInvalidations, ref, gen)
	c.state.revocationMu.Unlock()
}

func (c *credentialCaches) deleteBearerCacheEntries(ref BearerRef) {
	value, ok := c.bearerKeysByToken.LoadAndDelete(ref)
	if !ok {
		return
	}
	value.(*sync.Map).Range(func(key, _ any) bool {
		c.deleteBearerCacheEntry(key.(bearerCacheKeyParts))
		return true
	})
}

func (c *credentialCaches) indexBearerCacheEntry(key bearerCacheKeyParts, cached cachedSession) {
	indexSyncMap(&c.bearerKeysByToken, key.bearerRef(), key)
	if cached.user != nil && !cached.user.ID.IsZero() {
		indexSyncMap(&c.userBearerKeys, cached.user.ID.String(), key)
	}
}

func (c *credentialCaches) deleteBearerCacheEntry(key bearerCacheKeyParts) {
	value, ok := c.bearers.LoadAndDelete(key)
	if !ok {
		return
	}
	unindexSyncMap(&c.bearerKeysByToken, key.bearerRef(), key)
	if cached, ok := value.(cachedSession); ok && cached.user != nil {
		unindexSyncMap(&c.userBearerKeys, cached.user.ID.String(), key)
	}
}

func (c *credentialCaches) deleteStaleLastTouch(key, stale any) {
	c.lastTouch.CompareAndDelete(key, stale)
}

func (c *credentialCaches) deleteStaleSession(key any, stale cachedSession) {
	if !c.sessions.CompareAndDelete(key, stale) {
		return
	}
	if stale.user != nil {
		unindexSyncMap(&c.userSessions, stale.user.ID.String(), key)
	}
}

func (c *credentialCaches) deleteStaleBearer(key bearerCacheKeyParts, stale cachedSession) {
	if !c.bearers.CompareAndDelete(key, stale) {
		return
	}
	unindexSyncMap(&c.bearerKeysByToken, key.bearerRef(), key)
	if stale.user != nil {
		unindexSyncMap(&c.userBearerKeys, stale.user.ID.String(), key)
	}
}

func indexSyncMap(outer *sync.Map, indexKey, entryKey any) {
	value, _ := outer.LoadOrStore(indexKey, &sync.Map{})
	value.(*sync.Map).Store(entryKey, struct{}{})
}

func unindexSyncMap(outer *sync.Map, indexKey, entryKey any) {
	value, ok := outer.Load(indexKey)
	if !ok {
		return
	}
	entries := value.(*sync.Map)
	entries.Delete(entryKey)
	if isSyncMapEmpty(entries) {
		outer.CompareAndDelete(indexKey, entries)
	}
}

// Evict removes a session from all in-memory caches and records a
// session-scoped revocation mark. Call this on logout.
// Safe to call on a nil receiver.
func (c *AuthContextRegistry) Evict(sessionID string) {
	if c == nil || c.state == nil {
		return
	}
	c.state.revocationMu.Lock()
	if v, ok := c.state.sessions.LoadAndDelete(sessionID); ok {
		if cached, ok := v.(cachedSession); ok && cached.user != nil {
			unindexSyncMap(&c.state.userSessions, cached.user.ID.String(), sessionID)
		}
	}
	c.state.lastTouch.Delete(sessionID)
	gen := c.state.bumpGeneration()
	recordRevocation(&c.state.sessionRevocations, sessionID, gen)
	leases := c.state.removeIndexedLeasesLocked(func(lease *authenticatedLease) bool {
		return lease.user.Credential.MatchesSession(sessionID)
	}, c.state.leasesBySession[sessionID])
	c.state.revocationMu.Unlock()
	c.state.cancelLeases(leases)
}

// isSyncMapEmpty reports whether m has no entries. sync.Map has no Len(), so
// we probe with Range and break on the first element.
func isSyncMapEmpty(m *sync.Map) bool {
	empty := true
	m.Range(func(_, _ any) bool { empty = false; return false })
	return empty
}

// EvictByUserID removes cached user data after non-security profile or email
// changes. Credential revocation paths must use
// RevokeUserAuthContextAtGeneration instead. Safe to call on a nil receiver.
//
// Records a user-scoped invalidation unconditionally so validation that raced
// this call retries even when no indexed session existed yet.
func (c *AuthContextRegistry) EvictByUserID(userID string) {
	if c == nil || c.state == nil {
		return
	}
	if userID == "" {
		warnBlankRevocationTarget("EvictByUserID")
		return
	}
	c.state.revocationMu.Lock()
	gen := c.state.bumpGeneration()
	recordRevocation(&c.state.userInvalidations, userID, gen)
	c.evictSessionsByUserID(userID)
	c.evictBearersByUserGeneration(userID, 0)
	c.state.revocationMu.Unlock()
}

func (c *AuthContextRegistry) evictSessionsByUserID(userID string) {
	v, ok := c.state.userSessions.LoadAndDelete(userID)
	if !ok {
		return
	}
	v.(*sync.Map).Range(func(key, _ any) bool {
		sessionID := key.(string)
		c.state.lastTouch.Delete(sessionID)
		c.state.sessions.Delete(sessionID)
		return true
	})
}

// RevokeUserAuthContextAtGeneration rejects and cancels only auth contexts
// older than the persisted user credential generation. A zero generation is
// stays supported for revocation callers that intentionally invalidate every
// current credential for the user.
// The userID == "" guard is NOT redundant with userid.UserID's fail-closed
// comparisons, and must not be deleted as though it were. Matches is tuned for
// GRANT semantics -- false means "not authorized" -- but on this eviction path
// false means "do not revoke". A blank id reaching the Matches calls below
// would therefore skip every cached session, bearer, and lease and report a
// revocation that silently evicted nothing. Refusing the blank id up front is
// what keeps the polarity right.
func (c *AuthContextRegistry) RevokeUserAuthContextAtGeneration(userID string, userAuthGeneration int64) {
	if c == nil || c.state == nil {
		return
	}
	if userID == "" {
		warnBlankRevocationTarget("RevokeUserAuthContextAtGeneration", "generation", userAuthGeneration)
		return
	}
	c.state.revocationMu.Lock()
	effectiveGeneration := userAuthGeneration
	if existing, ok := c.state.userRevocations.Load(userID); ok && userAuthGeneration > 0 {
		if mark, ok := existing.(userRevocationMark); ok {
			if mark.userAuthGeneration >= effectiveGeneration {
				c.state.revocationMu.Unlock()
				return
			}
		}
	}
	gen := c.state.bumpGeneration()
	c.state.userRevocations.Store(userID, userRevocationMark{
		revocationMark:     revocationMark{generation: gen, recordedAt: time.Now()},
		userAuthGeneration: effectiveGeneration,
	})
	leases := c.state.removeIndexedLeasesLocked(func(lease *authenticatedLease) bool {
		return lease.user.ID.Matches(userID) && ShouldEvictForUserGeneration(lease.user.UserAuthGeneration, effectiveGeneration)
	}, c.state.leasesByUser[userID])
	c.evictSessionsByUserGeneration(userID, effectiveGeneration)
	c.evictBearersByUserGeneration(userID, effectiveGeneration)
	c.state.revocationMu.Unlock()
	c.state.cancelLeases(leases)
}

// CurrentSyntheticUser returns a fresh solo/synthetic identity ordered after
// the latest in-process revocation and advanced to the newest persisted user
// credential generation observed by the watcher.
func (c *AuthContextRegistry) CurrentSyntheticUser(user *UserInfo) *UserInfo {
	if user == nil {
		return nil
	}
	if c == nil || c.state == nil {
		out := *user
		return &out
	}
	return c.state.currentSyntheticUser(user)
}

// currentSyntheticUser clones a non-nil user, stamps it with the latest
// revocation generation, and advances it past any recorded user-wide
// revocation. Callers reach it directly (rather than newing an
// AuthContextRegistry façade) on the hot solo/public auth path.
func (s *authState) currentSyntheticUser(user *UserInfo) *UserInfo {
	s.revocationMu.Lock()
	defer s.revocationMu.Unlock()
	out := cloneUserInfoWithGeneration(user, s.revocationGen.Load())
	if value, ok := s.userRevocations.Load(user.ID.String()); ok {
		if mark, ok := value.(userRevocationMark); ok && mark.userAuthGeneration > out.UserAuthGeneration {
			out.UserAuthGeneration = mark.userAuthGeneration
		}
	}
	return out
}

func (c *AuthContextRegistry) evictSessionsByUserGeneration(userID string, userAuthGeneration int64) {
	value, ok := c.state.userSessions.Load(userID)
	if !ok {
		return
	}
	value.(*sync.Map).Range(func(key, _ any) bool {
		value, ok := c.state.sessions.Load(key)
		if !ok {
			return true
		}
		cached, ok := value.(cachedSession)
		if !ok || cached.user == nil || !cached.user.ID.Matches(userID) {
			return true
		}
		if !ShouldEvictForUserGeneration(cached.user.UserAuthGeneration, userAuthGeneration) {
			return true
		}
		sessionID, ok := key.(string)
		if !ok {
			return true
		}
		c.state.sessions.Delete(sessionID)
		c.state.lastTouch.Delete(sessionID)
		unindexSyncMap(&c.state.userSessions, userID, sessionID)
		return true
	})
}

func (c *AuthContextRegistry) evictBearersByUserGeneration(userID string, userAuthGeneration int64) {
	value, ok := c.state.userBearerKeys.Load(userID)
	if !ok {
		return
	}
	value.(*sync.Map).Range(func(key, _ any) bool {
		value, ok := c.state.bearers.Load(key)
		if !ok {
			return true
		}
		cached, ok := value.(cachedSession)
		if !ok || cached.user == nil || !cached.user.ID.Matches(userID) {
			return true
		}
		if ShouldEvictForUserGeneration(cached.user.UserAuthGeneration, userAuthGeneration) {
			c.state.deleteBearerCacheEntry(key.(bearerCacheKeyParts))
		}
		return true
	})
}

// IsAuthContextCurrent reports whether nothing revoked the concrete session,
// bearer, or user that authenticated a request since the validation of that
// request. It is deliberately identity-scoped; a revocation for another user
// must not reject an unrelated in-flight channel open.
func (c *AuthContextRegistry) IsAuthContextCurrent(user *UserInfo) bool {
	if c == nil || c.state == nil || user == nil {
		return true
	}
	c.state.revocationMu.Lock()
	defer c.state.revocationMu.Unlock()
	return c.isAuthContextCurrentLocked(user)
}

// CurrentCredentialExpiry returns the most recent expiry this process knows for
// the credential that authenticated user. For a cookie session it reads the
// shared session-cache entry -- which a concurrent slide advances by storing a
// fresh cached row -- so it reads a slide that landed after the caller
// captured user.CredentialExpiresAt. It falls back to the caller's captured
// deadline when the session is not cached, and for non-session credentials
// (bearers), whose cache entry is evicted rather than updated on rotation.
//
// OpenChannel calls this immediately before arming a channel's expiry so a slide
// that raced the channel's registration -- landing before the channel was
// indexed, so RescheduleExpiryBySession could not re-time it -- is still picked
// up, instead of arming the channel at the stale connect-time deadline and
// tearing a still-valid channel down early.
func (c *AuthContextRegistry) CurrentCredentialExpiry(ctx context.Context, user *UserInfo) CredentialDeadline {
	if user == nil {
		return UnsetDeadline()
	}
	if c == nil || c.state == nil {
		return user.CredentialExpiresAt
	}
	sessionID := user.Credential.SessionID()
	if sessionID == "" {
		// Bearer path: a rotation may have extended this bearer's deadline after
		// the credential was validated. The rotation evicts the bearer cache (no
		// row to read, unlike the session path), so consult the recorded per-bearer
		// extension and take the more permissive of it and the connect-time value.
		if ref, ok := user.Credential.BearerRef(); ok {
			if v, ok := c.state.bearerExpiries.Load(ref); ok {
				return user.CredentialExpiresAt.Later(v.(CredentialDeadline))
			}
		}
		return user.CredentialExpiresAt
	}
	// Session cache hit: return the deadline recorded on the cached row, which a
	// slide advances by storing a fresh copy. Take the more permissive of it and the
	// connect-time value -- the same Later() the bearer path, the DB-fallback path below, and the
	// lock-held twin currentCredentialExpiryLocked all use -- so the invariant
	// "never armed earlier than any deadline known for the credential" holds even
	// if the cached row is ever repopulated below the caller's captured deadline.
	if exp, ok := c.cachedSessionExpiry(sessionID); ok {
		return user.CredentialExpiresAt.Later(exp)
	}
	// Session cache MISS: the row was evicted (e.g. a concurrent user_info change)
	// after a slide extended the deadline, so the connect-time value captured at
	// request validation is stale. Read the authoritative DB expiry so the channel
	// is armed at the slid deadline, not torn down early -- the session-side
	// counterpart of the bearer path's bearerExpiries fallback. A transient lookup
	// failure or a gone/expired session degrades to the connect-time value.
	if c.sessionExpiry != nil {
		if exp, ok, err := c.sessionExpiry(ctx, sessionID); err != nil {
			slog.DebugContext(ctx, "current credential expiry: session expiry lookup failed",
				"session_id", sessionID, "error", err)
		} else if ok {
			return user.CredentialExpiresAt.Later(DeadlineAt(exp))
		}
	}
	return user.CredentialExpiresAt
}

// cachedSessionExpiry returns the credential expiry recorded on the session's
// cached row (which a slide advances by storing a fresh copy), or ok=false on a
// cache miss. The cached row is immutable once stored -- touchSession replaces it
// wholesale rather than mutating in place (see sweepCachesOnce, which relies on
// the same invariant to scan lock-free) -- so this reads it WITHOUT revocationMu,
// keeping the global auth lock off the OpenChannel expiry-arming hot path exactly
// as sessionCacheFresh/bearerCacheFresh keep it off per-request validation. The
// value is a sync.Map snapshot; the Later() merge at the call site tolerates a
// row repopulated below the caller's captured deadline, so the lock added nothing.
func (c *AuthContextRegistry) cachedSessionExpiry(sessionID string) (CredentialDeadline, bool) {
	if v, ok := c.state.sessions.Load(sessionID); ok {
		if cached, ok := v.(cachedSession); ok && cached.user != nil {
			return cached.user.CredentialExpiresAt, true
		}
	}
	return UnsetDeadline(), false
}

// RecordBearerExpiry notes the most permissive known teardown deadline for a
// bearer whose lifetime was just extended by an in-process rotation, so a
// channel opening in the validate->index window is armed at the extended
// deadline (read via CurrentCredentialExpiry) rather than the stale
// connect-time one. Idempotent and monotonic: an already-recorded deadline is
// replaced only by a more permissive one. A NeverExpires newExpiry supersedes any
// finite recorded deadline.
//
// The load-merge-store runs as a compare-and-swap loop so the monotonic
// guarantee holds by construction rather than only when callers happen to be
// serialized: a plain Load-then-Store could let a concurrent writer's
// less-permissive deadline clobber a more-permissive one recorded in the gap.
func (c *AuthContextRegistry) RecordBearerExpiry(ref BearerRef, newExpiry CredentialDeadline) {
	if c == nil || c.state == nil || !ref.IsValid() {
		return
	}
	for {
		prev, ok := c.state.bearerExpiries.Load(ref)
		if !ok {
			if _, loaded := c.state.bearerExpiries.LoadOrStore(ref, newExpiry); !loaded {
				return
			}
			// A concurrent writer inserted first; retry to merge against it.
			continue
		}
		prevExpiry := prev.(CredentialDeadline)
		merged := prevExpiry.Later(newExpiry)
		if merged.Equal(prevExpiry) {
			// Already at least as permissive as newExpiry -- nothing to store.
			return
		}
		if c.state.bearerExpiries.CompareAndSwap(ref, prev, merged) {
			return
		}
		// Lost the race against a concurrent writer/sweep; reload and re-merge.
	}
}

func (c *AuthContextRegistry) isAuthContextCurrentLocked(user *UserInfo) bool {
	gen := user.AuthGeneration
	if revokedAfter(&c.state.sessionRevocations, user.Credential.SessionID(), gen) {
		return false
	}
	if ref, ok := user.Credential.BearerRef(); ok {
		if bearerRevokedAfter(&c.state.bearerRevocations, ref, gen) {
			return false
		}
	}
	if userRevokedAfter(&c.state.userRevocations, user, gen) {
		return false
	}
	return true
}

// Stop terminates the background sweep goroutine. Safe to call multiple times
// and on a nil receiver (matches Evict/EvictByUserID).
func (c *AuthContextRegistry) Stop() {
	if c == nil {
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.state == nil {
		return
	}
	c.state.revocationMu.Lock()
	leases := c.state.removeAllLeasesLocked(func(*authenticatedLease) bool { return true })
	c.state.revocationMu.Unlock()
	c.state.cancelLeases(leases)
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		p := a.currentPolicy()
		ctx, refresh, err := a.authenticate(ctx, req.Spec().Procedure, req.Header().Get("Cookie"), req.Header().Get("Authorization"), p)
		if err != nil {
			// authenticate rejects an authenticated request too -- an unverified
			// user reaches the email-verification gate after the slide committed --
			// so this rejection carries the cookie for the same reason the handler's
			// does below.
			return nil, refresh.applyToError(err, p.SecureCookies)
		}
		resp, err := next(ctx, req)
		if err != nil {
			// A failed call has no response to hang a header on, but a
			// connect.Error carries its own metadata, and connect-go merges that
			// into the response header before it writes the status. Without this
			// branch a client whose calls keep failing -- a permission denial, a
			// validation error, a procedure that fails briefly -- slides the
			// row on every request and is still signed out at the login deadline,
			// because the throttle gives the cookie no other request to ride.
			return nil, refresh.applyToError(err, p.SecureCookies)
		}
		if resp == nil {
			return resp, err
		}
		refresh.applyTo(resp.Header(), p.SecureCookies)
		return resp, nil
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		p := a.currentPolicy()
		ctx, refresh, err := a.authenticate(ctx, conn.Spec().Procedure, conn.RequestHeader().Get("Cookie"), conn.RequestHeader().Get("Authorization"), p)
		// Before the handler runs, not after: the response header of a stream
		// goes out with the first message, and a long-lived stream sends that
		// message long before it returns. The write happens on the rejection path
		// too, because a stream carries an error in its body and not in the
		// header, so the header is the only place a cookie can travel either way.
		refresh.applyTo(conn.ResponseHeader(), p.SecureCookies)
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// authenticate validates the session and attaches user info to the context.
// Public procedures pass through with optional solo-mode user. Authenticated
// It checks an authenticated request for email verification when required, and
// accepts bearer tokens (Authorization: Bearer lmx_...) alongside the cookie path.
//
// The second result is the session slide that this request caused, which the
// caller writes to the response as a refreshed cookie. It is the zero value on
// every path that slides nothing: solo mode, a public procedure, and bearer
// auth all hold no session cookie to refresh.
//
// headerPresentsLeapMuxBearer reports whether an Authorization header carries a
// leapmux bearer at all, valid or not. It is what the solo rung yields to; see
// the comment in authenticate. AuthenticateHTTP asks the same question of a
// *http.Request through presentsLeapMuxBearer.
func headerPresentsLeapMuxBearer(authHeader string) bool {
	bearer, ok := BearerToken(authHeader)
	return ok && IsLeapMuxBearer(bearer)
}

func (a *authInterceptor) authenticate(ctx context.Context, procedure, cookieHeader, authHeader string, p Policy) (context.Context, sessionRefresh, error) {
	var noRefresh sessionRefresh

	// Solo mode authenticates every procedure -- public or not -- as the
	// synthetic user, and short-circuits the cookie path.
	//
	// It YIELDS to a presented lmx_ bearer, which is what makes the scope model
	// work on a solo hub. Reaching the port is the authentication there, so the
	// synthetic user carries an unscoped grant; a caller that presents a bearer
	// is asking to be its APP instead, having accepted a narrower grant on a
	// consent screen. Answering "you are the solo user" would discard that
	// narrowing without a word, and an agent handed file:read would still hold
	// everything the account can do.
	//
	// It yields on the PRESENCE of a bearer, not its validity. A revoked or
	// malformed one falls through to tryAuthenticateBearer and is refused
	// there; falling back to the solo rung would make a broken credential
	// stronger than a working one.
	if a.soloUser != nil && !headerPresentsLeapMuxBearer(authHeader) {
		return WithUser(ctx, a.state.currentSyntheticUser(a.soloUser)), noRefresh, nil
	}

	if publicProcedures[procedure] {
		return ctx, noRefresh, nil
	}

	if userInfo, ok, err := a.tryAuthenticateBearer(ctx, authHeader); err != nil {
		return ctx, noRefresh, err
	} else if ok {
		ctx = WithUser(ctx, userInfo)
		return ctx, noRefresh, a.authorize(procedure, userInfo, p)
	}

	// The cookie rung reads the SAME asymmetric fallback AuthenticateHTTP
	// reads, through the shared helper: the __Host- spelling first, and the
	// unprefixed one only on a hub that does not write the prefix. The two
	// ladders used to spell this separately, and the interceptor's single
	// spelling signed the same browser out of every Connect procedure the
	// moment an operator turned secure_cookies off, while the WebSockets --
	// which AuthenticateHTTP serves -- kept the session alive.
	token := sessionIDFromCookieHeader(cookieHeader, p.SecureCookies)
	if token == "" {
		return ctx, noRefresh, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	userInfo, err := a.validateTokenCached(ctx, token)
	if err != nil {
		return ctx, noRefresh, err
	}
	refresh := noRefresh
	if slidTo := a.touchSession(ctx, token, userInfo, p); !slidTo.IsZero() {
		refresh = sessionRefresh{sessionID: token, expiresAt: slidTo}
	}

	ctx = WithUser(ctx, userInfo)

	// The refresh travels with the rejection, not only with a success. The slide
	// above already committed to the row, and the throttle then blocks every
	// other request for a whole window, so a user whose calls are refused here --
	// an unverified user waiting to verify, whose every non-allowlisted procedure
	// is denied -- would slide the row again and again and never receive a cookie
	// for it.
	return ctx, refresh, a.authorize(procedure, userInfo, p)
}

// authorize runs every rung that decides whether an AUTHENTICATED caller may
// call this procedure.
//
// ONE method, for both credential paths. The bearer path and the cookie path
// used to spell the sequence separately, which is how the delegation refusal
// came to exist on one of them only. A rung added here now applies to both by
// construction rather than by the author remembering the second call site.
//
// The ORDER is deliberate:
//
//  1. Scope. Cheapest, applies to every kind, and running it first means an
//     app with no admin:read is refused for its own grant rather than told
//     whether its user is an administrator.
//  2. Email verification. An unverified account reaches almost nothing, and
//     the allowlist that clears it is a property of the account rather than
//     of the credential.
//  3. The admin gate, last, because it is the one that reads IsAdmin -- so a
//     caller that fails an earlier rung never learns it.
func (a *authInterceptor) authorize(procedure string, userInfo *UserInfo, p Policy) error {
	if err := enforceScope(procedure, userInfo); err != nil {
		return err
	}
	if err := a.enforceEmailVerification(procedure, userInfo, p); err != nil {
		return err
	}
	return enforceAdmin(procedure, userInfo)
}

// currentPolicy resolves the settings-backed auth policy once per
// request; a nil Policy closure (tests, bare constructions) keeps the
// zero value. The wrappers pass the result through the call chain, so
// one request resolves the (mutex-guarded, snapshot-backed) policy
// exactly once instead of re-reading it at every consumer.
func (a *authInterceptor) currentPolicy() Policy {
	if a.policy == nil {
		return Policy{}
	}
	return a.policy()
}

// enforceEmailVerification rejects a request from an unverified, non-admin user
// unless the procedure is on the pre-verification allowlist. Shared by the
// bearer and cookie auth paths so the gate cannot drift between them.
func (a *authInterceptor) enforceEmailVerification(procedure string, userInfo *UserInfo, p Policy) error {
	if userInfo.EmailVerificationFacts().Satisfied(p.EmailVerificationRequired) {
		return nil
	}
	if unverifiedAllowedProcedures[procedure] {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("email verification required"))
}

// enforceAdmin rejects a non-admin caller from an admin procedure, in the
// shape of enforceEmailVerification so the two gates read as one family.
// The unauthenticated case never reaches here: both callers sit behind a
// user-resolving branch. An unverified admin passes by design — admins are
// exempt from the email gate, and an admin who cannot receive mail must
// still be able to configure SMTP.
//
// It asks about the ACCOUNT alone. The parallel question -- was this
// CREDENTIAL granted hub administration? -- used to live beside it as
// enforceAdminScope, reading a single api_tokens.admin_scope boolean. It is
// now four named scopes (admin:read, admin:users, admin:settings,
// admin:workers) that enforceScope decides one rung earlier, so a credential
// minted to administer users can no longer also rewrite the hub's security
// settings.
func enforceAdmin(procedure string, userInfo *UserInfo) error {
	// Deny by PREFIX, never by the map. See adminProcedurePrefix: the map
	// is the rationale record, and a lookup miss there must not open a
	// procedure that admin.proto declares.
	if !strings.HasPrefix(procedure, adminProcedurePrefix) {
		return nil
	}
	if !userInfo.IsAdmin {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("administrator privileges are required"))
	}
	return nil
}

// tryAuthenticateBearer attempts Bearer-token auth. Returns (info, true,
// nil) on success, (nil, false, nil) when no bearer header is present
// (caller should fall back to cookies), and (nil, false, err) when a
// bearer header is present but rejected.
func (a *authInterceptor) tryAuthenticateBearer(ctx context.Context, authHeader string) (*UserInfo, bool, error) {
	if a.tokenValidator == nil || authHeader == "" {
		return nil, false, nil
	}
	bearer, ok := BearerToken(authHeader)
	if !ok || !IsLeapMuxBearer(bearer) {
		return nil, false, nil
	}

	kind, tokenID, secret, err := ParseBearer(bearer)
	if err != nil {
		return nil, false, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
	}
	cacheKey := bearerCacheKey(kind, tokenID, a.tokenValidator.HashSecret(secret))
	if v, ok := a.state.bearers.Load(cacheKey); ok {
		cached := v.(cachedSession)
		if a.bearerCacheFresh(cacheKey, cached) {
			return cloneUserInfoWithGeneration(cached.user, cached.gen), true, nil
		}
		a.state.revocationMu.Lock()
		a.state.deleteStaleBearer(cacheKey, cached)
		a.state.revocationMu.Unlock()
	}

	// Collapse concurrent misses for the same bearer into one
	// ValidateBearer call. The follower goroutines wait on the leader
	// and get the same (info, err); only one DB round-trip fires.
	resultCh := a.bearerFlight.DoChan(cacheKey.flightKey(), func() (any, error) {
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCacheTTL)
		defer cancel()
		for {
			// Re-check the cache inside the flight: a concurrent caller may
			// have populated it before this goroutine became the leader.
			if v, ok := a.state.bearers.Load(cacheKey); ok {
				cached := v.(cachedSession)
				if a.bearerCacheFresh(cacheKey, cached) {
					return cloneUserInfoWithGeneration(cached.user, cached.gen), nil
				}
			}

			validationGen := a.state.revocationGen.Load()
			u, err := a.tokenValidator.ValidateBearer(workCtx, bearer)
			if err != nil {
				return nil, err
			}

			a.state.revocationMu.Lock()
			if a.bearerRevoked(cacheKey, u, validationGen) {
				a.state.revocationMu.Unlock()
				return nil, connect.NewError(connect.CodeUnauthenticated, ErrTokenRevoked)
			}
			if bearerRevokedAfter(&a.state.bearerInvalidations, cacheKey.bearerRef(), validationGen) {
				a.state.revocationMu.Unlock()
				continue
			}
			if userInvalidatedAfter(&a.state.userInvalidations, u.ID.String(), validationGen) {
				a.state.revocationMu.Unlock()
				continue
			}
			cached := cachedSession{user: u, cachedAt: time.Now(), gen: validationGen}
			a.state.bearers.Store(cacheKey, cached)
			a.state.indexBearerCacheEntry(cacheKey, cached)
			a.state.revocationMu.Unlock()
			return cloneUserInfoWithGeneration(u, validationGen), nil
		}
	})
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, false, result.Err
		}
		// Every follower that collapsed into this flight shares the leader's
		// single *UserInfo. Hand each caller its own clone so the value the
		// cache-hit and DB-validate paths already isolate per caller is never
		// aliased across concurrent requests (the cookie path mutates the ctx
		// user in touchSession; keep the bearer path one refactor away from a
		// silent cross-request race, not on it). The generation was already
		// stamped by the leader, so re-cloning preserves it.
		info := result.Val.(*UserInfo)
		return cloneUserInfoWithGeneration(info, info.AuthGeneration), true, nil
	}
}

type bearerCacheKeyParts struct {
	kind       BearerKind
	tokenID    string
	secretHash string
}

func bearerCacheKey(kind BearerKind, tokenID string, secretHash []byte) bearerCacheKeyParts {
	return bearerCacheKeyParts{
		kind:       kind,
		tokenID:    tokenID,
		secretHash: string(secretHash),
	}
}

func (k bearerCacheKeyParts) flightKey() string {
	return fmt.Sprintf("%c:%d:%s:%x", byte(k.kind), len(k.tokenID), k.tokenID, k.secretHash)
}

// bearerRef is the reverse-index key for this cache entry's bearer row. Deriving
// it in one place keeps the cache key and the index key from disagreeing on how
// a bearer is keyed (an entry indexed under a bearer it can't be found by).
func (k bearerCacheKeyParts) bearerRef() BearerRef {
	return NewBearerRef(k.kind, k.tokenID)
}

// cacheEntryFresh reports whether cs is still inside its cache window: the TTL
// did not elapse AND the credential's own validity is still current. The
// per-credential revocation / invalidation checks are kind-specific and stay
// with each caller; sharing this prefix keeps the bearer and session hot paths
// from disagreeing on what "within the cache window" means.
func cacheEntryFresh(cs cachedSession) bool {
	return time.Since(cs.cachedAt) < sessionCacheTTL && cs.user.CredentialCurrent(time.Now())
}

// bearerCacheFresh returns true when cs is within TTL and no matching
// revocation or user-cache invalidation followed its validation.
func (a *authInterceptor) bearerCacheFresh(key bearerCacheKeyParts, cs cachedSession) bool {
	if !cacheEntryFresh(cs) {
		return false
	}
	// Lock-free: the revocation/invalidation marks live in concurrency-safe
	// sync.Maps and generations only advance, so a cache-hit freshness read
	// needs no exclusive lock. revocationMu guards the write/evict paths that
	// publish a mark and drain leases together; those are unaffected here. The
	// "serve one request racing a concurrent revocation" window is inherent
	// (cs was loaded before this check) and equally present with the lock, so
	// dropping it removes global-mutex contention from the hot auth path
	// without widening that window.
	return !a.bearerRevoked(key, cs.user, cs.gen) &&
		!bearerRevokedAfter(&a.state.bearerInvalidations, key.bearerRef(), cs.gen) &&
		!userInvalidatedAfter(&a.state.userInvalidations, cs.user.ID.String(), cs.gen)
}

// validateTokenCached returns cached UserInfo if the session was validated
// within sessionCacheTTL AND no revocation occurred since; otherwise
// queries the DB and caches the result.
func (a *authInterceptor) validateTokenCached(ctx context.Context, token string) (*UserInfo, error) {
	if v, ok := a.state.sessions.Load(token); ok {
		cached := v.(cachedSession)
		if a.sessionCacheFresh(token, cached) {
			return cloneUserInfoWithGeneration(cached.user, cached.gen), nil
		}
		a.state.revocationMu.Lock()
		a.state.deleteStaleSession(token, cached)
		a.state.revocationMu.Unlock()
	}

	for {
		validationGen := a.state.revocationGen.Load()
		userInfo, err := ValidateToken(ctx, a.store, token)
		if err != nil {
			return nil, err
		}

		a.state.revocationMu.Lock()
		if a.sessionRevoked(token, userInfo, validationGen) {
			a.state.revocationMu.Unlock()
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("session revoked"))
		}
		if userInvalidatedAfter(&a.state.userInvalidations, userInfo.ID.String(), validationGen) {
			a.state.revocationMu.Unlock()
			continue
		}
		a.state.sessions.Store(token, cachedSession{user: userInfo, cachedAt: time.Now(), gen: validationGen})
		indexSyncMap(&a.state.userSessions, userInfo.ID.String(), token)
		a.state.revocationMu.Unlock()

		return cloneUserInfoWithGeneration(userInfo, validationGen), nil
	}
}

func (a *authInterceptor) sessionCacheFresh(sessionID string, cached cachedSession) bool {
	if !cacheEntryFresh(cached) {
		return false
	}
	// Lock-free for the same reason as bearerCacheFresh: monotonic marks in
	// concurrency-safe sync.Maps, read off the hot auth path without revocationMu.
	return !a.sessionRevoked(sessionID, cached.user, cached.gen) &&
		!userInvalidatedAfter(&a.state.userInvalidations, cached.user.ID.String(), cached.gen)
}

// sessionRevoked and bearerRevoked are pure reads over the revocation-mark
// sync.Maps. They are safe to call without revocationMu (the maps are
// concurrency-safe and generations are monotonic); the write/evict paths call
// them while holding the lock only because the surrounding publish-then-store
// section must be atomic, not because the read itself needs the lock.
func (a *authInterceptor) sessionRevoked(sessionID string, user *UserInfo, validationGen uint64) bool {
	return revokedAfter(&a.state.sessionRevocations, sessionID, validationGen) ||
		userRevokedAfter(&a.state.userRevocations, user, validationGen)
}

func (a *authInterceptor) bearerRevoked(key bearerCacheKeyParts, user *UserInfo, validationGen uint64) bool {
	return bearerRevokedAfter(&a.state.bearerRevocations, key.bearerRef(), validationGen) ||
		userRevokedAfter(&a.state.userRevocations, user, validationGen)
}

// sessionTouchThreshold caps how long the Hub waits before it slides a session
// again. touchThreshold shortens it for a session whose whole life is shorter,
// so this constant is the ceiling and not the interval itself.
const sessionTouchThreshold = 5 * time.Minute

// touchThreshold is this interceptor's slide interval. It is also the longest
// that a session row can outlive its last touch, which is the span the mark
// sweep cutoff measures.
func (a *authInterceptor) touchThreshold(p Policy) time.Duration {
	return touchThreshold(p.SessionDuration)
}

// slideDuration is how far past now a touch writes the expiry. See
// sessionExpiry, which the login path uses for the same span.
func (a *authInterceptor) slideDuration(p Policy) time.Duration {
	return sessionLifetime(p.SessionDuration) + a.touchThreshold(p)
}

// touchSession slides the session expiry to now + slideDuration if the last
// touch was more than touchThreshold ago. An in-memory map throttles the
// check so that most requests skip the DB call entirely — only the first
// request after the threshold elapses hits SQLite.
//
// Returns the new expiry, or the zero time when nothing slid. The caller
// re-issues the session cookie with that expiry. The cookie carries its own
// Expires attribute, so a browser drops it at the deadline that the last
// Set-Cookie specified; without the re-issue the slide extends a row that the
// browser stops sending a cookie for, and the user is signed out
// one session duration after the login however active they were.
func (a *authInterceptor) touchSession(ctx context.Context, sessionID string, user *UserInfo, p Policy) time.Time {
	now := time.Now()
	threshold := a.touchThreshold(p)
	if v, ok := a.state.lastTouch.Load(sessionID); ok {
		if now.Sub(v.(time.Time)) < threshold {
			return time.Time{}
		}
	}

	// Record the attempt before the UPDATE runs, and whatever the UPDATE
	// answers: lastTouch is the per-process DB rate-limiter. A zero-row match
	// means the session was already touched within the threshold, and an error
	// means the store failed -- in both cases the next request must wait
	// rather than retry at once, which a store that keeps failing would turn
	// into one UPDATE for every authenticated request.
	a.state.lastTouch.Store(sessionID, now)

	newExpiry := now.Add(a.slideDuration(p)).UTC()
	// The trailing instant is the hub clock that judges the previous
	// expiry, so an expired session cannot match and revive.
	rows, err := a.store.Sessions().Touch(ctx, store.TouchSessionParams{
		ID:           sessionID,
		ExpiresAt:    newExpiry,
		LastActiveAt: now.Add(-threshold).UTC(),
	}, now.UTC())
	if err != nil {
		// Nothing above this call reports the failure, and the caller cannot
		// tell it apart from a throttled request. Log it, or a store that
		// refuses every touch signs each user out at their login deadline with
		// no operator-visible cause.
		slog.WarnContext(ctx, "failed to slide session expiry", "error", err)
		return time.Time{}
	}
	if rows == 0 {
		// The conditional UPDATE (WHERE last_active_at < threshold) matched no
		// row, so the DB expiry was NOT advanced -- most commonly on a freshly
		// (re)started Hub whose lastTouch map is empty but whose session was
		// touched moments ago. Do not slide the in-memory credential / lease /
		// channel deadlines past the un-advanced DB value, and do not re-issue a
		// cookie for a deadline that the row does not carry; a later touch past
		// the threshold advances DB, memory, and cookie together.
		return time.Time{}
	}
	// The slid DB expiry is authoritative and finite; carry it as a deadline for
	// the in-memory credential / lease / channel paths.
	newDeadline := DeadlineAt(newExpiry)
	if user != nil {
		user.CredentialExpiresAt = newDeadline
	}
	a.state.revocationMu.Lock()
	if value, ok := a.state.sessions.Load(sessionID); ok {
		cached := value.(cachedSession)
		if cached.user != nil {
			cached.user = cloneUserInfoWithGeneration(cached.user, cached.gen)
			cached.user.CredentialExpiresAt = newDeadline
			a.state.sessions.Store(sessionID, cached)
		}
	}
	// The session slid forward, so its long-lived connections must not be
	// torn down at the stale connect-time deadline: extend their leases here
	// (under the auth lock) and their channel expiries below (outside it, so
	// we never hold revocationMu across the channel-manager lock).
	a.state.renewLeasesLocked(a.state.leasesBySession[sessionID], newDeadline)
	a.state.revocationMu.Unlock()
	if rp := a.state.channelRescheduler.Load(); rp != nil {
		(*rp).RescheduleExpiryBySession(sessionID, newDeadline)
	}
	return newExpiry
}

const touchSweepInterval = 10 * time.Minute

// sweepCachesOnce removes stale entries from the lastTouch, session, and bearer
// caches and from the revocation/invalidation mark maps. It removes touch
// entries older than touchThreshold because the throttle is the only thing that
// reads them, and it treats an entry older than one window as absent: the
// deletion changes no decision, and holding the entry for the session's whole
// life retains one map entry per session that the process ever authenticated.
// It removes session and bearer cache entries older than sessionCacheTTL to
// avoid unlimited growth, and it sweeps revocation/invalidation marks older
// than slideDuration under revocationMu -- those must outlive the request
// snapshots that they invalidate, which is the session's life and not the
// throttle window.
//
// The session/bearer cache SCAN runs lock-free (like lastTouch and bearerExpiries
// above) so the O(cache size) walk does not stall every hot-path acquirer of
// revocationMu for its whole duration; only the far smaller delete+reverse-index
// unlink is done under the lock. The mark maps stay fully under the lock (their
// delete is unconditional -- see below).
func (a *authInterceptor) sweepCachesOnce() {
	now := time.Now()
	touchCutoff := now.Add(-a.touchThreshold(a.currentPolicy()))
	a.state.lastTouch.Range(func(key, value any) bool {
		if value.(time.Time).Before(touchCutoff) {
			a.state.deleteStaleLastTouch(key, value)
		}
		return true
	})
	// Drop recorded bearer extensions whose deadline is in the past. A zero
	// (never-expires) entry is retained until the bearer is evicted; it cannot be
	// aged out by time. bearerExpiries is not guarded by revocationMu, so this
	// sweeps outside the lock -- and must CompareAndDelete (not Delete) the exact
	// value it observed: a concurrent BearerRotatedExtending can RecordBearerExpiry
	// a fresh future deadline for the same ref between this Range callback reading
	// `value` and the delete, and an unconditional Delete would drop that new
	// extension, leaving a channel opening in the validate->index window to arm at
	// the stale connect-time deadline and be torn down early. This mirrors the
	// CompareAndDelete guard the sibling session/bearer stale-eviction paths use.
	a.state.bearerExpiries.Range(func(key, value any) bool {
		if at, ok := value.(CredentialDeadline).At(); ok && at.Before(now) {
			a.state.bearerExpiries.CompareAndDelete(key, value)
		}
		return true
	})

	// Scan the session and bearer caches for stale entries WITHOUT holding
	// revocationMu, collecting only the (key, snapshot) pairs to evict. Reading
	// cachedAt off a value observed during a sync.Map.Range is race-free: a
	// cachedSession is always replaced wholesale (touchSession stores a fresh copy,
	// never mutates one in place), so the snapshot is immutable. The delete itself
	// must run under the lock, so the locked phase runs it: deleteStale*
	// unlinks the reverse index via unindexSyncMap, whose nested empty-bucket
	// cleanup is not safe concurrently with a registration's indexSyncMap writes.
	// The CAS inside deleteStale* makes a concurrent refresh between this scan and
	// the delete a safe no-op -- the refreshed entry survives.
	type staleEntry struct {
		key    any
		cached cachedSession
	}
	var staleSessions, staleBearers []staleEntry
	a.state.sessions.Range(func(key, value any) bool {
		if cached := value.(cachedSession); now.Sub(cached.cachedAt) > sessionCacheTTL {
			staleSessions = append(staleSessions, staleEntry{key, cached})
		}
		return true
	})
	a.state.bearers.Range(func(key, value any) bool {
		if cached := value.(cachedSession); now.Sub(cached.cachedAt) > sessionCacheTTL {
			staleBearers = append(staleBearers, staleEntry{key, cached})
		}
		return true
	})

	a.state.revocationMu.Lock()
	for _, e := range staleSessions {
		a.state.deleteStaleSession(e.key, e.cached)
	}
	for _, e := range staleBearers {
		a.state.deleteStaleBearer(e.key.(bearerCacheKeyParts), e.cached)
	}
	// Revocation/invalidation marks stay swept under the lock: sweepRevocationMarks
	// uses an unconditional Delete (no CAS), so a lock-free scan could drop a fresh
	// re-revocation mark stored for the same key in the scan->delete gap -- a
	// security regression. These maps are also far smaller than the caches.
	revocationCutoff := now.Add(-a.slideDuration(a.currentPolicy()))
	sweepRevocationMarks(&a.state.sessionRevocations, revocationCutoff)
	sweepRevocationMarks(&a.state.userRevocations, revocationCutoff)
	sweepRevocationMarks(&a.state.userInvalidations, revocationCutoff)
	sweepRevocationMarks(&a.state.bearerRevocations, revocationCutoff)
	sweepRevocationMarks(&a.state.bearerInvalidations, revocationCutoff)
	a.state.revocationMu.Unlock()
}
