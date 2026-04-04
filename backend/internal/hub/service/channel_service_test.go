package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/password"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

type channelTestEnv struct {
	channelClient   leapmuxv1connect.ChannelServiceClient
	connectorClient leapmuxv1connect.WorkerConnectorServiceClient
	mgmtClient      leapmuxv1connect.WorkerManagementServiceClient
	authClient      leapmuxv1connect.AuthServiceClient
	queries         *gendb.Queries
	workerMgr       *workermgr.Manager
	channelMgr      *channelmgr.Manager
}

func setupChannelTestServer(t *testing.T) *channelTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)

	err = bootstrap.Run(context.Background(), q, false)
	require.NoError(t, err)

	cfg := testConfig()
	wMgr := workermgr.New()
	cMgr := channelmgr.New()
	pendingReqs := workermgr.NewPendingRequests(cfg.APITimeout)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(q, false, false))

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(q, cfg), opts)
	mux.Handle(authPath, authHandler)

	connPath, connHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(
		service.NewWorkerConnectorService(q, wMgr, nil, nil, nil, nil, nil), opts)
	mux.Handle(connPath, connHandler)

	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(
		service.NewWorkerManagementService(q, wMgr, nil, nil, false), opts)
	mux.Handle(mgmtPath, mgmtHandler)

	channelSvc := service.NewChannelService(q, wMgr, cMgr, pendingReqs)
	channelPath, channelHandler := leapmuxv1connect.NewChannelServiceHandler(channelSvc, opts)
	mux.Handle(channelPath, channelHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &channelTestEnv{
		channelClient:   leapmuxv1connect.NewChannelServiceClient(server.Client(), server.URL),
		connectorClient: leapmuxv1connect.NewWorkerConnectorServiceClient(server.Client(), server.URL),
		mgmtClient:      leapmuxv1connect.NewWorkerManagementServiceClient(server.Client(), server.URL),
		authClient:      leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL),
		queries:         q,
		workerMgr:       wMgr,
		channelMgr:      cMgr,
	}
}

func (e *channelTestEnv) adminToken(t *testing.T) string {
	t.Helper()
	resp, err := e.authClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)
	return sessionFromCookie(t, resp.Header().Get("Set-Cookie"))
}

func (e *channelTestEnv) createWorkerWithKey(t *testing.T, token string, publicKey []byte) string {
	t.Helper()
	ctx := context.Background()

	regResp, err := e.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Version: "v"},
	))
	require.NoError(t, err)

	approveReq := authedReq(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp.Msg.GetRegistrationToken(),
	}, token)
	approveResp, err := e.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.NoError(t, err)

	workerID := approveResp.Msg.GetWorkerId()

	// Set the public key.
	if len(publicKey) > 0 {
		err = e.queries.UpdateWorkerPublicKey(ctx, gendb.UpdateWorkerPublicKeyParams{
			PublicKey:       publicKey,
			MlkemPublicKey:  []byte{},
			SlhdsaPublicKey: []byte{},
			ID:              workerID,
		})
		require.NoError(t, err)
	}

	return workerID
}

func TestGetWorkerPublicKey(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	pubKey := []byte("fake-public-key-32-bytes-long!!!!")
	workerID := env.createWorkerWithKey(t, token, pubKey)

	resp, err := env.channelClient.GetWorkerPublicKey(ctx, authedReq(
		&leapmuxv1.GetWorkerPublicKeyRequest{WorkerId: workerID}, token))
	require.NoError(t, err)
	assert.Equal(t, pubKey, resp.Msg.GetPublicKey())
}

func TestGetWorkerPublicKey_NoKey(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Create worker without public key.
	workerID := env.createWorkerWithKey(t, token, nil)

	_, err := env.channelClient.GetWorkerPublicKey(ctx, authedReq(
		&leapmuxv1.GetWorkerPublicKeyRequest{WorkerId: workerID}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestGetWorkerPublicKey_NotFound(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerPublicKey(ctx, authedReq(
		&leapmuxv1.GetWorkerPublicKeyRequest{WorkerId: "nonexistent"}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetWorkerPublicKey_EmptyWorkerID(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerPublicKey(ctx, authedReq(
		&leapmuxv1.GetWorkerPublicKeyRequest{WorkerId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
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
		OrgID:    "test-org",
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentCh <- msg
			return nil
		},
	}
	env.workerMgr.Register(conn)

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
		t.Fatal("expected a message to be sent to worker")
	}
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
	adminUser, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	// Register a channel in the channel manager.
	channelID := id.Generate()
	env.channelMgr.Register(channelID, workerID, adminUser.ID, nil)

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
	adminUser, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	// Register a channel owned by admin in channel manager.
	channelID := id.Generate()
	env.channelMgr.Register(channelID, workerID, adminUser.ID, nil)

	// Create a second user.
	_, user2Token := env.createSecondUser(t)

	// user2 should not be able to close admin's channel.
	_, err = env.channelClient.CloseChannel(ctx, authedReq(
		&leapmuxv1.CloseChannelRequest{ChannelId: channelID}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetWorkerPublicKey_Unauthenticated(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()

	_, err := env.channelClient.GetWorkerPublicKey(ctx, connect.NewRequest(
		&leapmuxv1.GetWorkerPublicKeyRequest{WorkerId: "any"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// --- GetWorkerEncryptionMode tests ---

func TestGetWorkerEncryptionMode_WorkerOffline(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	_, err := env.channelClient.GetWorkerEncryptionMode(ctx, authedReq(
		&leapmuxv1.GetWorkerEncryptionModeRequest{WorkerId: workerID}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

func TestGetWorkerEncryptionMode_EmptyWorkerID(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	_, err := env.channelClient.GetWorkerEncryptionMode(ctx, authedReq(
		&leapmuxv1.GetWorkerEncryptionModeRequest{WorkerId: ""}, token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetWorkerEncryptionMode_Unauthenticated(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()

	_, err := env.channelClient.GetWorkerEncryptionMode(ctx, connect.NewRequest(
		&leapmuxv1.GetWorkerEncryptionModeRequest{WorkerId: "any"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestGetWorkerEncryptionMode_PostQuantum(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	// Simulate worker online with POST_QUANTUM mode.
	conn := &workermgr.Conn{
		WorkerID:       workerID,
		OrgID:          "test-org",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
	}
	env.workerMgr.Register(conn)

	resp, err := env.channelClient.GetWorkerEncryptionMode(ctx, authedReq(
		&leapmuxv1.GetWorkerEncryptionModeRequest{WorkerId: workerID}, token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, resp.Msg.GetEncryptionMode())
}

func TestGetWorkerEncryptionMode_Classic(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	conn := &workermgr.Conn{
		WorkerID:       workerID,
		OrgID:          "test-org",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}
	env.workerMgr.Register(conn)

	resp, err := env.channelClient.GetWorkerEncryptionMode(ctx, authedReq(
		&leapmuxv1.GetWorkerEncryptionModeRequest{WorkerId: workerID}, token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, resp.Msg.GetEncryptionMode())
}

func TestGetWorkerEncryptionMode_UnspecifiedDefaultsToPostQuantum(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	// Register with UNSPECIFIED — should be normalized to POST_QUANTUM.
	conn := &workermgr.Conn{
		WorkerID:       workerID,
		OrgID:          "test-org",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED,
	}
	env.workerMgr.Register(conn)

	resp, err := env.channelClient.GetWorkerEncryptionMode(ctx, authedReq(
		&leapmuxv1.GetWorkerEncryptionModeRequest{WorkerId: workerID}, token))
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
		OrgID:          "test-org",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentCh <- msg
			return nil
		},
	}
	env.workerMgr.Register(conn)

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
		t.Fatal("expected a message to be sent to worker")
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
		OrgID:          "test-org",
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentCh <- msg
			return nil
		},
	}
	env.workerMgr.Register(conn)

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
		t.Fatal("expected a message to be sent to worker")
	}
}

// --- PrepareWorkspaceAccess tests ---

func (e *channelTestEnv) createWorkspace(t *testing.T, ownerUserID, orgID string) string {
	t.Helper()
	wsID := id.Generate()
	err := e.queries.CreateWorkspace(context.Background(), gendb.CreateWorkspaceParams{
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

	adminUser, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, token, []byte("key"))

	// Simulate worker online with a mock connection that captures sent messages.
	sentMsgs := make(chan *leapmuxv1.ConnectResponse, 10)
	conn := &workermgr.Conn{
		WorkerID: workerID,
		OrgID:    adminUser.OrgID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentMsgs <- msg
			return nil
		},
	}
	env.workerMgr.Register(conn)

	// Register a channel for the admin user on this worker.
	channelID := id.Generate()
	env.channelMgr.Register(channelID, workerID, adminUser.ID, nil)

	// Create a workspace owned by the admin.
	wsID := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)

	// Call PrepareWorkspaceAccess.
	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsID,
		}, token))
	require.NoError(t, err)

	// Verify a ChannelAccessUpdate was sent to the worker.
	select {
	case msg := <-sentMsgs:
		update := msg.GetChannelAccessUpdate()
		require.NotNil(t, update)
		assert.Equal(t, channelID, update.GetChannelId())
		assert.Equal(t, wsID, update.GetWorkspaceId())
	default:
		t.Fatal("expected a ChannelAccessUpdate to be sent to worker")
	}
}

func TestPrepareWorkspaceAccess_OnlySendsToMatchingWorker(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	worker1ID := env.createWorkerWithKey(t, token, []byte("key1"))
	worker2ID := env.createWorkerWithKey(t, token, []byte("key2"))

	// Register both workers online.
	sent1 := make(chan *leapmuxv1.ConnectResponse, 10)
	conn1 := &workermgr.Conn{
		WorkerID: worker1ID,
		OrgID:    adminUser.OrgID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent1 <- msg
			return nil
		},
	}
	env.workerMgr.Register(conn1)

	sent2 := make(chan *leapmuxv1.ConnectResponse, 10)
	conn2 := &workermgr.Conn{
		WorkerID: worker2ID,
		OrgID:    adminUser.OrgID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sent2 <- msg
			return nil
		},
	}
	env.workerMgr.Register(conn2)

	// Register channels: one on each worker.
	ch1ID := id.Generate()
	env.channelMgr.Register(ch1ID, worker1ID, adminUser.ID, nil)
	ch2ID := id.Generate()
	env.channelMgr.Register(ch2ID, worker2ID, adminUser.ID, nil)

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
		t.Fatal("expected worker1 to receive ChannelAccessUpdate")
	}

	// worker2 should NOT have received anything.
	select {
	case msg := <-sent2:
		t.Fatalf("worker2 should not receive any message, got: %v", msg)
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

	adminUser, err := env.queries.GetUserByUsername(ctx, "admin")
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

func TestPrepareWorkspaceAccess_SharedWorkspaceAccess(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	adminUser, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	workerID := env.createWorkerWithKey(t, adminToken, []byte("key"))

	// Simulate worker online.
	sentMsgs := make(chan *leapmuxv1.ConnectResponse, 10)
	conn := &workermgr.Conn{
		WorkerID: workerID,
		OrgID:    adminUser.OrgID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			sentMsgs <- msg
			return nil
		},
	}
	env.workerMgr.Register(conn)

	// Create workspace owned by admin.
	wsID := env.createWorkspace(t, adminUser.ID, adminUser.OrgID)

	// Create user2 and grant workspace access.
	user2ID, user2Token := env.createSecondUser(t)
	err = env.queries.GrantWorkspaceAccess(ctx, gendb.GrantWorkspaceAccessParams{
		WorkspaceID: wsID,
		UserID:      user2ID,
	})
	require.NoError(t, err)

	// Register a channel for user2 on this worker.
	channelID := id.Generate()
	env.channelMgr.Register(channelID, workerID, user2ID, nil)

	// user2 should succeed because they have explicit workspace access.
	_, err = env.channelClient.PrepareWorkspaceAccess(ctx, authedReq(
		&leapmuxv1.PrepareWorkspaceAccessRequest{
			WorkerId:    workerID,
			WorkspaceId: wsID,
		}, user2Token))
	require.NoError(t, err)

	select {
	case msg := <-sentMsgs:
		update := msg.GetChannelAccessUpdate()
		require.NotNil(t, update)
		assert.Equal(t, channelID, update.GetChannelId())
		assert.Equal(t, wsID, update.GetWorkspaceId())
	default:
		t.Fatal("expected a ChannelAccessUpdate for user2's channel")
	}
}

func TestPrepareWorkspaceAccess_WorkerOffline(t *testing.T) {
	env := setupChannelTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	adminUser, err := env.queries.GetUserByUsername(ctx, "admin")
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

	adminUser, err := e.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	userID = id.Generate()
	hash, _ := password.Hash("pass2")
	_ = e.queries.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        adminUser.OrgID,
		Username:     "user2",
		PasswordHash: hash,
		DisplayName:  "User 2",
		IsAdmin:      0,
	})
	_ = e.queries.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  adminUser.OrgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	token, _, _, loginErr := auth.Login(ctx, e.queries, "user2", "pass2")
	require.NoError(t, loginErr)
	return
}
