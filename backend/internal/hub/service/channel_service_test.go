package service_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/password"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/mail"

	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

type channelTestEnv struct {
	channelClient   leapmuxv1connect.ChannelServiceClient
	connectorClient leapmuxv1connect.WorkerConnectorServiceClient
	mgmtClient      leapmuxv1connect.WorkerManagementServiceClient
	authClient      leapmuxv1connect.AuthServiceClient
	store           store.Store
	workerMgr       *workermgr.Manager
	channelMgr      *channelmgr.Manager
	pending         *workermgr.PendingRequests
}

type workspaceLookupCountingStore struct {
	store.Store
	getByIDCalls atomic.Int32
}

func (s *workspaceLookupCountingStore) Workspaces() store.WorkspaceStore {
	return workspaceLookupCountingWorkspaces{
		WorkspaceStore: s.Store.Workspaces(),
		getByIDCalls:   &s.getByIDCalls,
	}
}

type workspaceLookupCountingWorkspaces struct {
	store.WorkspaceStore
	getByIDCalls *atomic.Int32
}

func (s workspaceLookupCountingWorkspaces) GetByID(ctx context.Context, workspaceID string) (*store.Workspace, error) {
	s.getByIDCalls.Add(1)
	return s.WorkspaceStore.GetByID(ctx, workspaceID)
}

func setupChannelTestServer(t *testing.T) *channelTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	cfg := testConfig()
	wMgr := workermgr.New()
	cMgr := channelmgr.New()
	pendingReqs := workermgr.NewPendingRequests(cfg.APITimeout)

	mux := http.NewServeMux()
	interceptor, sc := auth.NewInterceptor(st, nil, false, false)
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(st, cfg, auth.NewCredentialLifecycleEffects(nil, nil, nil), nil, mail.NewStubSender(), mail.Renderer{}), opts)
	mux.Handle(authPath, authHandler)

	connPath, connHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(
		service.NewWorkerConnectorService(st, wMgr, nil, nil, nil, nil, nil, nil), opts)
	mux.Handle(connPath, connHandler)

	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(
		service.NewWorkerManagementService(st, wMgr, nil, nil, mail.NewStubSender(), mail.Renderer{}, cfg, nil), opts)
	mux.Handle(mgmtPath, mgmtHandler)

	channelSvc := service.NewChannelService(st, wMgr, cMgr, pendingReqs, sc)
	channelPath, channelHandler := leapmuxv1connect.NewChannelServiceHandler(channelSvc, opts)
	mux.Handle(channelPath, channelHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &channelTestEnv{
		channelClient:   leapmuxv1connect.NewChannelServiceClient(server.Client(), server.URL),
		connectorClient: leapmuxv1connect.NewWorkerConnectorServiceClient(server.Client(), server.URL),
		mgmtClient:      leapmuxv1connect.NewWorkerManagementServiceClient(server.Client(), server.URL),
		authClient:      leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL),
		store:           st,
		workerMgr:       wMgr,
		channelMgr:      cMgr,
		pending:         pendingReqs,
	}
}

// autoAckChannelAccessUpdate returns a SendFn that records each sent message
// on out and, for ChannelAccessUpdate payloads, synthesizes the matching
// ChannelAccessUpdateAck so PrepareWorkspaceAccess (which uses SendAndWait)
// doesn't block waiting for a worker that isn't present in this unit test.
func (e *channelTestEnv) autoAckChannelAccessUpdate(out chan<- *leapmuxv1.ConnectResponse) func(*leapmuxv1.ConnectResponse) error {
	return func(msg *leapmuxv1.ConnectResponse) error {
		out <- msg
		if msg.GetChannelAccessUpdate() != nil && msg.GetRequestId() != "" {
			e.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				RequestId: msg.GetRequestId(),
				Payload: &leapmuxv1.ConnectRequest_ChannelAccessUpdateAck{
					ChannelAccessUpdateAck: &leapmuxv1.ChannelAccessUpdateAck{},
				},
			})
		}
		return nil
	}
}

func (e *channelTestEnv) adminToken(t *testing.T) string {
	t.Helper()
	resp, err := e.authClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin123",
	}))
	require.NoError(t, err)
	return sessionFromCookie(t, resp.Header().Get("Set-Cookie"))
}

func (e *channelTestEnv) createWorkerWithKey(t *testing.T, token string, publicKey []byte) string {
	t.Helper()
	ctx := context.Background()

	// New flow: authenticated user mints a registration key, then the
	// worker presents it as a bearer credential. We exercise the actual
	// RPC path here so the auth interceptor allowlist and the consume
	// transaction are covered alongside the channel tests.
	createReq := authedReq(&leapmuxv1.CreateRegistrationKeyRequest{}, token)
	createResp, err := e.mgmtClient.CreateRegistrationKey(ctx, createReq)
	require.NoError(t, err)

	regReq := connect.NewRequest(&leapmuxv1.RegisterRequest{
		Version:   "v",
		PublicKey: publicKey,
	})
	regReq.Header().Set("Authorization", "Bearer "+createResp.Msg.GetRegistrationKey())
	regResp, err := e.connectorClient.Register(ctx, regReq)
	require.NoError(t, err)

	return regResp.Msg.GetWorkerId()
}

func registerOnlineWorker(t *testing.T, env *channelTestEnv, workerID string, mode leapmuxv1.EncryptionMode) {
	t.Helper()
	_, _ = env.workerMgr.Register(&workermgr.Conn{
		WorkerID:       workerID,
		EncryptionMode: mode,
	})
}

func TestGetWorkerHandshakeParams(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	pubKey := []byte("fake-public-key-32-bytes-long!!!!")
	workerID := env.createWorkerWithKey(t, token, pubKey)
	registerOnlineWorker(t, env, workerID, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	resp, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.NoError(t, err)
	assert.Equal(t, pubKey, resp.Msg.GetPublicKey())
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, resp.Msg.GetEncryptionMode())
}

func TestGetWorkerHandshakeParams_NoKey(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Create worker without public key but online.
	workerID := env.createWorkerWithKey(t, token, nil)
	registerOnlineWorker(t, env, workerID, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_NotFound(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: "nonexistent"}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_EmptyWorkerID(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_WorkerOffline(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Worker exists with a public key but is not registered as online.
	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_RejectsStaleAuthGeneration(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	checker := &freshnessAfterNCalls{staleAfter: 0}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: env.user.ID, OrgID: env.user.OrgID, Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.GetWorkerHandshakeParams(ctx, connect.NewRequest(&leapmuxv1.GetWorkerHandshakeParamsRequest{
		WorkerId: env.workerID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, int32(1), checker.calls.Load(), "handshake params should reject stale auth before worker lookup")
}

func TestOpenChannel_WorkerOffline(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	pubKey := []byte("fake-public-key-32-bytes-long!!!!")
	workerID := env.createWorkerWithKey(t, token, pubKey)

	_, err := env.channelClient.OpenChannel(ctx, authedReq(
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("handshake-msg-1"),
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestOpenChannel_EmptyFields(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Empty worker_id.
	_, err := env.channelClient.OpenChannel(ctx, authedReq(
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         "",
			HandshakePayload: []byte("data"),
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestOpenChannel_WithMockWorker(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	pubKey := []byte("fake-public-key-32-bytes-long!!!!")
	workerID := env.createWorkerWithKey(t, token, pubKey)

	// Simulate worker online by registering a mock connection.
	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := &workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentCh <- msg
			return nil
		},
	}
	_, _ = env.workerMgr.Register(conn)

	// OpenChannel should fail with timeout because mock worker doesn't respond.
	// Use a short timeout context.
	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err := env.channelClient.OpenChannel(shortCtx, authedReq(
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("handshake-msg-1"),
		}, token))
	require.Error(t, err)
	// The sent message should have been a ChannelOpenRequest.
	select {
	case sentMsg := <-sentCh:
		assert.NotNil(t, sentMsg.GetChannelOpen())
		assert.Equal(t, []byte("handshake-msg-1"), sentMsg.GetChannelOpen().GetHandshakePayload())
	default:
		require.Fail(t, "expected a message to be sent to worker")
	}
}

type freshnessAfterNCalls struct {
	calls      atomic.Int32
	staleAfter int32
}

type allowAllAuthFreshness struct{}

func (allowAllAuthFreshness) IsAuthContextCurrent(*auth.UserInfo) bool { return true }

func (allowAllAuthFreshness) CurrentCredentialExpiry(_ context.Context, u *auth.UserInfo) auth.CredentialDeadline {
	if u == nil {
		return auth.UnsetDeadline()
	}
	return u.CredentialExpiresAt
}

func TestNewChannelServiceRequiresAuthFreshnessChecker(t *testing.T) {
	require.Panics(t, func() {
		service.NewChannelService(nil, nil, nil, nil, nil)
	})
	var typedNil *auth.AuthContextRegistry
	require.Panics(t, func() {
		service.NewChannelService(nil, nil, nil, nil, typedNil)
	})
}

func (c *freshnessAfterNCalls) IsAuthContextCurrent(*auth.UserInfo) bool {
	return c.calls.Add(1) <= c.staleAfter
}

func (c *freshnessAfterNCalls) CurrentCredentialExpiry(_ context.Context, u *auth.UserInfo) auth.CredentialDeadline {
	if u == nil {
		return auth.UnsetDeadline()
	}
	return u.CredentialExpiresAt
}

type directOpenChannelEnv struct {
	store       store.Store
	user        *store.User
	workerID    string
	workspaceID string
	worker      *workermgr.Manager
	channels    *channelmgr.Manager
	pending     *workermgr.PendingRequests
	sent        chan *leapmuxv1.ConnectResponse
}

func setupDirectOpenChannelEnv(t *testing.T) *directOpenChannelEnv {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)

	user, err := st.Users().GetByUsername(context.Background(), hubtestutil.TestAdminUsername)
	require.NoError(t, err)

	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    user.ID,
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: user.OrgID, OwnerUserID: user.ID, Title: "ws",
	}))

	wMgr := workermgr.New()
	cMgr := channelmgr.New()
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return 100 * time.Millisecond })
	sent := make(chan *leapmuxv1.ConnectResponse, 1)
	_, _ = wMgr.Register(&workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent <- msg
			return nil
		},
	})
	return &directOpenChannelEnv{
		store:       st,
		user:        user,
		workerID:    workerID,
		workspaceID: workspaceID,
		worker:      wMgr,
		channels:    cMgr,
		pending:     pendingReqs,
		sent:        sent,
	}
}

func TestOpenChannel_UnregistersWhenAuthRevokedDuringRegistration(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)

	checker := &freshnessAfterNCalls{staleAfter: 1}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: env.user.ID, OrgID: env.user.OrgID, Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Empty(t, env.channels.CloseByUserRevocation(env.user.ID, 1), "stale auth must not leave a registered channel")
	assert.Equal(t, int32(2), checker.calls.Load(), "OpenChannel should check before and after registration")
	select {
	case <-env.sent:
		require.Fail(t, "stale auth must be rejected before the worker handshake is sent")
	default:
	}
}

func TestOpenChannel_ClosesWorkerChannelWhenAuthRevokedDuringHandshake(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	_, _ = env.worker.Register(&workermgr.Conn{
		WorkerID: env.workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			env.sent <- msg
			if msg.GetChannelOpen() != nil {
				env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
					Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
						ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
							ChannelId:        msg.GetChannelOpen().GetChannelId(),
							HandshakePayload: []byte("worker-handshake"),
						},
					},
				})
			}
			return nil
		},
	})

	checker := &freshnessAfterNCalls{staleAfter: 3}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: env.user.ID, OrgID: env.user.OrgID, Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Empty(t, env.channels.CloseByUserRevocation(env.user.ID, 1), "stale auth must not leave a registered channel")
	assert.Equal(t, int32(4), checker.calls.Load(), "OpenChannel should re-check after the worker opens the channel")

	var openedID string
	select {
	case sentMsg := <-env.sent:
		open := sentMsg.GetChannelOpen()
		require.NotNil(t, open, "first worker message should open the channel")
		openedID = open.GetChannelId()
	default:
		require.Fail(t, "expected ChannelOpen to be sent to worker")
	}
	select {
	case sentMsg := <-env.sent:
		closeMsg := sentMsg.GetChannelClose()
		require.NotNil(t, closeMsg, "revoked auth after worker open must send ChannelClose")
		assert.Equal(t, openedID, closeMsg.GetChannelId())
	case <-time.After(time.Second):
		require.Fail(t, "expected ChannelClose to compensate for worker-side open")
	}
}

func TestOpenChannel_ClosesWorkerChannelWhenOpenSendFails(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	_, _ = env.worker.Register(&workermgr.Conn{
		WorkerID: env.workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			env.sent <- msg
			if msg.GetChannelOpen() != nil {
				return errors.New("worker stream reset")
			}
			return nil
		},
	})
	channelSvc := service.NewChannelService(
		env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: env.user.ID, OrgID: env.user.OrgID, Username: env.user.Username,
	})

	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	open := <-env.sent
	require.NotNil(t, open.GetChannelOpen())
	select {
	case closeMsg := <-env.sent:
		require.NotNil(t, closeMsg.GetChannelClose())
		assert.Equal(t, open.GetChannelOpen().GetChannelId(), closeMsg.GetChannelClose().GetChannelId())
	case <-time.After(time.Second):
		t.Fatal("failed worker open attempt was not compensated with ChannelClose")
	}
	assert.False(t, env.channels.Exists(open.GetChannelOpen().GetChannelId()))
}

func TestOpenChannel_ClosesWhenCredentialExpires(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	_, _ = env.worker.Register(&workermgr.Conn{
		WorkerID: env.workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			env.sent <- msg
			if open := msg.GetChannelOpen(); open != nil {
				env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
					Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
						ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
							ChannelId:        open.GetChannelId(),
							HandshakePayload: []byte("worker-handshake"),
						},
					},
				})
			}
			return nil
		},
	})

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:                  env.user.ID,
		OrgID:               env.user.OrgID,
		Username:            env.user.Username,
		CredentialExpiresAt: auth.DeadlineAt(time.Now().Add(50 * time.Millisecond)),
	})
	resp, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.NoError(t, err)
	require.True(t, env.channels.Exists(resp.Msg.GetChannelId()))

	require.Eventually(t, func() bool {
		return !env.channels.Exists(resp.Msg.GetChannelId())
	}, time.Second, 10*time.Millisecond, "channel must close when its authenticating credential expires")

	var closeSeen bool
	for !closeSeen {
		select {
		case msg := <-env.sent:
			if closeMsg := msg.GetChannelClose(); closeMsg != nil {
				assert.Equal(t, resp.Msg.GetChannelId(), closeMsg.GetChannelId())
				closeSeen = true
			}
		case <-time.After(time.Second):
			require.Fail(t, "credential expiry must notify the worker")
		}
	}
}

func TestCloseChannelsByBearer_DoesNotBlockOnWorkerSend(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	started := make(chan struct{})
	_, _ = env.worker.Register(&workermgr.Conn{
		WorkerID: env.workerID,
		SendFn: func(*leapmuxv1.ConnectResponse) error {
			close(started)
			<-blocked
			return nil
		},
	})
	t.Cleanup(func() { close(blocked) })

	env.channels.RegisterWithAuthInfo("blocked-close", env.workerID, env.user.ID, channelmgr.AuthInfo{
		Credential: auth.APICredential("token-1"),
	}, nil)
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})

	returned := make(chan int, 1)
	go func() {
		returned <- channelSvc.CloseChannelsByBearer(auth.NewBearerRef(auth.BearerKindAPI, "token-1"))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		require.Fail(t, "worker close send did not start")
	}
	select {
	case count := <-returned:
		assert.Equal(t, 1, count)
	case <-time.After(100 * time.Millisecond):
		require.Fail(t, "local channel teardown must not wait for a blocked worker stream")
	}
}

func TestCloseChannelsByUserRevocation_BlockedWorkerDoesNotStarveHealthyWorker(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	blockedStarted := make(chan struct{}, 1)
	_, _ = env.worker.Register(&workermgr.Conn{
		WorkerID: env.workerID,
		SendFn: func(*leapmuxv1.ConnectResponse) error {
			select {
			case blockedStarted <- struct{}{}:
			default:
			}
			<-blocked
			return nil
		},
	})
	t.Cleanup(func() { close(blocked) })

	healthyWorkerID := id.Generate()
	healthySent := make(chan *leapmuxv1.ConnectResponse, 1)
	_, _ = env.worker.Register(&workermgr.Conn{
		WorkerID: healthyWorkerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			healthySent <- msg
			return nil
		},
	})

	for i := 0; i < 8; i++ {
		env.channels.RegisterWithAuthInfo(id.Generate(), env.workerID, env.user.ID, channelmgr.AuthInfo{}, nil)
	}
	healthyChannelID := id.Generate()
	env.channels.RegisterWithAuthInfo(healthyChannelID, healthyWorkerID, env.user.ID, channelmgr.AuthInfo{}, nil)
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})

	assert.Equal(t, 9, channelSvc.CloseChannelsByUserRevocation(env.user.ID, 1))
	assert.Equal(t, 0, channelSvc.CloseChannelsByUserRevocation(env.user.ID, 1), "local teardown must finish before worker delivery")

	select {
	case <-blockedStarted:
	case <-time.After(time.Second):
		require.Fail(t, "blocked worker send did not start")
	}
	select {
	case msg := <-healthySent:
		require.NotNil(t, msg.GetChannelClose())
		assert.Equal(t, healthyChannelID, msg.GetChannelClose().GetChannelId())
	case <-time.After(time.Second):
		require.Fail(t, "blocked worker must not starve close delivery to a healthy worker")
	}
}

func TestCloseChannelsByUserRevocation_BoundsBlockedWorkerSenders(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	var active atomic.Int32
	var peak atomic.Int32

	const workerCount = 20
	for i := 0; i < workerCount; i++ {
		workerID := id.Generate()
		_, _ = env.worker.Register(&workermgr.Conn{
			WorkerID: workerID,
			SendFn: func(*leapmuxv1.ConnectResponse) error {
				current := active.Add(1)
				for {
					previous := peak.Load()
					if current <= previous || peak.CompareAndSwap(previous, current) {
						break
					}
				}
				<-blocked
				active.Add(-1)
				return nil
			},
		})
		env.channels.RegisterWithAuthInfo(id.Generate(), workerID, env.user.ID, channelmgr.AuthInfo{}, nil)
	}
	t.Cleanup(func() { close(blocked) })

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	assert.Equal(t, workerCount, channelSvc.CloseChannelsByUserRevocation(env.user.ID, 1))
	require.Eventually(t, func() bool { return peak.Load() > 0 }, time.Second, time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.LessOrEqual(t, peak.Load(), int32(4), "blocked worker sends must use a bounded goroutine pool")
}

func TestCloseChannel_NotFound(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.CloseChannel(ctx, authedReq(
		&leapmuxv1.CloseChannelRequest{ChannelId: "nonexistent"}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCloseChannel_EmptyChannelID(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.CloseChannel(ctx, authedReq(
		&leapmuxv1.CloseChannelRequest{ChannelId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCloseChannel_Success(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Get admin user info.
	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	// Register a channel in the channel manager.
	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, workerID, adminUser.ID, channelmgr.AuthInfo{
		Credential: auth.SessionCredential(token),
	}, nil)

	// Close the channel.
	_, err = env.channelClient.CloseChannel(ctx, authedReq(
		&leapmuxv1.CloseChannelRequest{ChannelId: channelID}, token))
	require.NoError(t, err)

	// Verify channel is removed.
	assert.False(t, env.channelMgr.Exists(channelID))
}

func TestCloseChannel_WrongUser(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, adminToken, []byte("key"))

	// Get admin user info.
	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	// Register a channel owned by admin in channel manager.
	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, workerID, adminUser.ID, channelmgr.AuthInfo{}, nil)

	// Create a second user.
	_, user2Token := env.createSecondUser(t)

	// user2 should not be able to close admin's channel.
	_, err = env.channelClient.CloseChannel(ctx, authedReq(
		&leapmuxv1.CloseChannelRequest{ChannelId: channelID}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_Unauthenticated(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, connect.NewRequest(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: "any"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_Classic(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))
	registerOnlineWorker(t, env, workerID, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC)

	resp, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, resp.Msg.GetEncryptionMode())
}

func TestGetWorkerHandshakeParams_UnspecifiedDefaultsToPostQuantum(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))
	registerOnlineWorker(t, env, workerID, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED)

	resp, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID}, token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, resp.Msg.GetEncryptionMode())
}

// --- Handshake scenario tests with different encryption modes ---

func TestOpenChannel_PostQuantumHandshake(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := &workermgr.Conn{
		WorkerID:       workerID,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentCh <- msg
			return nil
		},
	}
	_, _ = env.workerMgr.Register(conn)

	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err := env.channelClient.OpenChannel(shortCtx, authedReq(
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("pq-handshake-msg1"),
		}, token))
	// Times out because mock worker doesn't respond.
	require.Error(t, err)

	select {
	case sentMsg := <-sentCh:
		assert.NotNil(t, sentMsg.GetChannelOpen())
		assert.Equal(t, []byte("pq-handshake-msg1"), sentMsg.GetChannelOpen().GetHandshakePayload())
	default:
		require.Fail(t, "expected a message to be sent to worker")
	}
}

func TestOpenChannel_ClassicHandshake(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := &workermgr.Conn{
		WorkerID:       workerID,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentCh <- msg
			return nil
		},
	}
	_, _ = env.workerMgr.Register(conn)

	shortCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err := env.channelClient.OpenChannel(shortCtx, authedReq(
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: []byte("classic-handshake-msg1"),
		}, token))
	require.Error(t, err)

	select {
	case sentMsg := <-sentCh:
		assert.NotNil(t, sentMsg.GetChannelOpen())
		assert.Equal(t, []byte("classic-handshake-msg1"), sentMsg.GetChannelOpen().GetHandshakePayload())
	default:
		require.Fail(t, "expected a message to be sent to worker")
	}
}

// --- PrepareWorkspaceAccess tests ---

func (e *channelTestEnv) createWorkspace(t *testing.T, ownerUserID, orgID string) string {
	t.Helper()
	wsID := id.Generate()
	err := e.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          wsID,
		OrgID:       orgID,
		OwnerUserID: ownerUserID,
	})
	require.NoError(t, err)
	return wsID
}

func TestPrepareWorkspaceAccess_Success(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	// Simulate worker online with a mock connection that captures sent
	// messages and auto-acks ChannelAccessUpdate.
	sentMsgs := make(chan *leapmuxv1.ConnectResponse, 10)
	conn := &workermgr.Conn{
		WorkerID: workerID,
		SendFn:   env.autoAckChannelAccessUpdate(sentMsgs),
	}
	_, _ = env.workerMgr.Register(conn)

	// Register a channel for the admin user on this worker.
	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, workerID, adminUser.ID, channelmgr.AuthInfo{}, nil)

	// Create a workspace owned by the admin.
	wsID := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)

	// Call PrepareWorkspaceAccess.
	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsID,
		}, token))
	require.NoError(t, err)

	// Verify a ChannelAccessUpdate was sent to the worker with a non-empty
	// request_id so the worker can ack it (used by SendAndWait to avoid a
	// race with the worker's async AddAccessibleWorkspaceID handler).
	select {
	case msg := <-sentMsgs:
		update := msg.GetChannelAccessUpdate()
		require.NotNil(t, update)
		assert.Equal(t, channelID, update.GetChannelId())
		assert.Equal(t, wsID, update.GetWorkspaceId())
		assert.NotEmpty(t, msg.GetRequestId(), "ChannelAccessUpdate must carry a request_id for ack correlation")
	default:
		require.Fail(t, "expected a ChannelAccessUpdate to be sent to worker")
	}
}

func TestPrepareWorkspaceAccess_RejectsStaleAuthGeneration(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	checker := &freshnessAfterNCalls{staleAfter: 0}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: env.user.ID, OrgID: env.user.OrgID, Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.PrepareWorkspaceAccess(ctx, connect.NewRequest(&leapmuxv1.PrepareWorkspaceAccessRequest{
		WorkerId:    env.workerID,
		WorkspaceId: env.workspaceID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, int32(1), checker.calls.Load(), "PrepareWorkspaceAccess should reject stale auth before worker updates")
	select {
	case <-env.sent:
		require.Fail(t, "stale auth must be rejected before sending ChannelAccessUpdate")
	default:
	}
}

func TestPrepareWorkspaceAccess_DoesNotWidenDelegationChannel(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	otherWorkspaceID := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID: otherWorkspaceID, OrgID: env.user.OrgID, OwnerUserID: env.user.ID, Title: "other-ws",
	}))

	cookieChannelID := id.Generate()
	delegationChannelID := id.Generate()
	env.channels.RegisterWithAuthInfo(cookieChannelID, env.workerID, env.user.ID, channelmgr.AuthInfo{
		Credential: auth.SessionCredential("session-1"),
	}, nil)
	env.channels.RegisterWithAuthInfo(delegationChannelID, env.workerID, env.user.ID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential("delegation-token-1", env.workspaceID, "worker-mint"),
	}, nil)

	sent := make(chan *leapmuxv1.ConnectResponse, 10)
	_, _ = env.worker.Register(&workermgr.Conn{
		WorkerID: env.workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent <- msg
			if msg.GetChannelAccessUpdate() != nil && msg.GetRequestId() != "" {
				env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
					RequestId: msg.GetRequestId(),
					Payload: &leapmuxv1.ConnectRequest_ChannelAccessUpdateAck{
						ChannelAccessUpdateAck: &leapmuxv1.ChannelAccessUpdateAck{},
					},
				})
			}
			return nil
		},
	})

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: env.user.ID, OrgID: env.user.OrgID, Username: env.user.Username, Credential: auth.SessionCredential("session-1"),
	})

	_, err := channelSvc.PrepareWorkspaceAccess(ctx, connect.NewRequest(&leapmuxv1.PrepareWorkspaceAccessRequest{
		WorkerId:    env.workerID,
		WorkspaceId: otherWorkspaceID,
	}))
	require.NoError(t, err)

	select {
	case msg := <-sent:
		update := msg.GetChannelAccessUpdate()
		require.NotNil(t, update)
		assert.Equal(t, cookieChannelID, update.GetChannelId())
		assert.Equal(t, otherWorkspaceID, update.GetWorkspaceId())
	default:
		require.Fail(t, "expected ChannelAccessUpdate for the unrestricted session channel")
	}
	select {
	case msg := <-sent:
		require.Failf(t, "delegation-scoped channel must not be widened", "got: %v", msg)
	default:
	}
}

// TestPrepareWorkspaceAccess_AckTimeout verifies that when the worker fails
// to reply with ChannelAccessUpdateAck within the context deadline, the hub
// returns CodeUnavailable instead of succeeding (which would race the
// worker's async access-set update and trip requireAccessibleWorkspace).
func TestPrepareWorkspaceAccess_AckTimeout(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	// Worker is online but NEVER acks — simulates a buggy or crashed
	// worker that received the frame but didn't (or couldn't) process it.
	sentMsgs := make(chan *leapmuxv1.ConnectResponse, 10)
	conn := &workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentMsgs <- msg
			return nil
		},
	}
	_, _ = env.workerMgr.Register(conn)

	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, workerID, adminUser.ID, channelmgr.AuthInfo{}, nil)

	wsID := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)

	// Short deadline so the test doesn't wait the default 10s timeout.
	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	_, err = env.channelClient.PrepareWorkspaceAccess(shortCtx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsID,
		}, token))
	require.Error(t, err)
	// Either code is correct: the handler returns CodeUnavailable when its
	// SendAndWait observes the context deadline, but because both the
	// client-side transport and the server handler share the same short
	// deadline, the connect transport can see the client context cancel
	// first and surface CodeDeadlineExceeded before the handler's error
	// response arrives. Both outcomes are "ack did not arrive", which is
	// what this test is verifying.
	code := connect.CodeOf(err)
	assert.Truef(t,
		code == connect.CodeUnavailable || code == connect.CodeDeadlineExceeded,
		"want CodeUnavailable or CodeDeadlineExceeded, got %v (%v)", code, err)

	// The hub should still have attempted to send the update before waiting.
	select {
	case msg := <-sentMsgs:
		require.NotNil(t, msg.GetChannelAccessUpdate())
	default:
		require.Fail(t, "expected ChannelAccessUpdate to be sent before waiting for ack")
	}
}

func TestPrepareWorkspaceAccess_OnlySendsToMatchingWorker(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	worker1ID := env.createWorkerWithKey(t, token, []byte("key1"))
	worker2ID := env.createWorkerWithKey(t, token, []byte("key2"))

	// Register both workers online, each with auto-ack for
	// ChannelAccessUpdate so SendAndWait doesn't hang.
	sent1 := make(chan *leapmuxv1.ConnectResponse, 10)
	conn1 := &workermgr.Conn{
		WorkerID: worker1ID,
		SendFn:   env.autoAckChannelAccessUpdate(sent1),
	}
	_, _ = env.workerMgr.Register(conn1)

	sent2 := make(chan *leapmuxv1.ConnectResponse, 10)
	conn2 := &workermgr.Conn{
		WorkerID: worker2ID,
		SendFn:   env.autoAckChannelAccessUpdate(sent2),
	}
	_, _ = env.workerMgr.Register(conn2)

	// Register channels: one on each worker.
	ch1ID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(ch1ID, worker1ID, adminUser.ID, channelmgr.AuthInfo{}, nil)
	ch2ID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(ch2ID, worker2ID, adminUser.ID, channelmgr.AuthInfo{}, nil)

	wsID := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)

	// PrepareWorkspaceAccess targeting worker1 only.
	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    worker1ID,
			WorkspaceId: wsID,
		}, token))
	require.NoError(t, err)

	// worker1 should have received the update.
	select {
	case msg := <-sent1:
		update := msg.GetChannelAccessUpdate()
		require.NotNil(t, update)
		assert.Equal(t, ch1ID, update.GetChannelId())
	default:
		require.Fail(t, "expected worker1 to receive ChannelAccessUpdate")
	}

	// worker2 should NOT have received anything.
	select {
	case msg := <-sent2:
		require.Failf(t, "worker2 should not receive any message", "got: %v", msg)
	default:
		// expected
	}
}

func TestPrepareWorkspaceAccess_WorkspaceNotFound(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	_, err := env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: "nonexistent",
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestPrepareWorkspaceAccess_NoAccessToWorkspace(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, adminToken, []byte("key"))

	// Create workspace owned by admin.
	wsID := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)

	// Create a second user who doesn't have access to the workspace.
	_, user2Token := env.createSecondUser(t)

	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsID,
		}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// A Worker serves only the user it is registered to. Owning a workspace never
// conveys a reach into someone else's Worker, so a caller naming their OWN
// workspace but another user's worker is still refused here. This pins the
// verifyWorkerAccess guard in PrepareWorkspaceAccess: the workspace checks
// above it bound WHICH workspace may be named, not WHICH worker.
func TestPrepareWorkspaceAccess_OwnWorkspaceDoesNotConveyWorkerAccess(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, adminToken, []byte("key"))

	// Worker is online and would auto-ack any ChannelAccessUpdate, so a leak
	// would show up as a delivered message rather than a timeout.
	sentMsgs := make(chan *leapmuxv1.ConnectResponse, 10)
	conn := &workermgr.Conn{
		WorkerID: workerID,
		SendFn:   env.autoAckChannelAccessUpdate(sentMsgs),
	}
	_, _ = env.workerMgr.Register(conn)

	// user2 owns a workspace of their own -- enough to pass the workspace
	// load-and-authorize check, so the worker guard is what refuses below.
	user2ID, user2Token := env.createSecondUser(t)
	user2, err := env.store.Users().GetByID(ctx, user2ID)
	require.NoError(t, err)
	wsUser2 := env.createWorkspace(t, user2ID, user2.OrgID)

	// Even with a channel registered for user2 on admin's worker -- a state
	// OpenChannel could never produce, since it asks this same question --
	// prepare-access must refuse.
	channelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(channelID, workerID, user2ID, channelmgr.AuthInfo{}, nil)

	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsUser2,
		}, user2Token))
	require.Error(t, err)
	// NotFound, not PermissionDenied: the guard refuses to confirm that a
	// worker the caller cannot use even exists.
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	// Nothing may reach the worker for a refused caller.
	select {
	case msg := <-sentMsgs:
		require.Failf(t, "no message should reach the worker", "got %v", msg)
	default:
	}

	// The worker's owner is still served on their own workspace and channel.
	wsAdmin := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)
	adminChannelID := id.Generate()
	env.channelMgr.RegisterWithAuthInfo(adminChannelID, workerID, adminUser.ID, channelmgr.AuthInfo{}, nil)
	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsAdmin,
		}, adminToken))
	require.NoError(t, err)

	select {
	case msg := <-sentMsgs:
		update := msg.GetChannelAccessUpdate()
		require.NotNil(t, update)
		assert.Equal(t, adminChannelID, update.GetChannelId())
		assert.Equal(t, wsAdmin, update.GetWorkspaceId())
	default:
		t.Fatal("expected a ChannelAccessUpdate for the worker's owner")
	}
}

// TestPrepareWorkspaceAccess_LoadsWorkspaceOnce pins the single-load shape of
// the authorization path: the owner check reuses the loaded workspace row
// rather than fetching it a second time.
func TestPrepareWorkspaceAccess_LoadsWorkspaceOnce(t *testing.T) {
	env := setupDirectOpenChannelEnv(t)
	ctx := context.Background()
	workspaceID := id.Generate()
	require.NoError(t, env.store.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: env.user.OrgID, OwnerUserID: env.user.ID, Title: "mine",
	}))

	countingStore := &workspaceLookupCountingStore{Store: env.store}
	channelSvc := service.NewChannelService(countingStore, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	_, err := channelSvc.PrepareWorkspaceAccess(
		auth.WithUser(ctx, &auth.UserInfo{ID: env.user.ID, OrgID: env.user.OrgID}),
		connect.NewRequest(&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    env.workerID,
			WorkspaceId: workspaceID,
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, int32(1), countingStore.getByIDCalls.Load(), "workspace authorization must reuse the loaded row")
}

func TestPrepareWorkspaceAccess_WorkerOffline(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.store.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))
	wsID := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)

	// Worker is NOT registered as online.
	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsID,
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestPrepareWorkspaceAccess_EmptyFields(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Empty worker_id.
	_, err := env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    "",
			WorkspaceId: "ws-1",
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Empty workspace_id.
	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    "w-1",
			WorkspaceId: "",
		}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func (e *channelTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	orgID := id.Generate()
	require.NoError(t, e.store.Orgs().Create(ctx, store.CreateOrgParams{
		ID:   orgID,
		Name: "user2",
	}))
	userID = id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = e.store.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	token, _, _, loginErr := auth.Login(ctx, e.store, "user2", "testpass2")
	require.NoError(t, loginErr)
	return
}
