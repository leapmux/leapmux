package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/wskeepalive"
)

// wsAuthenticator holds browser endpoint options. It supplies the shared HTTP
// authentication ladder (solo -> bearer -> cookie), the authenticated lease
// bind, and the keepalive pace. UserEventsHandler and ChannelRelayHandler
// embed it so those options cannot drift between the browser endpoints.
type wsAuthenticator struct {
	store          store.Store
	soloUser       *auth.UserInfo
	secureCookie   func(context.Context) bool
	tokenValidator *auth.TokenValidator
	authLease      webSocketAuthLease
	// keepalive is how often this endpoint probes its connections. Carried
	// per handler rather than read from a package variable so parallel tests
	// cannot race each other's pace; see wskeepalive.Pace.
	keepalive wskeepalive.Pace
	// requiredScope is the one scope a credential must hold to open this
	// endpoint at all. See newWSAuthenticator.
	requiredScope leapmuxv1.Scope
}

// newWSAuthenticator builds that option set, so a handler constructor states
// what is endpoint-SPECIFIC and nothing else.
//
// The type's promise -- one place, no drift between endpoints -- was never
// stronger than two literals that a person kept identical by hand, and adding
// the keepalive field needed the identical hunk in both. A constructor makes
// the next field an edit neither endpoint can miss.
//
// requiredScope is a PARAMETER rather than a field a handler may fill later:
// a value nobody stated is the proto zero, and a new WebSocket endpoint must
// not be CONSTRUCTIBLE without stating what its caller has to hold. These two
// sockets carry the account's whole event feed and its channels to every
// machine it owns, and the Connect interceptor's scope rung does not see
// either of them -- neither is a Connect procedure.
//
// A non-grantable value PANICS at construction, matching that registrar: a bad
// entry is a boot failure rather than a refusal in production nobody notices.
func newWSAuthenticator(
	st store.Store,
	authContexts *auth.AuthContextRegistry,
	soloUser *auth.UserInfo,
	secureCookie func(context.Context) bool,
	requiredScope leapmuxv1.Scope,
) wsAuthenticator {
	if !authscope.IsGrantable(requiredScope) {
		panic("service: WebSocket endpoint states no grantable scope: " + requiredScope.String())
	}
	return wsAuthenticator{
		store:         st,
		authLease:     newWebSocketAuthLease(authContexts),
		soloUser:      soloUser,
		secureCookie:  secureCookie,
		keepalive:     wskeepalive.Production(),
		requiredScope: requiredScope,
	}
}

// authenticate resolves the caller via the shared HTTP auth ladder so both
// browser endpoints use the same credential checks. The token validator
// is optional (nil accepts cookie auth only), and the caller wires it after
// construction, so this reads it at call time.
//
// It also runs the SCOPE rung, which is the whole reason this endpoint takes a
// required scope. An unscoped credential -- a browser session, solo mode, the
// control CLI's own grant -- passes unconditionally, because a scope subtracts
// from the account's authority and never adds to it.
func (a wsAuthenticator) authenticate(r *http.Request) (*auth.UserInfo, error) {
	// PER REQUEST, because the answer is: a trusted proxy that verified HTTPS
	// for this connection turns the secure cookie name on whatever the setting
	// says, and the handshake must read the same name the Connect procedures
	// write.
	secureCookie := false
	if a.secureCookie != nil {
		secureCookie = a.secureCookie(r.Context())
	}
	user, err := auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:         a.store,
		Validator:     a.tokenValidator,
		SoloUser:      a.soloUser,
		ReadCookie:    true,
		SecureCookies: secureCookie,
		Contexts:      a.authLease.registry,
		// The hub's shared gate, so a WebSocket handshake and a Connect
		// procedure admit exactly the same solo callers.
		SoloGate: a.authLease.registry.SoloGate(),
	})
	if err != nil {
		return nil, err
	}
	if !user.Scopes.Allows(a.requiredScope) {
		token, _ := authscope.Token(a.requiredScope)
		return nil, fmt.Errorf("%w: this credential was not granted %s", auth.ErrHTTPForbidden, token)
	}
	return user, nil
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
// credential's revocation and expiry state, plus the user's connection cap.
// cleanup always releases both.
//
// A refusal closes the socket here rather than returning a status the caller
// writes, because by this point the WebSocket is already upgraded -- there is no
// HTTP response left to write. Both refusals use StatusPolicyViolation: the
// browser treats it as final and stops reconnecting, which is right for a
// credential that will not come back on its own AND for a cap that a reconnect
// loop would only reach again. They differ in the REASON, which is what the
// client reads to tell the user which one it was.
//
// That reason is the outcome's own Label(), the same token the metric counts
// the refusal under, rather than a mapping beside it. The mapping this replaced
// was an `if` over one outcome that defaulted everything else to credential
// prose, so a THIRD refusal would have reached the wire claiming to be an
// expired credential -- advice ("re-authenticate") that a client cannot act on
// for a reason nobody wrote yet. Label()'s doc carries the constraints: every
// value is a valid close reason at 123 bytes or fewer, and renaming one is a
// wire change.
func (l webSocketAuthLease) bind(
	parent context.Context,
	user *auth.UserInfo,
	conn *websocket.Conn,
) (ctx context.Context, cleanup func(), outcome auth.LeaseOutcome) {
	ctx, cancel := context.WithCancel(parent)
	// Pass the parent context for the lease's off-lock DB expiry fallback: it must
	// outlive this call (the derived ctx is the lease's own, cancelled on release).
	release, outcome := l.registry.RegisterAuthenticatedLease(parent, user, cancel)
	if outcome != auth.LeaseGranted {
		metrics.ConnectionsRefusedTotal.WithLabelValues(outcome.Label()).Inc()
		_ = conn.Close(websocket.StatusPolicyViolation, outcome.Label())
		cancel()
		return ctx, func() {}, outcome
	}
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			release()
			cancel()
		})
	}, auth.LeaseGranted
}
