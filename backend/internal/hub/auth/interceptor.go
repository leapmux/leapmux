package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
)

// publicProcedures lists RPC procedures that do not require authentication.
var publicProcedures = map[string]bool{
	"/leapmux.v1.AuthService/Login":                          true,
	"/leapmux.v1.AuthService/SignUp":                         true,
	"/leapmux.v1.AuthService/VerifyEmail":                    true,
	"/leapmux.v1.AuthService/GetSystemInfo":                  true,
	"/leapmux.v1.OrgService/CheckOrgExists":                  true,
	"/leapmux.v1.WorkerConnectorService/RequestRegistration": true,
	"/leapmux.v1.WorkerConnectorService/PollRegistration":    true,
	"/leapmux.v1.WorkerConnectorService/Connect":             true,
	"/leapmux.v1.AuthService/GetOAuthProviders":              true,
	"/leapmux.v1.AuthService/GetPendingOAuthSignup":          true,
	"/leapmux.v1.AuthService/CompleteOAuthSignup":            true,
}

// unverifiedAllowedProcedures lists RPC procedures that unverified users may call.
var unverifiedAllowedProcedures = map[string]bool{
	"/leapmux.v1.AuthService/VerifyEmail":        true,
	"/leapmux.v1.AuthService/GetCurrentUser":     true,
	"/leapmux.v1.AuthService/Logout":             true,
	"/leapmux.v1.UserService/RequestEmailChange": true,
	"/leapmux.v1.UserService/VerifyEmailChange":  true,
}

// authInterceptor implements connect.Interceptor to validate session cookies
// on both unary and streaming RPCs.
type authInterceptor struct {
	queries                   *db.Queries
	soloMode                  bool
	secureCookie              bool
	emailVerificationRequired bool
	soloUser                  *UserInfo
	lastTouch                 sync.Map // sessionID → time.Time of last DB touch
}

// NewInterceptor creates a ConnectRPC interceptor that validates session cookies
// and attaches user info to the context. Public procedures (login, worker
// registration) are exempt from auth checks. In solo mode, all requests are
// automatically authenticated as the admin user.
//
// The returned SessionCache can be used to evict entries from the in-memory
// touch throttle (e.g., on logout).
func NewInterceptor(q *db.Queries, soloMode bool, secureCookie bool, emailVerificationRequired bool) (connect.Interceptor, *SessionCache) {
	a := &authInterceptor{queries: q, soloMode: soloMode, secureCookie: secureCookie, emailVerificationRequired: emailVerificationRequired}
	if soloMode {
		user, err := q.GetUserByUsername(context.Background(), bootstrap.Username(soloMode))
		if err == nil {
			a.soloUser = &UserInfo{
				ID:       user.ID,
				OrgID:    user.OrgID,
				Username: user.Username,
				IsAdmin:  user.IsAdmin == 1,
			}
		}
	}
	return a, &SessionCache{m: &a.lastTouch}
}

// SessionCache provides eviction access to the interceptor's in-memory
// session touch throttle.
type SessionCache struct {
	m *sync.Map
}

// Evict removes a session from the touch cache. Call this on logout.
func (c *SessionCache) Evict(sessionID string) {
	c.m.Delete(sessionID)
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
		if a.soloMode && a.soloUser != nil {
			ctx = WithUser(ctx, a.soloUser)
		}
		return ctx, nil
	}

	if a.soloMode {
		if a.soloUser == nil {
			return ctx, connect.NewError(connect.CodeInternal, fmt.Errorf("solo mode admin user not found"))
		}
		return WithUser(ctx, a.soloUser), nil
	}

	token := SessionIDFromHeader(cookieHeader, a.secureCookie)
	if token == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	userInfo, err := ValidateToken(ctx, a.queries, token)
	if err != nil {
		return ctx, err
	}

	a.touchSession(ctx, token)

	ctx = WithUser(ctx, userInfo)

	if a.emailVerificationRequired && !userInfo.IsAdmin && !userInfo.EmailVerified {
		if !publicProcedures[procedure] && !unverifiedAllowedProcedures[procedure] {
			return ctx, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("email verification required"))
		}
	}

	return ctx, nil
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
	_ = a.queries.TouchUserSession(ctx, db.TouchUserSessionParams{
		ExpiresAt:    newExpiry,
		ID:           sessionID,
		LastActiveAt: threshold,
	})
	a.lastTouch.Store(sessionID, now)
}
