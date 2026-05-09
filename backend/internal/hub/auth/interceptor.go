package auth

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/singleflight"

	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
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
// Procedure names come from the generated `leapmuxv1connect` constants
// so a rename in the proto definition turns a typo here into a build
// error instead of a silent auth bypass.
var publicProcedures = map[string]bool{
	leapmuxv1connect.AuthServiceLoginProcedure:                 true,
	leapmuxv1connect.AuthServiceSignUpProcedure:                true,
	leapmuxv1connect.AuthServiceGetSystemInfoProcedure:         true,
	leapmuxv1connect.OrgServiceCheckOrgExistsProcedure:         true,
	leapmuxv1connect.WorkerConnectorServiceRegisterProcedure:   true,
	leapmuxv1connect.WorkerConnectorServiceConnectProcedure:    true,
	leapmuxv1connect.AuthServiceGetOAuthProvidersProcedure:     true,
	leapmuxv1connect.AuthServiceGetPendingOAuthSignupProcedure: true,
	leapmuxv1connect.AuthServiceCompleteOAuthSignupProcedure:   true,
}

// unverifiedAllowedProcedures lists RPC procedures that authenticated
// users may call before their email is verified. The verify endpoint
// itself must be in this list — otherwise an unverified user couldn't
// complete verification.
var unverifiedAllowedProcedures = map[string]bool{
	leapmuxv1connect.AuthServiceGetCurrentUserProcedure:          true,
	leapmuxv1connect.AuthServiceLogoutProcedure:                  true,
	leapmuxv1connect.UserServiceRequestEmailChangeProcedure:      true,
	leapmuxv1connect.UserServiceVerifyEmailProcedure:             true,
	leapmuxv1connect.UserServiceResendVerificationEmailProcedure: true,
}

// sessionCacheTTL controls how long a validated session is cached in memory.
// During this window, repeated requests skip the DB query entirely. Session
// revocation (logout) evicts the entry immediately via SessionCache.Evict.
const sessionCacheTTL = 30 * time.Second

// cachedSession holds a validated UserInfo with the time it was cached
// plus the SessionCache revocation generation under which the
// validation happened. A cache hit whose `gen` is older than the
// current `SessionCache.revocationGen` is treated as a miss — this
// bounds cache-staleness after a cross-process revocation to one
// revocationwatcher poll interval rather than the full sessionCacheTTL.
//
// Why bump-on-evict instead of per-token re-check on hit? A cache hit
// must not query the DB (the whole point of the cache); a generation
// counter is one atomic load. The cost is that ANY eviction
// invalidates ALL cached entries on the next hit — revocations are
// rare, so the cost is negligible.
type cachedSession struct {
	user     *UserInfo
	cachedAt time.Time
	gen      uint64
}

// authInterceptor implements connect.Interceptor to validate session cookies
// on both unary and streaming RPCs.
type authInterceptor struct {
	store                     store.Store
	secureCookie              bool
	emailVerificationRequired bool
	soloUser                  *UserInfo
	tokenValidator            *TokenValidator
	lastTouch                 sync.Map // sessionID → time.Time of last DB touch
	sessionCache              sync.Map // sessionID → cachedSession
	userSessions              sync.Map // userID → *sync.Map (sessionID → struct{})
	bearerCache               sync.Map // tokenID → cachedSession
	// revocationGen is bumped on every Evict* call. Cache entries store
	// the generation under which they were validated; reads compare to
	// the current generation and treat older entries as misses. Shared
	// pointer between authInterceptor (reads) and SessionCache (writes).
	revocationGen *atomic.Uint64
	// bearerFlight collapses concurrent ValidateBearer calls for the
	// same tokenID into one DB hit. Without it, a long-lived agent
	// firing N concurrent RPCs right after a cache miss pays N DB
	// round-trips for the same bearer; the followers wait on the first
	// flight's result and write the cache once. Matters most for
	// delegation tokens that authenticate dozens of concurrent inner-
	// RPCs over a fresh Noise session.
	bearerFlight singleflight.Group
}

// NewInterceptor creates a ConnectRPC interceptor that validates session cookies
// and attaches user info to the context. Public procedures (login, worker
// registration) are exempt from auth checks. Pass a non-nil soloUser to enable
// solo mode, in which all requests are automatically authenticated as that
// user; pass nil for normal multi-user auth.
//
// The returned SessionCache can be used to evict entries from the in-memory
// touch throttle (e.g., on logout). Call SessionCache.Stop to terminate the
// background sweep goroutine.
func NewInterceptor(st store.Store, soloUser *UserInfo, secureCookie bool, emailVerificationRequired bool) (connect.Interceptor, *SessionCache) {
	return NewInterceptorWithTokens(st, soloUser, nil, secureCookie, emailVerificationRequired)
}

// NewInterceptorWithTokens is the variant that wires in a TokenValidator
// so Authorization: Bearer lmx_... credentials are accepted alongside
// the cookie path. Pass nil for tokenValidator to keep cookie-only
// behaviour (tests that don't need bearer auth).
func NewInterceptorWithTokens(st store.Store, soloUser *UserInfo, tokenValidator *TokenValidator, secureCookie bool, emailVerificationRequired bool) (connect.Interceptor, *SessionCache) {
	gen := &atomic.Uint64{}
	a := &authInterceptor{
		store:                     st,
		secureCookie:              secureCookie,
		emailVerificationRequired: emailVerificationRequired,
		soloUser:                  soloUser,
		tokenValidator:            tokenValidator,
		revocationGen:             gen,
	}
	sweepCtx, cancel := context.WithCancel(context.Background())
	sc := &SessionCache{
		touch:         &a.lastTouch,
		sessions:      &a.sessionCache,
		userSessions:  &a.userSessions,
		bearer:        &a.bearerCache,
		revocationGen: gen,
		cancel:        cancel,
	}
	periodic.Start(sweepCtx, periodic.Schedule{Interval: touchSweepInterval, SkipFirstRun: true}, func(context.Context) {
		a.sweepCachesOnce()
	})
	return a, sc
}

// SessionCache provides eviction access to the interceptor's in-memory
// session caches.
type SessionCache struct {
	touch         *sync.Map
	sessions      *sync.Map
	userSessions  *sync.Map // userID → *sync.Map (sessionID → struct{})
	bearer        *sync.Map // tokenID → cachedSession
	revocationGen *atomic.Uint64
	cancel        context.CancelFunc
}

// bumpGen advances the revocation generation so cache entries written
// before this call are treated as stale on the next hit. Safe on a nil
// receiver.
func (c *SessionCache) bumpGen() {
	if c == nil || c.revocationGen == nil {
		return
	}
	c.revocationGen.Add(1)
}

// EvictBearer drops a cached bearer-token validation by token id.
// Required so token revocation is immediate, not lagged by the cache TTL.
func (c *SessionCache) EvictBearer(tokenID string) {
	if c == nil || c.bearer == nil {
		return
	}
	c.bearer.Delete(tokenID)
	c.bumpGen()
}

// EvictBearersByUserID drops every cached bearer-token validation
// belonging to a user. Called from user-revocation paths (logout,
// password change, account deactivation) so freshly revoked
// delegation/api tokens stop validating from the in-memory cache
// immediately rather than after sessionCacheTTL.
func (c *SessionCache) EvictBearersByUserID(userID string) {
	if c == nil || c.bearer == nil || userID == "" {
		return
	}
	c.bearer.Range(func(key, value any) bool {
		cs, ok := value.(cachedSession)
		if ok && cs.user != nil && cs.user.ID == userID {
			c.bearer.Delete(key)
		}
		return true
	})
	c.bumpGen()
}

// Evict removes a session from all in-memory caches. Call this on logout
// or when cached user state becomes stale (e.g., after email verification).
// Safe to call on a nil receiver.
func (c *SessionCache) Evict(sessionID string) {
	if c == nil {
		return
	}
	if v, ok := c.sessions.LoadAndDelete(sessionID); ok {
		unindexSession(c.userSessions, v.(cachedSession).user.ID, sessionID)
	}
	c.touch.Delete(sessionID)
	c.bumpGen()
}

// unindexSession removes sessionID from the user's inner sessions map, and
// deletes the outer userSessions entry if the inner map becomes empty.
func unindexSession(userSessions *sync.Map, userID, sessionID string) {
	idx, ok := userSessions.Load(userID)
	if !ok {
		return
	}
	inner := idx.(*sync.Map)
	inner.Delete(sessionID)
	if isSyncMapEmpty(inner) {
		userSessions.Delete(userID)
	}
}

// isSyncMapEmpty reports whether m has no entries. sync.Map has no Len(), so
// we probe with Range and break on the first element.
func isSyncMapEmpty(m *sync.Map) bool {
	empty := true
	m.Range(func(_, _ any) bool { empty = false; return false })
	return empty
}

// EvictByUserID removes all cached sessions for a user. Call this after
// password changes or admin password resets to ensure invalidated sessions
// cannot be served from the cache. Safe to call on a nil receiver.
//
// Bumps the revocation generation unconditionally so a caller asking to
// invalidate a user's caches gets a stale-read shield even when the
// inner sessions map happens to be empty at the moment of the call —
// matching the contract the other Evict* methods follow.
func (c *SessionCache) EvictByUserID(userID string) {
	if c == nil {
		return
	}
	defer c.bumpGen()
	v, ok := c.userSessions.LoadAndDelete(userID)
	if !ok {
		return
	}
	v.(*sync.Map).Range(func(key, _ any) bool {
		sessionID := key.(string)
		c.touch.Delete(sessionID)
		c.sessions.Delete(sessionID)
		return true
	})
}

// Stop terminates the background sweep goroutine. Safe to call multiple times
// and on a nil receiver (matches Evict/EvictByUserID).
func (c *SessionCache) Stop() {
	if c == nil {
		return
	}
	c.cancel()
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := a.authenticate(ctx, req.Spec().Procedure, req.Header().Get("Cookie"), req.Header().Get("Authorization"))
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := a.authenticate(ctx, conn.Spec().Procedure, conn.RequestHeader().Get("Cookie"), conn.RequestHeader().Get("Authorization"))
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// authenticate validates the session and attaches user info to the context.
// Public procedures pass through with optional solo-mode user. Authenticated
// requests are checked for email verification when required. Bearer tokens
// (Authorization: Bearer lmx_...) are accepted alongside the cookie path.
func (a *authInterceptor) authenticate(ctx context.Context, procedure, cookieHeader, authHeader string) (context.Context, error) {
	if publicProcedures[procedure] {
		if a.soloUser != nil {
			ctx = WithUser(ctx, a.soloUser)
		}
		return ctx, nil
	}

	if a.soloUser != nil {
		return WithUser(ctx, a.soloUser), nil
	}

	if userInfo, ok, err := a.tryAuthenticateBearer(ctx, authHeader); err != nil {
		return ctx, err
	} else if ok {
		ctx = WithUser(ctx, userInfo)
		if a.emailVerificationRequired && !userInfo.IsAdmin && !userInfo.EmailVerified {
			if !unverifiedAllowedProcedures[procedure] {
				return ctx, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("email verification required"))
			}
		}
		return ctx, nil
	}

	token := SessionIDFromHeader(cookieHeader, a.secureCookie)
	if token == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	userInfo, err := a.validateTokenCached(ctx, token)
	if err != nil {
		return ctx, err
	}
	userInfo.SessionID = token

	a.touchSession(ctx, token)

	ctx = WithUser(ctx, userInfo)

	if a.emailVerificationRequired && !userInfo.IsAdmin && !userInfo.EmailVerified {
		if !unverifiedAllowedProcedures[procedure] {
			return ctx, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("email verification required"))
		}
	}

	return ctx, nil
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

	tokenID := BearerID(bearer)
	if tokenID == "" {
		return nil, false, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
	}
	currentGen := a.revocationGen.Load()
	if v, ok := a.bearerCache.Load(tokenID); ok {
		cached := v.(cachedSession)
		if a.bearerCacheFresh(cached, currentGen) {
			return cached.user, true, nil
		}
		a.bearerCache.Delete(tokenID)
	}

	// Collapse concurrent misses for the same tokenID into one
	// ValidateBearer call. The follower goroutines wait on the leader
	// and get the same (info, err); only one DB round-trip fires.
	info, err, _ := a.bearerFlight.Do(tokenID, func() (any, error) {
		// Re-check the cache inside the flight: a concurrent caller
		// may have populated it between our earlier Load and arriving
		// here.
		flightGen := a.revocationGen.Load()
		if v, ok := a.bearerCache.Load(tokenID); ok {
			cached := v.(cachedSession)
			if a.bearerCacheFresh(cached, flightGen) {
				return cached.user, nil
			}
		}
		u, err := a.tokenValidator.ValidateBearer(ctx, bearer)
		if err != nil {
			return nil, err
		}
		// Capture the generation AFTER ValidateBearer so a concurrent
		// EvictBearer racing with ValidateBearer is observed: the post-
		// validate Load will see the bumped generation and overwrite
		// our stored entry on the next hit.
		a.bearerCache.Store(tokenID, cachedSession{user: u, cachedAt: time.Now(), gen: a.revocationGen.Load()})
		return u, nil
	})
	if err != nil {
		return nil, false, err
	}
	return info.(*UserInfo), true, nil
}

// bearerCacheFresh returns true when `cs` is still within TTL AND was
// validated at-or-after the current revocation generation. Pulling the
// check into a method keeps the dual cache-read sites (outside and
// inside the flight) in sync.
func (a *authInterceptor) bearerCacheFresh(cs cachedSession, currentGen uint64) bool {
	if time.Since(cs.cachedAt) >= sessionCacheTTL {
		return false
	}
	return cs.gen >= currentGen
}

// validateTokenCached returns cached UserInfo if the session was validated
// within sessionCacheTTL AND no revocation has occurred since; otherwise
// queries the DB and caches the result.
func (a *authInterceptor) validateTokenCached(ctx context.Context, token string) (*UserInfo, error) {
	currentGen := a.revocationGen.Load()
	if v, ok := a.sessionCache.Load(token); ok {
		cached := v.(cachedSession)
		if time.Since(cached.cachedAt) < sessionCacheTTL && cached.gen >= currentGen {
			return cached.user, nil
		}
		a.sessionCache.Delete(token)
	}

	userInfo, err := ValidateToken(ctx, a.store, token)
	if err != nil {
		return nil, err
	}

	a.sessionCache.Store(token, cachedSession{user: userInfo, cachedAt: time.Now(), gen: a.revocationGen.Load()})

	// Register in user → sessions index for efficient EvictByUserID.
	idx, _ := a.userSessions.LoadOrStore(userInfo.ID, &sync.Map{})
	idx.(*sync.Map).Store(token, struct{}{})

	return userInfo, nil
}

const sessionTouchThreshold = 5 * time.Minute

// touchSession extends the session expiry by SessionDuration if the last
// touch was more than sessionTouchThreshold ago. An in-memory map gates the
// check so that most requests skip the DB call entirely — only the first
// request after the threshold elapses hits SQLite.
func (a *authInterceptor) touchSession(ctx context.Context, sessionID string) {
	now := time.Now()
	if v, ok := a.lastTouch.Load(sessionID); ok {
		if now.Sub(v.(time.Time)) < sessionTouchThreshold {
			return
		}
	}

	newExpiry := now.Add(SessionDuration).UTC()
	threshold := now.Add(-sessionTouchThreshold).UTC()
	if err := a.store.Sessions().Touch(ctx, store.TouchSessionParams{
		ID:           sessionID,
		ExpiresAt:    newExpiry,
		LastActiveAt: threshold,
	}); err == nil {
		a.lastTouch.Store(sessionID, now)
	}
}

const touchSweepInterval = 10 * time.Minute

// sweepCachesOnce removes stale entries from the lastTouch and sessionCache
// maps. Touch entries older than SessionDuration are removed since those
// sessions have expired. Session cache entries older than sessionCacheTTL
// are removed to avoid unbounded growth.
func (a *authInterceptor) sweepCachesOnce() {
	now := time.Now()
	touchCutoff := now.Add(-SessionDuration)
	a.lastTouch.Range(func(key, value any) bool {
		if value.(time.Time).Before(touchCutoff) {
			a.lastTouch.Delete(key)
		}
		return true
	})
	a.sessionCache.Range(func(key, value any) bool {
		cached := value.(cachedSession)
		if now.Sub(cached.cachedAt) > sessionCacheTTL {
			a.sessionCache.Delete(key)
			unindexSession(&a.userSessions, cached.user.ID, key.(string))
		}
		return true
	})
}
