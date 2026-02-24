package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/leapmux/leapmux/internal/hub/msgcodec"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

type agentTestEnv struct {
	client     leapmuxv1connect.AgentServiceClient
	queries    *gendb.Queries
	workerMgr  *workermgr.Manager
	workerConn *workermgr.Conn
	agentMgr   *agentmgr.Manager
	pending    *workermgr.PendingRequests
	agentSvc   *service.AgentService
	token      string
	orgID      string
	userID     string
	workerID   string
}

func setupAgentTest(t *testing.T) *agentTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)
	workerMgr := workermgr.New()
	agMgr := agentmgr.New()

	pending := workermgr.NewPendingRequests()
	worktreeHelper := service.NewWorktreeHelper(queries, workerMgr, pending)
	agentSvc := service.NewAgentService(queries, workerMgr, agMgr, pending, worktreeHelper)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(queries))
	path, handler := leapmuxv1connect.NewAgentServiceHandler(agentSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewAgentServiceClient(
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
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			// Simulate worker ack for AgentInput messages.
			if msg.GetAgentInput() != nil && msg.GetRequestId() != "" {
				go pending.Complete(msg.GetRequestId(), &leapmuxv1.ConnectRequest{
					RequestId: msg.GetRequestId(),
					Payload: &leapmuxv1.ConnectRequest_AgentInputAck{
						AgentInputAck: &leapmuxv1.AgentInputAck{
							AgentId: msg.GetAgentInput().GetAgentId(),
						},
					},
				})
			}
			return nil
		},
	}
	workerMgr.Register(workerConn)

	return &agentTestEnv{
		client:     client,
		queries:    queries,
		workerMgr:  workerMgr,
		workerConn: workerConn,
		agentMgr:   agMgr,
		pending:    pending,
		agentSvc:   agentSvc,
		token:      token,
		orgID:      orgID,
		userID:     userID,
		workerID:   workerID,
	}
}

// createWorkspaceInDB creates a workspace directly in the database.
func (e *agentTestEnv) createWorkspaceInDB(t *testing.T, title string) string {
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

// createAgentInDB creates an agent directly in the database, bypassing the
// RPC layer (which requires a live worker bidi stream to send AgentStartRequest).
func (e *agentTestEnv) createAgentInDB(t *testing.T, workspaceID, title string) string {
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

func TestAgentService_OpenAgent_WorkerOffline(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Test Workspace")

	// Unregister the worker so it's offline.
	env.workerMgr.Unregister(env.workerID, env.workerConn)

	_, err := env.client.OpenAgent(context.Background(), authedReq(&leapmuxv1.OpenAgentRequest{
		WorkspaceId: workspaceID,
		WorkerId:    env.workerID,
		WorkingDir:  "/home/user",
		Model:       "sonnet",
		Title:       "My Agent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAgentService_CloseAgent(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Test Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	_, err := env.client.CloseAgent(context.Background(), authedReq(&leapmuxv1.CloseAgentRequest{
		AgentId: agentID,
	}, env.token))
	require.NoError(t, err)

	// Verify the agent is closed by listing agents.
	listResp, err := env.client.ListAgents(context.Background(), authedReq(&leapmuxv1.ListAgentsRequest{
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)

	found := false
	for _, a := range listResp.Msg.GetAgents() {
		if a.GetId() == agentID {
			found = true
			assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE, a.GetStatus())
		}
	}
	assert.True(t, found, "closed agent not found in list")
}

func TestAgentService_ListAgents(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Test Workspace")

	// Create multiple agents directly in DB.
	for i := 0; i < 3; i++ {
		env.createAgentInDB(t, workspaceID, "Agent")
	}

	resp, err := env.client.ListAgents(context.Background(), authedReq(&leapmuxv1.ListAgentsRequest{
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetAgents(), 3)
}

func TestAgentService_RenameAgent(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Test Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Old Title")

	_, err := env.client.RenameAgent(context.Background(), authedReq(&leapmuxv1.RenameAgentRequest{
		AgentId: agentID,
		Title:   "New Title",
	}, env.token))
	require.NoError(t, err)

	// Verify via ListAgents.
	listResp, err := env.client.ListAgents(context.Background(), authedReq(&leapmuxv1.ListAgentsRequest{
		WorkspaceId: workspaceID,
	}, env.token))
	require.NoError(t, err)

	for _, a := range listResp.Msg.GetAgents() {
		if a.GetId() == agentID {
			assert.Equal(t, "New Title", a.GetTitle())
		}
	}
}

func TestAgentService_ListAgentMessages(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Test Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	// Insert messages directly.
	for i := int64(1); i <= 5; i++ {
		_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
			ID:                 id.Generate(),
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Content:            []byte(`{"type":"assistant"}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
	}

	// Forward pagination: get first page of 3 after seq 0.
	resp, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:  agentID,
		AfterSeq: 1, // Forward: messages with seq > 1.
		Limit:    3,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetMessages(), 3)
	assert.True(t, resp.Msg.GetHasMore())
	assert.Equal(t, int64(2), resp.Msg.GetMessages()[0].GetSeq())
	assert.Equal(t, int64(4), resp.Msg.GetMessages()[2].GetSeq())

	// Get second page.
	lastSeq := resp.Msg.GetMessages()[2].GetSeq()
	resp2, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:  agentID,
		AfterSeq: lastSeq,
		Limit:    3,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, resp2.Msg.GetMessages(), 1) // Only seq 5 remains.
	assert.False(t, resp2.Msg.GetHasMore())
}

func TestAgentService_HandleAgentOutput(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Agent Output Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Simulate agent output -- a complete assistant message.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello!"}]}}`),
	})

	select {
	case event := <-watcher.C():
		agentMsg := event.GetAgentMessage()
		require.NotNil(t, agentMsg, "expected AgentMessage event")
		assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, agentMsg.GetRole())
		assert.Equal(t, int64(1), agentMsg.GetSeq())
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for watcher event")
	}

	// Verify persistence.
	msgs, err := env.queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, msgs[0].Role)
}

func TestAgentService_HandleAgentOutput_StreamChunk(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Streaming")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Simulate a streaming chunk -- should broadcast but NOT persist.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"content_block_delta","delta":{"text":"Hi"}}`),
	})

	select {
	case event := <-watcher.C():
		chunk := event.GetStreamChunk()
		require.NotNil(t, chunk, "expected StreamChunk event")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for stream chunk")
	}

	// Verify no message was persisted.
	msgs, _ := env.queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   10,
	})
	assert.Empty(t, msgs)
}

func TestAgentService_HandleAgentOutput_SkipsRedundantNullStatus(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Status Dedup Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// First null status — should be persisted and broadcast.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"system","subtype":"status","status":null,"uuid":"u1"}`),
	})

	select {
	case event := <-watcher.C():
		agentMsg := event.GetAgentMessage()
		require.NotNil(t, agentMsg, "expected first null status to be broadcast")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for first null status broadcast")
	}

	// Second null status — should be skipped (redundant null→null).
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"system","subtype":"status","status":null,"uuid":"u2"}`),
	})

	select {
	case event := <-watcher.C():
		require.Fail(t, "expected second null status to be skipped, got event: %v", event)
	case <-time.After(200 * time.Millisecond):
		// Expected: no broadcast for redundant null→null.
	}

	// Verify only one message persisted in DB.
	msgs, err := env.queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	assert.Len(t, msgs, 1, "expected exactly one message in DB after redundant null status skip")
}

func TestAgentService_HandleAgentOutput_PersistsNullAfterCompacting(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Status Transition Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Send compacting status.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"system","subtype":"status","status":"compacting","uuid":"u1"}`),
	})

	select {
	case event := <-watcher.C():
		require.NotNil(t, event.GetAgentMessage(), "expected compacting status broadcast")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for compacting status broadcast")
	}

	// Send null status after compacting — should be persisted (compacting→null transition).
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"system","subtype":"status","status":null,"uuid":"u2"}`),
	})

	select {
	case event := <-watcher.C():
		require.NotNil(t, event.GetAgentMessage(), "expected null-after-compacting status broadcast")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for null-after-compacting broadcast")
	}

	// Send another null status — should be skipped (null→null now).
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"system","subtype":"status","status":null,"uuid":"u3"}`),
	})

	select {
	case event := <-watcher.C():
		require.Fail(t, "expected third null status to be skipped, got event: %v", event)
	case <-time.After(200 * time.Millisecond):
		// Expected: no broadcast for redundant null→null.
	}
}

func TestAgentService_HandleAgentOutput_NonStatusNotificationsUnaffected(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Non-Status Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Send two compact_boundary messages — both should be persisted.
	for i := 0; i < 2; i++ {
		env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
			AgentId: agentID,
			Content: []byte(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":50000}}`),
		})

		select {
		case event := <-watcher.C():
			require.NotNil(t, event.GetAgentMessage(), "expected compact_boundary broadcast #%d", i+1)
		case <-time.After(time.Second):
			require.Fail(t, "timeout waiting for compact_boundary broadcast #%d", i+1)
		}
	}
}

func TestAgentService_SendAgentMessage(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Message Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Msg Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "Hello from test",
	}, env.token))
	require.NoError(t, err)

	// Verify the message was broadcast to the watcher.
	select {
	case event := <-watcher.C():
		agentMsg := event.GetAgentMessage()
		require.NotNil(t, agentMsg, "expected AgentMessage event")
		assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_USER, agentMsg.GetRole())
		assert.Equal(t, int64(1), agentMsg.GetSeq())
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for message broadcast")
	}

	// Verify the message was persisted.
	msgs, err := env.queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_USER, msgs[0].Role)
}

func TestAgentService_SendAgentMessage_EmptyContent(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAgentService_SendAgentMessage_ClosedAgent_PersistsDeliveryError(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Close the agent (no session ID = fresh start needed).
	_ = env.queries.CloseAgent(context.Background(), agentID)

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Unregister the worker so it's offline — resume/fresh start will fail.
	env.workerMgr.Unregister(env.workerID, env.workerConn)

	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "Should fail with delivery error",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// Watcher should receive AgentMessage (user msg), then MessageError.
	var msgID string
	for i := 0; i < 2; i++ {
		select {
		case event := <-watcher.C():
			if am := event.GetAgentMessage(); am != nil {
				msgID = am.GetId()
			}
			if me := event.GetMessageError(); me != nil {
				assert.NotEmpty(t, me.GetError())
			}
		case <-time.After(2 * time.Second):
			require.Fail(t, "timeout waiting for event %d", i)
		}
	}

	// Verify the message was persisted with a delivery error.
	msg, dbErr := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      msgID,
		AgentID: agentID,
	})
	require.NoError(t, dbErr)
	assert.NotEmpty(t, msg.DeliveryError)
}

func TestAgentService_SendAgentMessage_NotFound(t *testing.T) {
	env := setupAgentTest(t)

	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: "nonexistent",
		Content: "Hello",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAgentService_CloseAgent_NotFound(t *testing.T) {
	env := setupAgentTest(t)

	_, err := env.client.CloseAgent(context.Background(), authedReq(&leapmuxv1.CloseAgentRequest{
		AgentId: "nonexistent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAgentService_RenameAgent_NotFound(t *testing.T) {
	env := setupAgentTest(t)

	_, err := env.client.RenameAgent(context.Background(), authedReq(&leapmuxv1.RenameAgentRequest{
		AgentId: "nonexistent",
		Title:   "New Title",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAgentService_RenameAgent_EmptyTitle(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	_, err := env.client.RenameAgent(context.Background(), authedReq(&leapmuxv1.RenameAgentRequest{
		AgentId: agentID,
		Title:   "",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAgentService_ListAgentMessages_NotFound(t *testing.T) {
	env := setupAgentTest(t)

	_, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: "nonexistent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestAgentService_OpenAgent_InvalidWorkingDir(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Invalid Dir Workspace")

	// Set up SendFn on the worker conn to intercept the AgentStartRequest,
	// then simulate the worker responding with an error via pending.Complete.
	conn := env.workerMgr.Get(env.workerID)
	conn.SendFn = func(msg *leapmuxv1.ConnectResponse) error {
		// Extract the request ID and simulate a worker error response.
		requestID := msg.GetRequestId()
		go func() {
			env.pending.Complete(requestID, &leapmuxv1.ConnectRequest{
				RequestId: requestID,
				Payload: &leapmuxv1.ConnectRequest_AgentStarted{
					AgentStarted: &leapmuxv1.AgentStarted{
						AgentId: msg.GetAgentStart().GetAgentId(),
						Error:   "stat working directory \"/nonexistent/path\": no such file or directory",
					},
				},
			})
		}()
		return nil
	}

	_, err := env.client.OpenAgent(context.Background(), authedReq(&leapmuxv1.OpenAgentRequest{
		WorkspaceId: workspaceID,
		WorkerId:    env.workerID,
		WorkingDir:  "/nonexistent/path",
		Model:       "sonnet",
		Title:       "Bad Dir Agent",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// Verify the agent was cleaned up from the DB.
	agents, _ := env.queries.ListAgentsByWorkspaceID(context.Background(), workspaceID)
	for _, a := range agents {
		assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, a.Status, "expected no active agents, found agent %s with status %v", a.ID, a.Status)
	}
}

func TestAgentService_OpenAgent_AuthRequired(t *testing.T) {
	env := setupAgentTest(t)
	_ = env

	_, err := env.client.OpenAgent(context.Background(), connect.NewRequest(&leapmuxv1.OpenAgentRequest{
		WorkspaceId: "some-workspace",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// --- Delivery error, Retry, Delete tests ---

func TestAgentService_SendAgentMessage_DeliveryError_WorkerOffline(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Take the worker offline.
	env.workerMgr.Unregister(env.workerID, env.workerConn)

	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "Hello offline",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	// Watcher should receive: AgentMessage (user msg), then MessageError.
	var msgID string
	for i := 0; i < 2; i++ {
		select {
		case event := <-watcher.C():
			if am := event.GetAgentMessage(); am != nil {
				msgID = am.GetId()
			}
			if me := event.GetMessageError(); me != nil {
				assert.NotEmpty(t, me.GetError())
				assert.Equal(t, msgID, me.GetMessageId())
			}
		case <-time.After(2 * time.Second):
			require.Fail(t, "timeout waiting for event %d", i)
		}
	}

	// Verify DB has delivery_error set.
	msg, dbErr := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      msgID,
		AgentID: agentID,
	})
	require.NoError(t, dbErr)
	assert.NotEmpty(t, msg.DeliveryError)
}

func TestAgentService_SendAgentMessage_DeliveryError_AckError(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Override SendFn to return an error ack.
	conn := env.workerMgr.Get(env.workerID)
	conn.SendFn = func(msg *leapmuxv1.ConnectResponse) error {
		requestID := msg.GetRequestId()
		if msg.GetAgentInput() != nil {
			go env.pending.Complete(requestID, &leapmuxv1.ConnectRequest{
				RequestId: requestID,
				Payload: &leapmuxv1.ConnectRequest_AgentInputAck{
					AgentInputAck: &leapmuxv1.AgentInputAck{
						AgentId:     msg.GetAgentInput().GetAgentId(),
						Error:       leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_INTERNAL,
						ErrorReason: "agent rejected input",
					},
				},
			})
		}
		return nil
	}

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "Hello error",
	}, env.token))
	require.Error(t, err)

	// Drain watcher: expect AgentMessage + MessageError.
	var msgID string
	var gotError bool
	for i := 0; i < 2; i++ {
		select {
		case event := <-watcher.C():
			if am := event.GetAgentMessage(); am != nil {
				msgID = am.GetId()
			}
			if me := event.GetMessageError(); me != nil {
				gotError = true
				assert.NotEmpty(t, me.GetError())
			}
		case <-time.After(2 * time.Second):
			require.Fail(t, "timeout")
		}
	}
	assert.True(t, gotError)

	// Verify DB.
	msg, dbErr := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      msgID,
		AgentID: agentID,
	})
	require.NoError(t, dbErr)
	assert.NotEmpty(t, msg.DeliveryError)
}

func TestAgentService_RetryAgentMessage_Success(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Create a message with delivery_error set.
	msgID := id.Generate()
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            []byte(`{"content":"Hello retry"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	_ = env.queries.SetMessageDeliveryError(context.Background(), gendb.SetMessageDeliveryErrorParams{
		DeliveryError: "worker was offline",
		ID:            msgID,
		AgentID:       agentID,
	})

	_, err := env.client.RetryAgentMessage(context.Background(), authedReq(&leapmuxv1.RetryAgentMessageRequest{
		AgentId:   agentID,
		MessageId: msgID,
	}, env.token))
	require.NoError(t, err)

	// Verify delivery_error is cleared.
	msg, dbErr := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      msgID,
		AgentID: agentID,
	})
	require.NoError(t, dbErr)
	assert.Empty(t, msg.DeliveryError)
}

func TestAgentService_RetryAgentMessage_Failure(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Create a message with delivery_error set.
	msgID := id.Generate()
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            []byte(`{"content":"Hello retry fail"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	_ = env.queries.SetMessageDeliveryError(context.Background(), gendb.SetMessageDeliveryErrorParams{
		DeliveryError: "old error",
		ID:            msgID,
		AgentID:       agentID,
	})

	// Override SendFn to return an error ack.
	conn := env.workerMgr.Get(env.workerID)
	conn.SendFn = func(msg *leapmuxv1.ConnectResponse) error {
		requestID := msg.GetRequestId()
		if msg.GetAgentInput() != nil {
			go env.pending.Complete(requestID, &leapmuxv1.ConnectRequest{
				RequestId: requestID,
				Payload: &leapmuxv1.ConnectRequest_AgentInputAck{
					AgentInputAck: &leapmuxv1.AgentInputAck{
						AgentId:     msg.GetAgentInput().GetAgentId(),
						Error:       leapmuxv1.AgentInputAckError_AGENT_INPUT_ACK_ERROR_INTERNAL,
						ErrorReason: "still failing",
					},
				},
			})
		}
		return nil
	}

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	_, err := env.client.RetryAgentMessage(context.Background(), authedReq(&leapmuxv1.RetryAgentMessageRequest{
		AgentId:   agentID,
		MessageId: msgID,
	}, env.token))
	require.Error(t, err)

	// Watcher should receive MessageError with updated error.
	select {
	case event := <-watcher.C():
		me := event.GetMessageError()
		require.NotNil(t, me)
		assert.NotEmpty(t, me.GetError())
	case <-time.After(2 * time.Second):
		require.Fail(t, "timeout waiting for message error")
	}

	// Verify DB has updated delivery_error.
	msg, dbErr := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      msgID,
		AgentID: agentID,
	})
	require.NoError(t, dbErr)
	assert.NotEmpty(t, msg.DeliveryError)
}

func TestAgentService_RetryAgentMessage_RejectsNonUserMessage(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	msgID := id.Generate()
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Content:            []byte(`{"type":"assistant"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	_ = env.queries.SetMessageDeliveryError(context.Background(), gendb.SetMessageDeliveryErrorParams{
		DeliveryError: "some error",
		ID:            msgID,
		AgentID:       agentID,
	})

	_, err := env.client.RetryAgentMessage(context.Background(), authedReq(&leapmuxv1.RetryAgentMessageRequest{
		AgentId:   agentID,
		MessageId: msgID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAgentService_RetryAgentMessage_RejectsNoDeliveryError(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	msgID := id.Generate()
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            []byte(`{"content":"No error here"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})

	_, err := env.client.RetryAgentMessage(context.Background(), authedReq(&leapmuxv1.RetryAgentMessageRequest{
		AgentId:   agentID,
		MessageId: msgID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAgentService_DeleteAgentMessage_Success(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	msgID := id.Generate()
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            []byte(`{"content":"Delete me"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	_ = env.queries.SetMessageDeliveryError(context.Background(), gendb.SetMessageDeliveryErrorParams{
		DeliveryError: "worker offline",
		ID:            msgID,
		AgentID:       agentID,
	})

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	_, err := env.client.DeleteAgentMessage(context.Background(), authedReq(&leapmuxv1.DeleteAgentMessageRequest{
		AgentId:   agentID,
		MessageId: msgID,
	}, env.token))
	require.NoError(t, err)

	// Watcher should receive MessageDeleted.
	select {
	case event := <-watcher.C():
		md := event.GetMessageDeleted()
		require.NotNil(t, md)
		assert.Equal(t, msgID, md.GetMessageId())
		assert.Equal(t, agentID, md.GetAgentId())
	case <-time.After(2 * time.Second):
		require.Fail(t, "timeout waiting for message deleted event")
	}

	// Verify the message is gone from DB.
	_, dbErr := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      msgID,
		AgentID: agentID,
	})
	assert.Error(t, dbErr)
}

func TestAgentService_DeleteAgentMessage_RejectsNoDeliveryError(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	msgID := id.Generate()
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 msgID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            []byte(`{"content":"No error"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})

	_, err := env.client.DeleteAgentMessage(context.Background(), authedReq(&leapmuxv1.DeleteAgentMessageRequest{
		AgentId:   agentID,
		MessageId: msgID,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAgentService_SendAgentMessage_ClearIntercepted(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Track AgentStop messages sent to the worker.
	var agentStopReceived bool
	conn := env.workerMgr.Get(env.workerID)
	origSendFn := conn.SendFn
	conn.SendFn = func(msg *leapmuxv1.ConnectResponse) error {
		if msg.GetAgentStop() != nil && msg.GetAgentStop().GetAgentId() == agentID {
			agentStopReceived = true
		}
		return origSendFn(msg)
	}

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Send /clear (with surrounding whitespace to test trimming).
	_, err := env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "  /clear  ",
	}, env.token))
	require.NoError(t, err)

	// Verify no user message was persisted.
	msgs, dbErr := env.queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, dbErr)
	assert.Empty(t, msgs, "expected no messages persisted for /clear")

	// Verify AgentStop was sent to the worker.
	assert.True(t, agentStopReceived, "expected AgentStop to be sent to worker")

	// Verify restartPending was set with clearSession=true.
	opts := env.agentSvc.ConsumeRestartPending(agentID)
	require.NotNil(t, opts, "expected restartPending to be set")
	assert.True(t, opts.ClearSession, "expected clearSession=true")
}

func TestAgentService_SendAgentMessage_ClearIntercepted_InactiveAgent(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Set a session ID so we can verify it gets cleared.
	err := env.queries.UpdateAgentSessionID(context.Background(), gendb.UpdateAgentSessionIDParams{
		AgentSessionID: "old-session",
		ID:             agentID,
	})
	require.NoError(t, err)

	// Close the agent to make it inactive.
	_, err = env.client.CloseAgent(context.Background(), authedReq(&leapmuxv1.CloseAgentRequest{
		AgentId: agentID,
	}, env.token))
	require.NoError(t, err)

	// Track AgentStop messages — none should be sent.
	var agentStopReceived bool
	conn := env.workerMgr.Get(env.workerID)
	origSendFn := conn.SendFn
	conn.SendFn = func(msg *leapmuxv1.ConnectResponse) error {
		if msg.GetAgentStop() != nil && msg.GetAgentStop().GetAgentId() == agentID {
			agentStopReceived = true
		}
		return origSendFn(msg)
	}

	// Send /clear.
	_, err = env.client.SendAgentMessage(context.Background(), authedReq(&leapmuxv1.SendAgentMessageRequest{
		AgentId: agentID,
		Content: "/clear",
	}, env.token))
	require.NoError(t, err)

	// Verify no AgentStop was sent.
	assert.False(t, agentStopReceived, "expected no AgentStop for inactive agent")

	// Verify no restartPending was set.
	opts := env.agentSvc.ConsumeRestartPending(agentID)
	assert.Nil(t, opts, "expected no restartPending for inactive agent")

	// Verify session ID was cleared.
	agent, err := env.queries.GetAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Empty(t, agent.AgentSessionID, "expected session ID to be cleared")

	// Verify a context_cleared LEAPMUX message was persisted.
	msgs, dbErr := env.queries.ListMessagesByAgentID(context.Background(), gendb.ListMessagesByAgentIDParams{
		AgentID: agentID,
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, dbErr)
	require.NotEmpty(t, msgs, "expected context_cleared message to be persisted")
	lastMsg := msgs[len(msgs)-1]
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, lastMsg.Role)
}

func TestAgentService_ListAgentMessages_BackwardPagination(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Backward Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Backward Agent")

	// Insert 10 messages (seq 1-10).
	for i := int64(1); i <= 10; i++ {
		_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
			ID:                 id.Generate(),
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Content:            []byte(`{"type":"assistant"}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
	}

	// Backward page: before_seq=8, limit=3 → expect seq 5,6,7, has_more=true.
	resp, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   agentID,
		BeforeSeq: 8,
		Limit:     3,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetMessages(), 3)
	assert.True(t, resp.Msg.GetHasMore())
	assert.Equal(t, int64(5), resp.Msg.GetMessages()[0].GetSeq())
	assert.Equal(t, int64(6), resp.Msg.GetMessages()[1].GetSeq())
	assert.Equal(t, int64(7), resp.Msg.GetMessages()[2].GetSeq())

	// Backward page: before_seq=3, limit=5 → expect seq 1,2, has_more=false.
	resp2, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   agentID,
		BeforeSeq: 3,
		Limit:     5,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp2.Msg.GetMessages(), 2)
	assert.False(t, resp2.Msg.GetHasMore())
	assert.Equal(t, int64(1), resp2.Msg.GetMessages()[0].GetSeq())
	assert.Equal(t, int64(2), resp2.Msg.GetMessages()[1].GetSeq())
}

func TestAgentService_ListAgentMessages_LatestN(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "LatestN Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "LatestN Agent")

	// Insert 10 messages (seq 1-10).
	for i := int64(1); i <= 10; i++ {
		_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
			ID:                 id.Generate(),
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Content:            []byte(`{"type":"assistant"}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
	}

	// Latest 3 → expect seq 8,9,10 in ascending order, has_more=true.
	resp, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID,
		Limit:   3,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetMessages(), 3)
	assert.True(t, resp.Msg.GetHasMore())
	assert.Equal(t, int64(8), resp.Msg.GetMessages()[0].GetSeq())
	assert.Equal(t, int64(9), resp.Msg.GetMessages()[1].GetSeq())
	assert.Equal(t, int64(10), resp.Msg.GetMessages()[2].GetSeq())

	// Latest 15 (more than total) → expect all 10, has_more=false.
	resp2, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID,
		Limit:   15,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp2.Msg.GetMessages(), 10)
	assert.False(t, resp2.Msg.GetHasMore())
	assert.Equal(t, int64(1), resp2.Msg.GetMessages()[0].GetSeq())
	assert.Equal(t, int64(10), resp2.Msg.GetMessages()[9].GetSeq())
}

func TestAgentService_ListAgentMessages_MaxLimit(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "MaxLimit Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "MaxLimit Agent")

	// Insert 60 messages.
	for i := int64(1); i <= 60; i++ {
		_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
			ID:                 id.Generate(),
			AgentID:            agentID,
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Content:            []byte(`{"type":"assistant"}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		})
	}

	// Limit=100 should be capped to 50.
	resp, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:  agentID,
		AfterSeq: 0,
		Limit:    100,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, resp.Msg.GetMessages(), 50)
	assert.True(t, resp.Msg.GetHasMore())

	// Limit=0 should default to 50 (no longer fetches all).
	resp2, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID,
		Limit:   0,
	}, env.token))
	require.NoError(t, err)
	assert.Len(t, resp2.Msg.GetMessages(), 50)
	assert.True(t, resp2.Msg.GetHasMore())
}

func TestAgentService_ListAgentMessages_InvalidBothCursors(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Invalid Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Invalid Agent")

	_, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:   agentID,
		AfterSeq:  5,
		BeforeSeq: 10,
		Limit:     10,
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAgentService_ListAgentMessages_EmptyAgent(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Empty Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Empty Agent")

	// No messages inserted. Latest N should return empty list.
	resp, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID,
		Limit:   50,
	}, env.token))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetMessages())
	assert.False(t, resp.Msg.GetHasMore())
}

func TestAgentService_ListAgentMessages_IncludesDeliveryError(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Workspace")
	agentID := env.createAgentInDB(t, workspaceID, "Agent")

	// Message with delivery error.
	msg1ID := id.Generate()
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 msg1ID,
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:            []byte(`{"content":"Failed msg"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})
	_ = env.queries.SetMessageDeliveryError(context.Background(), gendb.SetMessageDeliveryErrorParams{
		DeliveryError: "worker offline",
		ID:            msg1ID,
		AgentID:       agentID,
	})

	// Message without delivery error.
	_, _ = env.queries.CreateMessage(context.Background(), gendb.CreateMessageParams{
		ID:                 id.Generate(),
		AgentID:            agentID,
		Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Content:            []byte(`{"type":"assistant"}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
	})

	resp, err := env.client.ListAgentMessages(context.Background(), authedReq(&leapmuxv1.ListAgentMessagesRequest{
		AgentId:  agentID,
		AfterSeq: 0,
		Limit:    10,
	}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetMessages(), 2)
	assert.Equal(t, "worker offline", resp.Msg.GetMessages()[0].GetDeliveryError())
	assert.Empty(t, resp.Msg.GetMessages()[1].GetDeliveryError())
}

func TestMessageToProto_UpdatedAt(t *testing.T) {
	now := time.Now()

	t.Run("includes UpdatedAt when Valid", func(t *testing.T) {
		msg := &gendb.Message{
			ID:                 "msg-1",
			AgentID:            "agent-1",
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Content:            []byte(`{}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			CreatedAt:          now,
			UpdatedAt:          sql.NullTime{Time: now.Add(time.Minute), Valid: true},
		}
		proto := service.MessageToProto(msg)
		assert.NotEmpty(t, proto.GetUpdatedAt(), "expected UpdatedAt to be set")
		assert.NotEmpty(t, proto.GetCreatedAt())
	})

	t.Run("omits UpdatedAt when not Valid", func(t *testing.T) {
		msg := &gendb.Message{
			ID:                 "msg-2",
			AgentID:            "agent-1",
			Role:               leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Content:            []byte(`{}`),
			ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			CreatedAt:          now,
			UpdatedAt:          sql.NullTime{Valid: false},
		}
		proto := service.MessageToProto(msg)
		assert.Empty(t, proto.GetUpdatedAt(), "expected UpdatedAt to be empty")
	})
}

func TestHandleAgentOutput_ThreadMerge_SetsUpdatedAt(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Thread Merge Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// 1. Send an assistant message with a tool_use block.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_merge_1","name":"Bash","input":{"command":"echo hi"}}]}}`),
	})

	// Drain the watcher to get the initial assistant message broadcast.
	var parentMsgID string
	select {
	case event := <-watcher.C():
		agentMsg := event.GetAgentMessage()
		require.NotNil(t, agentMsg)
		parentMsgID = agentMsg.GetId()
		assert.Empty(t, agentMsg.GetUpdatedAt(), "initial message should not have UpdatedAt")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for assistant message broadcast")
	}

	// 2. Send a matching tool_result user message.
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_merge_1","content":"hello"}]}}`),
	})

	// Drain the watcher to get the merged thread broadcast.
	select {
	case event := <-watcher.C():
		agentMsg := event.GetAgentMessage()
		require.NotNil(t, agentMsg)
		assert.Equal(t, parentMsgID, agentMsg.GetId(), "merged message should have same ID as parent")
		assert.NotEmpty(t, agentMsg.GetUpdatedAt(), "merged message broadcast should have UpdatedAt")
		assert.NotEmpty(t, agentMsg.GetCreatedAt(), "merged message should retain CreatedAt")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for merged thread broadcast")
	}

	// 3. Verify the DB row has non-null updated_at.
	dbMsg, err := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      parentMsgID,
		AgentID: agentID,
	})
	require.NoError(t, err)
	assert.True(t, dbMsg.UpdatedAt.Valid, "DB message should have non-null updated_at after thread merge")

	// Verify the merged content has both messages.
	decompressed, err := msgcodec.Decompress(dbMsg.Content, dbMsg.ContentCompression)
	require.NoError(t, err)
	var wrapper struct {
		OldSeqs  []int64           `json:"old_seqs"`
		Messages []json.RawMessage `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(decompressed, &wrapper))
	assert.Len(t, wrapper.Messages, 2, "merged thread should contain 2 messages")
	assert.NotEmpty(t, wrapper.OldSeqs, "merged thread should have old_seqs")
}

func TestHandleAgentOutput_Standalone_NoUpdatedAt(t *testing.T) {
	env := setupAgentTest(t)
	workspaceID := env.createWorkspaceInDB(t, "Standalone Test")
	agentID := env.createAgentInDB(t, workspaceID, "Test Agent")

	watcher := env.agentMgr.Watch(agentID)
	defer env.agentMgr.Unwatch(agentID, watcher)

	// Send a standalone assistant message (no tool_use, no thread merge).
	env.agentSvc.HandleAgentOutput(context.Background(), &leapmuxv1.AgentOutput{
		AgentId: agentID,
		Content: []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello standalone"}]}}`),
	})

	var msgID string
	select {
	case event := <-watcher.C():
		agentMsg := event.GetAgentMessage()
		require.NotNil(t, agentMsg)
		msgID = agentMsg.GetId()
		assert.Empty(t, agentMsg.GetUpdatedAt(), "standalone message broadcast should not have UpdatedAt")
	case <-time.After(time.Second):
		require.Fail(t, "timeout waiting for standalone message broadcast")
	}

	// Verify DB has null updated_at.
	dbMsg, err := env.queries.GetMessageByAgentAndID(context.Background(), gendb.GetMessageByAgentAndIDParams{
		ID:      msgID,
		AgentID: agentID,
	})
	require.NoError(t, err)
	assert.False(t, dbMsg.UpdatedAt.Valid, "standalone DB message should have null updated_at")
}
