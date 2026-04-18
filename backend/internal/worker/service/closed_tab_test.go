package service

import (
	"context"
	"database/sql"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// testResponseWriter captures responses and stream messages sent by handlers.
//
// Some handlers (e.g. tunnel reads) emit stream messages from background
// goroutines, so writes are guarded by mu. Tests that read fields while a
// background goroutine may still be writing should call streamsSnapshot
// instead of accessing the slice directly.
type testResponseWriter struct {
	channelID string
	mu        sync.Mutex
	responses []*leapmuxv1.InnerRpcResponse
	errors    []testError
	streams   []*leapmuxv1.InnerStreamMessage
}

type testError struct {
	code    int32
	message string
}

func (w *testResponseWriter) SendResponse(r *leapmuxv1.InnerRpcResponse) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.responses = append(w.responses, r)
	return nil
}

func (w *testResponseWriter) SendError(code int32, msg string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.errors = append(w.errors, testError{code, msg})
	return nil
}

func (w *testResponseWriter) SendStream(m *leapmuxv1.InnerStreamMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.streams = append(w.streams, m)
	return nil
}

func (w *testResponseWriter) ChannelID() string { return w.channelID }

// streamsSnapshot returns a copy of streams under the lock so callers can
// iterate without racing concurrent SendStream writes from handler
// goroutines. The lock also establishes happens-before with the writer so
// proto payload bytes produced in another goroutine are safe to unmarshal.
func (w *testResponseWriter) streamsSnapshot() []*leapmuxv1.InnerStreamMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.InnerStreamMessage(nil), w.streams...)
}

// setupTestService creates a minimal service.Context with an in-memory DB
// and a channel manager that grants access to the given workspace IDs.
func setupTestService(t *testing.T, workspaceIDs ...string) (*Context, *channel.Dispatcher, *testResponseWriter) {
	t.Helper()

	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(sqlDB))

	// Set up a channel manager with a handshake so
	// AccessibleWorkspaceIDs returns the desired workspaces.
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	chmgr := channel.NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, func(*leapmuxv1.ConnectRequest) error { return nil }, 0, 0, nil)

	_, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)
	chmgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:              "test-ch",
		UserId:                 "user-1",
		HandshakePayload:       msg1,
		AccessibleWorkspaceIds: workspaceIDs,
	})

	svc := &Context{
		DB:        sqlDB,
		Queries:   db.New(sqlDB),
		Channels:  chmgr,
		Agents:    agent.NewManager(nil),
		HomeDir:   t.TempDir(),
		DataDir:   t.TempDir(),
		Watchers:  NewWatcherManager(),
		Terminals: terminal.NewManager(),
	}
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.Output.DataDir = svc.DataDir

	d := channel.NewDispatcher()
	RegisterAll(d, svc)

	w := &testResponseWriter{channelID: "test-ch"}
	return svc, d, w
}

// dispatch is a helper that marshals a request proto and dispatches it.
func dispatch(d *channel.Dispatcher, method string, req proto.Message, w *testResponseWriter) {
	payload, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}
	d.DispatchWith("user-1", &leapmuxv1.InnerRpcRequest{
		Method:  method,
		Payload: payload,
	}, w)
}

func TestListAgentMessages_ClosedAgent_ReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")

	// Create an agent and add a message.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	_, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:        "msg-1",
		AgentID:   "agent-1",
		Role:      leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:   []byte("hello"),
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Verify messages are returned when agent is open.
	dispatch(d, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{
		AgentId: "agent-1",
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Len(t, resp.GetMessages(), 1, "open agent should return messages")

	// Close the agent.
	require.NoError(t, svc.Queries.CloseAgent(ctx, "agent-1"))

	// Verify empty response for closed agent.
	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{
		AgentId: "agent-1",
	}, w2)
	require.Len(t, w2.responses, 1)
	var resp2 leapmuxv1.ListAgentMessagesResponse
	require.NoError(t, proto.Unmarshal(w2.responses[0].GetPayload(), &resp2))
	assert.Empty(t, resp2.GetMessages(), "closed agent should return empty messages")
	assert.False(t, resp2.GetHasMore(), "closed agent should return has_more=false")
}

func TestListAgents_ClosedAgent_NotReturned(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")

	// Create two agents.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-open",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-closed",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	// Close one agent.
	require.NoError(t, svc.Queries.CloseAgent(ctx, "agent-closed"))

	dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{
		TabIds: []string{"agent-open", "agent-closed"},
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1, "only open agent should be returned")
	assert.Equal(t, "agent-open", resp.GetAgents()[0].GetId())
}

func TestListTerminals_ClosedTerminal_NotReturned(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")

	// Create two terminals via DB.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-open",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("open screen"),
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-closed",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("closed screen"),
		ClosedAt:    sql.NullTime{Time: time.Now(), Valid: true},
	}))

	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{"term-open", "term-closed"},
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1, "only open terminal should be returned")
	assert.Equal(t, "term-open", resp.GetTerminals()[0].GetTerminalId())
	assert.Equal(t, []byte("open screen"), resp.GetTerminals()[0].GetScreen())
}

func TestWatchEvents_ClosedAgent_NotWatched(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")

	// Create an agent, add a message, then close it.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-closed",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	_, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:        "msg-1",
		AgentID:   "agent-closed",
		Role:      leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:   []byte("hello"),
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Queries.CloseAgent(ctx, "agent-closed"))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: "agent-closed", AfterSeq: 0},
		},
	}, w)

	// Closed agent should produce a single stream error (NOT_FOUND).
	require.Len(t, w.streams, 1, "closed agent should produce a stream error")
	assert.True(t, w.streams[0].GetIsError(), "stream message should be an error")
	assert.Equal(t, int32(5), w.streams[0].GetErrorCode(), "error code should be NOT_FOUND")

	// Verify no watcher was registered.
	svc.Watchers.mu.RLock()
	agentWatchers := len(svc.Watchers.agents["agent-closed"])
	svc.Watchers.mu.RUnlock()
	assert.Equal(t, 0, agentWatchers, "no watcher should be registered for closed agent")
}

func TestWatchEvents_ClosedTerminal_NotWatched(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")

	// Create a terminal and close it.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-closed",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("some screen"),
		ClosedAt:    sql.NullTime{Time: time.Now(), Valid: true},
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		TerminalIds: []string{"term-closed"},
	}, w)

	// Closed terminal should produce a single stream error (NOT_FOUND).
	require.Len(t, w.streams, 1, "closed terminal should produce a stream error")
	assert.True(t, w.streams[0].GetIsError(), "stream message should be an error")
	assert.Equal(t, int32(5), w.streams[0].GetErrorCode(), "error code should be NOT_FOUND")

	// Verify no watcher was registered.
	svc.Watchers.mu.RLock()
	termWatchers := len(svc.Watchers.terminals["term-closed"])
	svc.Watchers.mu.RUnlock()
	assert.Equal(t, 0, termWatchers, "no watcher should be registered for closed terminal")
}

func TestShutdown_PersistsTerminalScreenSnapshots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("terminal tests require /bin/sh and creack/pty, which are not available on Windows")
	}
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	workingDir := t.TempDir()

	// Start a real terminal.
	require.NoError(t, svc.Terminals.StartTerminal(terminal.Options{
		ID:            "term-1",
		WorkspaceID:   "ws-1",
		Shell:         "/bin/sh",
		WorkingDir:    workingDir,
		ShellStartDir: "",
		Cols:          80,
		Rows:          24,
	}, func([]byte) {}, nil))

	require.True(t, svc.Terminals.UpdateTitle("term-1", "user@host: ~/dir"))

	// Persist the initial record (like OpenTerminal does).
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		WorkingDir:  workingDir,
		HomeDir:     svc.HomeDir,
		Cols:        80,
		Rows:        24,
		Screen:      []byte{},
	}))

	// Send a command so the terminal has screen content.
	require.NoError(t, svc.Terminals.SendInput("term-1", []byte("echo shutdown_test\n")))

	// Wait for the terminal to have screen data.
	testutil.AssertEventually(t, func() bool {
		screen := svc.Terminals.ScreenSnapshot("term-1")
		return len(screen) > 0
	}, "expected terminal to have screen data")

	// Call Shutdown — should persist screen to DB.
	svc.Shutdown()

	// Verify screen data was saved to DB.
	dbTerm, err := svc.Queries.GetTerminal(ctx, "term-1")
	require.NoError(t, err)
	assert.True(t, len(dbTerm.Screen) > 0, "screen should be persisted after Shutdown")
	assert.Contains(t, string(dbTerm.Screen), "shutdown_test")
	assert.Contains(t, string(dbTerm.Screen), "[Connection to the terminal was lost.]")
	assert.Equal(t, "user@host: ~/dir", dbTerm.Title, "title should be persisted after Shutdown")
	assert.False(t, dbTerm.ClosedAt.Valid, "Shutdown should not set closed_at")

	// Clean up.
	svc.Terminals.StopAll()
}

func TestOpenTerminal_ExitPersistsDisconnectNotice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("terminal tests require /bin/sh and creack/pty, which are not available on Windows")
	}
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	workingDir := t.TempDir()

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		Shell:       "/bin/sh",
		WorkingDir:  workingDir,
		Cols:        80,
		Rows:        24,
	}, w)
	require.Len(t, w.responses, 1)

	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()
	require.NotEmpty(t, terminalID)

	w2 := &testResponseWriter{channelID: "test-ch"}
	dispatch(d, "SendInput", &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte("echo exit_notice_test\nexit\n"),
	}, w2)
	require.Empty(t, w2.errors)

	testutil.AssertEventually(t, func() bool {
		return svc.Terminals.IsExited(terminalID)
	}, "expected terminal to exit")

	testutil.AssertEventually(t, func() bool {
		dbTerm, err := svc.Queries.GetTerminal(ctx, terminalID)
		return err == nil &&
			dbTerm.ExitCode == 0 &&
			strings.Contains(string(dbTerm.Screen), "exit_notice_test") &&
			strings.Contains(string(dbTerm.Screen), "[Connection to the terminal was lost.]")
	}, "expected exit snapshot with disconnect notice to be persisted")
}
