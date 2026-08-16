package service

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/metrics"
)

// wsAuthenticator carries what every long-lived WebSocket endpoint needs before
// it can serve: the shared HTTP auth ladder (solo -> bearer -> cookie), the
// authenticated lease bind, the upgrade itself, and the pace its liveness probe
// runs at. Both UserEventsHandler and ChannelRelayHandler embed it, so that
// option set lives in one place and cannot drift between endpoints.
type wsAuthenticator struct {
	store          store.Store
	soloUser       *auth.UserInfo
	secureCookie   func() bool
	tokenValidator *auth.TokenValidator
	authLease      webSocketAuthLease
	// keepalive is how often this endpoint's connections are probed. Carried
	// per handler rather than read from a package variable so parallel tests
	// cannot race each other's pace; see wsKeepalivePace.
	keepalive wsKeepalivePace
}

// Endpoint names, and the only producer of these values.
//
// Each is a log field and a control-limiter label, spelled at the acceptWS call
// and again where the keepalive is armed. Two literals per endpoint is how the
// accept log and the keepalive log come to disagree about which socket they are
// describing -- the same drift metrics.go names as the reason its pool labels
// are constants rather than literals at each call site.
const (
	endpointChannel    = "channel"
	endpointUserEvents = "userevents"
)

// newWSAuthenticator builds that option set, so a handler constructor states
// what is endpoint-SPECIFIC and nothing else.
//
// The type's promise -- one place, no drift between endpoints -- was only ever
// as good as two literals kept in step by hand, and adding the keepalive field
// needed the identical hunk in both. A constructor makes the next field an edit
// neither endpoint can miss.
func newWSAuthenticator(
	st store.Store,
	authContexts *auth.AuthContextRegistry,
	soloUser *auth.UserInfo,
	secureCookie func() bool,
) wsAuthenticator {
	return wsAuthenticator{
		store:        st,
		authLease:    newWebSocketAuthLease(authContexts),
		soloUser:     soloUser,
		secureCookie: secureCookie,
		keepalive:    wsKeepaliveProduction(),
	}
}

// authenticate resolves the caller via the shared HTTP auth ladder so every WS
// endpoint stays interchangeable for credential plumbing. The token validator
// is optional (nil accepts cookie auth only) and is wired post-construction, so
// this reads it at call time.
func (a wsAuthenticator) authenticate(r *http.Request) (*auth.UserInfo, error) {
	secureCookie := false
	if a.secureCookie != nil {
		secureCookie = a.secureCookie()
	}
	return auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:     a.store,
		Validator: a.tokenValidator,
		SoloUser:  a.soloUser,
		Cookies:   []bool{secureCookie},
		Contexts:  a.authLease.registry,
	})
}

// acceptWS upgrades an authenticated request and installs everything a
// long-lived socket must have before it carries a byte: the control-frame
// limiter's hooks, the limiter's handle on the connection it may close, and the
// per-message read limit. Reports false when the upgrade failed, in which case
// it has already logged and the caller must return.
//
// One helper because the sequence was copy-pasted between the two endpoints,
// verbatim comment included, and one of its steps is silently skippable: a
// missed attach leaves the limiter's conn nil, so closeAbusive logs a flood and
// closes nothing -- the defence becomes a no-op with no signal that it did.
// acceptOptions' own doc already states this principle for the hooks ("One
// constructor so a new long-lived endpoint cannot pick up the subprotocol and
// silently miss the control-frame bound"); attach and the read limit are the
// rest of the same set, so they belong behind the same one call.
//
// The read limit is a parameter rather than a constant here because the two
// endpoints carry different payloads; naming it at the call site is what keeps
// each socket and the code reading from it from drifting apart.
func (a wsAuthenticator) acceptWS(
	w http.ResponseWriter,
	r *http.Request,
	endpoint, subprotocol string,
	readLimit int64,
	user *auth.UserInfo,
) (*websocket.Conn, bool) {
	// The library answers every inbound ping with a pong, unconditionally: a peer
	// that floods them makes the Hub write one per ping, contending with the
	// writer serving real traffic. The limiter bounds that, and unsolicited
	// pongs on the same budget.
	controlLimit := newWSControlLimiter(endpoint, user.ID.String(), nil)
	conn, err := websocket.Accept(w, r, controlLimit.acceptOptions(subprotocol))
	if err != nil {
		slog.Error("websocket upgrade failed", "endpoint", endpoint, "user_id", user.ID, "error", err)
		return nil, false
	}
	controlLimit.attach(conn)
	conn.SetReadLimit(readLimit)
	return conn, true
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
// browser treats it as terminal and stops reconnecting, which is right for a
// credential that will not come back on its own AND for a cap that a reconnect
// loop would only keep hitting. They differ in the REASON, which is what the
// client reads to tell the user which one it was.
//
// That reason is the outcome's own Label(), the same token the refusal is
// counted under, rather than a mapping beside it. The mapping this replaced was
// an `if` over one outcome that defaulted everything else to credential prose,
// so a THIRD refusal would have reached the wire claiming to be an expired
// credential -- advice ("re-authenticate") that a client cannot act on for a
// reason nobody has written yet. Label()'s doc carries the constraints: every
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
