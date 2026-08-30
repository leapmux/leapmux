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

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/sendq"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/mail"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
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

func setupChannelTestServer(t *testing.T) *channelTestEnv {
	t.Helper()

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	err = st.Migrator().Migrate(context.Background())
	require.NoError(t, err)

	hubtestutil.CreateTestAdmin(t, st)

	cfg := testConfig()
	set := servicetest.NewSettingsManager(t, st, nil)
	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return settings.DefaultTimeouts.APITimeout() })

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))), opts)
	mux.Handle(authPath, authHandler)

	connPath, connHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(
		service.NewWorkerConnectorService(st, wMgr, nil, nil, nil, nil, nil, sendq.NewMaxBytesPoolForTest()), opts)
	mux.Handle(connPath, connHandler)

	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(
		service.NewWorkerManagementService(st, wMgr, nil, nil, mail.NewStubSender(), mail.Renderer{}, set, nil), opts)
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
	conn, _ := workermgrtest.NewRecordedConn(t, workerID)
	conn.SetEncryptionMode(mode)
	_, _ = env.workerMgr.Register(conn)
}

func TestGetWorkerHandshakeParams(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: "nonexistent"}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_EmptyWorkerID(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, authedReq(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_WorkerOffline(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	checker := &freshnessAfterNCalls{staleAfter: 0}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username, AuthGeneration: 0,
	})

	_, err := channelSvc.GetWorkerHandshakeParams(ctx, connect.NewRequest(&leapmuxv1.GetWorkerHandshakeParamsRequest{
		WorkerId: env.workerID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, int32(1), checker.calls.Load(), "handshake params should reject stale auth before worker lookup")
}

func TestOpenChannel_WorkerOffline(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	pubKey := []byte("fake-public-key-32-bytes-long!!!!")
	workerID := env.createWorkerWithKey(t, token, pubKey)

	// Simulate worker online by registering a mock connection.
	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		sentCh <- msg
		return nil

	})

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
		assert.Equal(t, uint64(contracts.MaxMessageSize), sentMsg.GetChannelOpen().GetMaxMessageSize(),
			"hub must announce its resolved max_message_size on ChannelOpenRequest")
	case <-time.After(time.Second):
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
	t.Parallel()

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
		RegisteredBy:    userid.MustNew(user.ID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID: workspaceID, OwnerUserID: userid.MustNew(user.ID), Title: "ws",
	}))

	wMgr := workermgr.New(service.NewWorkerReachAuthorizer(st))
	cMgr := channelmgr.New(0)
	// Generous, because no test on this env asserts the deadline: every one of
	// them either completes the request from the conn's write callback or fails
	// before the request is ever issued. A short default is then a wall-clock
	// race the whole file runs against -- at 100ms, a `-race` run of the package
	// crossed it often enough to fail an assertion with CodeUnavailable, blaming
	// the error-code mapping for a timeout. Matches workermgr's own pending
	// tests. A test that WANTS the deadline should build its own env with a
	// short one, the way TestOpenChannel_WithMockWorker bounds its context.
	pendingReqs := workermgr.NewPendingRequests(func() time.Duration { return 30 * time.Second })
	sent := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		sent <- msg
		return nil

	})
	_, _ = wMgr.Register(conn)
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
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)

	checker := &freshnessAfterNCalls{staleAfter: 1}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username, AuthGeneration: 0,
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
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if msg.GetChannelOpen() != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        msg.GetChannelOpen().GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   msg.GetChannelOpen().GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	checker := &freshnessAfterNCalls{staleAfter: 3}
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, checker)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username, AuthGeneration: 0,
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

func TestOpenChannel_ClosesLocalChannelWhenOpenWriteFails(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if msg.GetChannelOpen() != nil {
			return errors.New("worker stream reset")
		}
		return nil
	})
	_, _ = env.worker.Register(conn)
	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID: userid.MustNew(env.user.ID), Username: env.user.Username,
	})

	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))

	open := <-env.sent
	require.NotNil(t, open.GetChannelOpen())
	// A write failure fences the conn, so ChannelClose compensation cannot be
	// delivered on the dead stream. The local channel must still be torn down.
	assert.False(t, env.channels.Exists(open.GetChannelOpen().GetChannelId()))
	select {
	case msg := <-env.sent:
		t.Fatalf("fenced conn must not deliver further frames, got %T", msg.GetPayload())
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOpenChannel_MapsWorkerErrorCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		code leapmuxv1.ChannelOpenErrorCode
		want connect.Code
	}{
		{
			name: "invalid max message size",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE,
			want: connect.CodeInvalidArgument,
		},
		{
			name: "channel already active",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE,
			want: connect.CodeFailedPrecondition,
		},
		{
			name: "no authenticated user",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_NO_AUTHENTICATED_USER,
			want: connect.CodeInternal,
		},
		{
			name: "handshake failed",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED,
			want: connect.CodeInvalidArgument,
		},
		{
			name: "unspecified stays internal",
			code: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_UNSPECIFIED,
			want: connect.CodeInternal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupDirectOpenChannelEnv(t)
			env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
			conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
				env.sent <- msg
				if open := msg.GetChannelOpen(); open != nil {
					env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
						Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
							ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
								ChannelId: open.GetChannelId(),
								Error:     "worker reject: " + tc.name,
								ErrorCode: tc.code,
							},
						},
					})
				}
				return nil

			})
			_, _ = env.worker.Register(conn)

			channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
			ctx := auth.WithUser(context.Background(), &auth.UserInfo{
				ID:       userid.MustNew(env.user.ID),
				Username: env.user.Username,
			})
			_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
				WorkerId:         env.workerID,
				HandshakePayload: []byte("handshake"),
			}))
			require.Error(t, err)
			assert.Equal(t, tc.want, connect.CodeOf(err), "code %v must map via channelwire.ConnectCodeFromChannelOpenError", tc.code)
		})
	}
}

func TestOpenChannel_RejectsErrorCodeOnlyWithoutErrorString(t *testing.T) {
	t.Parallel()

	// Hub must fail closed when ErrorCode is set even if Error is empty, so a
	// buggy/hostile worker cannot fall through to the success path.
	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						ErrorCode:        leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE,
						MaxMessageSize:   uint64(contracts.MaxMessageSize),
						HandshakePayload: []byte("should-not-matter"),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "worker rejected channel")
	assert.Contains(t, err.Error(), leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE.String(),
		"empty Error must fall back to the ErrorCode name so the reject is still attributable")
}

func TestOpenChannel_MapsInvalidMaxMessageSizeErrorCode(t *testing.T) {
	t.Parallel()

	// Kept as a focused alias of the table above for the original skew case.
	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId: open.GetChannelId(),
						Error:     "invalid hub max_message_size: below floor",
						ErrorCode: leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE,
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid hub max_message_size")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
		"structured INVALID_MAX_MESSAGE_SIZE must map to InvalidArgument, not Internal")
}

func TestOpenChannel_UnspecifiedWorkerErrorStaysInternal(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId: open.GetChannelId(),
						Error:     "handshake failed: corrupt",
						// ErrorCode unspecified → Internal (retryable-looking
						// worker fault, not a client config mistake).
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

func TestOpenChannel_RejectsWorkerEchoAboveHubMax(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	hubMax := 1 << 20 // 1 MiB — below the protocol default so an oversize echo is visible
	env.channels = channelmgr.New(hubMax)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						// Valid by itself, but above what this hub announced.
						MaxMessageSize: uint64(hubMax * 2),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "above hub max")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
		"peer echo above hub max is a Hub↔Worker protocol fault, not a client InvalidArgument")
}

func TestOpenChannel_RejectsWorkerEchoInvalidMaxMessageSize(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		echo uint64
	}{
		{name: "zero", echo: 0},
		{name: "below floor", echo: 1},
		{name: "above ceiling", echo: uint64(contracts.MaxConfigurableMessageSize + 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupDirectOpenChannelEnv(t)
			env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
			conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
				env.sent <- msg
				if open := msg.GetChannelOpen(); open != nil {
					env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
						Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
							ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
								ChannelId:        open.GetChannelId(),
								HandshakePayload: []byte("worker-handshake"),
								MaxMessageSize:   tc.echo,
							},
						},
					})
				}
				return nil

			})
			_, _ = env.worker.Register(conn)

			channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
			ctx := auth.WithUser(context.Background(), &auth.UserInfo{
				ID:       userid.MustNew(env.user.ID),
				Username: env.user.Username,
			})
			_, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
				WorkerId:         env.workerID,
				HandshakePayload: []byte("handshake"),
			}))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "max_message_size")
			assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
				"bad/missing worker echo is a peer/protocol fault, not a client InvalidArgument")
			if tc.echo == 0 {
				assert.Contains(t, err.Error(), "version skew")
			}
		})
	}
}

func TestOpenChannel_AdoptsWorkerLoweredMaxMessageSize(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	hubMax := 200_000
	lowered := 100_000
	env.channels = channelmgr.New(hubMax)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			assert.Equal(t, uint64(hubMax), open.GetMaxMessageSize(),
				"hub must announce its configured payload budget")
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   uint64(lowered),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	resp, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.NoError(t, err)
	assert.Equal(t, uint64(lowered), resp.Msg.GetMaxMessageSize())
	require.True(t, env.channels.Exists(resp.Msg.GetChannelId()))

	// Prove the per-channel tracker ceiling is MaxReassembledMessageSize(lowered):
	// past the bare payload must still fit (headroom), past the derived ceiling must not
	// (and must not silently keep the larger hub default).
	channelID := resp.Msg.GetChannelId()
	ceiling := channelwire.MaxReassembledMessageSize(lowered)
	chunkCiphertext := func(plaintext int) int {
		return plaintext + contracts.NoiseAEADAuthTagSize
	}
	const dir = "fe2w"
	const piece = 40_000

	trackAccum := func(corr uint64, target int, finalMore bool) error {
		total := 0
		for total+piece < target {
			if err := env.channels.ChunkTracker.Track(channelID, dir, corr, chunkCiphertext(piece), true); err != nil {
				return err
			}
			total += piece
		}
		remain := target - total
		return env.channels.ChunkTracker.Track(channelID, dir, corr, chunkCiphertext(remain), finalMore)
	}

	require.NoError(t, trackAccum(1, lowered+1, false),
		"reassembled ceiling must include envelope headroom past the bare payload")
	err = trackAccum(2, ceiling+1, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max size")
}

func TestOpenChannel_ReturnsNegotiatedMaxMessageSize(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   open.GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	resp, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.NoError(t, err)
	assert.Equal(t, uint64(contracts.MaxMessageSize), resp.Msg.GetMaxMessageSize(),
		"OpenChannelResponse must return the effective payload budget")
	assert.True(t, env.channels.Exists(resp.Msg.GetChannelId()))
}

// TestOpenChannel_PropagatesAuthenticatedUserId pins the user-owned model:
// OpenChannel must announce the authenticated principal both to the worker
// (ChannelOpenRequest.user_id) and back to the client (OpenChannelResponse.user_id).
func TestOpenChannel_PropagatesAuthenticatedUserId(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 1)
	var announcedToWorker string
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			announcedToWorker = open.GetUserId()
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   open.GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:       userid.MustNew(env.user.ID),
		Username: env.user.Username,
	})
	resp, err := channelSvc.OpenChannel(ctx, connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         env.workerID,
		HandshakePayload: []byte("handshake"),
	}))
	require.NoError(t, err)
	assert.Equal(t, env.user.ID, announcedToWorker,
		"ChannelOpenRequest must carry the authenticated user id to the worker")
	assert.Equal(t, env.user.ID, resp.Msg.GetUserId(),
		"OpenChannelResponse must echo the authenticated user id to the client")
}

func TestOpenChannel_ClosesWhenCredentialExpires(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	env.sent = make(chan *leapmuxv1.ConnectResponse, 2)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		env.sent <- msg
		if open := msg.GetChannelOpen(); open != nil {
			env.pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
					ChannelOpenResp: &leapmuxv1.ChannelOpenResponse{
						ChannelId:        open.GetChannelId(),
						HandshakePayload: []byte("worker-handshake"),
						MaxMessageSize:   open.GetMaxMessageSize(),
					},
				},
			})
		}
		return nil

	})
	_, _ = env.worker.Register(conn)

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	// The deadline has to clear the OpenChannel round trip below (a worker
	// registration, a pending ChannelOpen request/response, and a store read)
	// and still land well inside the one-second Eventually that follows. 50ms
	// cleared neither reliably: OpenChannel itself returned "authentication
	// expired" once this package ran under -race, where every one of those steps
	// is several times slower, and the test then failed at its FIRST require --
	// reporting a broken expiry path for what was really a stopwatch. 300ms
	// keeps roughly a 3x margin on both sides.
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:                  userid.MustNew(env.user.ID),
		Username:            env.user.Username,
		CredentialExpiresAt: auth.DeadlineAt(time.Now().Add(300 * time.Millisecond)),
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
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	started := make(chan struct{})
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		close(started)
		<-blocked
		return nil

	})
	_, _ = env.worker.Register(conn)
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
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	blockedStarted := make(chan struct{}, 1)
	conn := workermgrtest.NewConnWithWrite(t, env.workerID, func(msg *leapmuxv1.ConnectResponse) error {
		select {
		case blockedStarted <- struct{}{}:
		default:
		}
		<-blocked
		return nil

	})
	_, _ = env.worker.Register(conn)
	t.Cleanup(func() { close(blocked) })

	healthyWorkerID := id.Generate()
	healthySent := make(chan *leapmuxv1.ConnectResponse, 1)
	healthyConn := workermgrtest.NewConnWithWrite(t, healthyWorkerID, func(msg *leapmuxv1.ConnectResponse) error {
		healthySent <- msg
		return nil

	})
	_, _ = env.worker.Register(healthyConn)

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

func TestCloseChannelsByUserRevocation_DoesNotBlockOnParkedWorkerWrites(t *testing.T) {
	t.Parallel()

	env := setupDirectOpenChannelEnv(t)
	blocked := make(chan struct{})
	var started atomic.Int32

	const workerCount = 20
	for i := 0; i < workerCount; i++ {
		workerID := id.Generate()
		conn := workermgrtest.NewConnWithWrite(t, workerID, func(*leapmuxv1.ConnectResponse) error {
			started.Add(1)
			<-blocked
			return nil
		})
		_, _ = env.worker.Register(conn)
		env.channels.RegisterWithAuthInfo(id.Generate(), workerID, env.user.ID, channelmgr.AuthInfo{}, nil)
	}
	t.Cleanup(func() { close(blocked) })

	channelSvc := service.NewChannelService(env.store, env.worker, env.channels, env.pending, allowAllAuthFreshness{})
	returned := make(chan int, 1)
	go func() {
		returned <- channelSvc.CloseChannelsByUserRevocation(env.user.ID, 1)
	}()
	select {
	case count := <-returned:
		assert.Equal(t, workerCount, count, "local teardown must finish without waiting for drained writes")
	case <-time.After(time.Second):
		require.Fail(t, "revocation teardown blocked behind parked worker writes")
	}
	require.Eventually(t, func() bool { return started.Load() > 0 }, time.Second, time.Millisecond,
		"at least one close frame must still reach a worker drain")
}

func TestCloseChannel_NotFound(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.CloseChannel(ctx, authedReq(
		&leapmuxv1.CloseChannelRequest{ChannelId: "nonexistent"}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCloseChannel_EmptyChannelID(t *testing.T) {
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.CloseChannel(ctx, authedReq(
		&leapmuxv1.CloseChannelRequest{ChannelId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCloseChannel_Success(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()

	_, err := env.channelClient.GetWorkerHandshakeParams(ctx, connect.NewRequest(
		&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: "any"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestGetWorkerHandshakeParams_Classic(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		sentCh <- msg
		return nil

	})
	conn.SetEncryptionMode(leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM)

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
	t.Parallel()

	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	sentCh := make(chan *leapmuxv1.ConnectResponse, 1)
	conn := workermgrtest.NewConnWithWrite(t, workerID, func(msg *leapmuxv1.ConnectResponse) error {
		sentCh <- msg
		return nil

	})
	conn.SetEncryptionMode(leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC)

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

func (e *channelTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	userID = id.Generate()
	hash, _ := password.Hash("testpass2")
	_ = e.store.Users().Create(ctx, store.CreateUserParams{
		ID:           userID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		PasswordSet:  true,
		IsAdmin:      false,
	})
	token, _, _, loginErr := auth.Login(ctx, e.store, "user2", "testpass2", auth.DefaultSessionDuration)
	require.NoError(t, loginErr)
	return
}
