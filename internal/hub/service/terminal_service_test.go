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
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/terminalmgr"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

type terminalTestEnv struct {
	client      leapmuxv1connect.TerminalServiceClient
	queries     *gendb.Queries
	workerMgr   *workermgr.Manager
	workerConn  *workermgr.Conn
	termMgr     *terminalmgr.Manager
	pending     *workermgr.PendingRequests
	terminalSvc *service.TerminalService
	token       string
	orgID       string
	userID      string
	workerID    string
}

func setupTerminalTest(t *testing.T) *terminalTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)
	workerMgr := workermgr.New()
	termMgr := terminalmgr.New()

	tc, tcErr := timeout.NewFromDB(queries)
	require.NoError(t, tcErr)

	pending := workermgr.NewPendingRequests(tc.APITimeout)

	worktreeHelper := service.NewWorktreeHelper(queries, workerMgr, pending, tc)
	terminalSvc := service.NewTerminalService(queries, workerMgr, termMgr, pending, worktreeHelper)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(queries))
	path, handler := leapmuxv1connect.NewTerminalServiceHandler(terminalSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewTerminalServiceClient(
		server.Client(),
		server.URL,
		connect.WithGRPC(),
	)

	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)

	_ = queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "test-org"})
	_ = queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: string(hash),
		DisplayName:  "Test",
		IsAdmin:      1,
	})

	token, _, err := auth.Login(context.Background(), queries, "testuser", "pass")
	require.NoError(t, err)

	workerID := id.Generate()
	_ = queries.CreateWorker(context.Background(), gendb.CreateWorkerParams{
		ID:           workerID,
		OrgID:        orgID,
		Name:         "test-worker",
		Hostname:     "localhost",
		AuthToken:    id.Generate(),
		RegisteredBy: userID,
	})

	workerConn := &workermgr.Conn{
		WorkerID: workerID,
		OrgID:    orgID,
	}
	workerMgr.Register(workerConn)

	return &terminalTestEnv{
		client:      client,
		queries:     queries,
		workerMgr:   workerMgr,
		workerConn:  workerConn,
		termMgr:     termMgr,
		pending:     pending,
		terminalSvc: terminalSvc,
		token:       token,
		orgID:       orgID,
		userID:      userID,
		workerID:    workerID,
	}
}

// createWorkspaceInDB creates an active workspace owned by the primary test user.
func (e *terminalTestEnv) createWorkspaceInDB(t *testing.T, title string) string {
	t.Helper()
	workspaceID := id.Generate()
	err := e.queries.CreateWorkspace(context.Background(), gendb.CreateWorkspaceParams{
		ID:        workspaceID,
		OrgID:     e.orgID,
		CreatedBy: e.userID,
		Title:     title,
	})
	require.NoError(t, err)
	return workspaceID
}

// createSecondUser creates a second non-admin user in the same org and returns
// the user ID and auth token.
func (e *terminalTestEnv) createSecondUser(t *testing.T) (string, string) {
	t.Helper()
	userID := id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass2"), bcrypt.MinCost)
	err := e.queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        e.orgID,
		Username:     "testuser2",
		PasswordHash: string(hash),
		DisplayName:  "Test2",
		IsAdmin:      0,
	})
	require.NoError(t, err)

	token, _, err := auth.Login(context.Background(), e.queries, "testuser2", "pass2")
	require.NoError(t, err)
	return userID, token
}

func TestTerminalService_OpenTerminal_NotOwner(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "User1 Workspace")
	_, user2Token := env.createSecondUser(t)

	_, err := env.client.OpenTerminal(context.Background(), authedReq(&leapmuxv1.OpenTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Cols:        80,
		Rows:        24,
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTerminalService_OpenTerminal_WorkerOffline(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Worker Down")
	env.workerMgr.Unregister(env.workerID, env.workerConn)

	_, err := env.client.OpenTerminal(context.Background(), authedReq(&leapmuxv1.OpenTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		WorkerId:    env.workerID,
		Cols:        80,
		Rows:        24,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestTerminalService_OpenTerminal_MissingWorkspaceID(t *testing.T) {
	env := setupTerminalTest(t)

	_, err := env.client.OpenTerminal(context.Background(), authedReq(&leapmuxv1.OpenTerminalRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestTerminalService_CloseTerminal_NotOwner(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "User1 Workspace")
	_, user2Token := env.createSecondUser(t)

	_, err := env.client.CloseTerminal(context.Background(), authedReq(&leapmuxv1.CloseTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		TerminalId:  "some-terminal",
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTerminalService_SendInput_NotOwner(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "User1 Workspace")
	_, user2Token := env.createSecondUser(t)

	_, err := env.client.SendInput(context.Background(), authedReq(&leapmuxv1.SendInputRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		TerminalId:  "some-terminal",
		Data:        []byte("ls\n"),
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTerminalService_ResizeTerminal_NotOwner(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "User1 Workspace")
	_, user2Token := env.createSecondUser(t)

	_, err := env.client.ResizeTerminal(context.Background(), authedReq(&leapmuxv1.ResizeTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		TerminalId:  "some-terminal",
		Cols:        120,
		Rows:        40,
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTerminalService_ListTerminals_NotOwner(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "User1 Workspace")
	_, user2Token := env.createSecondUser(t)

	_, err := env.client.ListTerminals(context.Background(), authedReq(&leapmuxv1.ListTerminalsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
	}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTerminalService_OpenTerminal_InvalidWorkingDir(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Invalid Dir Workspace")

	// Set up SendFn on the worker conn to intercept the TerminalStartRequest,
	// then simulate the worker responding with an error via pending.Complete.
	conn := env.workerMgr.Get(env.workerID)
	conn.SendFn = func(msg *leapmuxv1.ConnectResponse) error {
		requestID := msg.GetRequestId()
		go func() {
			env.pending.Complete(requestID, &leapmuxv1.ConnectRequest{
				RequestId: requestID,
				Payload: &leapmuxv1.ConnectRequest_TerminalStarted{
					TerminalStarted: &leapmuxv1.TerminalStarted{
						TerminalId: msg.GetTerminalStart().GetTerminalId(),
						Error:      "stat working directory \"/nonexistent/path\": no such file or directory",
					},
				},
			})
		}()
		return nil
	}

	_, err := env.client.OpenTerminal(context.Background(), authedReq(&leapmuxv1.OpenTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		WorkerId:    env.workerID,
		Cols:        80,
		Rows:        24,
		WorkingDir:  "/nonexistent/path",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestTerminalService_HandleTerminalOutput(t *testing.T) {
	env := setupTerminalTest(t)
	terminalID := id.Generate()

	watcher := env.termMgr.Watch(terminalID)
	defer env.termMgr.Unwatch(terminalID, watcher)

	env.terminalSvc.HandleTerminalOutput(&leapmuxv1.TerminalOutput{
		TerminalId: terminalID,
		Data:       []byte("output data"),
	})

	select {
	case event := <-watcher.C():
		data := event.GetData()
		require.NotNil(t, data, "expected Data event")
		assert.Equal(t, "output data", string(data.GetData()))
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for terminal output")
	}
}

func TestTerminalService_HandleTerminalExited(t *testing.T) {
	env := setupTerminalTest(t)
	terminalID := id.Generate()

	watcher := env.termMgr.Watch(terminalID)
	defer env.termMgr.Unwatch(terminalID, watcher)

	env.terminalSvc.HandleTerminalExited(&leapmuxv1.TerminalExited{
		TerminalId: terminalID,
		ExitCode:   42,
	})

	select {
	case event := <-watcher.C():
		closed := event.GetClosed()
		require.NotNil(t, closed, "expected Closed event")
		assert.Equal(t, int32(42), closed.GetExitCode())
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for terminal exited event")
	}
}

func TestTerminalService_CloseTerminal_Success(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Terminal Workspace")
	terminalID := "some-terminal"
	env.terminalSvc.TrackTerminal(terminalID, workspaceID, env.workerID)

	_, err := env.client.CloseTerminal(context.Background(), authedReq(&leapmuxv1.CloseTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		TerminalId:  terminalID,
	}, env.token))
	require.NoError(t, err)
}

func TestTerminalService_CloseTerminal_NotifiesWatcher(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Terminal Workspace")
	terminalID := "watch-me"
	env.terminalSvc.TrackTerminal(terminalID, workspaceID, env.workerID)

	watcher := env.termMgr.Watch(terminalID)
	defer env.termMgr.Unwatch(terminalID, watcher)

	_, err := env.client.CloseTerminal(context.Background(), authedReq(&leapmuxv1.CloseTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		TerminalId:  terminalID,
	}, env.token))
	require.NoError(t, err)

	select {
	case event := <-watcher.C():
		closed := event.GetClosed()
		require.NotNil(t, closed, "expected Closed event")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for close event")
	}
}

func TestTerminalService_ResizeTerminal_Success(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Terminal Workspace")
	terminalID := "some-terminal"
	env.terminalSvc.TrackTerminal(terminalID, workspaceID, env.workerID)

	_, err := env.client.ResizeTerminal(context.Background(), authedReq(&leapmuxv1.ResizeTerminalRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		TerminalId:  terminalID,
		Cols:        120,
		Rows:        40,
	}, env.token))
	require.NoError(t, err)
}

func TestTerminalService_OpenTerminal_WrongOrgID(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Org Mismatch Workspace")

	otherOrgID := id.Generate()
	_ = env.queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: otherOrgID, Name: "other-org"})
	_ = env.queries.CreateOrgMember(context.Background(), gendb.CreateOrgMemberParams{
		OrgID:  otherOrgID,
		UserID: env.userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	})

	_, err := env.client.OpenTerminal(context.Background(), authedReq(&leapmuxv1.OpenTerminalRequest{
		OrgId:       otherOrgID,
		WorkspaceId: workspaceID,
		WorkerId:    env.workerID,
		Cols:        80,
		Rows:        24,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTerminalService_ListTerminals_WrongOrgID(t *testing.T) {
	env := setupTerminalTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Org Mismatch Workspace")

	otherOrgID := id.Generate()
	_ = env.queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: otherOrgID, Name: "other-org"})
	_ = env.queries.CreateOrgMember(context.Background(), gendb.CreateOrgMemberParams{
		OrgID:  otherOrgID,
		UserID: env.userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	})

	_, err := env.client.ListTerminals(context.Background(), authedReq(&leapmuxv1.ListTerminalsRequest{
		OrgId:       otherOrgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestTerminalService_Unauthenticated(t *testing.T) {
	env := setupTerminalTest(t)

	_, err := env.client.ListTerminals(context.Background(), connect.NewRequest(&leapmuxv1.ListTerminalsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
