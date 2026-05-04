package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// publicProcedures lists RPC procedures that do not require a session
// cookie. Worker registration (Register) is public from the
// session-cookie perspective because the worker process has no hub
// session — the handler validates an Authorization: Bearer <key> header
// itself. Connect is similarly authenticated by the long-lived
// auth_token in its own header.
var publicProcedures = map[string]bool{
	"/leapmux.v1.AuthService/Login":                 true,
	"/leapmux.v1.AuthService/SignUp":                true,
	"/leapmux.v1.AuthService/GetSystemInfo":         true,
	"/leapmux.v1.OrgService/CheckOrgExists":         true,
	"/leapmux.v1.WorkerConnectorService/Register":   true,
	"/leapmux.v1.WorkerConnectorService/Connect":    true,
	"/leapmux.v1.AuthService/GetOAuthProviders":     true,
	"/leapmux.v1.AuthService/GetPendingOAuthSignup": true,
	"/leapmux.v1.AuthService/CompleteOAuthSignup":   true,
}

// unverifiedAllowedProcedures lists RPC procedures that authenticated
// users may call before their email is verified. The verify endpoint
// itself must be in this list — otherwise an unverified user couldn't
// complete verification.
var unverifiedAllowedProcedures = map[string]bool{
	"/leapmux.v1.AuthService/GetCurrentUser":          true,
	"/leapmux.v1.AuthService/Logout":                  true,
	"/leapmux.v1.UserService/RequestEmailChange":      true,
	"/leapmux.v1.UserService/VerifyEmail":             true,
	"/leapmux.v1.UserService/ResendVerificationEmail": true,
}

// sessionCacheTTL controls how long a validated session is cached in memory.
// During this window, repeated requests skip the DB query entirely. Session
// revocation (logout) evicts the entry immediately via SessionCache.Evict.
const sessionCacheTTL = 30 * time.Second

// cachedSession holds a validated UserInfo with the time it was cached.
type cachedSession struct {
	user     *UserInfo
	cachedAt time.Time
}

// authInterceptor implements connect.Interceptor to validate session cookies
// on both unary and streaming RPCs.
type authInterceptor struct {
	store                     store.Store
	secureCookie              bool
	emailVerificationRequired bool
	soloUser                  *UserInfo
	lastTouch                 sync.Map // sessionID → time.Time of last DB touch
	sessionCache              sync.Map // sessionID → cachedSession
	userSessions              sync.Map // userID → *sync.Map (sessionID → struct{})
}

// NewInterceptor creates a ConnectRPC interceptor that validates session cookies
// and attaches user info to the context. Public procedures (login, worker
// registration) are exempt from auth checks. Pass a non-nil soloUser to enable
// solo mode, in which all requests are automatically authenticated as that
// user; pass nil for normal multi-user auth.
//
// The returned SessionCache can be used to evict entries from the in-memory
// touch throttle (e.g., on logout).
func NewInterceptor(st store.Store, soloUser *UserInfo, secureCookie bool, emailVerificationRequired bool) (connect.Interceptor, *SessionCache) {
	a := &authInterceptor{
		store:                     st,
		secureCookie:              secureCookie,
		emailVerificationRequired: emailVerificationRequired,
		soloUser:                  soloUser,
	}
	sc := &SessionCache{touch: &a.lastTouch, sessions: &a.sessionCache, userSessions: &a.userSessions, stop: make(chan struct{})}
	go a.sweepCaches(sc.stop)
	return a, sc
}

// SessionCache provides eviction access to the interceptor's in-memory
// session caches.
type SessionCache struct {
	touch        *sync.Map
	sessions     *sync.Map
	userSessions *sync.Map // userID → *sync.Map (sessionID → struct{})
	stop         chan struct{}
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
func (c *SessionCache) EvictByUserID(userID string) {
	if c == nil {
		return
	}
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

// Stop terminates the background sweep goroutine. Safe to call multiple times.
func (c *SessionCache) Stop() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := a.authenticate(ctx, req.Spec().Procedure, req.Header().Get("Cookie"))
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
		ctx, err := a.authenticate(ctx, conn.Spec().Procedure, conn.RequestHeader().Get("Cookie"))
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// authenticate validates the session and attaches user info to the context.
// Public procedures pass through with optional solo-mode user. Authenticated
// requests are checked for email verification when required.
func (a *authInterceptor) authenticate(ctx context.Context, procedure, cookieHeader string) (context.Context, error) {
	if publicProcedures[procedure] {
		if a.soloUser != nil {
			ctx = WithUser(ctx, a.soloUser)
		}
		return ctx, nil
	}

	if a.soloUser != nil {
		return WithUser(ctx, a.soloUser), nil
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

// validateTokenCached returns cached UserInfo if the session was validated
// within sessionCacheTTL, otherwise queries the DB and caches the result.
func (a *authInterceptor) validateTokenCached(ctx context.Context, token string) (*UserInfo, error) {
	if v, ok := a.sessionCache.Load(token); ok {
		cached := v.(cachedSession)
		if time.Since(cached.cachedAt) < sessionCacheTTL {
			return cached.user, nil
		}
		a.sessionCache.Delete(token)
	}

	userInfo, err := ValidateToken(ctx, a.store, token)
	if err != nil {
		return nil, err
	}

	a.sessionCache.Store(token, cachedSession{user: userInfo, cachedAt: time.Now()})

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

// sweepCaches periodically removes stale entries from the lastTouch and
// sessionCache maps. Touch entries older than SessionDuration are removed
// since those sessions have expired. Session cache entries older than
// sessionCacheTTL are removed to avoid unbounded growth.
// The goroutine exits when stop is closed.
func (a *authInterceptor) sweepCaches(stop <-chan struct{}) {
	ticker := time.NewTicker(touchSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
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
	}
}
