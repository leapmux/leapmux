package service

import (
	"context"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// wsAuthenticator carries the inputs every WebSocket handler needs to run the
// shared HTTP auth ladder (solo -> bearer -> cookie) and bind an authenticated
// lease. Both OrgEventsHandler and ChannelRelayHandler embed it, so the auth
// option set lives in one place and cannot drift between endpoints.
type wsAuthenticator struct {
	store          store.Store
	soloUser       *auth.UserInfo
	secureCookie   bool
	tokenValidator *auth.TokenValidator
	authLease      webSocketAuthLease
}

// authenticate resolves the caller via the shared HTTP auth ladder so every WS
// endpoint stays interchangeable for credential plumbing. The token validator
// is optional (nil accepts cookie auth only) and is wired post-construction, so
// this reads it at call time.
func (a wsAuthenticator) authenticate(r *http.Request) (*auth.UserInfo, error) {
	return auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:     a.store,
		Validator: a.tokenValidator,
		SoloUser:  a.soloUser,
		Cookies:   []bool{a.secureCookie},
		Contexts:  a.authLease.registry,
	})
}

type webSocketAuthLease struct {
	registry *auth.AuthContextRegistry
}

func newWebSocketAuthLease(registry *auth.AuthContextRegistry) webSocketAuthLease {
	if registry == nil {
		panic("service: WebSocket handler requires an auth context registry")
	}
	return webSocketAuthLease{registry: registry}
}

// bind creates the connection context and atomically registers it against the
// credential's revocation and expiry state. cleanup always releases both.
func (l webSocketAuthLease) bind(
	parent context.Context,
	user *auth.UserInfo,
	conn *websocket.Conn,
) (ctx context.Context, cleanup func(), current bool) {
	ctx, cancel := context.WithCancel(parent)
	// Pass the parent context for the lease's off-lock DB expiry fallback: it must
	// outlive this call (the derived ctx is the lease's own, cancelled on release).
	release, current := l.registry.RegisterAuthenticatedLease(parent, user, cancel)
	if !current {
		_ = conn.Close(websocket.StatusPolicyViolation, "authentication expired or revoked")
		cancel()
		return ctx, func() {}, false
	}
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			release()
			cancel()
		})
	}, true
}
