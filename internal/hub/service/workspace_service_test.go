package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/terminalmgr"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

type workspaceTestEnv struct {
	client       leapmuxv1connect.WorkspaceServiceClient
	httpClient   *http.Client
	queries      *gendb.Queries
	workerMgr    *workermgr.Manager
	workerConn   *workermgr.Conn
	agentMgr     *agentmgr.Manager
	termMgr      *terminalmgr.Manager
	workspaceSvc *service.WorkspaceService
	serverURL    string
	token        string
	orgID        string
	userID       string
	workerID     string
}

func setupWorkspaceTest(t *testing.T) *workspaceTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)
	workerMgr := workermgr.New()
	agMgr := agentmgr.New()
	tmMgr := terminalmgr.New()

	tc, tcErr := timeout.NewFromDB(queries)
	require.NoError(t, tcErr)

	pending := workermgr.NewPendingRequests(tc.APITimeout)
	worktreeHelper := service.NewWorktreeHelper(queries, workerMgr, pending, tc)
	workspaceSvc := service.NewWorkspaceService(queries, workerMgr, agMgr, tmMgr, pending, worktreeHelper)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(queries))
	path, handler := leapmuxv1connect.NewWorkspaceServiceHandler(workspaceSvc, opts)
	mux.Handle(path, handler)
	mux.Handle("/ws/watch-events", service.WSWatchEventsHandler(queries, workspaceSvc, nil, tc))

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewWorkspaceServiceClient(
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

	return &workspaceTestEnv{
		client:       client,
		httpClient:   server.Client(),
		queries:      queries,
		workerMgr:    workerMgr,
		workerConn:   workerConn,
		agentMgr:     agMgr,
		termMgr:      tmMgr,
		workspaceSvc: workspaceSvc,
		serverURL:    server.URL,
		token:        token,
		orgID:        orgID,
		userID:       userID,
		workerID:     workerID,
	}
}

// createWorkspaceInDB creates a workspace directly in the database, bypassing
// the RPC layer (which requires a live worker bidi stream).
func (e *workspaceTestEnv) createWorkspaceInDB(t *testing.T, title string) string {
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

func authedReq[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

func TestWorkspaceService_GetWorkspace(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "My Workspace")

	resp, err := env.client.GetWorkspace(context.Background(), authedReq(&leapmuxv1.GetWorkspaceRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)

	ws := resp.Msg.GetWorkspace()
	assert.Equal(t, "My Workspace", ws.GetTitle())
}

func TestWorkspaceService_GetWorkspace_NotFound(t *testing.T) {
	env := setupWorkspaceTest(t)

	_, err := env.client.GetWorkspace(context.Background(), authedReq(&leapmuxv1.GetWorkspaceRequest{
		OrgId:       env.orgID,
		WorkspaceId: "nonexistent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkspaceService_ListWorkspaces(t *testing.T) {
	env := setupWorkspaceTest(t)
	env.createWorkspaceInDB(t, "Workspace A")
	env.createWorkspaceInDB(t, "Workspace B")

	resp, err := env.client.ListWorkspaces(context.Background(), authedReq(&leapmuxv1.ListWorkspacesRequest{}, env.token))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetWorkspaces(), 2)
}

func TestWorkspaceService_Unauthenticated(t *testing.T) {
	env := setupWorkspaceTest(t)

	_, err := env.client.ListWorkspaces(context.Background(), connect.NewRequest(&leapmuxv1.ListWorkspacesRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestWorkspaceService_CreateWorkspace_AutoCreatesAgent(t *testing.T) {
	env := setupWorkspaceTest(t)

	resp, err := env.client.CreateWorkspace(context.Background(), authedReq(&leapmuxv1.CreateWorkspaceRequest{
		WorkerId: env.workerID,
		Title:    "Agent Workspace",
	}, env.token))
	require.NoError(t, err)

	workspaceID := resp.Msg.GetWorkspace().GetId()

	// Verify an agent was auto-created for this workspace.
	agents, err := env.queries.ListAgentsByWorkspaceID(context.Background(), workspaceID)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, agents[0].Status)
	assert.Equal(t, "opus", string(agents[0].Model))
}

func TestWorkspaceService_WorkerOffline(t *testing.T) {
	env := setupWorkspaceTest(t)
	env.workerMgr.Unregister(env.workerID, env.workerConn)

	_, err := env.client.CreateWorkspace(context.Background(), authedReq(&leapmuxv1.CreateWorkspaceRequest{
		WorkerId: env.workerID,
		Title:    "Should Fail",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func (e *workspaceTestEnv) createSecondUser(t *testing.T) (userID, token string) {
	t.Helper()
	userID = id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass2"), bcrypt.MinCost)
	_ = e.queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        e.orgID,
		Username:     "user2",
		PasswordHash: string(hash),
		DisplayName:  "User 2",
		IsAdmin:      0,
	})
	// Also add as org member so they can see org-shared workspaces.
	_ = e.queries.CreateOrgMember(context.Background(), gendb.CreateOrgMemberParams{
		OrgID:  e.orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER,
	})
	token, _, err := auth.Login(context.Background(), e.queries, "user2", "pass2")
	require.NoError(t, err)
	return
}

func TestWorkspaceService_DeleteWorkspace_NotOwner(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Not My Workspace")

	// Share the workspace with the org so user2 can see it.
	_, _ = env.queries.UpdateWorkspaceShareMode(context.Background(), gendb.UpdateWorkspaceShareModeParams{
		ShareMode: leapmuxv1.ShareMode_SHARE_MODE_ORG,
		ID:        workspaceID,
		CreatedBy: env.userID,
	})

	_, user2Token := env.createSecondUser(t)

	_, err := env.client.DeleteWorkspace(context.Background(), authedReq(
		&leapmuxv1.DeleteWorkspaceRequest{WorkspaceId: workspaceID}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestWorkspaceService_ListWorkspaceShares_NotVisible(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Private Workspace")

	_, user2Token := env.createSecondUser(t)

	_, err := env.client.ListWorkspaceShares(context.Background(), authedReq(
		&leapmuxv1.ListWorkspaceSharesRequest{WorkspaceId: workspaceID}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkspaceService_ListWorkspaceShares_VisibleShared(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Shared Workspace")

	// Share the workspace with the org.
	_, _ = env.queries.UpdateWorkspaceShareMode(context.Background(), gendb.UpdateWorkspaceShareModeParams{
		ShareMode: leapmuxv1.ShareMode_SHARE_MODE_ORG,
		ID:        workspaceID,
		CreatedBy: env.userID,
	})

	_, user2Token := env.createSecondUser(t)

	resp, err := env.client.ListWorkspaceShares(context.Background(), authedReq(
		&leapmuxv1.ListWorkspaceSharesRequest{WorkspaceId: workspaceID}, user2Token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ShareMode_SHARE_MODE_ORG, resp.Msg.GetShareMode())
}

func TestWorkspaceService_UpdateWorkspaceSharing_ToOrg(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Share Me")

	_, err := env.client.UpdateWorkspaceSharing(context.Background(), authedReq(
		&leapmuxv1.UpdateWorkspaceSharingRequest{WorkspaceId: workspaceID, ShareMode: leapmuxv1.ShareMode_SHARE_MODE_ORG}, env.token))
	require.NoError(t, err)

	// Verify the share mode was updated.
	resp, err := env.client.ListWorkspaceShares(context.Background(), authedReq(
		&leapmuxv1.ListWorkspaceSharesRequest{WorkspaceId: workspaceID}, env.token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ShareMode_SHARE_MODE_ORG, resp.Msg.GetShareMode())
}

func TestWorkspaceService_UpdateWorkspaceSharing_ToMembers(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Members Share")

	user2ID, _ := env.createSecondUser(t)

	_, err := env.client.UpdateWorkspaceSharing(context.Background(), authedReq(
		&leapmuxv1.UpdateWorkspaceSharingRequest{
			WorkspaceId: workspaceID,
			ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
			UserIds:     []string{user2ID},
		}, env.token))
	require.NoError(t, err)

	// Verify the share mode and members.
	resp, err := env.client.ListWorkspaceShares(context.Background(), authedReq(
		&leapmuxv1.ListWorkspaceSharesRequest{WorkspaceId: workspaceID}, env.token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ShareMode_SHARE_MODE_MEMBERS, resp.Msg.GetShareMode())
	require.Len(t, resp.Msg.GetMembers(), 1)
	assert.Equal(t, user2ID, resp.Msg.GetMembers()[0].GetUserId())
}

func TestWorkspaceService_UpdateWorkspaceSharing_BackToPrivate(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Revert Share")

	// First share with org.
	_, _ = env.client.UpdateWorkspaceSharing(context.Background(), authedReq(
		&leapmuxv1.UpdateWorkspaceSharingRequest{WorkspaceId: workspaceID, ShareMode: leapmuxv1.ShareMode_SHARE_MODE_ORG}, env.token))

	// Then revert to private.
	_, err := env.client.UpdateWorkspaceSharing(context.Background(), authedReq(
		&leapmuxv1.UpdateWorkspaceSharingRequest{WorkspaceId: workspaceID, ShareMode: leapmuxv1.ShareMode_SHARE_MODE_PRIVATE}, env.token))
	require.NoError(t, err)

	resp, err := env.client.ListWorkspaceShares(context.Background(), authedReq(
		&leapmuxv1.ListWorkspaceSharesRequest{WorkspaceId: workspaceID}, env.token))
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.ShareMode_SHARE_MODE_PRIVATE, resp.Msg.GetShareMode())
}

func TestWorkspaceService_UpdateWorkspaceSharing_InvalidMode(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Invalid Share")

	_, err := env.client.UpdateWorkspaceSharing(context.Background(), authedReq(
		&leapmuxv1.UpdateWorkspaceSharingRequest{WorkspaceId: workspaceID, ShareMode: leapmuxv1.ShareMode(99)}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestWorkspaceService_UpdateWorkspaceSharing_NotOwner(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Someone Else's Workspace")

	_, user2Token := env.createSecondUser(t)

	_, err := env.client.UpdateWorkspaceSharing(context.Background(), authedReq(
		&leapmuxv1.UpdateWorkspaceSharingRequest{WorkspaceId: workspaceID, ShareMode: leapmuxv1.ShareMode_SHARE_MODE_ORG}, user2Token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestWorkspaceService_CreateWorkspace_EmptyName(t *testing.T) {
	env := setupWorkspaceTest(t)

	// A whitespace-only title passes through to ValidateName which rejects it.
	_, err := env.client.CreateWorkspace(context.Background(), authedReq(&leapmuxv1.CreateWorkspaceRequest{
		WorkerId: env.workerID,
		Title:    " ",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestWorkspaceService_ListWorkspaces_Empty(t *testing.T) {
	env := setupWorkspaceTest(t)

	// List workspaces when none have been created; expect an empty list.
	resp, err := env.client.ListWorkspaces(context.Background(), authedReq(&leapmuxv1.ListWorkspacesRequest{}, env.token))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkspaces())
}

// createAgentInDB creates an agent directly in the database.
func (e *workspaceTestEnv) createAgentInDB(t *testing.T, workspaceID, title string) string {
	t.Helper()
	agentID := id.Generate()
	err := e.queries.CreateAgent(context.Background(), gendb.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
		WorkerID:    e.workerID,
		Title:       title,
		Model:       "haiku",
	})
	require.NoError(t, err)
	return agentID
}

// wsConnect opens a WebSocket to the test server's /ws/watch-events endpoint,
// sends the auth token and request, and returns the connection.
func wsConnect(t *testing.T, ctx context.Context, env *workspaceTestEnv, req *leapmuxv1.WatchEventsRequest) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(env.serverURL, "https://", "wss://", 1) + "/ws/watch-events"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"leapmux.watch-events.v1"},
		HTTPClient:   env.httpClient,
	})
	require.NoError(t, err)

	// Send auth token as text frame.
	err = conn.Write(ctx, websocket.MessageText, []byte(env.token))
	require.NoError(t, err)

	// Send request as protobuf binary frame.
	data, err := proto.Marshal(req)
	require.NoError(t, err)
	err = conn.Write(ctx, websocket.MessageBinary, data)
	require.NoError(t, err)

	return conn
}

// wsReceive reads a WatchEventsResponse from the WebSocket with a timeout.
func wsReceive(t *testing.T, conn *websocket.Conn, timeout time.Duration) *leapmuxv1.WatchEventsResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}

	var resp leapmuxv1.WatchEventsResponse
	err = proto.Unmarshal(data, &resp)
	require.NoError(t, err)
	return &resp
}

func TestWorkspaceService_WatchEvents_SingleAgent(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Watch Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Agents:      []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: -1}},
	})
	defer func() { _ = conn.CloseNow() }()

	// First event should be the status snapshot.
	resp := wsReceive(t, conn, 2*time.Second)
	agentEvent := resp.GetAgentEvent()
	require.NotNil(t, agentEvent)
	assert.Equal(t, agentID, agentEvent.GetAgentId())
	require.NotNil(t, agentEvent.GetStatusChange())

	// Broadcast a live event and verify it arrives.
	env.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_StreamEnd{
			StreamEnd: &leapmuxv1.AgentStreamEnd{},
		},
	})

	resp = wsReceive(t, conn, 2*time.Second)
	agentEvent = resp.GetAgentEvent()
	require.NotNil(t, agentEvent)
	assert.Equal(t, agentID, agentEvent.GetAgentId())
	require.NotNil(t, agentEvent.GetStreamEnd())
}

func TestWorkspaceService_WatchEvents_WithHistory(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "History Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Insert 3 messages.
	for i := int64(1); i <= 3; i++ {
		_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
			ID:                 id.Generate(),
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Content:            []byte(`{"type":"assistant"}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Agents:      []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: 0}},
	})
	defer func() { _ = conn.CloseNow() }()

	// Should receive 3 historical messages, then status snapshot.
	for i := 0; i < 3; i++ {
		resp := wsReceive(t, conn, 2*time.Second)
		agentEvent := resp.GetAgentEvent()
		require.NotNil(t, agentEvent)
		assert.Equal(t, agentID, agentEvent.GetAgentId())
		require.NotNil(t, agentEvent.GetAgentMessage(), "expected agentMessage at index %d", i)
	}

	// Status snapshot.
	resp := wsReceive(t, conn, 2*time.Second)
	require.NotNil(t, resp.GetAgentEvent().GetStatusChange())
}

func TestWorkspaceService_WatchEvents_LiveOnly(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "LiveOnly Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Insert a message that should NOT be replayed with afterSeq=-1.
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 id.Generate(),
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Content:            []byte(`{"type":"assistant"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Agents:      []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: -1}},
	})
	defer func() { _ = conn.CloseNow() }()

	// First event should be status snapshot (no history replay).
	resp := wsReceive(t, conn, 2*time.Second)
	agentEvent := resp.GetAgentEvent()
	require.NotNil(t, agentEvent)
	require.NotNil(t, agentEvent.GetStatusChange(), "expected statusChange as first event in live-only mode")
}

func TestWorkspaceService_WatchEvents_MultipleAgents(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Multi Agent Workspace")
	agent1 := env.createAgentInDB(t, workspaceID, "Agent 1")
	agent2 := env.createAgentInDB(t, workspaceID, "Agent 2")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: agent1, AfterSeq: -1},
			{AgentId: agent2, AfterSeq: -1},
		},
	})
	defer func() { _ = conn.CloseNow() }()

	// Receive 2 status snapshots (one per agent).
	agentIDs := make(map[string]bool)
	for i := 0; i < 2; i++ {
		resp := wsReceive(t, conn, 2*time.Second)
		agentEvent := resp.GetAgentEvent()
		require.NotNil(t, agentEvent)
		require.NotNil(t, agentEvent.GetStatusChange())
		agentIDs[agentEvent.GetAgentId()] = true
	}
	assert.True(t, agentIDs[agent1], "expected status for agent1")
	assert.True(t, agentIDs[agent2], "expected status for agent2")

	// Broadcast to agent2 and verify agent_id is correct.
	env.agentMgr.Broadcast(agent2, &leapmuxv1.AgentEvent{
		AgentId: agent2,
		Event: &leapmuxv1.AgentEvent_StreamEnd{
			StreamEnd: &leapmuxv1.AgentStreamEnd{},
		},
	})

	resp := wsReceive(t, conn, 2*time.Second)
	agentEvent := resp.GetAgentEvent()
	require.NotNil(t, agentEvent)
	assert.Equal(t, agent2, agentEvent.GetAgentId())
	require.NotNil(t, agentEvent.GetStreamEnd())
}

func TestWorkspaceService_WatchEvents_TerminalEvents(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Terminal Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")
	terminalID := id.Generate()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Agents:      []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: -1}},
		Terminals:   []*leapmuxv1.WatchTerminalEntry{{TerminalId: terminalID}},
	})
	defer func() { _ = conn.CloseNow() }()

	// Consume agent status snapshot.
	resp := wsReceive(t, conn, 2*time.Second)
	require.NotNil(t, resp.GetAgentEvent().GetStatusChange())

	// Broadcast terminal data.
	env.termMgr.Broadcast(terminalID, &leapmuxv1.TerminalEvent{
		TerminalId: terminalID,
		Event: &leapmuxv1.TerminalEvent_Data{
			Data: &leapmuxv1.TerminalData{Data: []byte("hello terminal")},
		},
	})

	resp = wsReceive(t, conn, 2*time.Second)
	termEvent := resp.GetTerminalEvent()
	require.NotNil(t, termEvent)
	assert.Equal(t, terminalID, termEvent.GetTerminalId())
	require.NotNil(t, termEvent.GetData())
	assert.Equal(t, "hello terminal", string(termEvent.GetData().GetData()))
}

func TestWorkspaceService_WatchEvents_DeduplicateEntries(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Dedup Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Send duplicate agent entries.
	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: agentID, AfterSeq: -1},
			{AgentId: agentID, AfterSeq: -1},
		},
	})
	defer func() { _ = conn.CloseNow() }()

	// Should receive exactly one status snapshot (deduplicated).
	resp := wsReceive(t, conn, 2*time.Second)
	require.NotNil(t, resp.GetAgentEvent().GetStatusChange())

	// Broadcast one event and verify it arrives only once.
	env.agentMgr.Broadcast(agentID, &leapmuxv1.AgentEvent{
		AgentId: agentID,
		Event: &leapmuxv1.AgentEvent_StreamEnd{
			StreamEnd: &leapmuxv1.AgentStreamEnd{},
		},
	})

	resp = wsReceive(t, conn, 2*time.Second)
	require.NotNil(t, resp.GetAgentEvent().GetStreamEnd())

	// Verify no duplicate arrives within a short window.
	readCtx, readCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer readCancel()
	_, _, err := conn.Read(readCtx)
	if err == nil {
		t.Fatal("received unexpected duplicate event")
	}
	// Expected: context deadline exceeded (timeout = no duplicate).
}

func TestWorkspaceService_WatchEvents_ControlRequestDedup(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "CR Dedup Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Insert a pending control request in the DB.
	requestID := id.Generate()
	err := env.queries.CreateControlRequest(context.Background(), gendb.CreateControlRequestParams{
		AgentID:   agentID,
		RequestID: requestID,
		Payload:   []byte(`{"tool":"bash","command":"ls"}`),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Agents:      []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: 0}},
	})
	defer func() { _ = conn.CloseNow() }()

	// First event: status snapshot.
	resp := wsReceive(t, conn, 2*time.Second)
	require.NotNil(t, resp.GetAgentEvent().GetStatusChange())

	// Second event: the replayed control request.
	resp = wsReceive(t, conn, 2*time.Second)
	agentEvent := resp.GetAgentEvent()
	require.NotNil(t, agentEvent)
	cr := agentEvent.GetControlRequest()
	require.NotNil(t, cr, "expected controlRequest event")
	assert.Equal(t, requestID, cr.GetRequestId())

	// Verify no duplicate control request arrives within a short window.
	readCtx, readCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer readCancel()
	_, _, readErr := conn.Read(readCtx)
	if readErr == nil {
		t.Fatal("received unexpected duplicate control request")
	}
	// Expected: context deadline exceeded (timeout = no duplicate).
}

func TestWorkspaceService_WatchEvents_NotFound(t *testing.T) {
	env := setupWorkspaceTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		WorkspaceId: "nonexistent",
		Agents:      []*leapmuxv1.WatchAgentEntry{{AgentId: "any", AfterSeq: -1}},
	})
	defer func() { _ = conn.CloseNow() }()

	// The server should close the WebSocket with a permission denied code.
	_, _, err := conn.Read(ctx)
	require.Error(t, err)
	var closeErr websocket.CloseError
	if assert.ErrorAs(t, err, &closeErr) {
		assert.Equal(t, websocket.StatusCode(4003), closeErr.Code)
	}
}

// singleTileLayout creates a minimal valid layout with one leaf node.
func singleTileLayout(tileID string) *leapmuxv1.LayoutNode {
	return &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Leaf{
			Leaf: &leapmuxv1.LayoutLeaf{Id: tileID},
		},
	}
}

// twoTileLayout creates a horizontal split layout with two leaf nodes.
func twoTileLayout(splitID, tile1, tile2 string) *leapmuxv1.LayoutNode {
	return &leapmuxv1.LayoutNode{
		Node: &leapmuxv1.LayoutNode_Split{
			Split: &leapmuxv1.LayoutSplit{
				Id:        splitID,
				Direction: leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL,
				Children: []*leapmuxv1.LayoutNode{
					{Node: &leapmuxv1.LayoutNode_Leaf{Leaf: &leapmuxv1.LayoutLeaf{Id: tile1}}},
					{Node: &leapmuxv1.LayoutNode_Leaf{Leaf: &leapmuxv1.LayoutLeaf{Id: tile2}}},
				},
				Ratios: []float64{0.5, 0.5},
			},
		},
	}
}

func TestWorkspaceService_SaveLayout_WithTabs(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "SaveLayout Tabs")
	ctx := context.Background()

	_, err := env.client.SaveLayout(ctx, authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Layout:      singleTileLayout("tile-1"),
		Tabs: []*leapmuxv1.WorkspaceTab{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1", Position: "b", TileId: "tile-1"},
		},
	}, env.token))
	require.NoError(t, err)

	// Verify tabs are persisted.
	resp, err := env.client.ListTabs(ctx, authedReq(&leapmuxv1.ListTabsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTabs(), 2)
	assert.Equal(t, "agent-1", resp.Msg.GetTabs()[0].GetTabId())
	assert.Equal(t, "a", resp.Msg.GetTabs()[0].GetPosition())
	assert.Equal(t, "tile-1", resp.Msg.GetTabs()[0].GetTileId())
	assert.Equal(t, "term-1", resp.Msg.GetTabs()[1].GetTabId())
}

func TestWorkspaceService_SaveLayout_TabsReplaced(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "SaveLayout Replace")
	ctx := context.Background()

	// First save with two tabs.
	_, err := env.client.SaveLayout(ctx, authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Layout:      singleTileLayout("tile-1"),
		Tabs: []*leapmuxv1.WorkspaceTab{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1", Position: "b", TileId: "tile-1"},
		},
	}, env.token))
	require.NoError(t, err)

	// Second save with different tabs — old tabs should be replaced.
	_, err = env.client.SaveLayout(ctx, authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Layout:      singleTileLayout("tile-1"),
		Tabs: []*leapmuxv1.WorkspaceTab{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2", Position: "c", TileId: "tile-1"},
		},
	}, env.token))
	require.NoError(t, err)

	resp, err := env.client.ListTabs(ctx, authedReq(&leapmuxv1.ListTabsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTabs(), 1)
	assert.Equal(t, "agent-2", resp.Msg.GetTabs()[0].GetTabId())
}

func TestWorkspaceService_SaveLayout_EmptyTabsPreservesExisting(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "SaveLayout Empty Tabs")
	ctx := context.Background()

	// Save with tabs.
	_, err := env.client.SaveLayout(ctx, authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Layout:      singleTileLayout("tile-1"),
		Tabs: []*leapmuxv1.WorkspaceTab{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1"},
		},
	}, env.token))
	require.NoError(t, err)

	// Save again without tabs field — existing tabs should be preserved.
	_, err = env.client.SaveLayout(ctx, authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Layout:      singleTileLayout("tile-1"),
	}, env.token))
	require.NoError(t, err)

	resp, err := env.client.ListTabs(ctx, authedReq(&leapmuxv1.ListTabsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTabs(), 1)
	assert.Equal(t, "agent-1", resp.Msg.GetTabs()[0].GetTabId())
}

func TestWorkspaceService_SaveLayout_CrossTileTabMove(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "SaveLayout Cross-Tile")
	ctx := context.Background()

	// Save with 2-tile layout and tabs spread across tiles.
	_, err := env.client.SaveLayout(ctx, authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Layout:      twoTileLayout("grid-1", "tile-1", "tile-2"),
		Tabs: []*leapmuxv1.WorkspaceTab{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1", Position: "b", TileId: "tile-2"},
		},
	}, env.token))
	require.NoError(t, err)

	// Verify both tabs are persisted with their tile IDs.
	resp, err := env.client.ListTabs(ctx, authedReq(&leapmuxv1.ListTabsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTabs(), 2)

	tabMap := map[string]string{}
	for _, tab := range resp.Msg.GetTabs() {
		tabMap[tab.GetTabId()] = tab.GetTileId()
	}
	assert.Equal(t, "tile-1", tabMap["agent-1"])
	assert.Equal(t, "tile-2", tabMap["term-1"])

	// Now "move" term-1 to tile-1 (simulate cross-tile drag).
	_, err = env.client.SaveLayout(ctx, authedReq(&leapmuxv1.SaveLayoutRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
		Layout:      twoTileLayout("grid-1", "tile-1", "tile-2"),
		Tabs: []*leapmuxv1.WorkspaceTab{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1", Position: "a", TileId: "tile-1"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "term-1", Position: "b", TileId: "tile-1"},
		},
	}, env.token))
	require.NoError(t, err)

	resp, err = env.client.ListTabs(ctx, authedReq(&leapmuxv1.ListTabsRequest{
		OrgId:       env.orgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)

	tabMap = map[string]string{}
	for _, tab := range resp.Msg.GetTabs() {
		tabMap[tab.GetTabId()] = tab.GetTileId()
	}
	assert.Equal(t, "tile-1", tabMap["agent-1"])
	assert.Equal(t, "tile-1", tabMap["term-1"])
}

func TestWorkspaceService_GetWorkspace_WrongOrgID(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Org Mismatch")

	// Create a second org that the user is a member of.
	otherOrgID := id.Generate()
	_ = env.queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: otherOrgID, Name: "other-org"})
	_ = env.queries.CreateOrgMember(context.Background(), gendb.CreateOrgMemberParams{
		OrgID:  otherOrgID,
		UserID: env.userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	})

	// Try to access the workspace using the wrong org ID.
	_, err := env.client.GetWorkspace(context.Background(), authedReq(&leapmuxv1.GetWorkspaceRequest{
		OrgId:       otherOrgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkspaceService_ListTabs_WrongOrgID(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Tabs Org Mismatch")

	otherOrgID := id.Generate()
	_ = env.queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: otherOrgID, Name: "other-org"})
	_ = env.queries.CreateOrgMember(context.Background(), gendb.CreateOrgMemberParams{
		OrgID:  otherOrgID,
		UserID: env.userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	})

	_, err := env.client.ListTabs(context.Background(), authedReq(&leapmuxv1.ListTabsRequest{
		OrgId:       otherOrgID,
		WorkspaceId: workspaceID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestWorkspaceService_WatchEvents_WrongOrgID(t *testing.T) {
	env := setupWorkspaceTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Watch Org Mismatch")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	otherOrgID := id.Generate()
	_ = env.queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: otherOrgID, Name: "other-org"})
	_ = env.queries.CreateOrgMember(context.Background(), gendb.CreateOrgMemberParams{
		OrgID:  otherOrgID,
		UserID: env.userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn := wsConnect(t, ctx, env, &leapmuxv1.WatchEventsRequest{
		OrgId:       otherOrgID,
		WorkspaceId: workspaceID,
		Agents:      []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, AfterSeq: -1}},
	})
	defer func() { _ = conn.CloseNow() }()

	// The server should close the WebSocket because the workspace doesn't belong to the other org.
	_, _, err := conn.Read(ctx)
	require.Error(t, err)
	var closeErr websocket.CloseError
	if assert.ErrorAs(t, err, &closeErr) {
		assert.Equal(t, websocket.StatusCode(4003), closeErr.Code)
	}
}
