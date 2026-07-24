package service

import (
	"bytes"
	"context"
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
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"google.golang.org/grpc/codes"
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

// testChannelID is the channel id setupTestService's handshake
// registers; every fresh testResponseWriter in this package shares it
// so AuthorizerFor resolves the same accessible-workspace set.
const testChannelID = "test-ch"

// newTestWriter returns a testResponseWriter bound to the package's
// canonical channel id. Use instead of the literal so a future channel
// id change is a single edit here.
func newTestWriter() *testResponseWriter {
	return &testResponseWriter{channelID: testChannelID}
}

// rejections returns every error the handler reported, in whichever
// shape it used: a unary InnerRpcResponse error for a unary method, or an
// InnerStreamMessage carrying IsError for a streaming one.
//
// A gate rejection means the same thing either way, so a test asserting
// that a method denies access should not have to know which kind of
// method it is -- and must not stop checking simply because the shape
// changed. Both shapes are collected here so the caller keeps asserting
// the code and the message.
func (w *testResponseWriter) rejections() []testError {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := append([]testError(nil), w.errors...)
	for _, m := range w.streams {
		if m.GetIsError() {
			out = append(out, testError{code: m.GetErrorCode(), message: m.GetErrorMessage()})
		}
	}
	return out
}

// streamsSnapshot returns a copy of streams under the lock so callers can
// iterate without racing concurrent SendStream writes from handler
// goroutines. The lock also establishes happens-before with the writer so
// proto payload bytes produced in another goroutine are safe to unmarshal.
func (w *testResponseWriter) streamsSnapshot() []*leapmuxv1.InnerStreamMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*leapmuxv1.InnerStreamMessage(nil), w.streams...)
}

// setupOption configures setupTestService. Use the with* helpers below
// rather than constructing setupConfig directly.
type setupOption func(*setupConfig)

type setupConfig struct {
	workspaceIDs []string
	remoteIPC    RemoteIPCFactory
}

// withWorkspaces grants the test channel access to the given workspace
// IDs. Without this option AccessibleWorkspaceIDs is empty, so every
// requireAccessibleWorkspace check returns PERMISSION_DENIED.
func withWorkspaces(ids ...string) setupOption {
	return func(c *setupConfig) { c.workspaceIDs = ids }
}

// withRemoteIPC wires the worker's RemoteIPC factory before handlers are
// registered so tests can assert mint/release semantics for the
// LEAPMUX_REMOTE_* token without poking svc.RemoteIPC directly.
func withRemoteIPC(ipc RemoteIPCFactory) setupOption {
	return func(c *setupConfig) { c.remoteIPC = ipc }
}

// setupTestService creates a minimal service.Service with an in-memory DB
// and a channel manager configured per the supplied options.
func setupTestService(t *testing.T, opts ...setupOption) (*Service, *channel.Dispatcher, *testResponseWriter) {
	t.Helper()

	var cfg setupConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(sqlDB))

	// Set up a channel manager with a handshake so
	// AccessibleWorkspaceIDs returns the desired workspaces.
	// Classical encryption keeps setupTestService cheap under -race: these
	// tests exercise service/dispatcher behaviour, not the PQ handshake, and
	// SLH-DSA under the race detector otherwise dominates the package runtime.
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	chmgr := channel.NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(*leapmuxv1.ConnectRequest) error { return nil }, nil, 0)
	t.Cleanup(chmgr.CloseAll)

	_, msg1, err := noiseutil.ClassicalInitiatorHandshake1(ck.X25519Public)
	require.NoError(t, err)
	chmgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:              testChannelID,
		UserId:                 "user-1",
		HandshakePayload:       msg1,
		AccessibleWorkspaceIds: cfg.workspaceIDs,
	})

	// Built through service.New, not by hand.
	//
	// A hand-rolled &Service{} is the same "declared but never wired"
	// hazard the Config embedding exists to remove, reintroduced in the
	// harness: it omitted PrivateEvents and FileTabPaths -- both
	// documented "always non-nil after New" -- so WatchWorkspacePrivateEvents
	// returned early on its own nil guard and any test dispatching it
	// passed without exercising anything. Going through the constructor
	// means these tests run the wiring production runs, and a field added
	// to New is covered the moment it exists.
	svc := New(Config{
		DB:        sqlDB,
		Channels:  chmgr,
		Send:      func(*leapmuxv1.ConnectRequest) error { return nil },
		Agents:    agent.NewManager(nil),
		Terminals: terminal.NewManager(),
		HomeDir:   t.TempDir(),
		DataDir:   t.TempDir(),
		// The test channel above is opened as "user-1", so make that the
		// worker's owner: the owner is the ordinary caller in production,
		// and the machine-scoped families (file/git/sysinfo/tunnel) admit
		// only them. In production this arrives from the Hub's
		// connect-time WorkerIdentity rather than at construction.
		SeedRegisteredBy: "user-1",
	})
	svc.RemoteIPC = cfg.remoteIPC

	d := channel.NewDispatcher()
	// RegisterAll binds svc.Cleanup itself, so tracked handlers dispatched
	// here gate Shutdown exactly the way they do in production.
	RegisterAll(d, svc)

	return svc, d, newTestWriter()
}

// startTestTerminal spawns a live PTY via svc.Terminals, persists a
// matching DB row so access-control lookups succeed, and registers the
// full cleanup chain. Returns the working directory assigned to the
// terminal so tests can use it for follow-up assertions. Used by tests
// that need a running terminal attached to an accessible workspace.
func startTestTerminal(t *testing.T, svc *Service, ctx context.Context, id, workspaceID string) string {
	t.Helper()
	workingDir := t.TempDir()

	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: id, WorkspaceID: workspaceID,
		Shell: testutil.TestShell(), WorkingDir: workingDir,
		Cols: 80, Rows: 24,
	}, func([]byte, int64) {}, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkspaceID: workspaceID, WorkingDir: workingDir, HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte{},
	}))
	return workingDir
}

// openTerminalViaRPC drives the OpenTerminal RPC end-to-end: dispatch,
// unmarshal, and wait for the PTY to register in the manager. Returns
// the terminal id minted by the worker. Tests that need to assert
// against the dispatch response should call dispatch directly.
func openTerminalViaRPC(t *testing.T, svc *Service, d *channel.Dispatcher, w *testResponseWriter, workspaceID, workingDir string) string {
	t.Helper()
	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: workspaceID,
		Shell:       testutil.TestShell(),
		WorkingDir:  workingDir,
		// 200 cols rather than 80 so cmd.exe's long t.TempDir-derived
		// prompt (e.g. `C:\Users\RUNNER~1\AppData\Local\Temp\<long
		// test name>\<id>>` ~ 90 chars) plus the trailing input does
		// not wrap. ConPTY's cooked-mode line editor can read partial
		// `exit 42` as `exit` when the line wraps, losing the digits
		// and exiting with errorlevel 0 instead of 42.
		Cols: 200,
		Rows: 24,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()
	require.NotEmpty(t, terminalID)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")
	return terminalID
}

// sendShellLine writes a complete shell command to the PTY. On Windows
// it writes one byte at a time so each char becomes its own ConPTY
// KEY_EVENT_RECORD — bulk writes can land out of order in the console
// input queue (e.g. `\r` processed ahead of trailing digits), so cmd
// sees `exit` instead of `exit 7`. On Unix the line goes through as a
// single write.
func sendShellLine(t *testing.T, d *channel.Dispatcher, terminalID string, line []byte) {
	t.Helper()
	if runtime.GOOS == "windows" {
		for i := 0; i < len(line); i++ {
			dispatch(d, "SendInput", &leapmuxv1.SendInputRequest{
				TerminalId: terminalID,
				Data:       []byte{line[i]},
			}, newTestWriter())
		}
		return
	}
	dispatch(d, "SendInput", &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       line,
	}, newTestWriter())
}

// exitTerminalAndWait sends `exit <code>` followed by Enter and waits
// for the PTY to register as exited. Use exitArg="" for a clean exit
// (code 0) or " 42" for a non-zero exit. The returned writer is
// retained for API compatibility; no current caller inspects it.
//
// The exit command is always sent with an explicit code (empty exitArg
// becomes " 0") so the shell's exit status does not depend on its
// inherited `$?`. macOS GitHub runners occasionally leave `$?` non-zero
// after `/bin/sh -i -l` init scripts, which would make a bare `exit`
// pick up that code instead of 0. The input is allowed to sit in the
// PTY's stdin buffer until the shell finishes its init scripts and
// reads it — no prompt-ready handshake is required because the parsed
// exit code does not depend on the shell having drained `$?`.
func exitTerminalAndWait(t *testing.T, svc *Service, d *channel.Dispatcher, terminalID, exitArg string) *testResponseWriter {
	t.Helper()
	if exitArg == "" {
		exitArg = " 0"
	}
	sendShellLine(t, d, terminalID, []byte("exit"+exitArg+testutil.TestShellEnter()))
	testutil.AssertEventually(t, func() bool { return svc.Terminals.IsExited(terminalID) }, "exit")
	return newTestWriter()
}

// drainAllInFlight joins any runAgentStartup / runTerminalStartup
// goroutines spawned during the test and waits for any in-flight close
// handlers tracked on svc.Cleanup. Call via `defer` immediately after
// setupTestService so it fires ahead of t.Cleanup-registered TempDir
// removal (test-body t.TempDir cleanups run first in LIFO order, and a
// `defer` runs even earlier — before any t.Cleanup). Without this, the
// background goroutines' trailing DB writes, git rollback, or broadcast
// work can race the cleanup, surfacing as "sql: database is closed"
// warnings or "directory not empty" TempDir removal failures.
func drainAllInFlight(svc *Service) {
	svc.AgentStartup.WaitForInFlight()
	svc.TerminalStartup.WaitForInFlight()
	svc.Cleanup.Wait()
}

// dispatch is a helper that marshals a request proto and dispatches it.
func dispatch(d *channel.Dispatcher, method string, req proto.Message, w *testResponseWriter) {
	payload, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}
	d.DispatchWith(context.Background(), userid.MustNew("user-1"), &leapmuxv1.InnerRpcRequest{
		Method:  method,
		Payload: payload,
	}, w)
}

func TestListAgentMessages_ClosedAgent_ReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	// Create an agent and add a message.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-1",
		AgentID:       "agent-1",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("hello"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
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
	w2 := newTestWriter()
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
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

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
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

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
		ClosedAt:    sqltime.SQLiteNullTimeOf(time.Now()),
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
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	// Create an agent, add a message, then close it.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-closed",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-1",
		AgentID:       "agent-closed",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("hello"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Queries.CloseAgent(ctx, "agent-closed"))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: "agent-closed", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST},
		},
	}, w)

	// Closed agent should produce a single stream error (NOT_FOUND).
	require.Len(t, w.streams, 1, "closed agent should produce a stream error")
	assert.True(t, w.streams[0].GetIsError(), "stream message should be an error")
	assert.Equal(t, int32(5), w.streams[0].GetErrorCode(), "error code should be NOT_FOUND")

	// Verify no watcher was registered.
	agentWatchers := svc.Watchers.agents.count("agent-closed")
	assert.Equal(t, 0, agentWatchers, "no watcher should be registered for closed agent")
}

func TestWatchEvents_ClosedTerminal_NotWatched(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	// Create a terminal and close it.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-closed",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("some screen"),
		ClosedAt:    sqltime.SQLiteNullTimeOf(time.Now()),
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-closed"}},
	}, w)

	// Closed terminal should produce a single stream error (NOT_FOUND).
	require.Len(t, w.streams, 1, "closed terminal should produce a stream error")
	assert.True(t, w.streams[0].GetIsError(), "stream message should be an error")
	assert.Equal(t, int32(5), w.streams[0].GetErrorCode(), "error code should be NOT_FOUND")

	// Verify no watcher was registered.
	termWatchers := svc.Watchers.terminals.count("term-closed")
	assert.Equal(t, 0, termWatchers, "no watcher should be registered for closed terminal")
}

// TestWatchEvents_ForeignWorkspaceAgent_NotWatched pins the access-control
// filter for the leakiest gateSetFilter handler: an OPEN agent that lives in a
// workspace outside the channel's accessible set must not be watched. This is a
// different rejection branch than TestWatchEvents_ClosedAgent_NotWatched — a
// closed agent is dropped by ListAgentsByIDs (the row never loads), whereas this
// agent loads fine and is rejected only by the !allowedWorkspaces check. Without
// that check a foreign workspace's live event stream would leak cross-tenant.
func TestWatchEvents_ForeignWorkspaceAgent_NotWatched(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	// Agent exists and is open, but in a workspace the channel cannot access.
	seedAgent(t, svc, "agent-foreign", "ws-other")

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: "agent-foreign", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST},
		},
	}, w)

	// All requested entities rejected => a single NOT_FOUND stream error.
	require.Len(t, w.streams, 1, "foreign-workspace agent should produce a stream error")
	assert.True(t, w.streams[0].GetIsError(), "stream message should be an error")
	assert.Equal(t, int32(5), w.streams[0].GetErrorCode(), "error code should be NOT_FOUND")

	agentWatchers := svc.Watchers.agents.count("agent-foreign")
	assert.Equal(t, 0, agentWatchers, "no watcher should be registered for a foreign-workspace agent")
}

// TestWatchEvents_ForeignWorkspaceTerminal_NotWatched is the terminal mirror:
// an open terminal in an inaccessible workspace must not be watched.
func TestWatchEvents_ForeignWorkspaceTerminal_NotWatched(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	seedTerminal(t, svc, "term-foreign", "ws-other")

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-foreign"}},
	}, w)

	require.Len(t, w.streams, 1, "foreign-workspace terminal should produce a stream error")
	assert.True(t, w.streams[0].GetIsError(), "stream message should be an error")
	assert.Equal(t, int32(5), w.streams[0].GetErrorCode(), "error code should be NOT_FOUND")

	termWatchers := svc.Watchers.terminals.count("term-foreign")
	assert.Equal(t, 0, termWatchers, "no watcher should be registered for a foreign-workspace terminal")
}

func TestShutdown_PersistsTerminalScreenSnapshots(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	workingDir := t.TempDir()

	// Start a real terminal.
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID:            "term-1",
		WorkspaceID:   "ws-1",
		Shell:         testutil.TestShell(),
		WorkingDir:    workingDir,
		ShellStartDir: "",
		Cols:          80,
		Rows:          24,
	}, func([]byte, int64) {}, nil))

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
	require.NoError(t, svc.Terminals.SendInput("term-1", []byte("echo shutdown_test"+testutil.TestShellEnter())))

	// Wait until the echo output appears in the screen buffer; otherwise
	// Shutdown can race ahead of the shell processing the input.
	testutil.AssertEventually(t, func() bool {
		screen, _, _ := svc.Terminals.ScreenSnapshotSince("term-1", 0)
		return bytes.Contains(screen, []byte("shutdown_test"))
	}, "expected terminal screen to contain 'shutdown_test'")

	// Call Shutdown — should persist screen to DB.
	svc.Shutdown()

	// Verify screen data was saved to DB.
	dbTerm, err := svc.Queries.GetTerminal(ctx, "term-1")
	require.NoError(t, err)
	assert.True(t, len(dbTerm.Screen) > 0, "screen should be persisted after Shutdown")
	assert.Contains(t, string(dbTerm.Screen), "shutdown_test")
	assert.Contains(t, string(dbTerm.Screen), "[Worker disconnected - Press Enter to restart]")
	assert.Equal(t, "user@host: ~/dir", dbTerm.Title, "title should be persisted after Shutdown")
	assert.False(t, dbTerm.ClosedAt.Valid, "Shutdown should not set closed_at")

	// Clean up.
	svc.Terminals.StopAll()
}

// TestShutdown_PreservesNaturalExitCode pins the contract that
// `Shutdown` does not clobber a previously-persisted exit code for a
// terminal that already exited naturally. Without the IsExited skip in
// Shutdown the exit_code column would be overwritten with
// exitCodeUnknown (-1) on every shutdown.
func TestShutdown_PreservesNaturalExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		// cmd.exe under ConPTY on the GitHub Windows runner exits with
		// errorlevel 0 regardless of the `exit N` argument typed at the
		// prompt, even when the input is echoed back to the screen
		// correctly. Reproduced with widened cols, byte-by-byte input,
		// /D to skip AutoRun, and explicit exit codes — none restore
		// the expected exit-code propagation. The contract under test
		// (Shutdown must not clobber a previously-persisted exit code)
		// is exercised cross-platform by
		// TestPersistTerminalOnExit_ShutdownDoesNotClobberRealExitCode
		// via a direct persistTerminalOnExit call.
		t.Skip("cmd.exe + ConPTY does not propagate `exit N` to OS exit code on this runner")
	}
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	terminalID := openTerminalViaRPC(t, svc, d, w, "ws-1", t.TempDir())
	// Exit with a non-zero, non-sentinel code so a regression that
	// writes the shutdown sentinel (-1) or the zero-value default (0)
	// is unambiguous.
	exitTerminalAndWait(t, svc, d, terminalID, " 42")
	testutil.AssertEventually(t, func() bool {
		row, err := svc.Queries.GetTerminal(ctx, terminalID)
		return err == nil && row.ExitCode == 42
	}, "exit handler must persist exit_code=42")

	svc.Shutdown()

	dbTerm, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.Equal(t, int64(42), dbTerm.ExitCode,
		"Shutdown must not clobber the natural exit code of an already-exited terminal")
}

func TestOpenTerminal_ExitPersistsExitedNotice(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	terminalID := openTerminalViaRPC(t, svc, d, w, "ws-1", t.TempDir())

	enter := testutil.TestShellEnter()
	w2 := newTestWriter()
	// Send the echo marker as a separate input so exitTerminalAndWait
	// can drive the canonical `exit + enter` flow and wait for IsExited.
	dispatch(d, "SendInput", &leapmuxv1.SendInputRequest{
		TerminalId: terminalID,
		Data:       []byte("echo exit_notice_test" + enter),
	}, w2)
	require.Empty(t, w2.errors)
	exitTerminalAndWait(t, svc, d, terminalID, "")

	// Assertions are split so a failure on any individual condition
	// surfaces a specific error message rather than an option "condition
	// never satisfied". The disconnect notice is the behavior under test;
	// the echoed-command check is an indirect sanity probe that the PTY
	// actually processed input.
	testutil.AssertEventually(t, func() bool {
		dbTerm, err := svc.Queries.GetTerminal(ctx, terminalID)
		return err == nil && dbTerm.ExitCode == 0
	}, "expected clean exit (exit_code=0) to be persisted")

	testutil.AssertEventually(t, func() bool {
		dbTerm, err := svc.Queries.GetTerminal(ctx, terminalID)
		return err == nil && strings.Contains(string(dbTerm.Screen), "[Terminal process exited (0) - Press Enter to restart]")
	}, "expected exit notice with exit code to be persisted in screen snapshot")

	// Skip the echoed-command substring check on Windows: cmd.exe under
	// ConPTY renders its banner and prompt through VT sequences that can
	// push the typed command off the visible 80×24 buffer by the time
	// the screen is snapshotted, so this check is unreliable there. The
	// disconnect-notice assertion above already covers the behavior the
	// test is meant to pin.
	if runtime.GOOS != "windows" {
		testutil.AssertEventually(t, func() bool {
			dbTerm, err := svc.Queries.GetTerminal(ctx, terminalID)
			return err == nil && strings.Contains(string(dbTerm.Screen), "exit_notice_test")
		}, "expected echoed command output to survive in screen snapshot")
	}
}

// TestWatchEvents_NarrowedRequest_UnsubscribesTheOmittedAgent pins the
// handler half of replace-semantics: a WatchEvents request states the
// channel's whole current interest, so an agent the client stops naming
// is unsubscribed.
//
// Nothing else can retire it. Closing a stream is client-local -- there
// is no cancel frame on the E2EE wire -- so the previous request's
// sender never errors and the send-failure sweep never fires. Before
// this, every closed tab left a registration that kept costing a
// marshal, an AEAD seal and a hub send on every event for the life of
// the channel.
func TestWatchEvents_NarrowedRequest_UnsubscribesTheOmittedAgent(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	for _, id := range []string{"agent-1", "agent-2"} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID:          id,
			WorkspaceID: "ws-1",
			WorkingDir:  "/tmp",
			HomeDir:     "/tmp",
		}))
	}

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}, {AgentId: "agent-2"}},
	}, w)
	require.Equal(t, 1, svc.Watchers.agents.count("agent-1"), "precondition: agent-1 watched")
	require.Equal(t, 1, svc.Watchers.agents.count("agent-2"), "precondition: agent-2 watched")

	// The agent-2 tab closed; the client re-issues with only agent-1.
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
	}, w)

	assert.Equal(t, 0, svc.Watchers.agents.count("agent-2"),
		"the agent the new request omits must be unsubscribed")
	assert.Equal(t, 1, svc.Watchers.agents.count("agent-1"),
		"the agent the new request still names stays watched")
}

// TestWatchEvents_TerminalLookupFailure_KeepsAndRebindsSubscriptions pins
// the guard on replace-semantics' sharp edge, and the half of it that a
// count assertion cannot see.
//
// The terminal lookup DEGRADES on error (it warns and carries on) rather
// than returning like the agent one, so a failed query leaves every
// requested terminal "rejected". Read as a statement of interest that
// would mean "this channel watches no terminals" and unsubscribe them
// all -- turning a transient DB blip into a silently dead UI that only a
// reconnect could fix.
//
// Keeping the set is necessary but not sufficient: the request arrived on
// a NEW stream, so the surviving registrations must be re-pointed at it.
// Left bound to the previous writer they address a correlation id the
// client has already torn down -- SendStream still succeeds, so nothing
// is retired, no error surfaces, and the terminal simply goes quiet.
func TestWatchEvents_TerminalLookupFailure_KeepsAndRebindsSubscriptions(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("s"),
	}))

	req := &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}},
	}
	dispatch(d, "WatchEvents", req, w)
	require.Equal(t, 1, svc.Watchers.terminals.count("term-1"), "precondition: term-1 watched")

	// Break ONLY the terminal lookup, so the agent half still verifies and
	// the request does not fall into the everything-was-rejected branch --
	// this has to exercise the degrade path itself.
	//
	// Done by dropping the table rather than swapping svc.Queries: Queries
	// is injected wiring, which the Service contract says is never written
	// once handlers dispatch, and a test that writes it anyway normalises
	// exactly the race that contract exists to forbid.
	_, err := svc.DB.Exec("DROP TABLE terminals")
	require.NoError(t, err)

	// Re-issue on a SECOND writer, standing in for the fresh stream a
	// resubscribe always arrives on.
	resubscribed := newTestWriter()
	dispatch(d, "WatchEvents", req, resubscribed)

	assert.Equal(t, 1, svc.Watchers.terminals.count("term-1"),
		"a failed lookup must not be read as 'watches no terminals'")
	assert.Same(t, resubscribed, svc.Watchers.terminals.senderFor("term-1", testChannelID),
		"the kept subscription must follow the stream that re-issued the request")
}

// TestWatchEvents_TerminalLookupFailure_TellsAFreshChannelToRetry covers
// the case rebinding cannot serve at all.
//
// Rebinding preserves whatever this channel already holds -- but a page
// refresh mints a NEW channel, which holds nothing. There is then no
// registration to keep, no registration to create, and (because at least
// one agent verified) no error either: the handler falls through the
// success path and the client is told its subscription is live. Its
// terminal panes stay blank for the channel's whole life, since a
// healthy-looking stream never trips the retry.
func TestWatchEvents_TerminalLookupFailure_TellsAFreshChannelToRetry(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))

	// No prior WatchEvents on this channel: nothing is registered, exactly
	// as after a page refresh.
	_, err := svc.DB.Exec("DROP TABLE terminals")
	require.NoError(t, err)

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}},
	}, w)

	assert.Equal(t, 0, svc.Watchers.terminals.count("term-1"),
		"precondition: a rebind on a fresh channel registers nothing")

	var sawError bool
	for _, m := range w.streamsSnapshot() {
		if m.GetIsError() {
			sawError = true
			assert.Equal(t, int32(codes.Unavailable), m.GetErrorCode(),
				"a failed lookup is a worker-side fault, so the client should retry")
		}
	}
	assert.True(t, sawError,
		"a request whose terminals were never registered must not report success")
}

// TestWatchEvents_EmptyRequestUnsubscribesWithoutAnError pins the only
// way a client can retire its subscriptions while keeping the channel.
//
// Closing a stream is client-local, so nothing reaches the worker; the
// frontend therefore sends an empty WatchEvents when its last tab on a
// worker closes. That has to be treated as a legitimate statement of
// interest -- unsubscribe everything, say nothing -- and NOT as the
// "you named entities and all of them were rejected" case, which
// answers with a NotFound stream error the client would retry on
// forever.
func TestWatchEvents_EmptyRequestUnsubscribesWithoutAnError(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          "term-1",
		WorkspaceID: "ws-1",
		WorkingDir:  "/tmp",
		HomeDir:     "/tmp",
		Cols:        80,
		Rows:        24,
		Screen:      []byte("s"),
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}},
	}, w)
	require.Equal(t, 1, svc.Watchers.agents.count("agent-1"), "precondition: agent-1 watched")
	require.Equal(t, 1, svc.Watchers.terminals.count("term-1"), "precondition: term-1 watched")

	unsubscribe := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{}, unsubscribe)

	assert.False(t, svc.Watchers.agents.hasEntity("agent-1"),
		"an empty request must retire the agent subscription")
	assert.False(t, svc.Watchers.terminals.hasEntity("term-1"),
		"an empty request must retire the terminal subscription")

	for _, s := range unsubscribe.streamsSnapshot() {
		assert.False(t, s.GetIsError(),
			"unsubscribing is a legitimate request, not a NotFound the client should retry")
	}
	assert.Empty(t, unsubscribe.errors, "and not an RPC error either")
}
