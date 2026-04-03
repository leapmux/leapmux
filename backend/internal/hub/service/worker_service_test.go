package service_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

type workerTestEnv struct {
	connectorClient leapmuxv1connect.WorkerConnectorServiceClient
	mgmtClient      leapmuxv1connect.WorkerManagementServiceClient
	authClient      leapmuxv1connect.AuthServiceClient
	queries         *gendb.Queries
}

func setupWorkerTestServer(t *testing.T) *workerTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)

	err = bootstrap.Run(context.Background(), q, false)
	require.NoError(t, err)

	bgMgr := workermgr.New()

	cfg := testConfig()
	pendingReqs := workermgr.NewPendingRequests(cfg.APITimeout)
	notifierSvc := notifier.New(q, bgMgr, pendingReqs, cfg)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(q, false))

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(q, cfg), opts)
	mux.Handle(authPath, authHandler)

	connPath, connHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(
		service.NewWorkerConnectorService(q, bgMgr), opts)
	mux.Handle(connPath, connHandler)

	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(
		service.NewWorkerManagementService(q, bgMgr, nil, notifierSvc, false), opts) //nolint:staticcheck // nil broadcaster is fine for tests that don't check control frames
	mux.Handle(mgmtPath, mgmtHandler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &workerTestEnv{
		connectorClient: leapmuxv1connect.NewWorkerConnectorServiceClient(server.Client(), server.URL),
		mgmtClient:      leapmuxv1connect.NewWorkerManagementServiceClient(server.Client(), server.URL),
		authClient:      leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL),
		queries:         q,
	}
}

func (e *workerTestEnv) adminToken(t *testing.T) string {
	t.Helper()
	resp, err := e.authClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username: "admin",
		Password: "admin",
	}))
	require.NoError(t, err)
	return resp.Msg.GetToken()
}

func TestRegistrationFlow(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()

	// Step 1: Worker requests registration (no auth required).
	regResp, err := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{
			Version: "0.1.0",
		},
	))
	require.NoError(t, err)
	regToken := regResp.Msg.GetRegistrationToken()
	assert.NotEmpty(t, regToken)
	assert.NotEmpty(t, regResp.Msg.GetRegistrationUrl())

	// Step 2: Worker polls — should be pending.
	pollResp, err := env.connectorClient.PollRegistration(ctx, connect.NewRequest(
		&leapmuxv1.PollRegistrationRequest{RegistrationToken: regToken},
	))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING, pollResp.Msg.GetStatus())

	// Step 3: Admin views registration details.
	token := env.adminToken(t)
	getRegReq := connect.NewRequest(&leapmuxv1.GetRegistrationRequest{
		RegistrationToken: regToken,
	})
	getRegReq.Header().Set("Authorization", "Bearer "+token)
	getRegResp, err := env.mgmtClient.GetRegistration(ctx, getRegReq)
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", getRegResp.Msg.GetVersion())

	// Step 4: Admin approves registration.
	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regToken,
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	approveResp, err := env.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.NoError(t, err)
	workerID := approveResp.Msg.GetWorkerId()
	require.NotEmpty(t, workerID)

	// Step 5: Worker polls again — should be approved with credentials.
	pollResp2, err := env.connectorClient.PollRegistration(ctx, connect.NewRequest(
		&leapmuxv1.PollRegistrationRequest{RegistrationToken: regToken},
	))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED, pollResp2.Msg.GetStatus())
	assert.Equal(t, workerID, pollResp2.Msg.GetWorkerId())
	assert.NotEmpty(t, pollResp2.Msg.GetAuthToken())
	assert.NotEmpty(t, pollResp2.Msg.GetOrgId())
	assert.NotEmpty(t, pollResp2.Msg.GetRegisteredBy(), "registered_by should be populated")
}

func TestWorkerManagement_ListAndGet(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Register and approve a worker.
	regResp, _ := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Version: "0.1"},
	))
	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp.Msg.GetRegistrationToken(),
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	approveResp, _ := env.mgmtClient.ApproveRegistration(ctx, approveReq)
	workerID := approveResp.Msg.GetWorkerId()

	// List workers.
	listReq := connect.NewRequest(&leapmuxv1.ListWorkersRequest{})
	listReq.Header().Set("Authorization", "Bearer "+token)
	listResp, err := env.mgmtClient.ListWorkers(ctx, listReq)
	require.NoError(t, err)
	require.Len(t, listResp.Msg.GetWorkers(), 1)
	assert.Equal(t, workerID, listResp.Msg.GetWorkers()[0].GetId())
	assert.False(t, listResp.Msg.GetWorkers()[0].GetOnline(), "expected offline (no active connection)")

	// Get single worker.
	getReq := connect.NewRequest(&leapmuxv1.GetWorkerRequest{WorkerId: workerID})
	getReq.Header().Set("Authorization", "Bearer "+token)
	getResp, err := env.mgmtClient.GetWorker(ctx, getReq)
	require.NoError(t, err)
	assert.Equal(t, workerID, getResp.Msg.GetWorker().GetId())
}

func TestWorkerManagement_Deregister(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Register and approve.
	workerID := env.createAndApproveWorker(t, token)

	// Deregister worker.
	deregReq := authedReq(&leapmuxv1.DeregisterWorkerRequest{WorkerId: workerID}, token)
	_, err := env.mgmtClient.DeregisterWorker(ctx, deregReq)
	require.NoError(t, err)

	// Verify it's gone from list.
	listReq := authedReq(&leapmuxv1.ListWorkersRequest{}, token)
	listResp, err := env.mgmtClient.ListWorkers(ctx, listReq)
	require.NoError(t, err)
	for _, b := range listResp.Msg.GetWorkers() {
		assert.NotEqual(t, workerID, b.GetId(), "deregistered worker still visible in list")
	}
}

func TestWorkerManagement_Deregister_NotOwner(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	// Admin creates a worker.
	workerID := env.createAndApproveWorker(t, adminToken)

	// Create a second non-admin user.
	_, user2Token := env.createSecondUser(t)

	// user2 tries to deregister admin's worker — should fail.
	deregReq := authedReq(&leapmuxv1.DeregisterWorkerRequest{WorkerId: workerID}, user2Token)
	_, err := env.mgmtClient.DeregisterWorker(ctx, deregReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkerManagement_Deregister_AlreadyDeregistering(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createAndApproveWorker(t, token)

	// First deregister.
	deregReq := authedReq(&leapmuxv1.DeregisterWorkerRequest{WorkerId: workerID}, token)
	_, err := env.mgmtClient.DeregisterWorker(ctx, deregReq)
	require.NoError(t, err)

	// Second deregister — should fail (no longer active).
	deregReq2 := authedReq(&leapmuxv1.DeregisterWorkerRequest{WorkerId: workerID}, token)
	_, err = env.mgmtClient.DeregisterWorker(ctx, deregReq2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkerManagement_Unauthenticated(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()

	// ListWorkers without token should fail.
	_, err := env.mgmtClient.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestPollRegistration_InvalidToken(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()

	_, err := env.connectorClient.PollRegistration(ctx, connect.NewRequest(
		&leapmuxv1.PollRegistrationRequest{RegistrationToken: "nonexistent-token"},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestPollRegistration_EmptyToken(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()

	_, err := env.connectorClient.PollRegistration(ctx, connect.NewRequest(
		&leapmuxv1.PollRegistrationRequest{RegistrationToken: ""},
	))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestApproveRegistration_AlreadyApproved(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Register and approve.
	regResp, _ := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Version: "v"},
	))
	regToken := regResp.Msg.GetRegistrationToken()

	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regToken,
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.NoError(t, err)

	// Try to approve again — should fail.
	approveReq2 := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regToken,
	})
	approveReq2.Header().Set("Authorization", "Bearer "+token)
	_, err = env.mgmtClient.ApproveRegistration(ctx, approveReq2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestApproveRegistration_EmptyToken(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: "",
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestApproveRegistration_UnknownToken(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: "nonexistent-reg-token",
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetRegistration_UnknownToken(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	req := connect.NewRequest(&leapmuxv1.GetRegistrationRequest{
		RegistrationToken: "nonexistent-reg-token",
	})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.GetRegistration(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetRegistration_EmptyToken(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	req := connect.NewRequest(&leapmuxv1.GetRegistrationRequest{
		RegistrationToken: "",
	})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.GetRegistration(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestGetWorker_NotFound(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	req := connect.NewRequest(&leapmuxv1.GetWorkerRequest{WorkerId: "nonexistent-worker-id"})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.GetWorker(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func (e *workerTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	ctx := context.Background()

	// Get the admin's org ID.
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
	// Add as org member.
	_ = e.queries.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  adminUser.OrgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	token, _, loginErr := auth.Login(ctx, e.queries, "user2", "pass2")
	require.NoError(t, loginErr)
	return
}

func (e *workerTestEnv) createAndApproveWorker(t *testing.T, token string) string {
	t.Helper()
	ctx := context.Background()

	regResp, err := e.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Version: "v"},
	))
	require.NoError(t, err)
	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp.Msg.GetRegistrationToken(),
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	approveResp, err := e.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.NoError(t, err)
	return approveResp.Msg.GetWorkerId()
}

func TestWorkerManagement_Deregister_Nonexistent(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	// Admin tries to deregister a nonexistent worker.
	deregReq := authedReq(&leapmuxv1.DeregisterWorkerRequest{WorkerId: "nonexistent-worker-id"}, adminToken)
	_, err := env.mgmtClient.DeregisterWorker(ctx, deregReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkerService_ListWorkers_Empty(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// No workers have been registered, so listing should return an empty list.
	listReq := authedReq(&leapmuxv1.ListWorkersRequest{}, token)
	listResp, err := env.mgmtClient.ListWorkers(ctx, listReq)
	require.NoError(t, err)
	assert.Empty(t, listResp.Msg.GetWorkers())
}

// --- Unix socket auto-approve tests ---

// unixSocketTestEnv extends workerTestEnv with a channel manager to capture
// control frames, and provides a ConnectRPC client connected via Unix socket.
type unixSocketTestEnv struct {
	workerTestEnv
	cMgr           *channelmgr.Manager
	unixConnClient leapmuxv1connect.WorkerConnectorServiceClient
	unixMgmtClient leapmuxv1connect.WorkerManagementServiceClient
	controlFrames  []*leapmuxv1.ChannelMessage
}

func setupUnixSocketTestServer(t *testing.T) *unixSocketTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	q := gendb.New(sqlDB)
	err = bootstrap.Run(context.Background(), q, false)
	require.NoError(t, err)

	bgMgr := workermgr.New()
	cMgr := channelmgr.New()

	cfg := testConfig()
	pendingReqs := workermgr.NewPendingRequests(cfg.APITimeout)
	notifierSvc := notifier.New(q, bgMgr, pendingReqs, cfg)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(q, false))

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(q, cfg), opts)
	mux.Handle(authPath, authHandler)

	broadcaster := service.NewHubEventBroadcaster(cMgr)
	broadcaster.SetDebounceInterval(50 * time.Millisecond)

	connSvc := service.NewWorkerConnectorService(q, bgMgr)
	connSvc.SetChannelMgr(cMgr)
	connSvc.SetBroadcaster(broadcaster)
	connSvc.SetPollTimeout(3 * time.Second)
	connPath, connHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(connSvc, opts)
	mux.Handle(connPath, connHandler)

	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(
		service.NewWorkerManagementService(q, bgMgr, broadcaster, notifierSvc, false), opts)
	mux.Handle(mgmtPath, mgmtHandler)

	// Start a Unix socket server.
	// Use a short path under /tmp to stay within the 104-byte macOS limit.
	sockDir, err := os.MkdirTemp("", "lmtest")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "s.sock")
	unixLn, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(unixLn) }()
	t.Cleanup(func() { _ = srv.Close() })

	// Also start a TCP server for non-socket tests.
	tcpServer := httptest.NewServer(mux)
	t.Cleanup(tcpServer.Close)

	// Build an HTTP client that dials the Unix socket.
	unixClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	env := &unixSocketTestEnv{
		workerTestEnv: workerTestEnv{
			connectorClient: leapmuxv1connect.NewWorkerConnectorServiceClient(tcpServer.Client(), tcpServer.URL),
			mgmtClient:      leapmuxv1connect.NewWorkerManagementServiceClient(tcpServer.Client(), tcpServer.URL),
			authClient:      leapmuxv1connect.NewAuthServiceClient(tcpServer.Client(), tcpServer.URL),
			queries:         q,
		},
		cMgr:           cMgr,
		unixConnClient: leapmuxv1connect.NewWorkerConnectorServiceClient(unixClient, "http://localhost"),
		unixMgmtClient: leapmuxv1connect.NewWorkerManagementServiceClient(unixClient, "http://localhost"),
	}

	return env
}

// bindControlFrameListener registers a fake WebSocket connection on the channel
// manager for the given user and captures all received messages.
func (e *unixSocketTestEnv) bindControlFrameListener(userID string) {
	e.cMgr.BindUser(userID, "test-conn", func(msg *leapmuxv1.ChannelMessage) error {
		e.controlFrames = append(e.controlFrames, msg)
		return nil
	}, nil)
}

// waitForDebounce waits long enough for the debounced control frame to fire.
func (e *unixSocketTestEnv) waitForDebounce() {
	time.Sleep(150 * time.Millisecond)
}

// assertWorkersChangedReceived verifies that a WorkersChanged control frame was
// received at the given index.
func (e *unixSocketTestEnv) assertWorkersChangedReceived(t *testing.T, index int) {
	t.Helper()
	require.Greater(t, len(e.controlFrames), index, "expected at least %d control frame(s)", index+1)

	msg := e.controlFrames[index]
	assert.Equal(t, channelmgr.HubControlChannelID, msg.GetChannelId())

	var frame leapmuxv1.HubControlFrame
	require.NoError(t, proto.Unmarshal(msg.GetCiphertext(), &frame))
	assert.Contains(t, frame.GetEvents(), leapmuxv1.HubControlEvent_HUB_CONTROL_EVENT_WORKERS_CHANGED)
}

func TestRegistration_NotAutoApprovedViaTCP(t *testing.T) {
	env := setupUnixSocketTestServer(t)
	ctx := context.Background()

	// Request registration via TCP — should NOT be auto-approved.
	regResp, err := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Version: "0.1.0"},
	))
	require.NoError(t, err)
	regToken := regResp.Msg.GetRegistrationToken()

	// Poll should return PENDING (not auto-approved).
	pollResp, err := env.connectorClient.PollRegistration(ctx, connect.NewRequest(
		&leapmuxv1.PollRegistrationRequest{RegistrationToken: regToken},
	))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING, pollResp.Msg.GetStatus())
}

func TestApproveRegistration_SendsWorkersChanged(t *testing.T) {
	env := setupUnixSocketTestServer(t)
	ctx := context.Background()

	// Register control frame listener for the admin.
	admin, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)
	env.bindControlFrameListener(admin.ID)

	// Register worker via TCP (not auto-approved).
	regResp, err := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Version: "0.1.0"},
	))
	require.NoError(t, err)
	regToken := regResp.Msg.GetRegistrationToken()

	// Admin approves the registration.
	token := env.adminToken(t)
	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regToken,
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	_, err = env.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.NoError(t, err)

	// Verify WorkersChanged control frame was sent (after debounce).
	env.waitForDebounce()
	env.assertWorkersChangedReceived(t, 0)
}

func TestDeregisterWorker_SendsWorkersChanged(t *testing.T) {
	env := setupUnixSocketTestServer(t)
	ctx := context.Background()

	// Register control frame listener for the admin.
	admin, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)
	env.bindControlFrameListener(admin.ID)

	// Create and approve a worker via TCP.
	token := env.adminToken(t)
	workerID := env.createAndApproveWorker(t, token)

	// Clear the WorkersChanged from approval.
	env.controlFrames = nil

	// Deregister the worker.
	deregReq := connect.NewRequest(&leapmuxv1.DeregisterWorkerRequest{
		WorkerId: workerID,
	})
	deregReq.Header().Set("Authorization", "Bearer "+token)
	_, err = env.mgmtClient.DeregisterWorker(ctx, deregReq)
	require.NoError(t, err)

	// Verify WorkersChanged control frame was sent (after debounce).
	env.waitForDebounce()
	env.assertWorkersChangedReceived(t, 0)
}

func TestRegistration_MultipleAutoApproveViaUnixSocket(t *testing.T) {
	env := setupUnixSocketTestServer(t)
	ctx := context.Background()

	// Register control frame listener for the admin.
	admin, err := env.queries.GetUserByUsername(ctx, "admin")
	require.NoError(t, err)
	env.bindControlFrameListener(admin.ID)

	// Auto-approve two workers via Unix socket in quick succession.
	for i := 0; i < 2; i++ {
		regResp, regErr := env.unixConnClient.RequestRegistration(ctx, connect.NewRequest(
			&leapmuxv1.RequestRegistrationRequest{Version: "0.1.0"},
		))
		require.NoError(t, regErr)

		pollResp, pollErr := env.unixConnClient.PollRegistration(ctx, connect.NewRequest(
			&leapmuxv1.PollRegistrationRequest{RegistrationToken: regResp.Msg.GetRegistrationToken()},
		))
		require.NoError(t, pollErr)
		assert.Equal(t, leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED, pollResp.Msg.GetStatus())
	}

	// Debouncing should consolidate the two events into a single frame.
	env.waitForDebounce()
	assert.Len(t, env.controlFrames, 1)
	env.assertWorkersChangedReceived(t, 0)

	// Verify two distinct workers exist.
	listReq := authedReq(&leapmuxv1.ListWorkersRequest{}, env.adminToken(t))
	listResp, err := env.mgmtClient.ListWorkers(ctx, listReq)
	require.NoError(t, err)
	assert.Len(t, listResp.Msg.GetWorkers(), 2)
}
