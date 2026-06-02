package service

import (
	"bytes"
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

// testChannelID is the channel id setupTestService's handshake
// registers; every fresh testResponseWriter in this package shares it
// so AuthorizerForSender resolves the same accessible-workspace set.
const testChannelID = "test-ch"

// newTestWriter returns a testResponseWriter bound to the package's
// canonical channel id. Use instead of the literal so a future channel
// id change is a single edit here.
func newTestWriter() *testResponseWriter {
	return &testResponseWriter{channelID: testChannelID}
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

// setupTestService creates a minimal service.Context with an in-memory DB
// and a channel manager configured per the supplied options.
func setupTestService(t *testing.T, opts ...setupOption) (*Context, *channel.Dispatcher, *testResponseWriter) {
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
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	chmgr := channel.NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, func(*leapmuxv1.ConnectRequest) error { return nil }, 0, 0, nil)

	_, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)
	chmgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:              testChannelID,
		UserId:                 "user-1",
		HandshakePayload:       msg1,
		AccessibleWorkspaceIds: cfg.workspaceIDs,
	})

	svc := &Context{
		DB:              sqlDB,
		Queries:         db.New(sqlDB),
		Channels:        chmgr,
		Agents:          agent.NewManager(nil),
		HomeDir:         t.TempDir(),
		DataDir:         t.TempDir(),
		Watchers:        NewWatcherManager(),
		Terminals:       terminal.NewManager(),
		AgentStartup:    newAgentStartupRegistry(),
		TerminalStartup: newTerminalStartupRegistry(),
		RemoteIPC:       cfg.remoteIPC,
	}
	svc.Output = NewOutputHandler(svc.DB, svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.Output.DataDir = svc.DataDir

	d := channel.NewDispatcher()
	RegisterAll(d, svc)

	return svc, d, newTestWriter()
}

// startTestTerminal spawns a live PTY via svc.Terminals, persists a
// matching DB row so access-control lookups succeed, and registers the
// full cleanup chain. Returns the working directory assigned to the
// terminal so tests can use it for follow-up assertions. Used by tests
// that need a running terminal attached to an accessible workspace.
func startTestTerminal(t *testing.T, svc *Context, ctx context.Context, id, workspaceID string) string {
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
func openTerminalViaRPC(t *testing.T, svc *Context, d *channel.Dispatcher, w *testResponseWriter, workspaceID, workingDir string) string {
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
func exitTerminalAndWait(t *testing.T, svc *Context, d *channel.Dispatcher, terminalID, exitArg string) *testResponseWriter {
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
func drainAllInFlight(svc *Context) {
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
	d.DispatchWith(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
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
		CreatedAt:     time.Now(),
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
		CreatedAt:     time.Now(),
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
		ClosedAt:    sql.NullTime{Time: time.Now(), Valid: true},
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-closed"}},
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
	// surfaces a specific error message rather than a generic "condition
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
