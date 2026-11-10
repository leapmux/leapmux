package service_test

import (
	"context"
	"net/http"
	"strings"
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
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
)

func registerStreamWorker(t *testing.T, env *regKeyEnv) *store.Worker {
	t.Helper()
	_, token := registerSelfDeregisterableWorker(t, env, t.Context())
	worker, err := env.store.Workers().GetByAuthTokenIncludeDeleted(t.Context(), token)
	require.NoError(t, err)
	return worker
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
		peer := dialWorker(t, testutil.DeadlineContext(t), env.server, worker.AuthToken)
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
	peer := dialWorker(t, testutil.DeadlineContext(t), env.server, worker.AuthToken)
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
				HTTPHeader: http.Header{"Authorization": []string{"Bearer " + worker.AuthToken}}, Subprotocols: []string{channelwire.WSSubprotocolWorkerStream},
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
	env := setupRegKeyEnv(t)
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
	peer := dialWorker(t, ctx, env.server, worker.AuthToken)
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

func sizedInventoryRequest(t *testing.T, size int) *leapmuxv1.ConnectRequest {
	t.Helper()
	tab := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: strings.Repeat("x", size-32)}
	req := &leapmuxv1.ConnectRequest{RequestId: "r1", Payload: &leapmuxv1.ConnectRequest_WorkerTabInventory{WorkerTabInventory: &leapmuxv1.WorkerTabInventory{Tabs: []*leapmuxv1.TabIdent{tab}}}}
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
			peer := dialWorker(t, testutil.DeadlineContext(t), env.server, worker.AuthToken)
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
