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
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// delegationBearer is everything a /ws/userevents test needs to dial with a
// delegation Bearer: the validator to hand the handler, the session cache to
// mount it against (and to evict through), and the minted credential.
type delegationBearer struct {
	tv           *auth.TokenValidator
	sessionCache *auth.AuthContextRegistry
	tokenID      string
	secret       string
}

// authHeader is the Authorization header for this bearer.
func (b delegationBearer) authHeader() http.Header {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindDelegation, b.tokenID, b.secret))
	return hdr
}

// newDelegationBearer registers a worker, wires a token validator with its
// session cache, and mints a delegation token for `userID` -- the whole
// preamble a /ws/userevents test needs before it can dial.
//
// Extracted because the three tests in this file that need one had it
// character-for-character identical, so a field added to CreateWorkerParams or
// CreateDelegationTokenParams had to be found in three places to stay
// compiling.
func newDelegationBearer(t *testing.T, st store.Store, userID string) delegationBearer {
	t.Helper()
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	_, sessionCache := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, TokenValidator: tv})
	t.Cleanup(sessionCache.Stop)

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		WorkerID:         workerID,
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))
	return delegationBearer{tv: tv, sessionCache: sessionCache, tokenID: tokenID, secret: secret}
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

func newGatedListener(inner net.Listener) *gatedListener {
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
		st, registry, bearer.sessionCache, nil, false, sendq.NewMaxBytesPoolForTest()).
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

	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, sendq.NewMaxBytesPoolForTest()).
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

	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, sendq.NewMaxBytesPoolForTest()).
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
	handler, served := servedOnce(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, pool).
		WithTokenValidator(bearer.tv).
		WithForcedKeepaliveProbesForTest(ticks, 10*time.Second))
	srv := httptest.NewUnstartedServer(handler)
	gate := newGatedListener(srv.Listener)
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
	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, sendq.NewMaxBytesPoolForTest()).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	// A first connect presents no cursor, so it takes the FALLBACK arm.
	coldBefore := testutil.ToFloat64(metrics.UserEventsSubscribeTotal.WithLabelValues("initial", "no_cursor"))
	observedBefore := testutil.CollectAndCount(metrics.UserEventsSubscribeDuration)

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
	require.Greater(t, testutil.CollectAndCount(metrics.UserEventsSubscribeDuration), observedBefore-1,
		"the duration histogram must be observed at all")

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
	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, sendq.NewMaxBytesPoolForTest()).
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
	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, sendq.NewMaxBytesPoolForTest()).
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
			wantReason: channelwire.CloseReasonSnapshotTooLarge,
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
			handler, served := servedOnce(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, pool).
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

	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false, sendq.NewMaxBytesPoolForTest()).
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
	assert.Equal(t, channelwire.CloseReasonTooManyConnections, closeErr.Reason,
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
