package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
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
	channelID  string
	mu         sync.Mutex
	responses  []*leapmuxv1.InnerRpcResponse
	errors     []testError
	streams    []*leapmuxv1.InnerStreamMessage
	streamCtrl channel.StreamController
	failStream bool
}

// killStreamSends makes every later SendStream fail, simulating a dead
// transport for the replay paths that classify send errors.
func (w *testResponseWriter) killStreamSends() {
	w.mu.Lock()
	w.failStream = true
	w.mu.Unlock()
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
	if w.failStream {
		return errors.New("test: transport gone")
	}
	w.streams = append(w.streams, m)
	return nil
}

func (w *testResponseWriter) ChannelID() string   { return w.channelID }
func (*testResponseWriter) MaxPayloadBudget() int { return 0 }
func (w *testResponseWriter) BindStream(ctrl channel.StreamController) (func(), bool) {
	w.mu.Lock()
	w.streamCtrl = ctrl
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		w.streamCtrl = nil
		w.mu.Unlock()
	}, true
}

// deliverStreamRequest invokes the bound StreamController as if an
// InnerStreamRequest arrived on the wire.
func (w *testResponseWriter) deliverStreamRequest(payload []byte, cancel bool) {
	w.mu.Lock()
	ctrl := w.streamCtrl
	w.mu.Unlock()
	if ctrl == nil {
		return
	}
	if cancel {
		ctrl.OnCancel()
		return
	}
	ctrl.OnClientFrame(payload)
}

// testChannelID is the channel id setupTestService's handshake
// registers; every fresh testResponseWriter in this package shares it so a
// dispatched handler always finds the same live channel session.
const testChannelID = "test-ch"

// newTestWriter returns a testResponseWriter bound to the package's
// canonical channel id. Use instead of the literal so a future channel
// id change is a single edit here.
func newTestWriter() *testResponseWriter {
	return &testResponseWriter{channelID: testChannelID}
}

// registerAgentWatch installs sender as a watcher for agentID on channelID in
// mode, bypassing session ownership. Handler tests use it so broadcasts reach a
// capture writer; production always registers through SetAgentWatchesForSession.
func registerAgentWatch(svc *Service, channelID, agentID string, mode leapmuxv1.WatchMode, sender channel.ResponseWriter) {
	svc.Watchers.agents.setWatches(channelID, []watchEntry{{id: agentID, mode: mode}}, sender)
}

// registerTerminalWatch is the terminal twin of registerAgentWatch.
func registerTerminalWatch(svc *Service, channelID, terminalID string, mode leapmuxv1.WatchMode, sender channel.ResponseWriter) {
	svc.Watchers.terminals.setWatches(channelID, []watchEntry{{id: terminalID, mode: mode}}, sender)
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
	remoteIPC    ControlIPCFactory
	rewriteQuery func(string) string
}

// withRemoteIPC wires the worker's RemoteIPC factory before handlers are
// registered so tests can assert mint/release semantics for the
// LEAPMUX_CONTROL_* token without poking svc.ControlIPC directly.
func withRemoteIPC(ipc ControlIPCFactory) setupOption {
	return func(c *setupConfig) { c.remoteIPC = ipc }
}

// withQueryRewrite runs every statement through rewrite before it reaches the
// database, so a test can fault ONE generated query and leave the rest alone.
//
// This is the only seam that reaches a read a handler makes AFTER its own id
// gate already proved the row exists. Schema surgery cannot separate the two:
// the gate and the read address the same table, and rewriting the read is the
// only way to produce the missing row or the store fault that the gate makes
// unreachable. Match on the `-- name: <Query> :one` header that sqlc puts at
// the top of each statement, not on the SQL body, so a reworded query still
// selects the same seam.
func withQueryRewrite(rewrite func(query string) string) setupOption {
	return func(c *setupConfig) { c.rewriteQuery = rewrite }
}

// rewritingDBTX hands each statement to rewrite and runs whatever comes back.
// A rewrite that returns its argument unchanged is a passthrough.
type rewritingDBTX struct {
	inner   db.DBTX
	rewrite func(string) string
}

func (r rewritingDBTX) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return r.inner.ExecContext(ctx, r.rewrite(query), args...)
}

func (r rewritingDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return r.inner.PrepareContext(ctx, r.rewrite(query))
}

func (r rewritingDBTX) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.inner.QueryContext(ctx, r.rewrite(query), args...)
}

func (r rewritingDBTX) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return r.inner.QueryRowContext(ctx, r.rewrite(query), args...)
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
	require.NoError(t, workerdb.Migrate(context.Background(), sqlDB))

	// Set up a channel manager with a completed handshake so dispatched
	// handlers see a real session.
	// Classical encryption keeps setupTestService cheap under -race: these
	// tests exercise service/dispatcher behaviour, not the PQ handshake, and
	// SLH-DSA under the race detector otherwise dominates the package runtime.
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	chmgr := channel.NewManager(ck, leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC, func(*leapmuxv1.ConnectRequest) error { return nil }, nil, 0, 0)
	t.Cleanup(chmgr.CloseAll)

	_, msg1, err := noiseutil.ClassicalInitiatorHandshake1(ck.X25519Public)
	require.NoError(t, err)
	chmgr.HandleOpen(&leapmuxv1.ChannelOpenRequest{
		ChannelId:        testChannelID,
		UserId:           "user-1",
		HandshakePayload: msg1,
		MaxMessageSize:   uint64(channelwire.MaxMessageSize),
	})

	// Built through service.New, not by hand.
	//
	// A hand-rolled &Service{} is the same "declared but never wired"
	// hazard the Config embedding exists to remove, reintroduced in the
	// harness: it omitted PrivateEvents and FileTabPaths -- both
	// documented "always non-nil after New" -- so WatchWorkerPrivateEvents
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
	svc.ControlIPC = cfg.remoteIPC
	// Before RegisterAll, exactly like every other field of Service: the
	// struct's own contract makes that the last point at which a write is
	// ordered ahead of every handler goroutine.
	if cfg.rewriteQuery != nil {
		svc.Queries = db.New(rewritingDBTX{inner: sqlDB, rewrite: cfg.rewriteQuery})
	}

	d := channel.NewDispatcher()
	// RegisterAll binds svc.Cleanup itself, so tracked handlers dispatched
	// here gate Shutdown exactly the way they do in production.
	RegisterAll(d, svc)

	return svc, d, newTestWriter()
}

// startTestTerminal spawns a live PTY via svc.Terminals, persists a
// matching DB row so the requireTerminal lookup succeeds, and registers
// the full cleanup chain. Returns the working directory assigned to the
// terminal so tests can use it for follow-up assertions.
func startTestTerminal(t *testing.T, svc *Service, ctx context.Context, id string) string {
	t.Helper()
	workingDir := t.TempDir()

	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: id, Shell: testutil.TestShell(), WorkingDir: workingDir,
		Cols: 80, Rows: 24,
	}, func([]byte, int64, []terminal.Signal) {}, nil))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkingDir: workingDir, HomeDir: "/tmp",
		Cols: 80, Rows: 24, Screen: []byte{},
	}))
	return workingDir
}

// openTerminalViaRPC drives the OpenTerminal RPC end-to-end: dispatch,
// unmarshal, and wait for the PTY to register in the manager. Returns
// the terminal id minted by the worker. Tests that need to assert
// against the dispatch response should call dispatch directly.
//
// Always registers terminal cleanup (same as startTestTerminal). Callers
// typically pass t.TempDir() as workingDir; that cleanup is registered
// before this helper runs, so LIFO cleanup stops the shell first. Without
// that order, Windows unlinkat on the temp dir fails with "used by
// another process" because the live cmd.exe still has the directory open
// as its CWD. Shutdown deliberately leaves PTYs running (it only
// broadcasts the disconnect notice), so relying on svc.Shutdown alone
// does not release the handle.
func openTerminalViaRPC(t *testing.T, svc *Service, d *channel.Dispatcher, w *testResponseWriter, workingDir string) string {
	t.Helper()
	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		Shell:      testutil.TestShell(),
		WorkingDir: workingDir,
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
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
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

// dispatch is a helper that marshals a request proto and dispatches it as the
// worker's own owner ("user-1", the identity setupTestService seeds).
func dispatch(d *channel.Dispatcher, method string, req proto.Message, w *testResponseWriter) {
	dispatchAs(d, userid.MustNew("user-1"), method, req, w)
}

// dispatchAs is dispatch with an explicit caller, for the access-control probes
// that need someone OTHER than the worker's owner.
func dispatchAs(d *channel.Dispatcher, caller userid.UserID, method string, req proto.Message, w *testResponseWriter) {
	payload, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}
	d.DispatchWith(context.Background(), caller, &leapmuxv1.InnerRpcRequest{
		Method:  method,
		Payload: payload,
	}, w)
}

func TestListAgentMessages_ClosedAgent_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Create an agent and add a message.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-1",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
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
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, "agent-1")))

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
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Create two agents.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-open",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
	}))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-closed",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
	}))

	// Close one agent.
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, "agent-closed")))

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
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Create two terminals via DB.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         "term-open",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
		Cols:       80,
		Rows:       24,
		Screen:     []byte("open screen"),
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         "term-closed",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
		Cols:       80,
		Rows:       24,
		Screen:     []byte("closed screen"),
		ClosedAt:   sqltime.SQLiteNullTimeOf(time.Now()),
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
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Create an agent, add a message, then close it.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-closed",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
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
	require.NoError(t, closeErr(svc.Queries.CloseAgent(ctx, "agent-closed")))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: "agent-closed", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST},
		},
	}, w)

	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	require.Len(t, ack.GetRejectedAgents(), 1)
	assert.Equal(t, "agent-closed", ack.GetRejectedAgents()[0].GetEntityId())
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
		ack.GetRejectedAgents()[0].GetReason())
	assert.False(t, streamEndedWithError(w), "partial rejection must not end the stream")

	// Verify no watcher was registered.
	agentWatchers := svc.Watchers.agents.count("agent-closed")
	assert.Equal(t, 0, agentWatchers, "no watcher should be registered for closed agent")
}

func TestWatchEvents_ClosedTerminal_NotWatched(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Create a terminal and close it.
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         "term-closed",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
		Cols:       80,
		Rows:       24,
		Screen:     []byte("some screen"),
		ClosedAt:   sqltime.SQLiteNullTimeOf(time.Now()),
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-closed"}},
	}, w)

	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	require.Len(t, ack.GetRejectedTerminals(), 1)
	assert.Equal(t, "term-closed", ack.GetRejectedTerminals()[0].GetEntityId())
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
		ack.GetRejectedTerminals()[0].GetReason())
	assert.False(t, streamEndedWithError(w), "partial rejection must not end the stream")

	// Verify no watcher was registered.
	termWatchers := svc.Watchers.terminals.count("term-closed")
	assert.Equal(t, 0, termWatchers, "no watcher should be registered for closed terminal")
}

// TestWatchEvents_UnknownAgent_NotWatched pins the rejection branch that
// survives: an id this worker holds no OPEN row for is refused, and nothing is
// registered for it. (A CLOSED agent takes the same path -- ListAgentsByIDs
// filters closed_at IS NULL, so the row simply never loads.)
func TestWatchEvents_UnknownAgent_NotWatched(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: "agent-unknown", Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST},
		},
	}, w)

	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	require.Len(t, ack.GetRejectedAgents(), 1)
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
		ack.GetRejectedAgents()[0].GetReason())
	assert.False(t, streamEndedWithError(w))

	assert.Equal(t, 0, svc.Watchers.agents.count("agent-unknown"),
		"no watcher should be registered for an agent this worker does not hold")
}

// TestWatchEvents_UnknownTerminal_NotWatched is the terminal mirror.
func TestWatchEvents_UnknownTerminal_NotWatched(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-unknown"}},
	}, w)

	ack := waitWatchUpdateAck(t, w)
	require.NotNil(t, ack)
	require.Len(t, ack.GetRejectedTerminals(), 1)
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
		ack.GetRejectedTerminals()[0].GetReason())
	assert.False(t, streamEndedWithError(w))

	assert.Equal(t, 0, svc.Watchers.terminals.count("term-unknown"),
		"no watcher should be registered for a terminal this worker does not hold")
}

// TestShutdown_BroadcastsDisconnectNoticeToLiveWatchers pins the half of the
// shutdown notice that reaches the USER rather than the database.
//
// A worker that is going down cannot replay anything afterwards -- the process
// is gone -- so this broadcast is the only chance a browser has to learn its
// terminal died. TestShutdown_PersistsTerminalScreenSnapshots covers the DB
// row, but it starts the terminal with a throwaway output handler, so nothing
// was asserting that a subscriber gets the notice at all.
func TestShutdown_BroadcastsDisconnectNoticeToLiveWatchers(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	// Via the RPC so the terminal carries the real broadcasting output
	// handler (makeTerminalOutputFn) rather than a test stub.
	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{
			TerminalId: terminalID,
			Mode:       leapmuxv1.WatchMode_WATCH_MODE_FULL,
		}},
	}, wWatch)
	waitWatchUpdateAck(t, wWatch)
	waitTerminalWatchCount(t, svc, terminalID, 1)

	svc.Shutdown()

	require.Eventually(t, func() bool {
		var got []string
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			if data := resp.GetTerminalEvent().GetData(); data != nil {
				got = append(got, string(data.GetData()))
			}
		}
		return strings.Contains(strings.Join(got, ""), "[Worker disconnected - Press Enter to restart]")
	}, 2*time.Second, 10*time.Millisecond,
		"Shutdown must broadcast the disconnect notice to live watchers, not only persist it")
}

func TestShutdown_StopsRunningAgents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	require.True(t, svc.Agents.HasAgent("agent-1"))

	svc.Shutdown()

	testutil.AssertEventually(t, func() bool {
		return !svc.Agents.HasAgent("agent-1")
	}, "Shutdown must StopAll agents; Setsid children do not die with the worker process group")
}

// TestShutdown_BroadcastsDisconnectNoticeBeforeDraining pins the ORDER, which
// is the half that actually decides whether the user sees the notice.
//
// Shutdown's drains wait for in-flight agent/terminal startups, and a startup
// parked in its CLI handshake can hold that for tens of seconds. Broadcasting
// after them meant that under load the Hub's idle timeout could declare the
// worker gone and tear down its channels first, so the notice was written to a
// stream nobody was relaying -- the browser's terminal just stopped, with
// nothing logged. The broadcast now happens before anything that can block.
func TestShutdown_BroadcastsDisconnectNoticeBeforeDraining(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{
			TerminalId: terminalID,
			Mode:       leapmuxv1.WatchMode_WATCH_MODE_FULL,
		}},
	}, wWatch)
	waitWatchUpdateAck(t, wWatch)
	waitTerminalWatchCount(t, svc, terminalID, 1)

	// An agent startup that never finishes on its own: this is what Shutdown's
	// AgentStartup.WaitForInFlight parks on.
	release := make(chan struct{})
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		<-release
		return nil, nil
	}
	wAgent := newTestWriter()
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, wAgent)
	require.Empty(t, wAgent.errors)

	shutdownDone := make(chan struct{})
	go func() {
		svc.Shutdown()
		close(shutdownDone)
	}()
	// Unblock the startup no matter how this test exits, so Shutdown can return
	// and the deferred cleanups are not left waiting on it.
	defer func() {
		close(release)
		<-shutdownDone
	}()

	noticeSeen := func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			if data := resp.GetTerminalEvent().GetData(); data != nil &&
				bytes.Contains(data.GetData(), []byte("[Worker disconnected - Press Enter to restart]")) {
				return true
			}
		}
		return false
	}

	require.Eventually(t, noticeSeen, 5*time.Second, 10*time.Millisecond,
		"the notice must reach watchers while the drain is still parked")

	// And the drain really is still parked -- otherwise the assertion above
	// would pass even with the broadcast left at the end of Shutdown.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before the parked startup was released; the ordering was not exercised")
	default:
	}
}

func TestShutdown_PersistsTerminalScreenSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	workingDir := t.TempDir()

	// Start a real terminal.
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID:            "term-1",
		Shell:         testutil.TestShell(),
		WorkingDir:    workingDir,
		ShellStartDir: "",
		Cols:          80,
		Rows:          24,
	}, func([]byte, int64, []terminal.Signal) {}, nil))

	require.True(t, svc.Terminals.UpdateTitle("term-1", "user@host: ~/dir"))

	// Persist the initial record (like OpenTerminal does).
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         "term-1",
		WorkingDir: workingDir,
		HomeDir:    svc.HomeDir,
		Cols:       80,
		Rows:       24,
		Screen:     []byte{},
	}))

	// Send a command so the terminal has screen content.
	require.NoError(t, svc.Terminals.SendInput("term-1", []byte("echo shutdown_test"+testutil.TestShellEnter())))

	// Wait until the echo output appears in the screen buffer; otherwise
	// Shutdown can race ahead of the shell processing the input.
	testutil.AssertEventually(t, func() bool {
		screen, _, _ := svc.Terminals.ScreenSnapshotSince("term-1", 0)
		return bytes.Contains(screen, []byte("shutdown_test"))
	}, "expected terminal screen to contain 'shutdown_test'")

	// Kill the PTY after Shutdown via RegisterTerminalCleanup (not
	// StopAll): Shutdown deliberately leaves shells running, and on
	// Windows cmd.exe holding workingDir as CWD makes t.TempDir's
	// RemoveAll fail unless the process is stopped and reaped first.
	testutil.RegisterTerminalCleanup(t, svc.Terminals, "term-1")

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
}

// TestShutdown_PreservesNaturalExitCode pins the contract that
// `Shutdown` does not clobber a previously-persisted exit code for a
// terminal that already exited naturally. Without the IsExited skip in
// Shutdown the exit_code column would be overwritten with
// exitCodeUnknown (-1) on every shutdown.
func TestShutdown_PreservesNaturalExitCode(t *testing.T) {
	t.Parallel()

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
	svc, d, w := setupTestService(t)

	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())
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
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

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
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	for _, id := range []string{"agent-1", "agent-2"} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID:         id,
			WorkingDir: "/tmp",
			HomeDir:    "/tmp",
		}))
	}

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}, {AgentId: "agent-2"}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)
	waitAgentWatchCount(t, svc, "agent-2", 1)

	// The agent-2 tab closed; the client revises with only agent-1.
	payload, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
	})
	require.NoError(t, err)
	w.deliverStreamRequest(payload, false)

	require.Eventually(t, func() bool {
		return svc.Watchers.agents.count("agent-2") == 0 && svc.Watchers.agents.count("agent-1") == 1
	}, time.Second, 10*time.Millisecond)
}

// TestWatchEvents_TerminalLookupFailure_KeepsSubscriptions pins the guard
// on replace-semantics' sharp edge for a LOOKUP_FAILED terminal half.
//
// The terminal lookup DEGRADES on error (it warns and carries on) rather
// than returning like the agent one, so a failed query leaves every
// requested terminal "rejected". Read as a statement of interest that
// would mean "this channel watches no terminals" and unsubscribe them
// all -- turning a transient DB blip into a silently dead UI that only a
// reconnect could fix.
//
// Keeping the set is necessary but not sufficient: the surviving
// registrations must stay bound to the same long-lived stream writer.
func TestWatchEvents_TerminalLookupFailure_KeepsAndRebindsSubscriptions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-1",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         "term-1",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
		Cols:       80,
		Rows:       24,
		Screen:     []byte("s"),
	}))

	req := &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}},
	}
	dispatch(d, "WatchEvents", req, w)
	waitTerminalWatchCount(t, svc, "term-1", 1)

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

	// Re-issue on the SAME writer via a stream revision.
	payload, err := proto.Marshal(req)
	require.NoError(t, err)
	w.deliverStreamRequest(payload, false)

	assert.Equal(t, 1, svc.Watchers.terminals.count("term-1"),
		"a failed lookup must not be read as 'watches no terminals'")
	assert.Same(t, w, svc.Watchers.terminals.senderFor("term-1", testChannelID),
		"the kept subscription must stay on the same stream")
	ack := waitWatchUpdateAckWhere(t, w, func(a *leapmuxv1.WatchUpdateAck) bool {
		return len(a.GetRejectedTerminals()) == 1
	})
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
		ack.GetRejectedTerminals()[0].GetReason())
}

// TestWatchEvents_TerminalLookupFailure_TellsAFreshChannelToRetry covers
// LOOKUP_FAILED when the channel holds no prior terminal registration.
//
// A long-lived stream preserves whatever this channel already holds --
// but a page refresh mints a NEW channel, which holds nothing. There is
// then no registration to keep and none to create; the ack must list
// LOOKUP_FAILED so the client retries rather than treating the stream as
// successfully watching the terminal.
func TestWatchEvents_TerminalLookupFailure_TellsAFreshChannelToRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-1",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
	}))

	// No prior WatchEvents on this channel: nothing is registered, exactly
	// as after a page refresh.
	_, err := svc.DB.Exec("DROP TABLE terminals")
	require.NoError(t, err)

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}},
	}, w)

	waitAgentWatchCount(t, svc, "agent-1", 1)
	assert.Equal(t, 0, svc.Watchers.terminals.count("term-1"),
		"precondition: a fresh channel registers nothing for failed terminals")

	ack := waitWatchUpdateAckWhere(t, w, func(a *leapmuxv1.WatchUpdateAck) bool {
		return len(a.GetRejectedTerminals()) == 1
	})
	require.NotNil(t, ack)
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
		ack.GetRejectedTerminals()[0].GetReason())
	assert.False(t, streamEndedWithError(w),
		"a failed terminal lookup must not kill the stream")
}

// TestWatchEvents_EmptyRequestUnsubscribesWithoutAnError pins the only
// way a client can retire its subscriptions while keeping the stream.
//
// A cancel frame ends the stream; an empty WatchEvents revision keeps the
// stream open and clears interest. That has to be treated as a legitimate
// statement -- unsubscribe everything, ack with no rejections -- and NOT
// as the "you named entities and all of them were rejected" case.
func TestWatchEvents_EmptyRequestUnsubscribesWithoutAnError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:         "agent-1",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:         "term-1",
		WorkingDir: "/tmp",
		HomeDir:    "/tmp",
		Cols:       80,
		Rows:       24,
		Screen:     []byte("s"),
	}))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents:    []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1"}},
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}},
	}, w)
	waitAgentWatchCount(t, svc, "agent-1", 1)
	waitTerminalWatchCount(t, svc, "term-1", 1)

	unsubscribe := newTestWriter()
	payload, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{})
	require.NoError(t, err)
	w.deliverStreamRequest(payload, false)

	require.Eventually(t, func() bool {
		return !svc.Watchers.agents.hasEntity("agent-1") && !svc.Watchers.terminals.hasEntity("term-1")
	}, time.Second, 10*time.Millisecond)

	ack := waitWatchUpdateAckWhere(t, w, func(a *leapmuxv1.WatchUpdateAck) bool {
		return len(a.GetRejectedAgents()) == 0 && len(a.GetRejectedTerminals()) == 0 &&
			!svc.Watchers.agents.hasEntity("agent-1")
	})
	require.NotNil(t, ack)
	assert.Empty(t, ack.GetRejectedAgents())
	assert.Empty(t, ack.GetRejectedTerminals())
	assert.False(t, streamEndedWithError(w),
		"unsubscribing is a legitimate request, not an error the client should retry")
	_ = unsubscribe
}

// TestShutdown_LiveOutputBetweenPassesDoesNotDoubleTheNotice pins the
// idempotency of Shutdown's two-pass broadcast against the case the screen-byte
// check cannot see.
//
// The second pass exists for terminals that were still starting during the
// drains. Its guard used to be `bytes.HasSuffix(screen,
// terminalExitedNoticeSuffix)`, which only holds while the screen STOPS at the
// notice -- and Shutdown does not stop the PTYs, so a terminal running a build
// or a `tail -f` appends more output between the passes. The suffix check then
// failed and the user got the notice twice, with live output sandwiched
// between, both in the browser and in the restored scrollback.
func TestShutdown_LiveOutputBetweenPassesDoesNotDoubleTheNotice(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	notified := make(map[string]struct{})
	svc.broadcastTerminalsDisconnected(notified)

	// The shell keeps writing after the first pass, exactly as a build or a
	// pager would across the drains between the two sweeps.
	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("build still running...\r\n")))

	svc.broadcastTerminalsDisconnected(notified)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(row.Screen), "[Worker disconnected - Press Enter to restart]"),
		"the notice must be appended exactly once per terminal per shutdown")
}

// TestPersistTerminalOnExit_PreservesClosedAt pins the column UpsertTerminal
// would otherwise blank.
//
// UpsertTerminal's DO UPDATE assigns `closed_at = excluded.closed_at`, so a
// params struct that leaves the field zero writes NULL. Shutdown's sweep
// snapshots a terminal, a CloseTerminal handler still on the dispatcher
// goroutine stamps closed_at, and the sweep's upsert then lands after it --
// RESURRECTING a tab the user just closed, whose screen blob is now out of
// reach of DeleteClosedTerminalsBefore.
func TestPersistTerminalOnExit_PreservesClosedAt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	// The close lands while the terminal is still in the manager, which is
	// precisely the interleave: RemoveTerminal and the DB stamp are two steps.
	_, closeErr := svc.Queries.CloseTerminal(ctx, terminalID)
	require.NoError(t, closeErr)

	require.True(t, svc.persistTerminalOnExit(terminalID, exitCodeUnknown))

	row, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "the shutdown upsert must not un-close a closed terminal")
}

// TestShutdown_RefusesNewTabs pins the precondition Shutdown's own comment
// asserts but nothing used to enforce.
//
// Both worker entry points deliberately keep the Hub stream live ACROSS
// Shutdown, so its broadcasts can actually leave, and the dispatcher has no
// gate of its own. An OpenTerminal arriving during the drains therefore spawned
// a PTY that installed after BOTH disconnect-notice sweeps -- an orphaned shell
// the user was never told about -- and called wg.Add(1) on the startup
// WaitGroup that WaitForInFlight was already blocked inside, which the
// sync.WaitGroup docs call out as misuse rather than merely a lost message.
func TestShutdown_RefusesNewTabs(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	svc.Shutdown()

	for _, tc := range []struct {
		method string
		req    proto.Message
	}{
		{"OpenTerminal", &leapmuxv1.OpenTerminalRequest{WorkingDir: t.TempDir(), Shell: "/bin/zsh"}},
		{"OpenAgent", &leapmuxv1.OpenAgentRequest{
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			w := newTestWriter()
			dispatch(d, tc.method, tc.req, w)

			require.Len(t, w.errors, 1, "%s must be refused once shutdown has begun", tc.method)
			assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
			assert.Contains(t, w.errors[0].message, "shutting down")
			assert.Empty(t, w.responses, "and must not hand back a tab it is about to kill")
		})
	}

	// Nothing was registered, so the drains have nothing new to wait on.
	assert.False(t, svc.Terminals.HasTerminal("any"))
}
