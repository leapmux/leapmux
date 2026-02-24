package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
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

	err = bootstrap.Run(context.Background(), q)
	require.NoError(t, err)

	bgMgr := workermgr.New()
	agentMgr := agentmgr.New()

	tc, tcErr := timeout.NewFromDB(q)
	require.NoError(t, tcErr)

	pendingReqs := workermgr.NewPendingRequests(tc.APITimeout)
	notifierSvc := notifier.New(q, bgMgr, pendingReqs, agentMgr, tc)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(q))

	authPath, authHandler := leapmuxv1connect.NewAuthServiceHandler(service.NewAuthService(q), opts)
	mux.Handle(authPath, authHandler)

	connPath, connHandler := leapmuxv1connect.NewWorkerConnectorServiceHandler(
		service.NewWorkerConnectorService(q, bgMgr), opts)
	mux.Handle(connPath, connHandler)

	mgmtPath, mgmtHandler := leapmuxv1connect.NewWorkerManagementServiceHandler(
		service.NewWorkerManagementService(q, bgMgr, notifierSvc), opts)
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
			Hostname: "dev-machine",
			Os:       "darwin",
			Arch:     "arm64",
			Version:  "0.1.0",
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
	assert.Equal(t, "dev-machine", getRegResp.Msg.GetHostname())

	// Step 4: Admin approves registration.
	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regToken,
		Name:              "my-dev-machine",
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
}

func TestWorkerManagement_ListAndGet(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Register and approve a worker.
	regResp, _ := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Hostname: "h1", Os: "linux", Arch: "amd64", Version: "0.1"},
	))
	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp.Msg.GetRegistrationToken(),
		Name:              "worker-1",
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
	assert.Equal(t, "worker-1", listResp.Msg.GetWorkers()[0].GetName())
	assert.False(t, listResp.Msg.GetWorkers()[0].GetOnline(), "expected offline (no active connection)")

	// Get single worker.
	getReq := connect.NewRequest(&leapmuxv1.GetWorkerRequest{WorkerId: workerID})
	getReq.Header().Set("Authorization", "Bearer "+token)
	getResp, err := env.mgmtClient.GetWorker(ctx, getReq)
	require.NoError(t, err)
	assert.Equal(t, "h1", getResp.Msg.GetWorker().GetHostname())
}

func TestWorkerManagement_Deregister(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Register and approve.
	workerID := env.createAndApproveWorker(t, token, "to-deregister")

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
	workerID := env.createAndApproveWorker(t, adminToken, "admin-owned")

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

	workerID := env.createAndApproveWorker(t, token, "double-deregister")

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

func TestWorkerManagement_Rename(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createAndApproveWorker(t, token, "original-name")

	// Rename.
	renameReq := authedReq(&leapmuxv1.RenameWorkerRequest{
		WorkerId: workerID, Name: "new-name",
	}, token)
	_, err := env.mgmtClient.RenameWorker(ctx, renameReq)
	require.NoError(t, err)

	// Verify.
	getReq := authedReq(&leapmuxv1.GetWorkerRequest{WorkerId: workerID}, token)
	getResp, err := env.mgmtClient.GetWorker(ctx, getReq)
	require.NoError(t, err)
	assert.Equal(t, "new-name", getResp.Msg.GetWorker().GetName())
}

func TestWorkerManagement_Rename_NotOwner(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	workerID := env.createAndApproveWorker(t, adminToken, "admin-rename-test")

	_, user2Token := env.createSecondUser(t)

	renameReq := authedReq(&leapmuxv1.RenameWorkerRequest{
		WorkerId: workerID, Name: "hijacked",
	}, user2Token)
	_, err := env.mgmtClient.RenameWorker(ctx, renameReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkerManagement_Rename_InvalidName(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	workerID := env.createAndApproveWorker(t, token, "valid-name")

	renameReq := authedReq(&leapmuxv1.RenameWorkerRequest{
		WorkerId: workerID, Name: "",
	}, token)
	_, err := env.mgmtClient.RenameWorker(ctx, renameReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
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
		&leapmuxv1.RequestRegistrationRequest{Hostname: "h", Os: "l", Arch: "a", Version: "v"},
	))
	regToken := regResp.Msg.GetRegistrationToken()

	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regToken,
		Name:              "first-approval",
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.NoError(t, err)

	// Try to approve again — should fail.
	approveReq2 := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regToken,
		Name:              "second-approval",
	})
	approveReq2.Header().Set("Authorization", "Bearer "+token)
	_, err = env.mgmtClient.ApproveRegistration(ctx, approveReq2)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestApproveRegistration_EmptyName(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	regResp, _ := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Hostname: "h", Os: "l", Arch: "a", Version: "v"},
	))

	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp.Msg.GetRegistrationToken(),
		Name:              "",
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestApproveRegistration_EmptyToken(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: "",
		Name:              "some-name",
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
		Name:              "some-name",
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

func TestApproveRegistration_DuplicateName(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	token := env.adminToken(t)

	// Register and approve first worker with name "dup-name".
	regResp1, _ := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Hostname: "h1", Os: "l", Arch: "a", Version: "v"},
	))
	approve1 := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp1.Msg.GetRegistrationToken(),
		Name:              "dup-name",
	})
	approve1.Header().Set("Authorization", "Bearer "+token)
	_, err := env.mgmtClient.ApproveRegistration(ctx, approve1)
	require.NoError(t, err)

	// Register another worker and try to approve with the same name.
	regResp2, _ := env.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Hostname: "h2", Os: "l", Arch: "a", Version: "v"},
	))
	approve2 := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp2.Msg.GetRegistrationToken(),
		Name:              "dup-name",
	})
	approve2.Header().Set("Authorization", "Bearer "+token)
	_, err = env.mgmtClient.ApproveRegistration(ctx, approve2)
	require.Error(t, err)
	// Should fail due to UNIQUE(org_id, name) constraint.
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
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

func (e *workerTestEnv) createAndApproveWorker(t *testing.T, token, name string) string {
	t.Helper()
	ctx := context.Background()

	regResp, err := e.connectorClient.RequestRegistration(ctx, connect.NewRequest(
		&leapmuxv1.RequestRegistrationRequest{Hostname: "h", Os: "l", Arch: "a", Version: "v"},
	))
	require.NoError(t, err)
	approveReq := connect.NewRequest(&leapmuxv1.ApproveRegistrationRequest{
		RegistrationToken: regResp.Msg.GetRegistrationToken(),
		Name:              name,
	})
	approveReq.Header().Set("Authorization", "Bearer "+token)
	approveResp, err := e.mgmtClient.ApproveRegistration(ctx, approveReq)
	require.NoError(t, err)
	return approveResp.Msg.GetWorkerId()
}

func TestWorkerManagement_ListWorkerShares_NotVisible(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	// Admin creates a worker (private by default).
	workerID := env.createAndApproveWorker(t, adminToken, "private-worker")

	// Create a second non-admin user in the same org.
	_, user2Token := env.createSecondUser(t)

	// user2 tries to list shares of admin's private worker — should get NotFound.
	req := authedReq(&leapmuxv1.ListWorkerSharesRequest{WorkerId: workerID}, user2Token)
	_, err := env.mgmtClient.ListWorkerShares(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkerManagement_ListWorkerShares_Visible(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	// Admin creates a worker and shares it to org.
	workerID := env.createAndApproveWorker(t, adminToken, "org-shared-worker")
	shareReq := authedReq(&leapmuxv1.UpdateWorkerSharingRequest{
		WorkerId:  workerID,
		ShareMode: leapmuxv1.ShareMode_SHARE_MODE_ORG,
	}, adminToken)
	_, err := env.mgmtClient.UpdateWorkerSharing(ctx, shareReq)
	require.NoError(t, err)

	// Create a second non-admin user in the same org.
	_, user2Token := env.createSecondUser(t)

	// user2 lists shares — should succeed.
	listReq := authedReq(&leapmuxv1.ListWorkerSharesRequest{WorkerId: workerID}, user2Token)
	listResp, err := env.mgmtClient.ListWorkerShares(ctx, listReq)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ShareMode_SHARE_MODE_ORG, listResp.Msg.GetShareMode())
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

func TestWorkerManagement_UpdateWorkerSharing_NotOwner(t *testing.T) {
	env := setupWorkerTestServer(t)
	ctx := context.Background()
	adminToken := env.adminToken(t)

	// Admin creates a worker.
	workerID := env.createAndApproveWorker(t, adminToken, "sharing-test-worker")

	// Create a second non-admin user in the same org.
	_, user2Token := env.createSecondUser(t)

	// user2 tries to update sharing on admin's worker — should get PermissionDenied.
	shareReq := authedReq(&leapmuxv1.UpdateWorkerSharingRequest{
		WorkerId:  workerID,
		ShareMode: leapmuxv1.ShareMode_SHARE_MODE_ORG,
	}, user2Token)
	_, err := env.mgmtClient.UpdateWorkerSharing(ctx, shareReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
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
