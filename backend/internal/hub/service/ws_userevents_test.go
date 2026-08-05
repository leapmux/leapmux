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
	_, sessionCache := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
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

	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false).
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

	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false).
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

	srv := httptest.NewUnstartedServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false).
		WithTokenValidator(bearer.tv))
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
	return &parkWindowEnv{mgr: mgr, gate: gate, conn: conn, ctx: ctx}
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
	// stalled bootstrap write finish; the handler's queue.Release(), and with it
	// the drop verdict, comes immediately after that write returns.
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
	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false).
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
	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false).
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
	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false).
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
