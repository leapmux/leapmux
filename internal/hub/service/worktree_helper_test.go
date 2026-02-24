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
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

type worktreeTestEnv struct {
	client       leapmuxv1connect.AgentServiceClient
	queries      *gendb.Queries
	workerMgr    *workermgr.Manager
	workerConn   *workermgr.Conn
	agentMgr     *agentmgr.Manager
	pending      *workermgr.PendingRequests
	agentSvc     *service.AgentService
	token        string
	orgID        string
	userID       string
	workerID     string
	workspaceID  string
	worktreeID   string
	worktreePath string
	agentAID     string
}

// setupWorktreeTest creates a test environment with:
//   - A workspace, worker, org, and user
//   - A worktree record in the DB
//   - Agent A created and registered in worktree_tabs
//   - A mock SendFn that handles AgentStart and GitWorktreeRemove
func setupWorktreeTest(t *testing.T) *worktreeTestEnv {
	t.Helper()
	ctx := context.Background()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)
	workerMgr := workermgr.New()
	agMgr := agentmgr.New()
	tc, tcErr := timeout.NewFromDB(queries)
	require.NoError(t, tcErr)

	pending := workermgr.NewPendingRequests(tc.APITimeout)
	worktreeHelper := service.NewWorktreeHelper(queries, workerMgr, pending, tc)
	agentSvc := service.NewAgentService(queries, workerMgr, agMgr, pending, worktreeHelper, tc)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(queries))
	path, handler := leapmuxv1connect.NewAgentServiceHandler(agentSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAgentServiceClient(
		server.Client(), server.URL, connect.WithGRPC(),
	)

	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	_ = queries.CreateOrg(ctx, gendb.CreateOrgParams{ID: orgID, Name: "test-org"})
	_ = queries.CreateUser(ctx, gendb.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "testuser",
		PasswordHash: string(hash), DisplayName: "Test", IsAdmin: 1,
	})
	token, _, err := auth.Login(ctx, queries, "testuser", "pass")
	require.NoError(t, err)

	workerID := id.Generate()
	_ = queries.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID: workerID, OrgID: orgID, Name: "test-worker",
		Hostname: "localhost", AuthToken: id.Generate(), RegisteredBy: userID,
	})

	workerConn := &workermgr.Conn{
		WorkerID: workerID,
		OrgID:    orgID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			reqID := msg.GetRequestId()
			if reqID == "" {
				return nil // fire-and-forget (e.g. AgentStop)
			}
			switch {
			case msg.GetAgentStart() != nil:
				go pending.Complete(reqID, &leapmuxv1.ConnectRequest{
					RequestId: reqID,
					Payload: &leapmuxv1.ConnectRequest_AgentStarted{
						AgentStarted: &leapmuxv1.AgentStarted{},
					},
				})
			case msg.GetGitWorktreeRemove() != nil:
				go pending.Complete(reqID, &leapmuxv1.ConnectRequest{
					RequestId: reqID,
					Payload: &leapmuxv1.ConnectRequest_GitWorktreeRemoveResp{
						GitWorktreeRemoveResp: &leapmuxv1.GitWorktreeRemoveResponse{
							IsClean: true,
						},
					},
				})
			}
			return nil
		},
	}
	workerMgr.Register(workerConn)

	workspaceID := id.Generate()
	_ = queries.CreateWorkspace(ctx, gendb.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, CreatedBy: userID, Title: "Test Workspace",
	})

	// Create a worktree record in the DB (simulating a previously created worktree).
	worktreeID := id.Generate()
	worktreePath := "/home/user/project-worktrees/test-branch"
	err = queries.CreateWorktree(ctx, gendb.CreateWorktreeParams{
		ID:           worktreeID,
		WorkerID:     workerID,
		WorktreePath: worktreePath,
		RepoRoot:     "/home/user/project",
		BranchName:   "test-branch",
	})
	require.NoError(t, err)

	// Create Agent A and register it with the worktree.
	agentAID := id.Generate()
	err = queries.CreateAgent(ctx, gendb.CreateAgentParams{
		ID:          agentAID,
		WorkspaceID: workspaceID,
		WorkerID:    workerID,
		WorkingDir:  worktreePath,
		Title:       "Agent A",
		Model:       "haiku",
	})
	require.NoError(t, err)

	err = queries.AddWorktreeTab(ctx, gendb.AddWorktreeTabParams{
		WorktreeID: worktreeID,
		TabType:    leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:      agentAID,
	})
	require.NoError(t, err)

	return &worktreeTestEnv{
		client:       client,
		queries:      queries,
		workerMgr:    workerMgr,
		workerConn:   workerConn,
		agentMgr:     agMgr,
		pending:      pending,
		agentSvc:     agentSvc,
		token:        token,
		orgID:        orgID,
		userID:       userID,
		workerID:     workerID,
		workspaceID:  workspaceID,
		worktreeID:   worktreeID,
		worktreePath: worktreePath,
		agentAID:     agentAID,
	}
}

// TestWorktreeHelper_SecondTabAutoAssociated verifies that when a second agent
// is opened without createWorktree but with a workingDir matching an existing
// worktree path, the agent is automatically associated in worktree_tabs.
func TestWorktreeHelper_SecondTabAutoAssociated(t *testing.T) {
	env := setupWorktreeTest(t)
	ctx := context.Background()

	// Verify Agent A is registered.
	count, err := env.queries.CountWorktreeTabs(ctx, env.worktreeID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	// Open Agent B without createWorktree, but with workingDir matching the worktree path.
	resp, err := env.client.OpenAgent(ctx, authedReq(&leapmuxv1.OpenAgentRequest{
		WorkspaceId: env.workspaceID,
		WorkerId:    env.workerID,
		WorkingDir:  env.worktreePath,
		Model:       "haiku",
		Title:       "Agent B",
	}, env.token))
	require.NoError(t, err)

	// Agent B should be auto-associated with the worktree.
	count, err = env.queries.CountWorktreeTabs(ctx, env.worktreeID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "Agent B should be auto-associated with the worktree")

	// Verify the specific tab entry exists.
	agentBID := resp.Msg.GetAgent().GetId()
	wt, err := env.queries.GetWorktreeForTab(ctx, gendb.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:   agentBID,
	})
	require.NoError(t, err, "Agent B should have a worktree_tab entry")
	assert.Equal(t, env.worktreeID, wt.ID)
}

// TestWorktreeHelper_ClosingFirstTabPreservesWorktree verifies that closing
// the first tab does not delete the worktree when a second tab is also
// associated with it.
func TestWorktreeHelper_ClosingFirstTabPreservesWorktree(t *testing.T) {
	env := setupWorktreeTest(t)
	ctx := context.Background()

	// Open Agent B (should be auto-associated with the worktree).
	_, err := env.client.OpenAgent(ctx, authedReq(&leapmuxv1.OpenAgentRequest{
		WorkspaceId: env.workspaceID,
		WorkerId:    env.workerID,
		WorkingDir:  env.worktreePath,
		Model:       "haiku",
		Title:       "Agent B",
	}, env.token))
	require.NoError(t, err)

	// Close Agent A.
	_, err = env.client.CloseAgent(ctx, authedReq(&leapmuxv1.CloseAgentRequest{
		AgentId: env.agentAID,
	}, env.token))
	require.NoError(t, err)

	// Worktree should still exist (not deleted).
	_, err = env.queries.GetWorktreeByID(ctx, env.worktreeID)
	require.NoError(t, err, "worktree should still exist after closing first tab")

	// Worktree tab count should be 1 (Agent B still registered).
	count, err := env.queries.CountWorktreeTabs(ctx, env.worktreeID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "Agent B should still be registered")
}

// TestWorktreeHelper_RegisterBeforeSendAndWait verifies that worktree tab
// registration happens before the blocking SendAndWait for agent start,
// preventing a race condition where closing the first tab during agent B's
// startup could delete the worktree.
func TestWorktreeHelper_RegisterBeforeSendAndWait(t *testing.T) {
	env := setupWorktreeTest(t)
	ctx := context.Background()

	// Channel to synchronize: signals when AgentStart request has been sent
	// (meaning CreateAgent and RegisterTabForWorktree should have completed).
	agentStartReceived := make(chan struct{}, 1)
	agentStartRelease := make(chan struct{})

	// Override SendFn to also handle GitInfo (needed by CreateWorktreeIfRequested)
	// and to delay the AgentStart response.
	env.workerConn.SendFn = func(msg *leapmuxv1.ConnectResponse) error {
		reqID := msg.GetRequestId()
		if reqID == "" {
			return nil
		}
		switch {
		case msg.GetGitInfo() != nil:
			go env.pending.Complete(reqID, &leapmuxv1.ConnectRequest{
				RequestId: reqID,
				Payload: &leapmuxv1.ConnectRequest_GitInfoResp{
					GitInfoResp: &leapmuxv1.GitInfoResponse{
						IsGitRepo:     true,
						RepoRoot:      "/home/user/project",
						RepoDirName:   "project",
						CurrentBranch: "main",
					},
				},
			})
		case msg.GetAgentStart() != nil:
			// Signal that we've reached SendAndWait for AgentStart.
			select {
			case agentStartReceived <- struct{}{}:
			default:
			}
			// Delay the response until released.
			go func() {
				<-agentStartRelease
				env.pending.Complete(reqID, &leapmuxv1.ConnectRequest{
					RequestId: reqID,
					Payload: &leapmuxv1.ConnectRequest_AgentStarted{
						AgentStarted: &leapmuxv1.AgentStarted{},
					},
				})
			}()
		case msg.GetGitWorktreeRemove() != nil:
			go env.pending.Complete(reqID, &leapmuxv1.ConnectRequest{
				RequestId: reqID,
				Payload: &leapmuxv1.ConnectRequest_GitWorktreeRemoveResp{
					GitWorktreeRemoveResp: &leapmuxv1.GitWorktreeRemoveResponse{
						IsClean: true,
					},
				},
			})
		}
		return nil
	}

	// Start OpenAgent with createWorktree=true (same branch) in a goroutine.
	// CreateWorktreeIfRequested will find the existing worktree and reuse it.
	openDone := make(chan struct{})
	go func() {
		defer close(openDone)
		_, _ = env.client.OpenAgent(ctx, authedReq(&leapmuxv1.OpenAgentRequest{
			WorkspaceId:    env.workspaceID,
			WorkerId:       env.workerID,
			WorkingDir:     "/home/user/project",
			Model:          "haiku",
			Title:          "Agent B",
			CreateWorktree: true,
			WorktreeBranch: "test-branch",
		}, env.token))
	}()

	// Wait for the AgentStart request to be sent. At this point,
	// CreateWorktreeIfRequested and CreateAgent have completed. With the fix,
	// RegisterTabForWorktree has also completed (before SendAndWait).
	select {
	case <-agentStartReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for AgentStart to be sent")
	}

	// Agent B should already be registered (before SendAndWait returns).
	count, err := env.queries.CountWorktreeTabs(ctx, env.worktreeID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "Agent B should be registered before SendAndWait completes")

	// Close Agent A while Agent B's SendAndWait is still pending.
	_, err = env.client.CloseAgent(ctx, authedReq(&leapmuxv1.CloseAgentRequest{
		AgentId: env.agentAID,
	}, env.token))
	require.NoError(t, err)

	// Worktree should still exist.
	_, err = env.queries.GetWorktreeByID(ctx, env.worktreeID)
	require.NoError(t, err, "worktree should not be deleted while Agent B is registered")

	// Release the delayed AgentStart response.
	close(agentStartRelease)

	// Wait for OpenAgent to complete.
	select {
	case <-openDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OpenAgent to complete")
	}
}
