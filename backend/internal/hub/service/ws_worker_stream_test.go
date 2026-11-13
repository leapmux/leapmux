package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
)

type streamWorkerFixture struct {
	*store.Worker
	token string
}

func registerStreamWorker(t *testing.T, env *regKeyEnv) streamWorkerFixture {
	t.Helper()
	_, token := registerSelfDeregisterableWorker(t, env, t.Context())
	worker, err := env.store.Workers().GetByAuthTokenHashIncludeDeleted(t.Context(), hubtestutil.TestSecretHasher().Hash(token))
	require.NoError(t, err)
	return streamWorkerFixture{Worker: worker, token: token}
}

func TestWorkerStream_RefusesTheUpgradeForABadCredential(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	validator, err := auth.NewTokenValidator(env.store, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	tokenID, secret := id.Generate(), auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(t.Context(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.adminID(t)), ClientID: oauthapp.ControlCLIClientID,
		InstallationName: "worker-stream-test", GrantedScopes: "worker:read", SecretHash: validator.HashSecret(secret),
	}))
	userToken := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	_, err = validator.ValidateBearer(t.Context(), userToken)
	require.NoError(t, err, "the rejected user credential must otherwise be valid")
	for _, tc := range []struct{ name, token string }{{"missing", ""}, {"unknown", "unknown-token"}, {"user API token", userToken}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workerhub.DialWorkerStream(t.Context(), env.server.Client(), env.server.URL, tc.token)
			requireUpgradeRefused(t, err, http.StatusUnauthorized)
		})
	}
	assert.Empty(t, env.wMgr.OwnerConnsForTrustedPath(env.adminID(t)))
}

func TestWorkerStream_AnswersAStoreFaultInTheTokenReadWith500(t *testing.T) {
	t.Parallel()
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store { return workerFaultStore{Store: st} }})
	_, err := workerhub.DialWorkerStream(t.Context(), env.server.Client(), env.server.URL, "anything")
	requireUpgradeRefused(t, err, http.StatusInternalServerError)
	assert.ErrorContains(t, err, "internal server error")
}

func TestWorkerStream_ClosesADeletedWorkerWithItsReason(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	env := setupRegKeyEnv(t)
	worker := registerStreamWorker(t, env)
	require.NoError(t, env.store.Workers().MarkDeleted(t.Context(), worker.ID))
	for _, hello := range []bool{true, false} {
		peer := dialWorker(t, testutil.DeadlineContext(t), env.server, worker.token)
		if hello {
			_ = peer.Send(helloRequest(postQuantumHello(0x31)))
		}
		_, err := peer.Receive()
		requireWorkerClose(t, err, websocket.StatusPolicyViolation, channelwire.WorkerCloseReasonDeleted)
	}
	found := false
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "refusing a deleted worker's stream") {
			found = true
			assert.Contains(t, line, "level=INFO")
			assert.NotContains(t, line, "worker_id=")
		}
	}
	require.True(t, found)
	assert.False(t, env.wMgr.OnlineForTrustedPath(worker.ID))
}

func TestWorkerStream_AdmitsADeregisteringWorker(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	worker := registerStreamWorker(t, env)
	_, err := env.store.Workers().ForceDeregister(t.Context(), worker.ID)
	require.NoError(t, err)
	peer := dialWorker(t, testutil.DeadlineContext(t), env.server, worker.token)
	require.NoError(t, peer.Send(helloRequest(postQuantumHello(0x31))))
	first, err := peer.Receive()
	require.NoError(t, err)
	require.NotNil(t, first.GetWorkerIdentity())
}

func TestWorkerStream_RefusesAMalformedFrame(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"short first frame", "empty payload", "lying prefix after hello"} {
		t.Run(name, func(t *testing.T) {
			env := setupRegKeyEnv(t)
			worker := registerStreamWorker(t, env)
			ctx := testutil.DeadlineContext(t)
			conn, _, err := websocket.Dial(ctx, channelwire.WorkerStreamURL(env.server.URL), &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": []string{"Bearer " + worker.token}}, Subprotocols: []string{channelwire.WSSubprotocolWorkerStream},
			})
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			data := []byte{0, 0, 0}
			if name == "empty payload" {
				data = make([]byte, 4)
			}
			if name == "lying prefix after hello" {
				require.NoError(t, channelwire.WriteProtoFrame(ctx, conn, helloRequest(postQuantumHello(0x31))))
				var greeting leapmuxv1.ConnectResponse
				require.NoError(t, channelwire.ReadProtoFrame(ctx, conn, &greeting))
				require.NotNil(t, greeting.GetWorkerIdentity())
				data = []byte{0, 0, 0, 10, 1}
			}
			require.NoError(t, conn.Write(ctx, websocket.MessageBinary, data))
			_, _, err = conn.Read(ctx)
			requireWorkerClose(t, err, websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage)
			testutil.AssertEventually(t, func() bool { return !env.wMgr.OnlineForTrustedPath(worker.ID) })
		})
	}
}

func TestWorkerStream_DropsAWorkerThatStopsAnsweringPings(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{workerProbeTimeout: 50 * time.Millisecond})
	peer, _, _ := connectAdminWorker(t, env)
	_ = peer
	for range 2 {
		select {
		case env.probes <- time.Now():
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	testutil.AssertEventually(t, func() bool {
		return strings.Contains(logs.String(), `reason="worker stopped answering pings"`) && len(env.wMgr.OwnerConnsForTrustedPath(env.adminID(t))) == 0
	})
	assert.Empty(t, env.wMgr.OwnerConnsForTrustedPath(env.adminID(t)))
}

func TestWorkerStream_KeepsAWorkerThatAnswersPings(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	peer, _, _ := connectAdminWorker(t, env)
	go func() {
		for {
			if _, err := peer.Receive(); err != nil {
				return
			}
		}
	}()
	ctx := testutil.DeadlineContext(t)
	for range 5 {
		select {
		case env.probes <- time.Now():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	assert.Len(t, env.wMgr.OwnerConnsForTrustedPath(env.adminID(t)), 1)
}

type lastSeenCountingStore struct {
	store.Store
	writes *atomic.Int64
	seen   chan struct{}
}

func (s lastSeenCountingStore) Workers() store.WorkerStore {
	return lastSeenCountingWorkers{WorkerStore: s.Store.Workers(), writes: s.writes, seen: s.seen}
}

type lastSeenCountingWorkers struct {
	store.WorkerStore
	writes *atomic.Int64
	seen   chan struct{}
}

func (w lastSeenCountingWorkers) UpdateLastSeen(ctx context.Context, id string) error {
	if err := w.WorkerStore.UpdateLastSeen(ctx, id); err != nil {
		return err
	}
	w.writes.Add(1)
	select {
	case w.seen <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func TestWorkerStream_UpdatesLastSeenOnEachAnsweredPing(t *testing.T) {
	t.Parallel()
	var writes atomic.Int64
	seen := make(chan struct{})
	env := newRegKeyEnv(t, testConfig(), seedSMTP, regKeyEnvOptions{connectorStore: func(st store.Store) store.Store { return lastSeenCountingStore{Store: st, writes: &writes, seen: seen} }})
	worker := registerStreamWorker(t, env)
	ctx := testutil.DeadlineContext(t)
	peer := dialWorker(t, ctx, env.server, worker.token)
	require.NoError(t, peer.Send(helloRequest(postQuantumHello(0x31))))
	select {
	case <-seen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	first, err := peer.Receive()
	require.NoError(t, err)
	require.NotNil(t, first.GetWorkerIdentity())
	go func() {
		for {
			if _, err := peer.Receive(); err != nil {
				return
			}
		}
	}()
	for range 4 {
		select {
		case env.probes <- time.Now():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case <-seen:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	assert.Equal(t, int64(5), writes.Load())
}

type workerSeenStore struct {
	store.Store
	update func(context.Context, string) error
}

func (s workerSeenStore) Workers() store.WorkerStore {
	return workerSeenWorkers{WorkerStore: s.Store.Workers(), update: s.update}
}

type workerSeenWorkers struct {
	store.WorkerStore
	update func(context.Context, string) error
}

func (w workerSeenWorkers) UpdateLastSeen(ctx context.Context, workerID string) error {
	return w.update(ctx, workerID)
}

func TestWorkerStream_AnswersProbesWhileLastSeenWriteWaits(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var writes atomic.Int64
	initialSeen := make(chan struct{})
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWrite := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseWrite)
	st := hubtestutil.OpenTestStore(t)
	env := regKeyEnvFromStore(t, st, testConfig(), seedSMTP, regKeyEnvOptions{
		connectorStore: func(st store.Store) store.Store {
			return workerSeenStore{Store: st, update: func(writeCtx context.Context, workerID string) error {
				n := writes.Add(1)
				if n == 1 {
					defer close(initialSeen)
				}
				if n == 2 {
					close(entered)
					select {
					case <-release:
					case <-writeCtx.Done():
						return writeCtx.Err()
					}
				}
				return st.Workers().UpdateLastSeen(writeCtx, workerID)
			}}
		},
	}, testutil.NewQuartzMock(t))
	worker := registerStreamWorker(t, env)
	ticks := make(chan time.Time)
	handler := service.NewWorkerStreamHandler(env.connector).WithForcedKeepaliveProbesForTest(ticks, 10*time.Second)
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	pings := make(chan struct{}, 1)
	conn, _, err := websocket.Dial(ctx, channelwire.WorkerStreamURL(server.URL), &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + worker.token}},
		Subprotocols: []string{channelwire.WSSubprotocolWorkerStream},
		OnPingReceived: func(context.Context, []byte) bool {
			select {
			case pings <- struct{}{}:
			default:
			}
			return true
		},
	})
	require.NoError(t, err)
	readDone := make(chan struct{})
	t.Cleanup(func() {
		releaseWrite()
		_ = conn.CloseNow()
		select {
		case <-handlerDone:
		case <-time.After(30 * time.Second):
			t.Error("the Worker stream handler did not finish")
		}
	})
	require.NoError(t, channelwire.WriteProtoFrame(ctx, conn, helloRequest(postQuantumHello(0x31))))
	var greeting leapmuxv1.ConnectResponse
	require.NoError(t, channelwire.ReadProtoFrame(ctx, conn, &greeting))
	require.NotNil(t, greeting.GetWorkerIdentity())
	select {
	case <-initialSeen:
	case <-ctx.Done():
		t.Fatal("the initial last-seen write did not finish")
	}
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = conn.CloseNow(); <-readDone })
	channelID := id.Generate()
	require.True(t, env.channelMgr.RegisterWithAuthInfo(channelID, worker.ID, worker.RegisteredBy, channelmgr.AuthInfo{}, nil))
	probe := func() {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-ctx.Done():
			t.Fatal("the Hub stopped accepting probes while its last-seen write waited")
		}
		select {
		case <-pings:
		case <-ctx.Done():
			t.Fatal("the Worker received no ping while its last-seen write waited")
		}
	}
	probe()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the answered probe did not start its last-seen write")
	}
	for range 4 {
		probe()
	}
	assert.Equal(t, int64(2), writes.Load(), "persistence must serialize writes while probes continue")
	assert.True(t, env.wMgr.OnlineForTrustedPath(worker.ID))
	assert.True(t, env.channelMgr.Exists(channelID), "a slow last-seen write must not close an active channel")
}

func sizedInventoryRequest(t *testing.T, size int) *leapmuxv1.ConnectRequest {
	t.Helper()
	tab := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: strings.Repeat("x", size-32)}
	req := &leapmuxv1.ConnectRequest{RequestId: "r1", Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{Complete: true, Tabs: []*leapmuxv1.TabIdent{tab}}}}
	tab.TabId += strings.Repeat("x", size-proto.Size(req))
	require.Equal(t, size, proto.Size(req))
	return req
}

func TestWorkerStream_HonoursTheReadLimit(t *testing.T) {
	t.Parallel()
	for _, extra := range []int{0, 1} {
		t.Run(map[int]string{0: "at the limit", 1: "over the limit"}[extra], func(t *testing.T) {
			env := setupRegKeyEnv(t)
			worker := registerStreamWorker(t, env)
			peer := dialWorker(t, testutil.DeadlineContext(t), env.server, worker.token)
			require.NoError(t, peer.Send(helloRequest(postQuantumHello(0x31))))
			_, err := peer.Receive()
			require.NoError(t, err)
			require.NoError(t, peer.Send(sizedInventoryRequest(t, channelwire.WorkerStreamMaxMessageBytes+extra)))
			reply, err := peer.Receive()
			if extra == 0 {
				require.NoError(t, err)
				require.NotNil(t, reply.GetWorkerTabInventoryResp())
				assert.Equal(t, "r1", reply.GetRequestId())
			} else {
				assert.Equal(t, websocket.StatusMessageTooBig, websocket.CloseStatus(err))
				testutil.AssertEventually(t, func() bool { return !env.wMgr.OnlineForTrustedPath(worker.ID) })
			}
		})
	}
}

func TestNewWorkerStreamHandlerRequiresAConnector(t *testing.T) {
	t.Parallel()
	require.Panics(t, func() { service.NewWorkerStreamHandler(nil) })
}

func TestWorkerStream_InterruptedInventoryDoesNotDeleteTabs(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	worker := registerStreamWorker(t, env)
	manager, err := env.registry.Get(t.Context(), worker.RegisteredBy)
	require.NoError(t, err)
	manager.SeedStateForTest(func(state *leapmuxv1.UserCrdtState) {
		state.Workspaces["ws"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "ws", RootNodeId: "root"}
		state.Nodes["root"] = &leapmuxv1.NodeRecord{NodeId: "root", Kind: &leapmuxv1.LWWNodeKind{Value: leapmuxv1.NodeKind_NODE_KIND_LEAF}}
		for _, tabID := range []string{"present", "stale"} {
			state.Tabs[tabID] = &leapmuxv1.TabRecord{TabId: tabID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE, WorkerId: &leapmuxv1.LWWString{Value: worker.ID}, TileId: &leapmuxv1.LWWString{Value: "root"}, Position: &leapmuxv1.LWWString{Value: "a0"}}
		}
	})
	open := func() *workerTestPeer {
		peer := dialWorker(t, testutil.DeadlineContext(t), env.server, worker.token)
		require.NoError(t, peer.Send(helloRequest(postQuantumHello(0x31))))
		greeting, err := peer.Receive()
		require.NoError(t, err)
		require.NotNil(t, greeting.GetWorkerIdentity())
		return peer
	}
	report := &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{{TabId: "present", TabType: leapmuxv1.TabType_TAB_TYPE_FILE}}}
	peer := open()
	require.NoError(t, peer.Send(&leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: report}}))
	require.NoError(t, peer.Close())
	testutil.AssertEventually(t, func() bool { return !env.wMgr.OnlineForTrustedPath(worker.ID) })
	require.Contains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "stale")
	peer = open()
	report = proto.CloneOf(report)
	report.Complete = true
	require.NoError(t, peer.Send(&leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: report}}))
	ack, err := peer.Receive()
	require.NoError(t, err)
	require.NotNil(t, ack.GetWorkerTabInventoryResp())
	require.Contains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "present")
	assert.NotContains(t, manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), "stale")
	require.NoError(t, peer.Send(&leapmuxv1.ConnectRequest{Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: report}}))
	_, err = peer.Receive()
	requireWorkerClose(t, err, websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage)
}
