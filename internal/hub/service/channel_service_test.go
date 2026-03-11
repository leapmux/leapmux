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
	"golang.org/x/crypto/bcrypt"

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
	opts := connect.WithInterceptors(auth.NewInterceptor(q, false))

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(q, cfg), opts)
	mux.Handle(authPath, authHandler)

	connPath, connHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(
		service.NewWorkerConnectorService(q, wMgr, false), opts)
	mux.Handle(connPath, connHandler)

	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(
		service.NewWorkerManagementService(q, wMgr, nil, false), opts)
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
	return resp.Msg.GetToken()
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
			PublicKey: publicKey,
			ID:        workerID,
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

	// Empty handshake_payload.
	_, err = env.channelClient.OpenChannel(ctx, authedReq(
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         "some-id",
			HandshakePayload: nil,
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

func (e *channelTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	adminUser, err := e.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)

	userID = id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass2"), bcrypt.MinCost)
	_ = e.queries.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        adminUser.OrgID,
		Username:     "user2",
		PasswordHash: string(hash),
		DisplayName:  "User 2",
		IsAdmin:      0,
	})
	_ = e.queries.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  adminUser.OrgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	token, _, loginErr := auth.Login(ctx, e.queries, "user2", "pass2")
	require.NoError(t, loginErr)
	return
}
