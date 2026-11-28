package droid

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const droidChildProcessTestEnv = "LEAPMUX_DROID_CHILD_PROCESS_TEST"
const droidChildConnectionSessionID = "52af9b7b-5e82-4932-83c0-a2bf1caa2fcd"

// TestDroidChildProcessHelper is the stream JSON-RPC process in the routing test.
func TestDroidChildProcessHelper(t *testing.T) {
	if os.Getenv(droidChildProcessTestEnv) != "1" {
		return
	}
	logPath := os.Getenv("LEAPMUX_DROID_CHILD_PROCESS_LOG")
	scanner := bufio.NewScanner(os.Stdin)
	pendingExit := false
	for scanner.Scan() {
		var request droidEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Type != droidTypeRequest {
			continue
		}
		var params struct {
			SessionID string `json:"sessionId"`
			TurnID    string `json:"turnId"`
		}
		_ = json.Unmarshal(request.Params, &params)
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, err = fmt.Fprintf(file, "%d %s %s\n", os.Getpid(), request.Method, params.SessionID)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			os.Exit(2)
		}
		if request.Method == "droid.test_release_exit" && pendingExit {
			os.Exit(7)
		}
		if request.Method == "droid.test_emit_turn_completed" {
			notif, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "type": droidTypeNotification, "method": droidMethodSessionNotif,
				"params": map[string]any{
					"sessionId": params.SessionID,
					"notification": map[string]any{
						"type":   contracts.DroidNotificationAgentTurnCompleted,
						"turnId": params.TurnID,
						"reason": contracts.DroidTurnEndReasonCompleted,
					},
				},
			})
			if err != nil {
				os.Exit(2)
			}
			if _, err := fmt.Fprintln(os.Stdout, string(notif)); err != nil {
				os.Exit(2)
			}
			continue
		}
		if request.Method == "droid.test_emit_error" {
			notif, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "type": droidTypeNotification, "method": droidMethodSessionNotif,
				"params": map[string]any{
					"sessionId": params.SessionID,
					"notification": map[string]any{
						"type":    contracts.DroidNotificationError,
						"message": "child turn boom",
					},
				},
			})
			if err != nil {
				os.Exit(2)
			}
			if _, err := fmt.Fprintln(os.Stdout, string(notif)); err != nil {
				os.Exit(2)
			}
			continue
		}
		if request.Method == droidMethodAddUserMessage {
			// Droid acknowledges enqueueing before the process can exit during the turn.
			if _, err := fmt.Fprintf(os.Stdout, `{"type":"response","id":%q,"result":{}}`+"\n", request.ID); err != nil {
				os.Exit(2)
			}
		}
		if request.Method == droidMethodAddUserMessage && os.Getenv("LEAPMUX_DROID_CHILD_EXIT_AFTER_RELEASE") == "1" {
			pendingExit = true
			continue
		}
		if request.Method == droidMethodAddUserMessage && os.Getenv("LEAPMUX_DROID_CHILD_EXIT_ON_ADD") == "1" {
			os.Exit(7)
		}
		if request.Method == droidMethodLoadSession && os.Getenv("LEAPMUX_DROID_CHILD_LOAD_ERROR") == "1" {
			if _, err := fmt.Fprintf(os.Stdout, `{"type":"response","id":%q,"error":{"code":-32000,"message":"load denied"}}`+"\n", request.ID); err != nil {
				os.Exit(2)
			}
			continue
		}
		if request.Method == droidMethodLoadSession {
			if gateAddr := os.Getenv("LEAPMUX_DROID_CHILD_LOAD_GATE_ADDR"); gateAddr != "" {
				conn, err := net.Dial("tcp", gateAddr)
				if err != nil {
					os.Exit(2)
				}
				_, _ = io.ReadAll(conn)
				_ = conn.Close()
			}
		}
		if request.Method != droidMethodInitializeSession && request.Method != droidMethodLoadSession {
			continue
		}
		sessionID := params.SessionID
		if sessionID == "" {
			sessionID = "main-session"
		}
		response := newDroidEnvelope(droidTypeResponse)
		response.ID = request.ID
		response.Result, _ = json.Marshal(map[string]any{
			"sessionId": sessionID, "settings": map[string]any{}, "availableModels": []any{},
		})
		line, err := response.Marshal()
		if err != nil {
			os.Exit(2)
		}
		if _, err := fmt.Fprintln(os.Stdout, string(line)); err != nil {
			os.Exit(2)
		}
	}
	if scanner.Err() != nil {
		os.Exit(2)
	}
}

type droidProcessCall struct {
	pid       string
	method    string
	sessionID string
}

func readDroidProcessCalls(path string) []droidProcessCall {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var calls []droidProcessCall
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			call := droidProcessCall{pid: fields[0], method: fields[1]}
			if len(fields) > 2 {
				call.sessionID = fields[2]
			}
			calls = append(calls, call)
		}
	}
	return calls
}

type droidProcessFixture struct {
	root    *Agent
	sink    *agenttest.Sink
	home    string
	logPath string
}

func newDroidProcessFixture(t *testing.T, model string, extraEnv ...string) droidProcessFixture {
	t.Helper()
	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	logPath := filepath.Join(home, "rpc-calls.log")
	binary, err := os.Executable()
	require.NoError(t, err)
	env := []string{
		droidChildProcessTestEnv + "=1",
		"LEAPMUX_DROID_CHILD_PROCESS_LOG=" + logPath,
	}
	env = append(env, extraEnv...)
	spec := launch.Spec{Program: binary, PrefixArgs: []string{"-test.run=^TestDroidChildProcessHelper$", "--"}, Env: env}
	sink := &agenttest.Sink{}
	options := agent.Options{
		AgentID: "root-agent", WorkingDir: work, HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), StartupTimeout: 10 * time.Second,
		Options: optionmap.Map{agent.OptionIDModel: model},
	}
	started, err := startProcess(t.Context(), options, agent.NewProviderServices(sink), droidLaunch{spec: spec, shutdownGrace: Registration().ShutdownGrace})
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(a.Stop)
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "droid-tool-task-1", ProviderChildKey: droidChildConnectionSessionID, Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask,

		RowKey: droidChildConnectionSessionID, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusSucceeded,
	}))
	return droidProcessFixture{root: a, sink: sink, home: home, logPath: logPath}
}

func TestChildSendLoadsItsOwnStreamSession(t *testing.T) {
	fixture := newDroidProcessFixture(t, "")
	a, logPath := fixture.root, fixture.logPath

	require.NoError(t, agenttest.SendChildInput(a, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"}))
	require.Eventually(t, func() bool {
		for _, call := range readDroidProcessCalls(logPath) {
			if call.method == droidMethodAddUserMessage {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "a native add_user_message request must reach one process")
	calls := readDroidProcessCalls(logPath)
	var rootPID, childPID string
	for _, call := range calls {
		if call.method == droidMethodInitializeSession {
			rootPID = call.pid
		}
		if call.method == droidMethodLoadSession && call.sessionID == droidChildConnectionSessionID {
			childPID = call.pid
		}
	}
	require.NotEmpty(t, rootPID, "the root initialized its own native session")
	for _, call := range calls {
		if call.method != droidMethodAddUserMessage {
			continue
		}
		assert.Equal(t, childPID, call.pid, "a child-tab send never enters the root process")
		assert.NotEqual(t, rootPID, call.pid, "the root stream cannot select a child with params.sessionId")
		assert.Equal(t, droidChildConnectionSessionID, call.sessionID)
	}
	require.NotEmpty(t, childPID, "a distinct process loaded the announced child session")
	assert.NotEqual(t, rootPID, childPID)
}

func TestChildSendSucceedsOnForkedRootAgent(t *testing.T) {
	fixture := newDroidProcessFixture(t, "")
	a, logPath := fixture.root, fixture.logPath
	// Simulate a root agent launched via fork so childConnection inherits Fork* options.
	a.launchOpts.Session = agent.ForkSession("source-session-id", filepath.Join(fixture.home, "source-work"))

	require.NoError(t, agenttest.SendChildInput(a, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"}))
	require.Eventually(t, func() bool {
		for _, call := range readDroidProcessCalls(logPath) {
			if call.method == droidMethodAddUserMessage {
				return true
			}
		}
		return false
	}, 30*time.Second, 10*time.Millisecond, "a native add_user_message request must reach the child process")
	calls := readDroidProcessCalls(logPath)
	assert.Zero(t, countDroidProcessCalls(calls, droidMethodForkSession), "a bound child must load its session, not fork")
	assert.Equal(t, 1, countDroidProcessCalls(calls, droidMethodLoadSession))
	assert.Equal(t, 1, countDroidProcessCalls(calls, droidMethodAddUserMessage))
}

func TestChildProcessSinkWritesDirectlyWithoutArchiveDrop(t *testing.T) {
	t.Parallel()
	recorder := &agenttest.Sink{}
	root := agent.NewModelProgressResetSink(agent.NewProviderServices(recorder))
	childSink := agent.NewModelProgressResetSink(root.ChildSink("child-1"))
	childSink.UpdateSessionID(steerChildID)
	childSink.OpenSpan("proc-tool", "")
	childSink.SetSpanType("proc-tool", "Execute")
	childSink.ReportProgress(agent.OutputTailProgress("proc-tool", "running", false))
	require.NoError(t, childSink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assembled","text":"direct"}`), IdempotencyKey: droidMessageWriteKey("m1", 0)}, agent.SpanInfo{}))
	require.NoError(t, childSink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"agent_turn_completed"}`)}, agent.SpanInfo{}))
	_, err := childSink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"type":"settings_updated"}`)})
	require.NoError(t, err)
	assert.Len(t, recorder.Child("child-1").Messages(), 2, "the bound child writes its own chat rows and turn end directly")
	assert.Len(t, recorder.Child("child-1").PersistedNotifications(), 1, "status notifications still reach the child")
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "proc-tool"}}, recorder.Child("child-1").OpenSpans(), "bound child spans reach the child sink directly")
	assert.Empty(t, recorder.Child("child-1").ClosedSpans(), "non-interrupted turn end keeps open bound child spans alive")
	assert.Equal(t, steerChildID, recorder.Child("child-1").LastSessionID())
}

func countDroidProcessCalls(calls []droidProcessCall, method string) int {
	count := 0
	for _, call := range calls {
		if call.method == method {
			count++
		}
	}
	return count
}

func TestConcurrentChildSendsLoadOneProcess(t *testing.T) {
	fixture := newDroidProcessFixture(t, "")
	const senders = 8
	start := make(chan struct{})
	results := make(chan error, senders)
	var group sync.WaitGroup
	for range senders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- agenttest.SendChildInput(fixture.root, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"})
		}()
	}
	close(start)
	group.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, agent.ErrAgentBusy)
		}
	}
	assert.Equal(t, 1, accepted, "the child accepts one turn while the others stay queued")
	require.Eventually(t, func() bool {
		return countDroidProcessCalls(readDroidProcessCalls(fixture.logPath), droidMethodAddUserMessage) == 1
	}, 10*time.Second, 10*time.Millisecond)
	calls := readDroidProcessCalls(fixture.logPath)
	assert.Equal(t, 1, countDroidProcessCalls(calls, droidMethodLoadSession), "concurrent sends share one child process")
	assert.Equal(t, 1, countDroidProcessCalls(calls, droidMethodAddUserMessage))
}

func TestChildLoadFailureReleasesTheConnectionSlot(t *testing.T) {
	fixture := newDroidProcessFixture(t, "", "LEAPMUX_DROID_CHILD_LOAD_ERROR=1")
	for range 2 {
		err := agenttest.SendChildInput(fixture.root, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"})
		require.ErrorContains(t, err, "load denied")
	}
	calls := readDroidProcessCalls(fixture.logPath)
	assert.Equal(t, 2, countDroidProcessCalls(calls, droidMethodLoadSession), "a failed child load does not keep a dead slot")
	assert.Equal(t, 0, countDroidProcessCalls(calls, droidMethodAddUserMessage))
	row, ok := fixture.sink.BackgroundTask(droidChildConnectionSessionID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "a failed send does not revive a finished child")
}

func TestChildProcessExitClosesTheRunningRegistryRow(t *testing.T) {
	fixture := newDroidProcessFixture(t, "", "LEAPMUX_DROID_CHILD_EXIT_ON_ADD=1")
	require.NoError(t, agenttest.SendChildInput(fixture.root, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"}))
	require.Eventually(t, func() bool {
		row, ok := fixture.sink.BackgroundTask(droidChildConnectionSessionID)
		return ok && row.Status == bgtask.StatusFailed
	}, 10*time.Second, 10*time.Millisecond, "a child process that exits without an outcome cannot leave a running row")
}

type heldDroidReviveSink struct {
	agent.ProviderServices
	entered chan struct{}
	release chan struct{}
}

func (s heldDroidReviveSink) ReviveBackgroundTask(rowKey string) error {
	close(s.entered)
	<-s.release
	return s.ProviderServices.ReviveBackgroundTask(rowKey)
}

func TestChildExitBeforeReviveCannotLeaveARunningRow(t *testing.T) {
	fixture := newDroidProcessFixture(t, "", "LEAPMUX_DROID_CHILD_EXIT_AFTER_RELEASE=1")
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.root.sink = agent.DecorateProviderServices(fixture.root.sink, func(direct agent.ProviderServices) agent.ProviderServices {
		return heldDroidReviveSink{ProviderServices: direct, entered: entered, release: release}
	})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFn()
	result := make(chan error, 1)
	go func() {
		result <- agenttest.SendChildInput(fixture.root, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"})
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the child send did not reach registry revival")
	}
	fixture.root.childConnMu.Lock()
	connection := fixture.root.childConns[droidChildConnectionSessionID]
	fixture.root.childConnMu.Unlock()
	require.NotNil(t, connection)
	require.NoError(t, connection.agent.request("droid.test_release_exit", struct{}{}))
	select {
	case <-connection.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the child process exit did not finish before registry revival")
	}
	releaseFn()
	require.NoError(t, <-result)
	row, ok := fixture.sink.BackgroundTask(droidChildConnectionSessionID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status, "an exited child cannot be revived after its watcher finished")
}

func TestStoppingRootRemovesChildRuntimeSettings(t *testing.T) {
	fixture := newDroidProcessFixture(t, "custom:Droid-0")
	require.NoError(t, agenttest.SendChildInput(fixture.root, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"}))
	pattern := filepath.Join(fixture.home, ".factory", "leapmux-runtime-settings-*.json")
	files, err := filepath.Glob(pattern)
	require.NoError(t, err)
	require.Len(t, files, 2, "the root and child own separate runtime settings files")
	fixture.root.Stop()
	files, err = filepath.Glob(pattern)
	require.NoError(t, err)
	assert.Empty(t, files, "Stop waits for both processes before removing their settings")
	assert.ErrorIs(t, agenttest.SendChildInput(fixture.root, droidChildConnectionSessionID, agent.ChildInput{Content: "later"}), errAgentStopped)
}

func TestChildBoundProcessOnTurnEndClosesRootBackgroundTask(t *testing.T) {
	cases := []struct {
		name       string
		triggerRPC string
		wantStatus bgtask.Status
	}{
		{name: "agent_turn_completed closes row as succeeded", triggerRPC: "droid.test_emit_turn_completed", wantStatus: bgtask.StatusSucceeded},
		{name: "error notification closes row as failed", triggerRPC: "droid.test_emit_error", wantStatus: bgtask.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newDroidProcessFixture(t, "")
			result, err := fixture.root.SendChildInput(droidChildConnectionSessionID, agent.ChildInput{Content: "run bound turn"})
			require.NoError(t, err)

			row, ok := fixture.sink.BackgroundTask(droidChildConnectionSessionID)
			require.True(t, ok)
			require.Equal(t, bgtask.StatusRunning, row.Status)

			fixture.root.childConnMu.Lock()
			connection := fixture.root.childConns[droidChildConnectionSessionID]
			fixture.root.childConnMu.Unlock()
			require.NotNil(t, connection)
			require.NotNil(t, connection.agent)

			require.NoError(t, connection.agent.request(tc.triggerRPC, map[string]any{
				"sessionId": droidChildConnectionSessionID,
				"turnId":    result.NativeReceipt.TurnID,
			}))

			require.Eventually(t, func() bool {
				item, exists := fixture.sink.BackgroundTask(droidChildConnectionSessionID)
				return exists && item.Status == tc.wantStatus
			}, 10*time.Second, 10*time.Millisecond, "bound child onTurnEnd must close the root background task row")
			assert.False(t, connection.agent.PublishTurnActive().Active)
		})
	}
}

func TestStopRacingInFlightChildStartProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	logPath := filepath.Join(home, "rpc-calls.log")
	binary, err := os.Executable()
	require.NoError(t, err)

	processDone := make(chan struct{})
	root, sink, _ := newSteerAgentWithProcessDone(t, processDone)
	root.workingDir = work
	root.homeDir = home
	root.launchOpts = agent.Options{
		AgentID:        "root-agent",
		WorkingDir:     work,
		HomeDir:        home,
		Shell:          terminal.ResolveDefaultShell(),
		StartupTimeout: 10 * time.Second,
	}
	root.launch = droidLaunch{
		spec: launch.Spec{
			Program:    binary,
			PrefixArgs: []string{"-test.run=^TestDroidChildProcessHelper$", "--"},
			Env: []string{
				droidChildProcessTestEnv + "=1",
				"LEAPMUX_DROID_CHILD_PROCESS_LOG=" + logPath,
				"LEAPMUX_DROID_CHILD_LOAD_GATE_ADDR=" + ln.Addr().String(),
			},
		},
		shutdownGrace: Registration().ShutdownGrace,
	}
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole:     bgtask.RoleChildTask,
		SpawnSpanID:      "droid-tool-task-1",
		ProviderChildKey: droidChildConnectionSessionID,
		Title:            "Inspect the note",
	})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		Role: bgtask.RoleChildTask, RowKey: droidChildConnectionSessionID, Kind: bgtask.KindSubagent,
		ChildAgentID: childID, ParentAgentID: root.AgentID(), Title: "Inspect the note", Status: bgtask.StatusSucceeded,
	}))

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- agenttest.SendChildInput(root, droidChildConnectionSessionID, agent.ChildInput{Content: "follow up"})
	}()

	ctx := testutil.DeadlineContext(t)
	var gateConn net.Conn
	select {
	case gateConn = <-accepted:
	case <-ctx.Done():
		t.Fatal("child helper did not reach load_session gate")
	}
	var closeOnce sync.Once
	releaseGate := func() { closeOnce.Do(func() { _ = gateConn.Close() }) }
	defer releaseGate()

	root.childConnMu.Lock()
	connection := root.childConns[droidChildConnectionSessionID]
	root.childConnMu.Unlock()
	require.NotNil(t, connection)

	// Close processDone without canceling root.Context() so Stop reaches
	// stopChildConnections (setting childClosing=true) while the child handshake
	// can still complete and return a live *Agent to exercise the post-start
	// childClosing cleanup path.
	close(processDone)
	stopDone := make(chan struct{})
	go func() {
		root.Stop()
		close(stopDone)
	}()

	testutil.RequireEventually(t, func() bool {
		root.childConnMu.Lock()
		closing := root.childClosing
		root.childConnMu.Unlock()
		return closing
	}, "Stop must mark childClosing while load_session is held")

	releaseGate()

	select {
	case err := <-sendErr:
		require.ErrorIs(t, err, errAgentStopped)
	case <-ctx.Done():
		t.Fatal("SendChildInput did not return after load_session gate released")
	}
	select {
	case <-stopDone:
	case <-ctx.Done():
		t.Fatal("Stop did not finish after load_session gate released")
	}

	root.childConnMu.Lock()
	_, exists := root.childConns[droidChildConnectionSessionID]
	root.childConnMu.Unlock()
	assert.False(t, exists, "failed in-flight child slot must be removed from childConns")

	select {
	case <-connection.ready:
	default:
		t.Fatal("connection.ready must be closed")
	}
	select {
	case <-connection.done:
	default:
		t.Fatal("connection.done must be closed")
	}
	require.NotNil(t, connection.agent)
	assert.True(t, connection.agent.IsStopped(), "child agent started during Stop must be stopped")
	select {
	case <-connection.agent.ProcessDone():
	case <-ctx.Done():
		t.Fatal("child process did not exit after Stop")
	}
}
