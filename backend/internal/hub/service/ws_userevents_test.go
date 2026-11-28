package service_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

func TestUserEvents_SubscriberClientIDCarriesNoSessionSecret(t *testing.T) {
	env := setupRegKeyEnv(t)
	token := env.login(t, hubtestutil.TestAdminUsername, hubtestutil.TestAdminPassword)
	interceptor, contexts := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{SessionHasher: hubtestutil.TestSecretHasher(), Store: env.store})
	mux := http.NewServeMux()
	mux.Handle(contracts.WSRouteUserEvents, service.NewUserEventsHandler(env.store, hubtestutil.TestSecretHasher(), env.registry, contexts, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()))
	server := connect.NewServer(interceptor)
	leapmuxv1connect.RegisterUserCRDTHandler(server, service.NewCRDTService(env.store, env.registry, nil, nil))
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	header := http.Header{"Cookie": []string{auth.CookieName + "=" + token}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+contracts.WSRouteUserEvents, &websocket.DialOptions{HTTPHeader: header})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	var event leapmuxv1.WatchUserEvent
	require.NoError(t, proto.Unmarshal(payload, &event))
	require.NotNil(t, event.GetInitial())
	client := leapmuxv1connect.NewUserCRDTClient(connect.NewClient(connecthttp.NewTransport(httpServer.Client(), httpServer.URL)))
	snapshot, err := client.GetMaterialized(authedReq(ctx, &leapmuxv1.GetMaterializedRequest{}, token))
	require.NoError(t, err)
	for _, clientID := range []string{event.GetInitial().GetSubscriberClientId(), snapshot.GetState().GetSubscriberClientId()} {
		require.NotEmpty(t, clientID)
		require.True(t, strings.HasPrefix(clientID, "session:"))
		assert.NotContains(t, clientID, sessionCookieSecret(token))
	}
}

// testBearer is everything a /ws/userevents test needs to dial with a Bearer
// token: the validator to hand the handler, the session cache to mount it
// against (and to evict through), and the minted credential.
type testBearer struct {
	tv           *auth.TokenValidator
	sessionCache *auth.AuthContextRegistry
	kind         auth.BearerKind
	tokenID      string
	secret       string
}

// authHeader is the Authorization header for this bearer.
func (b testBearer) authHeader() http.Header {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+auth.FormatBearer(b.kind, b.tokenID, b.secret))
	return hdr
}

func newBearerValidator(t *testing.T, st store.Store) (*auth.TokenValidator, *auth.AuthContextRegistry) {
	t.Helper()
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	_, sessionCache := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{
		SessionHasher:  hubtestutil.TestSecretHasher(),
		Store:          st,
		TokenValidator: tv,
	})
	t.Cleanup(sessionCache.Stop)
	return tv, sessionCache
}

// newDelegationBearer registers a worker, wires a token validator with its
// session cache, and mints a delegation token for `userID` -- the whole
// preamble a /ws/userevents test needs before it can dial.
func newDelegationBearer(t *testing.T, st store.Store, userID string) testBearer {
	t.Helper()
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:            workerID,
		AuthTokenHash: hubtestutil.TestSecretHasher().Hash(id.Generate()),
		RegisteredBy:  userid.MustNew(userID),
		PublicKeys:    storetest.WorkerKeys(workerkeystest.SeededWorkerFill),
	}))

	tv, sessionCache := newBearerValidator(t, st)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		GrantedScopes: "workspace:read workspace:write worker:read",
		ID:            tokenID,
		UserID:        userid.MustNew(userID),
		WorkerID:      workerID,
		SecretHash:    tv.HashSecret(secret),
		ExpiresAt:     time.Now().Add(time.Hour),
	}))
	return testBearer{
		tv:           tv,
		sessionCache: sessionCache,
		kind:         auth.BearerKindDelegation,
		tokenID:      tokenID,
		secret:       secret,
	}
}

func newScopedAPIBearer(t *testing.T, st store.Store, userID, grantedScopes string) testBearer {
	t.Helper()
	tv, sessionCache := newBearerValidator(t, st)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	expiresAt := time.Now().Add(time.Hour)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "scoped-test",
		GrantedScopes:    grantedScopes,
		Issuance:         store.APITokenIssuanceAuthorizationCode,
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        &expiresAt,
	}))
	return testBearer{
		tv:           tv,
		sessionCache: sessionCache,
		kind:         auth.BearerKindAPI,
		tokenID:      tokenID,
		secret:       secret,
	}
}

// stallAfterBytes is where a gatedListener connection stops writing: past the
// WebSocket handshake response (a few hundred bytes) and inside the bootstrap
// frame that follows it (two orders of magnitude bigger). Anything between the
// two works; this sits comfortably clear of both, and the frame-size assertion
// in the park-overflow test states the upper half of that gap as a check rather
// than a hope.
const stallAfterBytes = 4 << 10

// gatedListener hands out connections whose first post-handshake write stalls
// until the test releases it, holding the handler inside writeUserEvent for as
// long as the test needs.
//
// The parking window a park-overflow test must land its burst in ends the
// instant the bootstrap write returns. Keeping that window open with a multi-MB
// frame and a client that does not read makes its width a property of the OS's
// socket buffers: Windows' loopback absorbed the whole frame, so the write
// returned immediately, the window closed before the burst, and the test failed
// on Windows CI alone. Stalling the write in-process makes the window explicit
// and the same on every platform.
type gatedListener struct {
	net.Listener
	stalled     chan struct{} // buffered(1): a write has reached the gate
	release     chan struct{} // closed by Release to let the stalled write run
	releaseOnce sync.Once
}

func newGuardedListener(inner net.Listener) *gatedListener {
	return &gatedListener{
		Listener: inner,
		stalled:  make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
}

func (l *gatedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &gatedConn{Conn: conn, gate: l}, nil
}

// Release opens the gate. Idempotent, so a test can both release it as a step
// and register it as cleanup — which it must, or a failure before the release
// leaves the handler stalled and httptest.Server.Close waiting on it forever.
func (l *gatedListener) Release() {
	l.releaseOnce.Do(func() { close(l.release) })
}

// waitStalled blocks until a write reaches the gate.
func (l *gatedListener) waitStalled(t *testing.T) {
	t.Helper()
	select {
	case <-l.stalled:
	case <-time.After(10 * time.Second):
		t.Fatal("no write reached the write gate: the handler never got to the bootstrap frame")
	}
}

// gatedConn stalls the write that would carry the connection past
// stallAfterBytes, and every write after it, until the gate opens. The check is
// on the cumulative total INCLUDING this write, so a bootstrap frame that the
// WebSocket layer hands over in one call stalls before any of it reaches the
// socket rather than sailing through on a stale count.
type gatedConn struct {
	net.Conn
	gate    *gatedListener
	mu      sync.Mutex
	written int
}

func (c *gatedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.written += len(p)
	stall := c.written > stallAfterBytes
	c.mu.Unlock()
	if stall {
		select {
		case c.gate.stalled <- struct{}{}:
		default:
		}
		<-c.gate.release
	}
	return c.Conn.Write(p)
}

// servedOnce wraps a handler so a test can wait for ServeHTTP to RETURN, which
// is the only point at which its deferred teardown -- unsubscribe, queue close,
// pool detach -- has actually run.
//
// httptest.Server.Close is not that seam: it waits for outstanding requests, and
// a hijacked WebSocket connection is not one, so it can return while the handler
// goroutine is still unwinding. Asserting on the pool right after it read a
// half-torn-down Hub and failed intermittently.
func servedOnce(h http.Handler) (http.Handler, <-chan struct{}) {
	done := make(chan struct{})
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer once.Do(func() { close(done) })
		h.ServeHTTP(w, r)
	}), done
}

// TestUserEventsHandler_RevokingTheBearerClosesTheSubscription is the surviving
// half of a test deleted with the workspace scope axis.
//
// That test asserted three things; two were about the per-workspace narrowing
// this commit removed, but its tail was not: it pinned that evicting a bearer
// from the session cache CLOSES the `/ws/userevents` socket it authenticated.
// That property lives in `ws_userevents.go`'s authLease binding, which is
// untouched and still load-bearing -- a revoked delegation bearer left streaming
// a user's entire CRDT is the failure it prevents.
//
// Nothing replaced it: the only other eviction-closes-the-socket test dials
// `/ws/channel`, a different endpoint, so this file's endpoint had no direct
// coverage at all.
// The pool charges an opening frame only once it EXISTS, so the build itself is
// unbudgeted -- and the answer it gives a connect that arrives too late is
// retry-later, which sends that client back to build the same full-account
// snapshot again. Under a reconnect storm, which is the case the budget exists
// for, the pool's own backpressure became the driver of allocations it could not
// see: used_bytes sat under Capacity while RSS climbed.
//
// So the number of builds in flight is bounded at the connect path, BEFORE the
// allocation, which is the thing a byte charge structurally cannot do.
func TestUserEventsHandler_RefusesAConnectWhenEveryBuildSlotIsTaken(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Allowed")
	bearer := newDelegationBearer(t, st, user.ID)

	j := newMemJournal()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
				WorkspaceId: workspaceID, RootNodeId: "root-allowed",
			}
			s.Nodes["root-allowed"] = &leapmuxv1.NodeRecord{NodeId: "root-allowed"}
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	handler := service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hdr := bearer.authHeader()

	// Every slot the budget could hold a worst-case snapshot in is already
	// building one. Stated AS the bound rather than as a number beside it.
	held, release := handler.FillBootstrapGateForTest()
	require.Positive(t, held, "a pool must admit at least one concurrent build")

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err, "the upgrade still succeeds -- the refusal is a close, not a failed handshake")
	_, _, readErr := conn.Read(ctx)
	assert.Equal(t, websocket.StatusTryAgainLater, websocket.CloseStatus(readErr),
		"a connect that cannot get a build slot must be told to retry, not served")
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// ...and the gate is a gate, not a latch: a slot freed by a build that
	// finished admits the next connect.
	release()
	next, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer func() { _ = next.Close(websocket.StatusNormalClosure, "") }()
	payload, err := channelwire.ReadFramedBytes(ctx, next)
	require.NoError(t, err, "once a slot is free the connect must be served normally")
	assert.NotEmpty(t, payload)
}

func TestUserEventsHandler_RevokingTheBearerClosesTheSubscription(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Allowed")
	bearer := newDelegationBearer(t, st, user.ID)

	j := newMemJournal()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
				WorkspaceId: workspaceID, RootNodeId: "root-allowed",
			}
			s.Nodes["root-allowed"] = &leapmuxv1.NodeRecord{NodeId: "root-allowed"}
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	srv := httptest.NewServer(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hdr := bearer.authHeader()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// The subscription is live: the initial materialized state arrives, and a
	// delegation bearer sees what its user owns (no workspace narrowing any more).
	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	event := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, event))
	require.NotNil(t, event.GetInitial())
	require.Contains(t, event.GetInitial().GetWorkspaces(), workspaceID)

	bearer.sessionCache.EvictBearer(auth.NewBearerRef(auth.BearerKindDelegation, bearer.tokenID))

	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	_, err = channelwire.ReadFramedBytes(readCtx, conn)
	require.Error(t, err, "revoking the delegation bearer must close the user-event subscription")
	// DeadlineExceeded would mean the socket simply went quiet, which is the
	// failure: the lease was cancelled and the stream stayed open.
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the user-event subscription remained open after its authenticated lease was cancelled")
}

// TestUserEventsHandler_AFailedSubscribeIsStillCounted pins that
// leapmux_userevents_subscribe_total is a COMPLETE partition of connect
// outcomes, not just the successful ones.
//
// The counter was originally incremented inside an `if err == nil` guard, which
// made the failure arm invisible: a deployment whose ACL resolve started
// erroring showed a FALL in subscribe volume, indistinguishable from a fall in
// traffic, with no error series to explain it -- the metric going quiet exactly
// when an operator reaches for it. A connect that selects no bootstrap arm is
// labelled with the zero mode and reason, both of which spell themselves
// "invalid".
//
// The failure is driven through the resume tail read, which is the reachable
// hard-error return: the ACL resolve FILTERS workspaces the user cannot see
// rather than rejecting them, so a forbidden workspace_ids yields an empty
// filter and a perfectly successful connect.
func TestUserEventsHandler_AFailedSubscribeIsStillCounted(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")

	bearer := newDelegationBearer(t, st, user.ID)

	j := newMemJournal()
	j.listErr = errors.New("journal unavailable")
	// Built here rather than inside the factory so the cursor below can be
	// minted against this manager's LIVE epoch. A mismatched epoch (or a cursor
	// under the 24h retention floor) is a perfectly ordinary FALLBACK, which
	// succeeds -- and would never reach the tail read this test fails.
	mgr := crdt.NewManager(userid.MustNew(user.ID), j, allowAllAuth{}, nil, time.Now)
	require.NoError(t, mgr.Bootstrap(context.Background()))
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()
	registry := crdt.NewRegistry(func(_ context.Context, _ userid.UserID) (*crdt.Manager, error) {
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	srv := httptest.NewServer(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	before := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := bearer.authHeader()
	// A cursor is what selects the RESUME arm, whose tail read the journal above
	// fails. It must be recent enough to clear the retention floor and carry the
	// live epoch, or the connect FALLBACKs instead and succeeds.
	cursor := channelwire.EncodeResumeHLC(&leapmuxv1.HLC{
		Physical: time.Now().UnixMilli(), Logical: 0, ClientId: "c1",
	})
	conn, _, dialErr := websocket.Dial(ctx,
		"ws"+srv.URL[len("http"):]+
			"?resume_after_hlc="+cursor+
			"&resume_epoch="+strconv.FormatInt(epoch, 10),
		&websocket.DialOptions{HTTPHeader: hdr})
	if dialErr == nil {
		// The handler upgrades before subscribing, so the failure arrives as a
		// close frame rather than a failed dial.
		_, readErr := channelwire.ReadFramedBytes(ctx, conn)
		require.Error(t, readErr, "a failed subscribe must not stream a bootstrap frame")
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}

	after := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid"))
	require.Equal(t, before+1, after,
		"a failed connect must still be counted, or the metric has no denominator for failures")
}

// parkWindowEnv is a /ws/userevents connection frozen inside the parking
// window: the handler is stalled at the write gate on its bootstrap frame, the
// subscriber is registered, and every commit made through it parks.
//
// Both halves of the Release verdict -- flush and drop -- need exactly this
// setup and differ only in how many batches they commit into it, so the setup
// is one function and each test states just its burst.
type parkWindowEnv struct {
	mgr  *crdt.Manager
	gate *gatedListener
	conn *websocket.Conn
	ctx  context.Context
	// pool is this handler's private byte budget and served closes when the
	// handler has finished unwinding -- together they let a test observe what
	// the subscriber queue holds and what it gives back.
	pool   *sendq.Pool
	srv    *httptest.Server
	served <-chan struct{}
	// keepaliveTicks is this handler's probe trigger, replacing the interval
	// ticker. UNBUFFERED, which is the whole point: a send completes only when a
	// probe loop is parked on the receive, so a test can tell "a probe loop
	// exists" from "one does not" without waiting on a clock.
	keepaliveTicks chan time.Time
}

func newParkWindowEnv(t *testing.T) *parkWindowEnv {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Big")

	bearer := newDelegationBearer(t, st, user.ID)

	// Big enough that the bootstrap frame runs past the listener's write gate,
	// so the handler stalls on it with the parking window still open. It does
	// NOT have to be big enough to fill a socket buffer -- the gate, not the OS,
	// is what holds the window open (see gatedListener).
	j := newMemJournal()
	var mgr *crdt.Manager
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		m := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, m.Bootstrap(ctx))
		m.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
				WorkspaceId: workspaceID, RootNodeId: "root",
			}
			s.Nodes["root"] = &leapmuxv1.NodeRecord{
				NodeId: "root",
				Kind:   &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF},
			}
			// Padded so the bootstrap frame clears stallAfterBytes by a wide
			// margin. Every field completenessCheck requires must be present, or
			// a commit is rejected INCOMPLETE_RECORD and nothing is broadcast.
			pad := strings.Repeat("p", 512)
			for i := range 200 {
				tabID := fmt.Sprintf("tab-%05d", i)
				s.Tabs[tabID] = &leapmuxv1.TabRecord{
					TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
					TileId:       &leapmuxv1.LWWString{Value: "root"},
					WorkerId:     &leapmuxv1.LWWString{Value: "wkr"},
					Position:     &leapmuxv1.LWWString{Value: "p1"},
					FileDiffBase: &leapmuxv1.LWWString{Value: pad},
				}
			}
		})
		mgr = m
		return m, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	pool := sendq.NewMaxBytesPoolForTest()
	// Probes are driven by the test rather than by an interval, so no case here
	// is sized by a duration -- and with nobody sending, no probe ever fires,
	// which is what keeps the other tests on this env free of the keepalive
	// entirely.
	ticks := make(chan time.Time)
	handler, served := servedOnce(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, pool).
		WithTokenValidator(bearer.tv).
		WithForcedKeepaliveProbesForTest(ticks, 10*time.Second))
	srv := httptest.NewUnstartedServer(handler)
	gate := newGuardedListener(srv.Listener)
	srv.Listener = gate
	srv.Start()
	t.Cleanup(srv.Close)
	// Registered after srv.Close so it runs BEFORE it: a failure that returns
	// with the gate shut would otherwise leave Close waiting on a stalled write.
	t.Cleanup(gate.Release)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	hdr := bearer.authHeader()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	conn.SetReadLimit(64 << 20)

	// The handshake is through; the handler is now stalled at the gate on the
	// bootstrap frame, with the parking window held open for as long as the test
	// wants it. Nothing here waits on a clock or on a socket buffer.
	gate.waitStalled(t)
	require.NotNil(t, mgr, "the registry factory must have run")
	return &parkWindowEnv{
		mgr: mgr, gate: gate, conn: conn, ctx: ctx,
		pool: pool, srv: srv, served: served, keepaliveTicks: ticks,
	}
}

// commit submits one batch into the open parking window and reports whether it
// committed. SubmitInternal broadcasts on the manager goroutine before it
// answers, so a true return means the batch's frames are already parked.
func (e *parkWindowEnv) commit(t *testing.T, batchID, position string) bool {
	t.Helper()
	res, err := e.mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{{
			BatchId: batchID,
			Ops: []*leapmuxv1.CrdtOp{{
				OpId: "op-" + batchID,
				Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
					NodeId: "root",
					Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: position},
				}},
			}},
		}},
	})
	return err == nil && len(res) == 1 && res[0].GetCommitted() != nil
}

// TestUserEventsHandler_ParkedFramesFlushAfterTheBootstrapWrite is the other
// half of the Release verdict: frames that park during the bootstrap write and
// FIT in the buffer must be flushed to the client, after the bootstrap frame
// and before steady-state traffic.
//
// It guards the drop arm from over-firing. The overflow test below only asserts
// that ITS connection dies, so a regression that returned unconditionally --
// dropping every connection the instant its bootstrap frame is on the wire, and
// stalling every client that reconnects into a commit -- passes it, and passed
// every other test in this file too.
func TestUserEventsHandler_ParkedFramesFlushAfterTheBootstrapWrite(t *testing.T) {
	env := newParkWindowEnv(t)

	// One batch, far below the 256-slot cap: it parks, and nothing is dropped.
	require.True(t, env.commit(t, "solo", "z0001"), "the batch must commit, or nothing parks")
	env.gate.Release()

	payload, err := channelwire.ReadFramedBytes(env.ctx, env.conn)
	require.NoError(t, err)
	bootstrap := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, bootstrap))
	require.NotNil(t, bootstrap.GetInitial(), "the bootstrap frame comes first")

	// The parked batch follows it on the same connection -- not dropped, and not
	// re-ordered ahead of the bootstrap.
	payload, err = channelwire.ReadFramedBytes(env.ctx, env.conn)
	require.NoError(t, err, "a park buffer that did not overflow must be flushed, not dropped")
	parked := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, parked))
	require.Equal(t, "solo", parked.GetBatch().GetBatchId(),
		"the flushed frame must be the batch that parked during the write")
}

// TestUserEventsHandler_ParkOverflowDuringTheBootstrapWriteDropsTheConnection
// covers the last stretch of the parking window: the bootstrap WRITE.
//
// The manager checks Overflowed() before it builds the bootstrap frame, and the
// subscriber stays registered (and therefore parking) until Release() runs after
// that frame is on the wire. On a large account the write is the longest part of
// that window and is bounded only by relayWriteTimeout, so a burst of commits
// can fill the 256-slot park buffer inside it. Every further Send returns
// ErrSubscriberSlow, which liveCatchUpSink DISCARDS -- so before this fix the
// handler flushed whatever survived and streamed on, leaving the tab
// permanently missing that span with nothing logged and nothing to recover from.
//
// The connection must be dropped instead: every parked batch frame is strictly
// above the bootstrap's max_hlc, so the client reconnects with that cursor and
// delta-resumes exactly what was lost.
func TestUserEventsHandler_ParkOverflowDuringTheBootstrapWriteDropsTheConnection(t *testing.T) {
	env := newParkWindowEnv(t)

	// Overflow the 256-slot park buffer from inside that window.
	committed := 0
	for i := range 150 {
		if env.commit(t, fmt.Sprintf("burst-%d", i), fmt.Sprintf("z%04d", i)) {
			committed++
		}
	}
	ctx, conn, gate := env.ctx, env.conn, env.gate
	// Each committed batch broadcasts TWO frames -- the batch itself and its
	// batch_end boundary -- so the park buffer only overflows past
	// parkedFrameCap/2 commits. Stating the precondition as that arithmetic
	// rather than as a round number below it is what keeps a partially-
	// committing burst from failing at the overflow assertion instead, with a
	// diagnosis that blames the feature.
	require.Greater(t, committed*2, service.ParkedFrameCapForTest,
		"the burst must commit enough batches to overflow the park buffer, or there is nothing to drop")

	// The whole burst is parked -- SubmitInternal broadcasts on the manager
	// goroutine before it answers, so delivery is done when the loop is. Let the
	// stalled bootstrap write finish; the handler's queue.Bootstrapped(), and
	// with it the drop verdict, comes immediately after that write returns.
	gate.Release()

	// The bootstrap frame itself still arrives.
	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	require.Greater(t, len(payload), stallAfterBytes,
		"the bootstrap frame must outrun the write gate, or the handler never stalls inside it")
	event := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, event))
	require.NotNil(t, event.GetInitial(), "the bootstrap frame is written before the drop is noticed")

	// ...and then the socket CLOSES rather than flushing a park buffer with a
	// hole in it. Before the fix a parked frame arrived here instead, and the
	// dropped span was gone for the life of the connection.
	_, err = channelwire.ReadFramedBytes(ctx, conn)
	require.Error(t, err,
		"a park-buffer overflow during the bootstrap write must drop the connection, not flush over the hole")
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the socket must be closed, not merely quiet")
}

// TestUserEventsHandler_DoesNotProbeBeforeAnythingReadsTheSocket pins WHEN the
// liveness probe may be armed, which is a correctness property rather than a
// tuning one.
//
// coder/websocket processes a pong on the read path, so a probe issued while
// nothing is reading cannot see its answer: it times out and cancels the
// connection. This handler reads nothing until after the bootstrap frame is on
// the wire, and everything before that -- the ACL resolve, the resume scan or
// baseline walk, the snapshot marshal, and a write bounded only by
// relayWriteTimeout -- is the pre-bootstrap window. Arming there tore down
// perfectly healthy connections mid-bootstrap, on exactly the large accounts a
// reconnect storm hits hardest, and the operator got one Debug line for it.
//
// Both halves are asserted by SENDING on the handler's tick channel, which is
// unbuffered: a send that succeeds proves a probe loop is parked on the
// receive, and one that cannot proves none is. Nothing here waits on a clock.
func TestUserEventsHandler_DoesNotProbeBeforeAnythingReadsTheSocket(t *testing.T) {
	env := newParkWindowEnv(t)

	// The handler is stalled inside the bootstrap write, which is the last and
	// longest stretch of the window in which nothing reads this socket.
	select {
	case env.keepaliveTicks <- time.Now():
		t.Fatal("a keepalive probe loop was already armed while the handler was still inside the " +
			"bootstrap write: nothing reads the socket yet, so the probe could not see its pong " +
			"and would cancel a healthy connection mid-bootstrap")
	default:
	}

	require.True(t, env.commit(t, "parked", "z0001"), "the batch must commit, or nothing parks")
	env.gate.Release()

	payload, err := channelwire.ReadFramedBytes(env.ctx, env.conn)
	require.NoError(t, err, "the connection must survive the whole pre-bootstrap window")
	bootstrap := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, bootstrap))
	require.NotNil(t, bootstrap.GetInitial())
	payload, err = channelwire.ReadFramedBytes(env.ctx, env.conn)
	require.NoError(t, err)
	parked := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, parked))
	require.Equal(t, "parked", parked.GetBatch().GetBatchId())

	// Keep the client's read path running so it answers probes, the way a real
	// browser does. Errors end it: the connection closes at cleanup.
	go func() {
		for {
			if _, err := channelwire.ReadFramedBytes(env.ctx, env.conn); err != nil {
				return
			}
		}
	}()

	// And now the probe loop IS armed, and its probes are answered. Two ticks,
	// because the loop only comes back for the second one after the first
	// Ping RETURNED -- and a Ping that failed would exit the loop, cancel the
	// connection, and close `served` instead.
	for i := range 2 {
		select {
		case env.keepaliveTicks <- time.Now():
		case <-env.served:
			t.Fatalf("the keepalive tore the connection down at probe %d, so its pong was not read", i)
		case <-env.ctx.Done():
			t.Fatalf("no probe loop took tick %d: a streaming connection has no liveness probe at all", i)
		}
	}
}

// TestUserEventsHandler_CountsTheSuccessArmsAndTheDuration pins the label pair
// the metric is actually queried by.
//
// Only the {invalid, invalid} cell had coverage, and that cell is the one a
// dashboard never graphs. The join of Mode().Label() with Reason().String()
// happens HERE, in the service layer -- crdt's vocabulary test pins the strings
// but not which one goes in which position -- so a refactor that swapped the two
// WithLabelValues arguments, or dropped the Observe, shipped a series no
// dashboard matches with the whole suite green.
func TestUserEventsHandler_CountsTheSuccessArmsAndTheDuration(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Allowed")

	j := newMemJournal()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
				WorkspaceId: workspaceID, RootNodeId: "root",
			}
			s.Nodes["root"] = &leapmuxv1.NodeRecord{NodeId: "root"}
			// A non-zero max_hlc, so the snapshot below carries a cursor the
			// second connect can actually resume from -- decideResume refuses a
			// zero one, and the arm under test would never be reached.
			s.MaxHlc = &leapmuxv1.HLC{Physical: time.Now().UnixMilli(), Logical: 0, ClientId: "hub"}
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	bearer := newDelegationBearer(t, st, user.ID)
	srv := httptest.NewServer(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	// A first connect presents no cursor, so it takes the FALLBACK arm.
	coldBefore := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("initial", "no_cursor"))
	registryForMetrics := prometheus.NewPedanticRegistry()
	registryForMetrics.MustRegister(metrics.UserEventsSubscribeDuration)
	sampleCount := func(mode string) uint64 {
		families, err := registryForMetrics.Gather()
		require.NoError(t, err)
		var count uint64
		matched := false
		for _, family := range families {
			if family.GetName() != "leapmux_userevents_subscribe_duration_seconds" {
				continue
			}
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() != "mode" || label.GetValue() != mode {
						continue
					}
					require.False(t, matched, "duplicate histogram child for mode %s", mode)
					require.NotNil(t, metric.GetHistogram())
					matched = true
					count = metric.GetHistogram().GetSampleCount()
				}
			}
		}
		return count
	}
	initialObservations := sampleCount("initial")
	deltaObservations := sampleCount("delta")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.NoError(t, err)
	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
	event := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, event))
	require.NotNil(t, event.GetInitial(), "a cursorless connect must take the snapshot arm")
	maxHlc := event.GetInitial().GetMaxHlc()
	epoch := event.GetInitial().GetCurrentEpoch()
	_ = conn.Close(websocket.StatusNormalClosure, "")

	require.Equal(t, coldBefore+1, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("initial", "no_cursor")),
		"a cursorless connect must land in {initial, no_cursor}, not some other cell")
	require.Equal(t, initialObservations+1, sampleCount("initial"))
	require.Equal(t, deltaObservations, sampleCount("delta"))

	// ...and a connect that presents that snapshot's own cursor RESUMEs.
	require.NotNil(t, maxHlc, "fixture: the snapshot must carry a max_hlc to resume from")
	deltaBefore := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("delta", "resumed"))
	resumed, _, err := websocket.Dial(ctx,
		"ws"+srv.URL[len("http"):]+
			"?resume_after_hlc="+channelwire.EncodeResumeHLC(maxHlc)+
			"&resume_epoch="+strconv.FormatInt(epoch, 10),
		&websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.NoError(t, err)
	payload, err = channelwire.ReadFramedBytes(ctx, resumed)
	require.NoError(t, err)
	event = &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, event))
	require.NotNil(t, event.GetDelta(), "fixture: the cursor must be honored, or the label under test is not reached")
	_ = resumed.Close(websocket.StatusNormalClosure, "")

	require.Equal(t, deltaBefore+1, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("delta", "resumed")),
		"a resumed connect must land in {delta, resumed}")
	require.Equal(t, initialObservations+1, sampleCount("initial"))
	require.Equal(t, deltaObservations+1, sampleCount("delta"))
}

// TestUserEventsHandler_CountsAConnectThatNeverReachedSubscribe closes the hole
// the counter's own doc names.
//
// registry.Get is where the manager loads its state from the journal, so a hub
// whose user_state read starts failing returns BEFORE the subscribe -- and while
// that return was uncounted, the series fell to zero across the fleet with no
// error cell to explain it, which is exactly the "indistinguishable from a fall
// in traffic" pathology the mode=invalid arm was added for.
func TestUserEventsHandler_CountsAConnectThatNeverReachedSubscribe(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")

	registry := crdt.NewRegistry(func(context.Context, userid.UserID) (*crdt.Manager, error) {
		return nil, errors.New("user_state read failed")
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	bearer := newDelegationBearer(t, st, user.ID)
	srv := httptest.NewServer(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	before := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.Error(t, err, "the upgrade must fail when the manager cannot be built")

	require.Equal(t, before+1, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid")),
		"a connect that failed before the subscribe must still be counted")
}

// TestUserEventsHandler_RefusesACursorUnderANarrowedFilter pins the hub-side
// half of the narrow-mint/wide-replay invariant.
//
// The persisted cursor is per-USER and, since the checkpoint seed, cross-TAB --
// so one minted under a narrow workspace filter and replayed under a wider one
// can miss ops. The browser already refuses the pairing, but that made a
// correctness invariant the property of one client out of three; enforcing it
// here makes it unspellable for every client, including ones not written yet.
//
// REJECTED, not silently degraded to a snapshot: the malformed-cursor arm
// immediately above takes the same position, and for the same reason.
func TestUserEventsHandler_RefusesACursorUnderANarrowedFilter(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Allowed")

	j := newMemJournal()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	bearer := newDelegationBearer(t, st, user.ID)
	srv := httptest.NewServer(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	before := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cursor := channelwire.EncodeResumeHLC(&leapmuxv1.HLC{
		Physical: time.Now().UnixMilli(), Logical: 0, ClientId: "c1",
	})
	_, _, err := websocket.Dial(ctx,
		"ws"+srv.URL[len("http"):]+
			"?workspace_ids="+workspaceID+
			"&resume_after_hlc="+cursor+
			"&resume_epoch=1",
		&websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.Error(t, err, "workspace_ids together with a resume cursor must be refused, not served")
	require.Contains(t, err.Error(), "400",
		"...loudly, as a client bug, rather than degraded to a snapshot")

	// Counted like every other authenticated connect that selects no arm.
	require.Equal(t, before+1, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid")))

	// The SAME narrowed connect without a cursor is still perfectly legal --
	// the guard is on the pairing, not on narrowing.
	conn, _, err := websocket.Dial(ctx,
		"ws"+srv.URL[len("http"):]+"?workspace_ids="+workspaceID,
		&websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.NoError(t, err, "a narrowed connect with NO cursor must still be served")
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// TestUserEventsHandler_ParkedFramesAreChargedAndReturned is the end-to-end
// half of issue #361: not that subscriberQueue accounts correctly in isolation,
// but that a REAL connection charges the Hub's shared budget for what it is
// holding and gives every byte back when it goes away.
//
// The park window is what makes this observable. Inside it the handler is
// stalled on the bootstrap write while commits pile up in the queue, so there
// is a moment where the frames are unambiguously the connection's to account
// for. Before this change the same moment charged nothing at all: 320 slots
// were a frame count over payloads nothing at that layer sized.
func TestUserEventsHandler_ParkedFramesAreChargedAndReturned(t *testing.T) {
	env := newParkWindowEnv(t)
	require.Equal(t, int64(1), env.pool.Members(), "the live subscriber must be in the pool")

	// The gate holds the handler INSIDE the bootstrap write, so the frame it is
	// writing is resident right now -- and charged, which is the whole point of
	// charging it: N tabs reconnecting at once really is N of these at once, and
	// nothing else bounds how many tabs reconnect.
	bootstrapBytes := env.pool.Used()
	require.Positive(t, bootstrapBytes,
		"the bootstrap frame must be charged for as long as it is on the wire")

	// Commit into the open window. Each batch's frames park, and parking is
	// what charges them -- on top of the bootstrap frame, because that is all
	// one connection's memory.
	for i := range 20 {
		require.True(t, env.commit(t, fmt.Sprintf("b%02d", i), fmt.Sprintf("p%02d", i)))
	}
	charged := env.pool.Used()
	require.Greater(t, charged, bootstrapBytes,
		"parked frames must be charged on top of the bootstrap frame, not instead of it")
	require.Less(t, charged, env.pool.Capacity(), "and must stay inside the budget")

	// Let the bootstrap write finish; the connection then drains normally.
	env.gate.Release()
	payload, err := channelwire.ReadFramedBytes(env.ctx, env.conn)
	require.NoError(t, err)
	bootstrap := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, proto.Unmarshal(payload, bootstrap))
	require.NotNil(t, bootstrap.GetInitial())

	// Tear the connection down and wait for the handler to finish unwinding, so
	// the assertion below is about its teardown rather than about timing.
	require.NoError(t, env.conn.Close(websocket.StatusNormalClosure, ""))
	<-env.served

	assert.Zero(t, env.pool.Used(),
		"every byte a connection charged must come back when it ends -- a leak here is permanent, "+
			"and shrinks the budget every other subscriber draws on")
	assert.Zero(t, env.pool.Members(), "and the queue must leave the pool")
}

// TestUserEventsHandler_RefusesTheConnectWhenTheBudgetCannotHoldTheBootstrap
// pins the answer to a Hub that cannot charge a bootstrap frame at connect time.
//
// Serving the connect anyway is what it cannot afford: the bootstrap frame is
// the largest single thing the connection holds, it is unique to this
// subscriber so there is no sharing to soften it, and a reconnect storm is
// precisely when every tab wants one at once. Refusing is available here in a
// way it is not on /ws/channel -- the socket is already upgraded, so the Hub can
// close with a code the client reads, rather than failing a handshake the
// browser cannot read a status out of.
//
// WHICH code depends on whether a retry could ever work, and that is the whole
// point of the split below. A frame larger than the pool's entire capacity is
// refused at every occupancy, so "retry later" is advice that can only produce
// the same refusal forever -- the client rebuilds the same oversized snapshot
// every few seconds while the user watches an app that never loads. A pool that
// is merely full will drain.
func TestUserEventsHandler_RefusesTheConnectWhenTheBudgetCannotHoldTheBootstrap(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Allowed")
	bearer := newDelegationBearer(t, st, user.ID)

	j := newMemJournal()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
				WorkspaceId: workspaceID, RootNodeId: "root-allowed",
			}
			s.Nodes["root-allowed"] = &leapmuxv1.NodeRecord{NodeId: "root-allowed"}
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	// The `bound` label is the operator's half of the same split, and it has to
	// draw the line in the same place as the close code: a permanent refusal
	// counted as ordinary memory pressure puts a cell on the memory-pressure
	// dashboard that never clears, next to an occupancy graph that never spikes.
	dropped := func(bound string) float64 {
		return testutil.ToFloat64(metrics.UserEventsFramesDroppedTotal.WithLabelValues("bootstrap", bound))
	}

	for _, tt := range []struct {
		name       string
		pool       func() *sendq.Pool
		wantStatus websocket.StatusCode
		wantReason string
		wantBound  string
		otherBound string
	}{
		{
			// Smaller than any frame this account can produce. Config validation
			// rejects a pool this small, so this is the runtime shape of an
			// account whose snapshot outgrew a legitimately-configured budget.
			name: "a frame larger than the whole pool is terminal, because no retry can work",
			pool: func() *sendq.Pool {
				return sendq.NewPool(sendq.PoolConfig{Capacity: 256, MinFloor: 128, MaxFloor: 128})
			},
			wantStatus: websocket.StatusPolicyViolation,
			wantReason: contracts.CloseReasonSnapshotTooLarge,
			wantBound:  "capacity",
			otherBound: "bytes",
		},
		{
			// Room for the frame in principle, but another member is holding it
			// all. Draining is what fixes this, so the client must retry.
			name: "a merely-full pool is recoverable, because draining fixes it",
			pool: func() *sendq.Pool {
				pool := sendq.NewPool(sendq.PoolConfig{Capacity: 1 << 20, MinFloor: 512, MaxFloor: 512})
				hog := pool.AttachShared(func(error) bool { return false })
				require.Equal(t, sendq.Admitted, hog.Admit(1<<20, 1<<20), "the hog must be able to fill the pool")
				return pool
			},
			wantStatus: websocket.StatusTryAgainLater,
			wantBound:  "bytes",
			otherBound: "capacity",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool := tt.pool()
			wantBefore, otherBefore := dropped(tt.wantBound), dropped(tt.otherBound)
			// Whatever the fixture attached to create the condition; the
			// assertion below is that the REFUSED connect adds nothing to it.
			baseline := pool.Members()
			handler, served := servedOnce(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, pool).
				WithTokenValidator(bearer.tv))
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):],
				&websocket.DialOptions{HTTPHeader: bearer.authHeader()})
			require.NoError(t, err, "the upgrade itself must succeed -- the refusal is a close code, not a failed handshake")
			defer func() { _ = conn.Close(websocket.StatusInternalError, "") }()

			_, err = channelwire.ReadFramedBytes(ctx, conn)
			require.Error(t, err, "no bootstrap frame may be sent when it could not be charged")
			assert.Equal(t, tt.wantStatus, websocket.CloseStatus(err))
			if tt.wantReason != "" {
				var closeErr websocket.CloseError
				require.ErrorAs(t, err, &closeErr)
				assert.Equal(t, tt.wantReason, closeErr.Reason,
					"the client branches on this token to tell the user which of the two happened")
			}

			// And the refused connect left nothing behind: a refusal that
			// stranded its frame would shrink the budget every retry draws on,
			// so a storm would make itself permanent.
			<-served
			assert.Equal(t, baseline, pool.Members(), "a refused connect must not leave a member attached")

			assert.Equal(t, wantBefore+1, dropped(tt.wantBound),
				"the refusal must be counted under the bound that names what an operator has to do")
			assert.Equal(t, otherBefore, dropped(tt.otherBound),
				"...and not under the other, which calls for the opposite conclusion about occupancy")
		})
	}
}

// The per-user connection cap, end to end: a second connection for the same
// user is refused with a code and a reason its client can act on.
//
// The upgrade SUCCEEDS and the refusal arrives as a close frame, which is the
// only shape that works here -- a browser cannot read a status out of a failed
// WebSocket handshake, so refusing before the upgrade would tell the user
// nothing. And the reason has to be the shared token rather than prose: every
// other policy-violation close on this socket means "re-authenticate", where
// this one means "close a tab", and a client that cannot tell them apart gives
// the opposite advice.
func TestUserEventsHandler_RefusesBeyondThePerUserConnectionCap(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Allowed")
	bearer := newDelegationBearer(t, st, user.ID)
	// One connection per user, so the second dial is the one under test.
	bearer.sessionCache.SetMaxConnectionsPerUser(1)

	j := newMemJournal()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(ctx))
		mgr.SeedStateForTest(func(s *leapmuxv1.UserCrdtState) {
			s.Workspaces[workspaceID] = &leapmuxv1.WorkspaceContentsRecord{
				WorkspaceId: workspaceID, RootNodeId: "root-allowed",
			}
			s.Nodes["root-allowed"] = &leapmuxv1.NodeRecord{NodeId: "root-allowed"}
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	srv := httptest.NewServer(service.NewUserEventsHandler(st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	refusalsBefore := testutil.ToFloat64(
		metrics.ConnectionsRefusedTotal.WithLabelValues(auth.LeaseRefusedTooManyConnections.Label()))
	connectsBefore := func() float64 {
		var none crdt.SubscribeOutcome
		return testutil.ToFloat64(
			metrics.UserEventsSubscribeTotal.WithLabelValues(none.Mode().Label(), none.Reason().Label()))
	}()

	first, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):],
		&websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.NoError(t, err)
	defer func() { _ = first.Close(websocket.StatusNormalClosure, "") }()
	// Drain the bootstrap so the first subscription is genuinely established
	// before the second dial -- otherwise the cap could be racing the register.
	_, err = channelwire.ReadFramedBytes(ctx, first)
	require.NoError(t, err)

	second, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):],
		&websocket.DialOptions{HTTPHeader: bearer.authHeader()})
	require.NoError(t, err, "the upgrade must succeed -- the refusal is a close code, not a failed handshake")
	defer func() { _ = second.Close(websocket.StatusInternalError, "") }()

	_, err = channelwire.ReadFramedBytes(ctx, second)
	require.Error(t, err, "a refused connection must not receive a bootstrap frame")
	var closeErr websocket.CloseError
	require.ErrorAs(t, err, &closeErr)
	assert.Equal(t, websocket.StatusPolicyViolation, closeErr.Code,
		"the client must read this as terminal, not retry into the same refusal")
	assert.Equal(t, contracts.CloseReasonTooManyConnections, closeErr.Reason,
		"the reason is what tells the user to close a tab rather than re-authenticate")

	// The connection that was already open is untouched: the NEWEST pays.
	//
	// Asserted as "still open" rather than "still delivering": nothing is being
	// broadcast, so the honest signal is that the read blocks until ITS OWN
	// deadline instead of returning the close the evicted connection would get.
	idleCtx, idleCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer idleCancel()
	_, err = channelwire.ReadFramedBytes(idleCtx, first)
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"an established connection must survive a refused newcomer, not be evicted for it")
	assert.NotErrorAs(t, err, &closeErr,
		"and must not have been closed by the hub")

	// At LEAST one more, not exactly one: this counter is process-global and
	// TestChannelRelay_RefusesBeyondThePerUserConnectionCap drives the same
	// refusal through the same bind, under the same label. Both are t.Parallel
	// and `service`/`service_test` compile into ONE binary, so an exact delta is
	// a race -- and it would fail in the test whose whole purpose is proving the
	// cap is observable.
	assert.GreaterOrEqual(t, testutil.ToFloat64(
		metrics.ConnectionsRefusedTotal.WithLabelValues(auth.LeaseRefusedTooManyConnections.Label())),
		refusalsBefore+1,
		"the cap binding must be visible from outside")
	assert.GreaterOrEqual(t, func() float64 {
		var none crdt.SubscribeOutcome
		return testutil.ToFloat64(
			metrics.UserEventsSubscribeTotal.WithLabelValues(none.Mode().Label(), none.Reason().Label()))
	}(), connectsBefore+1,
		"a refused connect is still a connect outcome; the series is a complete partition")
}

func readWatchUserEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) *leapmuxv1.WatchUserEvent {
	t.Helper()
	evt := &leapmuxv1.WatchUserEvent{}
	require.NoError(t, channelwire.ReadProtoFrame(ctx, conn, evt))
	return evt
}

func newLayoutTestRegistry(t *testing.T, st store.Store, onFactory func()) *crdt.Registry {
	t.Helper()
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		if onFactory != nil {
			onFactory()
		}
		mgr := crdt.NewManager(want, service.NewCRDTJournal(st), service.NewCRDTAuthChecker(st), nil, time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(service.NewLifecycleOutboxReader(st)))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	return registry
}

func dialUserEvents(t *testing.T, ctx context.Context, baseURL string, bearer testBearer, opts channelwire.UserEventsOptions) *websocket.Conn {
	t.Helper()
	wsURL, err := channelwire.UserEventsURL(baseURL, opts)
	require.NoError(t, err)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
		HTTPHeader:   bearer.authHeader(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func TestUserEventsHandler_WorkersOnlyTopic(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "workers-topic-owner")
	bearer := newScopedAPIBearer(t, st, user.ID, "worker:read")

	var factoryCalls atomic.Int32
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(want, newMemJournal(), allowAllAuth{}, nil, time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	handler := service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	// Default (no topics) requires workspace:read, so a worker:read-only token gets 403.
	defaultURL, err := channelwire.UserEventsURL(srv.URL, channelwire.UserEventsOptions{})
	require.NoError(t, err)
	_, resp, err := websocket.Dial(ctx, defaultURL, &websocket.DialOptions{
		Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
		HTTPHeader:   bearer.authHeader(),
	})
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Explicit topics=workers requires only worker:read and skips CRDT bootstrap completely.
	conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS},
	})

	// A single BroadcastWorkersChanged right after dial delivers WorkersChanged
	// without retrying because the handler registers its wake channel before HTTP 101.
	registry.BroadcastWorkersChanged(user.ID)
	evt := readWatchUserEvent(t, ctx, conn)
	assert.Nil(t, evt.GetInitial(), "workers-only topic must never emit UserMaterialized")
	require.NotNil(t, evt.GetWorkersChanged())

	assert.Zero(t, factoryCalls.Load(), "workers-only topic must not bootstrap a CRDT manager")

	// Revoking the API bearer closes the lightweight subscription promptly.
	bearer.sessionCache.EvictBearer(auth.NewBearerRef(auth.BearerKindAPI, bearer.tokenID))
	readCtx, readCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer readCancel()
	for {
		_, err = channelwire.ReadFramedBytes(readCtx, conn)
		if err != nil {
			break
		}
	}
	require.Error(t, err)
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}

func TestUserEventsHandler_WorkersTopicShutdownEmitsNoSpuriousFrame(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "workers-shutdown-owner")
	bearer := newScopedAPIBearer(t, st, user.ID, "worker:read")

	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, newMemJournal(), allowAllAuth{}, nil, time.Now)
		return mgr, mgr.Bootstrap(ctx)
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	handler, served := servedOnce(service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS},
	})

	registry.Shutdown(5 * time.Second)

	_, err := channelwire.ReadFramedBytes(ctx, conn)
	require.Error(t, err, "registry shutdown must close the workers stream without emitting a spurious WorkersChanged frame")
	assert.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(err))

	select {
	case <-served:
	case <-ctx.Done():
		t.Fatal("ServeHTTP did not exit after registry shutdown")
	}
}

func TestUserEventsHandler_LayoutJournalTopic(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "layout-ws-owner")
	owner := userid.MustNew(user.ID)
	bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")

	var factoryCalls atomic.Int32
	registry := newLayoutTestRegistry(t, st, func() { factoryCalls.Add(1) })

	wsSvc := service.NewWorkspaceService(st, registry, nil)
	handler := service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	// 1. Account-wide stream: initial cursor + live layout wake without loading a CRDT manager.
	conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
	})

	initialEvt := readWatchUserEvent(t, ctx, conn)
	assert.Nil(t, initialEvt.GetInitial(), "layout_journal must not send UserMaterialized")
	initialPage := initialEvt.GetLayoutEvents()
	require.NotNil(t, initialPage)
	require.NotEmpty(t, initialPage.GetCursor())
	assert.Zero(t, initialPage.GetAfterSequence())
	assert.Empty(t, initialPage.GetRecords())

	_, loaded := registry.Loaded(user.ID)
	assert.False(t, loaded, "initial layout_journal connect must not load a CRDT manager")
	assert.Zero(t, factoryCalls.Load(), "initial layout_journal connect must not invoke the CRDT factory")

	// Direct layout append + SignalLayoutEvents pushes a live frame without loading a CRDT manager.
	directFact, err := proto.Marshal(&leapmuxv1.LayoutEventFact{
		Event: &leapmuxv1.LayoutEventFact_Workspace{
			Workspace: &leapmuxv1.LayoutWorkspaceChange{
				WorkspaceId: "ws-direct",
				Operation:   leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME,
				Title:       "DirectWake",
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		_, appendErr := tx.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
			Owner:              owner,
			ObservedUnixMillis: time.Now().UnixMilli(),
			Facts:              [][]byte{directFact},
		})
		return appendErr
	}))
	registry.SignalLayoutEvents(user.ID)

	directEvt := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, directEvt.GetLayoutEvents())
	require.Len(t, directEvt.GetLayoutEvents().GetRecords(), 1)
	assert.Equal(t, "DirectWake", directEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetTitle())

	_, loaded = registry.Loaded(user.ID)
	assert.False(t, loaded, "live layout_journal wake must not load a CRDT manager")
	assert.Zero(t, factoryCalls.Load(), "live layout_journal wake must not invoke the CRDT factory")

	// 2. Live create/rename pushed via WorkspaceService.
	created, err := wsSvc.CreateWorkspace(authedCtx, &leapmuxv1.CreateWorkspaceRequest{Title: "Alpha"})
	require.NoError(t, err)
	wsID := created.GetWorkspaceId()

	createEvt := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, createEvt.GetLayoutEvents())
	require.Len(t, createEvt.GetLayoutEvents().GetRecords(), 1)
	assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_CREATE,
		createEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetOperation())
	assert.Equal(t, "Alpha", createEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetTitle())

	_, err = wsSvc.RenameWorkspace(authedCtx, &leapmuxv1.RenameWorkspaceRequest{WorkspaceId: wsID, Title: "Beta"})
	require.NoError(t, err)

	renameEvt := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, renameEvt.GetLayoutEvents())
	require.Len(t, renameEvt.GetLayoutEvents().GetRecords(), 1)
	assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME,
		renameEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetOperation())
	assert.Equal(t, "Beta", renameEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetTitle())
	renameSeq := renameEvt.GetLayoutEvents().GetRecords()[0].GetSequence()

	// 3. Scoped workspace stream: delete delivers the delete record, then End, then closes cleanly.
	scopedConn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
		LayoutWorkspaceID: wsID,
	})

	scopedInit := readWatchUserEvent(t, ctx, scopedConn)
	require.NotNil(t, scopedInit.GetLayoutEvents())

	_, err = wsSvc.DeleteWorkspace(authedCtx, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: wsID})
	require.NoError(t, err)

	delEvt := readWatchUserEvent(t, ctx, scopedConn)
	require.NotNil(t, delEvt.GetLayoutEvents())
	require.Len(t, delEvt.GetLayoutEvents().GetRecords(), 1)
	assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE,
		delEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetOperation())
	delSeq := delEvt.GetLayoutEvents().GetRecords()[0].GetSequence()

	endEvt := readWatchUserEvent(t, ctx, scopedConn)
	require.NotNil(t, endEvt.GetLayoutEvents().GetEnd())

	_, err = channelwire.ReadFramedBytes(ctx, scopedConn)
	require.Error(t, err)
	assert.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(err))

	// 4. Gap handling: prune past initial cursor and reconnect with stale cursor.
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		return tx.LayoutEvents().PruneThrough(ctx, owner, renameSeq)
	}))
	gapConn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
		LayoutAfterCursor: initialPage.GetCursor(),
	})

	gapEvt := readWatchUserEvent(t, ctx, gapConn)
	require.NotNil(t, gapEvt.GetLayoutEvents().GetGap())
	assert.Equal(t, renameSeq, gapEvt.GetLayoutEvents().GetGap().GetResumeAfterSequence())

	// After the gap frame advances the cursor to renameSeq, the remaining record (delSeq = delete) arrives.
	postGapEvt := readWatchUserEvent(t, ctx, gapConn)
	require.NotNil(t, postGapEvt.GetLayoutEvents())
	require.Len(t, postGapEvt.GetLayoutEvents().GetRecords(), 1)
	assert.Equal(t, delSeq, postGapEvt.GetLayoutEvents().GetRecords()[0].GetSequence())
}

func TestUserEventsHandler_WorkersAndLayoutJournalMultiplexed(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "mux-ws-owner")
	owner := userid.MustNew(user.ID)
	wsOnlyBearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")
	bothBearer := newScopedAPIBearer(t, st, user.ID, "workspace:read worker:read")

	registry := newLayoutTestRegistry(t, st, nil)

	wsSvc := service.NewWorkspaceService(st, registry, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	created, err := wsSvc.CreateWorkspace(authedCtx, &leapmuxv1.CreateWorkspaceRequest{Title: "Mux"})
	require.NoError(t, err)
	wsID := created.GetWorkspaceId()

	// Explicit workers + layout_journal requires BOTH workspace:read AND worker:read.
	wsOnlySrv := httptest.NewServer(service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, wsOnlyBearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(wsOnlyBearer.tv))
	t.Cleanup(wsOnlySrv.Close)

	muxOpts := channelwire.UserEventsOptions{
		Topics: []leapmuxv1.UserEventTopic{
			leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS,
			leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL,
		},
		LayoutWorkspaceID: wsID,
	}
	muxURL, err := channelwire.UserEventsURL(wsOnlySrv.URL, muxOpts)
	require.NoError(t, err)
	_, resp, err := websocket.Dial(ctx, muxURL, &websocket.DialOptions{
		Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
		HTTPHeader:   wsOnlyBearer.authHeader(),
	})
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// With both scopes, one WebSocket multiplexes both topics and stays open for workers even after workspace End.
	srv := httptest.NewServer(service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bothBearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bothBearer.tv))
	t.Cleanup(srv.Close)

	conn := dialUserEvents(t, ctx, srv.URL, bothBearer, muxOpts)

	// Initial layout cursor frame arrives first.
	initEvt := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, initEvt.GetLayoutEvents())

	// Worker change arrives on the same WebSocket.
	registry.BroadcastWorkersChanged(user.ID)
	workerEvt := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, workerEvt.GetWorkersChanged())

	// Workspace delete produces delete record + End frame, but does NOT close the WS because workers is still subscribed.
	_, err = wsSvc.DeleteWorkspace(authedCtx, &leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: wsID})
	require.NoError(t, err)

	delEvt := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, delEvt.GetLayoutEvents())
	require.Len(t, delEvt.GetLayoutEvents().GetRecords(), 1)
	assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE,
		delEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetOperation())

	endEvt := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, endEvt.GetLayoutEvents().GetEnd())

	// WorkersChanged still arrives after layout_journal ended.
	registry.BroadcastWorkersChanged(user.ID)
	workerEvtAfterEnd := readWatchUserEvent(t, ctx, conn)
	require.NotNil(t, workerEvtAfterEnd.GetWorkersChanged())
}

func TestUserEventsHandler_RejectsInvalidTopicsAndLayoutCursor(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "bad-query-owner")
	bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read worker:read")

	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, newMemJournal(), allowAllAuth{}, nil, time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	handler := service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"unknown topic", "topics=bogus"},
		{"invalid base64 layout cursor", "topics=layout_journal&layout_after_cursor=***invalid***"},
		{"layout workspace without layout topic", "topics=workers&layout_workspace_id=ws-1"},
		{"resume cursor without crdt topic", "topics=workers&resume_after_hlc=100.0.c&resume_epoch=1"},
		{"workspace_ids without crdt topic (layout_journal)", "topics=layout_journal&workspace_ids=ws-1"},
		{"workspace_ids without crdt topic (workers)", "topics=workers&workspace_ids=ws-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.test/ws/userevents?"+tc.query, nil)
			req.Header = bearer.authHeader()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestUserEventsHandler_EnforcesAuthorizationBeforeQueryValidationAndMetrics(t *testing.T) {
	// Not parallel: reads process-global UserEventsSubscribeTotal.
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "authz-before-query-owner")
	noTopicScopeBearer := newScopedAPIBearer(t, st, user.ID, "account:read")
	workerOnlyBearer := newScopedAPIBearer(t, st, user.ID, "worker:read")

	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, newMemJournal(), allowAllAuth{}, nil, time.Now)
		return mgr, mgr.Bootstrap(ctx)
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	for _, tc := range []struct {
		name   string
		bearer testBearer
		query  string
	}{
		{
			name:   "token without topic scopes and bogus topic gets 403",
			bearer: noTopicScopeBearer,
			query:  "topics=bogus",
		},
		{
			name:   "token without topic scopes and bad resume cursor gets 403",
			bearer: noTopicScopeBearer,
			query:  "resume_after_hlc=bad",
		},
		{
			name:   "worker:read-only token requesting crdt with bad resume cursor gets 403",
			bearer: workerOnlyBearer,
			query:  "topics=crdt&resume_after_hlc=bad",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := service.NewUserEventsHandler(
				st, hubtestutil.TestSecretHasher(), registry, tc.bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
			).WithTokenValidator(tc.bearer.tv)

			before := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid"))
			req := httptest.NewRequest(http.MethodGet, "http://example.test/ws/userevents?"+tc.query, nil)
			req.Header = tc.bearer.authHeader()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Equal(t, before, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid")),
				"pre-subscribe 403 refusals must not increment UserEventsSubscribeTotal")
		})
	}
}

func TestUserEventsHandler_CountsLightweightConnectsAndRefusals(t *testing.T) {
	// Not parallel: reads process-global UserEventsSubscribeTotal.
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "lightweight-metrics-owner")
	bearer := newScopedAPIBearer(t, st, user.ID, "worker:read")
	bearer.sessionCache.SetMaxConnectionsPerUser(1)

	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		mgr := crdt.NewManager(want, newMemJournal(), allowAllAuth{}, nil, time.Now)
		return mgr, mgr.Bootstrap(ctx)
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	baseHandler := service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv)
	servedCh := make(chan struct{}, 4)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { servedCh <- struct{}{} }()
		baseHandler.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	lightweightBefore := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("lightweight", "no_crdt"))
	invalidBefore := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid"))

	// 1. Established lightweight connect counts under {lightweight, no_crdt}.
	first := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS},
	})
	registry.BroadcastWorkersChanged(user.ID)
	require.NotNil(t, readWatchUserEvent(t, ctx, first).GetWorkersChanged())
	assert.Equal(t, lightweightBefore+1, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("lightweight", "no_crdt")))

	// 2. Lease refusal on a second lightweight connect counts under {invalid, invalid}.
	second := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS},
	})
	_, err := channelwire.ReadFramedBytes(ctx, second)
	require.Error(t, err)
	assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
	select {
	case <-servedCh:
	case <-ctx.Done():
		t.Fatal("refused lightweight ServeHTTP did not return")
	}
	assert.Equal(t, invalidBefore+1, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid")))

	// 3. Failed WebSocket upgrade on a lightweight route counts under {invalid, invalid}.
	req := httptest.NewRequest(http.MethodGet, "http://example.test/ws/userevents?topics=workers", nil)
	req.Header = bearer.authHeader()
	rec := httptest.NewRecorder()
	baseHandler.ServeHTTP(rec, req)
	assert.Equal(t, invalidBefore+2, testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("invalid", "invalid")))
}

func TestUserEventsHandler_CRDTAndLayoutJournalMultiplexed(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "crdt-layout-mux-owner")
	owner := userid.MustNew(user.ID)
	bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")

	registry := newLayoutTestRegistry(t, st, nil)

	wsSvc := service.NewWorkspaceService(st, registry, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	t.Run("streams initial layout cursor and live layout records alongside CRDT bootstrap and batches", func(t *testing.T) {
		handler := service.NewUserEventsHandler(
			st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
		).WithTokenValidator(bearer.tv)
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)

		conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
			Topics: []leapmuxv1.UserEventTopic{
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_CRDT,
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL,
			},
		})

		// CRDT bootstrap always arrives before pumpLayoutJournal starts.
		firstEvt := readWatchUserEvent(t, ctx, conn)
		require.NotNil(t, firstEvt.GetInitial(), "combined crdt+layout_journal must send UserMaterialized first")

		// Initial layout cursor arrives next with no records.
		layoutInit := readWatchUserEvent(t, ctx, conn)
		require.NotNil(t, layoutInit.GetLayoutEvents())
		require.NotEmpty(t, layoutInit.GetLayoutEvents().GetCursor())
		assert.Empty(t, layoutInit.GetLayoutEvents().GetRecords())

		// A workspace creation produces both a CRDT batch and a live layout journal record on the same WebSocket.
		created, err := wsSvc.CreateWorkspace(authedCtx, &leapmuxv1.CreateWorkspaceRequest{Title: "Combined"})
		require.NoError(t, err)

		var gotCRDTBatch, gotLayoutCreate bool
		for !gotCRDTBatch || !gotLayoutCreate {
			evt := readWatchUserEvent(t, ctx, conn)
			if evt.GetBatch() != nil {
				gotCRDTBatch = true
			}
			if le := evt.GetLayoutEvents(); le != nil {
				for _, rec := range le.GetRecords() {
					wsChange := rec.GetFact().GetWorkspace()
					if wsChange != nil &&
						wsChange.GetWorkspaceId() == created.GetWorkspaceId() &&
						wsChange.GetOperation() == leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_CREATE &&
						wsChange.GetTitle() == "Combined" {
						gotLayoutCreate = true
					}
				}
			}
		}
		assert.True(t, gotCRDTBatch)
		assert.True(t, gotLayoutCreate)
	})

	t.Run("layout error closes combined socket with StatusPolicyViolation and tears down cleanly", func(t *testing.T) {
		pool := sendq.NewMaxBytesPoolForTest()
		handler, served := servedOnce(service.NewUserEventsHandler(
			st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, pool,
		).WithTokenValidator(bearer.tv))
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)

		conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
			Topics: []leapmuxv1.UserEventTopic{
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_CRDT,
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL,
			},
			LayoutWorkspaceID: "ws-does-not-exist",
		})

		// CRDT bootstrap is written before pumpLayoutJournal runs; the layout error closes the socket next.
		firstEvt := readWatchUserEvent(t, ctx, conn)
		require.NotNil(t, firstEvt.GetInitial())

		_, err := channelwire.ReadFramedBytes(ctx, conn)
		require.Error(t, err)
		assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))

		select {
		case <-served:
		case <-ctx.Done():
			t.Fatal("ServeHTTP did not exit after pumpLayoutJournal closed on error")
		}
		assert.Zero(t, pool.Used(), "CRDT queue bytes must return to the pool after layout-error teardown")
		assert.Zero(t, pool.Members(), "CRDT subscriber queue must detach from the pool after layout-error teardown")
	})
}

func TestUserEventsHandler_LightweightArmsKeepaliveImmediately(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "lightweight-keepalive-owner")
	bearer := newScopedAPIBearer(t, st, user.ID, "worker:read")
	registry := newLayoutTestRegistry(t, st, nil)

	ticks := make(chan time.Time)
	handler, served := servedOnce(service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv).WithForcedKeepaliveProbesForTest(ticks, 10*time.Second))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics: []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS},
	})

	// Keep the client read loop active so coder/websocket processes pings and replies with pongs.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, err := channelwire.ReadFramedBytes(ctx, conn); err != nil {
				return
			}
		}
	}()

	// Two ticks confirm the probe loop starts immediately and receives the pong from the first probe.
	for i := range 2 {
		select {
		case ticks <- time.Now():
		case <-served:
			t.Fatalf("lightweight keepalive closed the connection at probe %d", i)
		case <-ctx.Done():
			t.Fatalf("lightweight connection did not accept keepalive tick %d", i)
		}
	}

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, ""))
	select {
	case <-served:
	case <-ctx.Done():
		t.Fatal("ServeHTTP did not exit after lightweight client close")
	}
	<-readDone
}

func TestUserEventsHandler_LayoutJournalRejectsInvalidWorkspaceAndCursor(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "layout-reject-owner")
	owner := userid.MustNew(user.ID)
	bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")

	registry := newLayoutTestRegistry(t, st, nil)

	wsSvc := service.NewWorkspaceService(st, registry, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	wsA, err := wsSvc.CreateWorkspace(authedCtx, &leapmuxv1.CreateWorkspaceRequest{Title: "Scope A"})
	require.NoError(t, err)
	wsB, err := wsSvc.CreateWorkspace(authedCtx, &leapmuxv1.CreateWorkspaceRequest{Title: "Scope B"})
	require.NoError(t, err)

	scopeACursorResp, err := wsSvc.ReadLayoutEvents(authedCtx, &leapmuxv1.ReadLayoutEventsRequest{
		WorkspaceId: wsA.GetWorkspaceId(),
	})
	require.NoError(t, err)
	validScopeACursor := scopeACursorResp.GetCursor()
	require.NotEmpty(t, validScopeACursor)

	tamperedSigCursor := append([]byte(nil), validScopeACursor...)
	tamperedSigCursor[len(tamperedSigCursor)-1] ^= 0xff

	handler := service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name     string
		opts     channelwire.UserEventsOptions
		wantCode string
	}{
		{
			name: "nonexistent layout_workspace_id",
			opts: channelwire.UserEventsOptions{
				Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
				LayoutWorkspaceID: "ws-nonexistent",
			},
			wantCode: connect.CodeNotFound.String(),
		},
		{
			name: "valid-base64 invalid-signature layout_after_cursor",
			opts: channelwire.UserEventsOptions{
				Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
				LayoutWorkspaceID: wsA.GetWorkspaceId(),
				LayoutAfterCursor: tamperedSigCursor,
			},
			wantCode: connect.CodeFailedPrecondition.String(),
		},
		{
			name: "valid-base64 wrong-scope layout_after_cursor",
			opts: channelwire.UserEventsOptions{
				Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
				LayoutWorkspaceID: wsB.GetWorkspaceId(),
				LayoutAfterCursor: validScopeACursor,
			},
			wantCode: connect.CodeFailedPrecondition.String(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := dialUserEvents(t, ctx, srv.URL, bearer, tc.opts)

			_, err := channelwire.ReadFramedBytes(ctx, conn)
			require.Error(t, err)
			var closeErr websocket.CloseError
			require.ErrorAs(t, err, &closeErr)
			assert.Equal(t, websocket.StatusPolicyViolation, closeErr.Code)
			gotCode, gotMsg, ok := channelwire.ParseUserEventsPolicyCloseReason(closeErr.Reason)
			require.True(t, ok, "policy close reason must carry structured connect code: %q", closeErr.Reason)
			assert.Equal(t, tc.wantCode, gotCode)
			assert.NotEmpty(t, gotMsg)
		})
	}
}

func TestUserEventsHandler_LightweightLifecycleDrainDeliversWorkersAndEvents(t *testing.T) {
	t.Parallel()

	t.Run("drains pending offline mutation while still delivering workers_changed", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		user := storetest.SeedUser(t, st, "lightweight-drain-owner")
		owner := userid.MustNew(user.ID)
		wsID := storetest.SeedWorkspace(t, st, user.ID, "Before")
		bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read worker:read")

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

		// Write an initial cursor and an offline workspace rename so lifecycle_outbox has a pending row.
		offline := service.NewWorkspaceService(st, nil, nil)
		start, err := offline.ReadLayoutEvents(authedCtx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: wsID})
		require.NoError(t, err)
		_, err = offline.RenameWorkspace(authedCtx, &leapmuxv1.RenameWorkspaceRequest{
			WorkspaceId: wsID,
			Title:       "Drained Title",
		})
		require.NoError(t, err)

		entered := make(chan struct{})
		release := make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(unblock)

		registry := crdt.NewRegistry(func(bootCtx context.Context, want userid.UserID) (*crdt.Manager, error) {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-bootCtx.Done():
				return nil, bootCtx.Err()
			}
			mgr := crdt.NewManager(want, service.NewCRDTJournal(st), service.NewCRDTAuthChecker(st), nil, time.Now)
			if err := mgr.Bootstrap(bootCtx); err != nil {
				return nil, err
			}
			return mgr, nil
		}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(service.NewLifecycleOutboxReader(st)))
		t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

		handler := service.NewUserEventsHandler(
			st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
		).WithTokenValidator(bearer.tv)
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)

		conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
			Topics: []leapmuxv1.UserEventTopic{
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS,
				leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL,
			},
			LayoutWorkspaceID: wsID,
			LayoutAfterCursor: start.GetCursor(),
		})

		// Wait until pumpLayoutJournal starts the lifecycle drain and blocks on it.
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("pumpLayoutJournal did not start the pending lifecycle drain")
		}

		// BroadcastWorkersChanged must still be delivered while waiting for the drain to finish.
		registry.BroadcastWorkersChanged(user.ID)
		workerEvt := readWatchUserEvent(t, ctx, conn)
		require.NotNil(t, workerEvt.GetWorkersChanged(), "workers_changed must arrive during an active lifecycle drain")

		// Release the drain and verify the drained offline rename arrives as a layout record.
		unblock()
		layoutEvt := readWatchUserEvent(t, ctx, conn)
		require.NotNil(t, layoutEvt.GetLayoutEvents())
		require.Len(t, layoutEvt.GetLayoutEvents().GetRecords(), 1)
		wsChange := layoutEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace()
		require.NotNil(t, wsChange)
		assert.Equal(t, leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME, wsChange.GetOperation())
		assert.Equal(t, "Drained Title", wsChange.GetTitle())
	})

	t.Run("closes with StatusTryAgainLater when pending lifecycle drain fails", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		user := storetest.SeedUser(t, st, "lightweight-drain-err-owner")
		owner := userid.MustNew(user.ID)
		wsID := storetest.SeedWorkspace(t, st, user.ID, "Before")
		bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

		offline := service.NewWorkspaceService(st, nil, nil)
		start, err := offline.ReadLayoutEvents(authedCtx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: wsID})
		require.NoError(t, err)
		_, err = offline.RenameWorkspace(authedCtx, &leapmuxv1.RenameWorkspaceRequest{
			WorkspaceId: wsID,
			Title:       "Unreachable Title",
		})
		require.NoError(t, err)

		registry := crdt.NewRegistry(func(context.Context, userid.UserID) (*crdt.Manager, error) {
			return nil, errors.New("manager bootstrap unavailable")
		}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(service.NewLifecycleOutboxReader(st)))
		t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

		handler := service.NewUserEventsHandler(
			st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
		).WithTokenValidator(bearer.tv)
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)

		conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
			Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
			LayoutWorkspaceID: wsID,
			LayoutAfterCursor: start.GetCursor(),
		})

		_, err = channelwire.ReadFramedBytes(ctx, conn)
		require.Error(t, err)
		assert.Equal(t, websocket.StatusTryAgainLater, websocket.CloseStatus(err))
	})

	t.Run("committed layout page followed by failing pending lifecycle drain closes with StatusTryAgainLater instead of hanging", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		user := storetest.SeedUser(t, st, "committed-drain-err-owner")
		owner := userid.MustNew(user.ID)
		wsID := storetest.SeedWorkspace(t, st, user.ID, "Before")
		bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

		offline := service.NewWorkspaceService(st, nil, nil)
		start, err := offline.ReadLayoutEvents(authedCtx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: wsID})
		require.NoError(t, err)

		// Append a committed layout fact AND enqueue a pending lifecycle outbox row.
		committedFact, err := proto.Marshal(&leapmuxv1.LayoutEventFact{
			Event: &leapmuxv1.LayoutEventFact_Workspace{
				Workspace: &leapmuxv1.LayoutWorkspaceChange{
					WorkspaceId: wsID,
					Operation:   leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME,
					Title:       "CommittedPage",
				},
			},
		})
		require.NoError(t, err)
		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			_, appendErr := tx.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
				Owner:              owner,
				ObservedUnixMillis: time.Now().UnixMilli(),
				Facts:              [][]byte{committedFact},
			})
			return appendErr
		}))
		_, err = offline.RenameWorkspace(authedCtx, &leapmuxv1.RenameWorkspaceRequest{
			WorkspaceId: wsID,
			Title:       "PendingDrainTitle",
		})
		require.NoError(t, err)

		registry := crdt.NewRegistry(func(context.Context, userid.UserID) (*crdt.Manager, error) {
			return nil, errors.New("manager bootstrap unavailable")
		}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(service.NewLifecycleOutboxReader(st)))
		t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

		handler := service.NewUserEventsHandler(
			st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
		).WithTokenValidator(bearer.tv)
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)

		conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
			Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
			LayoutWorkspaceID: wsID,
			LayoutAfterCursor: start.GetCursor(),
		})

		// First the committed page must still be delivered.
		pageEvt := readWatchUserEvent(t, ctx, conn)
		require.NotNil(t, pageEvt.GetLayoutEvents())
		require.Len(t, pageEvt.GetLayoutEvents().GetRecords(), 1)
		assert.Equal(t, "CommittedPage", pageEvt.GetLayoutEvents().GetRecords()[0].GetFact().GetWorkspace().GetTitle())

		// At the tail, the failing pending lifecycle drain closes the socket with StatusTryAgainLater.
		_, err = channelwire.ReadFramedBytes(ctx, conn)
		require.Error(t, err)
		assert.Equal(t, websocket.StatusTryAgainLater, websocket.CloseStatus(err))
	})

	t.Run("canceled context during layout read does not close with StatusTryAgainLater", func(t *testing.T) {
		t.Parallel()
		st := hubtestutil.OpenTestStore(t)
		user := storetest.SeedUser(t, st, "canceled-layout-read-owner")
		owner := userid.MustNew(user.ID)
		wsID := storetest.SeedWorkspace(t, st, user.ID, "Before")
		bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

		offline := service.NewWorkspaceService(st, nil, nil)
		start, err := offline.ReadLayoutEvents(authedCtx, &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: wsID})
		require.NoError(t, err)
		_, err = offline.RenameWorkspace(authedCtx, &leapmuxv1.RenameWorkspaceRequest{
			WorkspaceId: wsID,
			Title:       "BlockedDrain",
		})
		require.NoError(t, err)

		entered := make(chan struct{})
		release := make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(unblock)

		registry := crdt.NewRegistry(func(bootCtx context.Context, want userid.UserID) (*crdt.Manager, error) {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-bootCtx.Done():
				return nil, bootCtx.Err()
			}
			mgr := crdt.NewManager(want, service.NewCRDTJournal(st), service.NewCRDTAuthChecker(st), nil, time.Now)
			return mgr, mgr.Bootstrap(bootCtx)
		}, nil, crdt.WithManagerIdleTTL(0), crdt.WithLifecycleOutbox(service.NewLifecycleOutboxReader(st)))
		t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

		handler, served := servedOnce(service.NewUserEventsHandler(
			st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
		).WithTokenValidator(bearer.tv))
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)

		conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
			Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
			LayoutWorkspaceID: wsID,
			LayoutAfterCursor: start.GetCursor(),
		})

		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("pumpLayoutJournal did not enter blocked lifecycle drain")
		}

		// Evicting the bearer cancels the connection context while readLayoutEventPageWithDrain is blocked.
		bearer.sessionCache.EvictBearer(auth.NewBearerRef(auth.BearerKindAPI, bearer.tokenID))

		_, err = channelwire.ReadFramedBytes(ctx, conn)
		require.Error(t, err)
		assert.NotErrorIs(t, err, context.DeadlineExceeded)
		assert.NotEqual(t, websocket.StatusTryAgainLater, websocket.CloseStatus(err),
			"context cancellation during layout read must not close with StatusTryAgainLater")

		select {
		case <-served:
		case <-ctx.Done():
			t.Fatal("ServeHTTP did not exit after connection context cancellation")
		}
	})
}

func TestUserEventsHandler_LayoutJournalMultiPageCatchUp(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "layout-multipage-owner")
	owner := userid.MustNew(user.ID)
	bearer := newScopedAPIBearer(t, st, user.ID, "workspace:read")
	registry := newLayoutTestRegistry(t, st, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	authedCtx := auth.WithUser(ctx, &auth.UserInfo{ID: owner})

	wsSvc := service.NewWorkspaceService(st, registry, nil)
	start, err := wsSvc.ReadLayoutEvents(authedCtx, &leapmuxv1.ReadLayoutEventsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, start.GetCursor())

	// Commit more than one page (> 128 events) without calling SignalLayoutEvents.
	const totalEvents = 135
	facts := make([][]byte, totalEvents)
	for i := range totalEvents {
		raw, marshalErr := proto.Marshal(&leapmuxv1.LayoutEventFact{
			Event: &leapmuxv1.LayoutEventFact_Workspace{
				Workspace: &leapmuxv1.LayoutWorkspaceChange{
					WorkspaceId: "ws-multipage",
					Operation:   leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME,
					Title:       fmt.Sprintf("Title-%03d", i+1),
				},
			},
		})
		require.NoError(t, marshalErr)
		facts[i] = raw
	}
	require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
		_, appendErr := tx.LayoutEvents().Append(ctx, store.AppendLayoutEventsParams{
			Owner:              owner,
			ObservedUnixMillis: time.Now().UnixMilli(),
			Facts:              facts,
		})
		return appendErr
	}))

	handler := service.NewUserEventsHandler(
		st, hubtestutil.TestSecretHasher(), registry, bearer.sessionCache, nil, insecureCookies, sendq.NewMaxBytesPoolForTest(),
	).WithTokenValidator(bearer.tv)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Dial with the saved cursor. Because registry is non-nil, the pump has no
	// polling ticker and advances past page 1 only when caughtUp returns false.
	conn := dialUserEvents(t, ctx, srv.URL, bearer, channelwire.UserEventsOptions{
		Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
		LayoutAfterCursor: start.GetCursor(),
	})

	var (
		pages     []*leapmuxv1.ReadLayoutEventsResponse
		delivered []*leapmuxv1.LayoutEventRecord
	)
	for len(delivered) < totalEvents {
		evt := readWatchUserEvent(t, ctx, conn)
		page := evt.GetLayoutEvents()
		require.NotNil(t, page)
		require.NotEmpty(t, page.GetRecords())
		pages = append(pages, page)
		delivered = append(delivered, page.GetRecords()...)
	}

	require.GreaterOrEqual(t, len(pages), 2, "135 events must span multiple pages")
	assert.Less(t, pages[0].GetScannedSequence(), pages[0].GetHighWaterSequence(),
		"first page must be behind high water so caughtUp returns false")
	lastPage := pages[len(pages)-1]
	assert.Equal(t, int64(totalEvents), lastPage.GetScannedSequence())
	assert.Equal(t, int64(totalEvents), lastPage.GetHighWaterSequence())

	require.Len(t, delivered, totalEvents)
	for i, rec := range delivered {
		wantSeq := int64(i + 1)
		assert.Equal(t, wantSeq, rec.GetSequence())
		assert.Equal(t, fmt.Sprintf("Title-%03d", i+1), rec.GetFact().GetWorkspace().GetTitle())
	}
}
