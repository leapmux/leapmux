package service_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "Big")

	bearer := newDelegationBearer(t, st, user.ID)

	// Big enough that the bootstrap frame takes long enough to write that the
	// commits below land inside the window.
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
			// Padded so the bootstrap frame is multi-MB and its write is long
			// enough for the burst below to land inside the parking window.
			// Every field completenessCheck requires must be present, or the
			// burst is rejected INCOMPLETE_RECORD and nothing is broadcast.
			pad := strings.Repeat("p", 512)
			for i := range 8000 {
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

	srv := httptest.NewServer(service.NewUserEventsHandler(st, registry, bearer.sessionCache, nil, false).
		WithTokenValidator(bearer.tv))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hdr := bearer.authHeader()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	conn.SetReadLimit(64 << 20)

	// Do NOT read yet: the handler blocks in writeUserEvent on the multi-MB
	// bootstrap frame, which holds the parking window open.
	time.Sleep(500 * time.Millisecond)
	require.NotNil(t, mgr, "the registry factory must have run")
	// Overflow the 256-slot park buffer from inside that window.
	committed := 0
	for i := range 150 {
		res, subErr := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
			Batches: []*leapmuxv1.OpBatch{{
				BatchId: fmt.Sprintf("burst-%d", i),
				Ops: []*leapmuxv1.CrdtOp{{
					OpId: fmt.Sprintf("op-burst-%d", i),
					Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
						NodeId: "root",
						Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: fmt.Sprintf("z%04d", i)},
					}},
				}},
			}},
		})
		if subErr == nil && len(res) == 1 && res[0].GetCommitted() != nil {
			committed++
		}
	}
	// Each committed batch broadcasts TWO frames -- the batch itself and its
	// batch_end boundary -- so the park buffer only overflows past
	// parkedFrameCap/2 commits. Stating the precondition as that arithmetic
	// rather than as a round number below it is what keeps a partially-
	// committing burst from failing at the overflow assertion instead, with a
	// diagnosis that blames the feature.
	require.Greater(t, committed*2, service.ParkedFrameCapForTest,
		"the burst must commit enough batches to overflow the park buffer, or there is nothing to drop")

	// The bootstrap frame itself still arrives.
	payload, err := channelwire.ReadFramedBytes(ctx, conn)
	require.NoError(t, err)
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
