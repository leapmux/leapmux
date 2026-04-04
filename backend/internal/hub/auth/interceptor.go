package auth

import (
	"context"
	"fmt"
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
}

// authInterceptor implements connect.Interceptor to validate session cookies
// on both unary and streaming RPCs.
type authInterceptor struct {
	queries      *db.Queries
	soloMode     bool
	secureCookie bool
	soloUser     *UserInfo
}

// NewInterceptor creates a ConnectRPC interceptor that validates session cookies
// and attaches user info to the context. Public procedures (login, worker
// registration) are exempt from auth checks. In solo mode, all requests are
// automatically authenticated as the admin user.
func NewInterceptor(q *db.Queries, soloMode bool, secureCookie bool) connect.Interceptor {
	a := &authInterceptor{queries: q, soloMode: soloMode, secureCookie: secureCookie}
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
	return a
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if publicProcedures[req.Spec().Procedure] {
			if a.soloMode && a.soloUser != nil {
				ctx = WithUser(ctx, a.soloUser)
			}
			return next(ctx, req)
		}

		if a.soloMode {
			if a.soloUser == nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("solo mode admin user not found"))
			}
			ctx = WithUser(ctx, a.soloUser)
			return next(ctx, req)
		}

		token := SessionIDFromHeader(req.Header().Get("Cookie"), a.secureCookie)
		if token == "" {
			return nil, connect.NewError(connect.CodeUnauthenticated, nil)
		}

		userInfo, err := ValidateToken(ctx, a.queries, token)
		if err != nil {
			return nil, err
		}

		// Sliding window: extend session expiry, throttled to once per 5 minutes.
		a.touchSession(ctx, token)

		ctx = WithUser(ctx, userInfo)
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next // Client-side streaming is not intercepted on the server.
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if publicProcedures[conn.Spec().Procedure] {
			if a.soloMode && a.soloUser != nil {
				ctx = WithUser(ctx, a.soloUser)
			}
			return next(ctx, conn)
		}

		if a.soloMode {
			if a.soloUser == nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("solo mode admin user not found"))
			}
			ctx = WithUser(ctx, a.soloUser)
			return next(ctx, conn)
		}

		token := SessionIDFromHeader(conn.RequestHeader().Get("Cookie"), a.secureCookie)
		if token == "" {
			return connect.NewError(connect.CodeUnauthenticated, nil)
		}

		userInfo, err := ValidateToken(ctx, a.queries, token)
		if err != nil {
			return err
		}

		a.touchSession(ctx, token)

		ctx = WithUser(ctx, userInfo)
		return next(ctx, conn)
	}
}

const sessionTouchThreshold = 5 * time.Minute

// touchSession extends the session expiry by 24 hours if the last activity
// was more than 5 minutes ago. This provides a sliding window without
// updating the DB on every single request.
func (a *authInterceptor) touchSession(ctx context.Context, sessionID string) {
	threshold := time.Now().Add(-sessionTouchThreshold).UTC()
	newExpiry := time.Now().Add(24 * time.Hour).UTC()
	_ = a.queries.TouchUserSession(ctx, db.TouchUserSessionParams{
		ExpiresAt:    newExpiry,
		ID:           sessionID,
		LastActiveAt: threshold,
	})
}
