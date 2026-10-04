package droid

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
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
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "droid-tool-task-1", ProviderChildKey: droidChildConnectionSessionID, Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: droidChildConnectionSessionID, Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusCompleted,
	}))
	return droidProcessFixture{root: a, sink: sink, home: home, logPath: logPath}
}

func TestChildSendLoadsItsOwnStreamSession(t *testing.T) {
	fixture := newDroidProcessFixture(t, "")
	a, logPath := fixture.root, fixture.logPath

	require.NoError(t, a.SendChildInput(droidChildConnectionSessionID, "follow up", nil))
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

func TestChildProcessSinkLeavesChatRowsToTheArchive(t *testing.T) {
	t.Parallel()
	recorder := &agenttest.Sink{}
	sink := droidArchiveOnlySink{ProviderServices: agent.NewProviderServices(recorder)}
	sink.UpdateSessionID(steerChildID)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assembled","text":"duplicate"}`)}, agent.SpanInfo{}))
	require.NoError(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"agent_turn_completed"}`)}, agent.SpanInfo{}))
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, []byte(`{"type":"settings_updated"}`))
	require.NoError(t, err)
	assert.Empty(t, recorder.Messages(), "the archive remains the only chat writer")
	assert.Len(t, recorder.PersistedNotifications(), 1, "status notifications still reach the child")
	assert.Equal(t, steerChildID, recorder.LastSessionID())
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
			results <- fixture.root.SendChildInput(droidChildConnectionSessionID, "follow up", nil)
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
		err := fixture.root.SendChildInput(droidChildConnectionSessionID, "follow up", nil)
		require.ErrorContains(t, err, "load denied")
	}
	calls := readDroidProcessCalls(fixture.logPath)
	assert.Equal(t, 2, countDroidProcessCalls(calls, droidMethodLoadSession), "a failed child load does not keep a dead slot")
	assert.Equal(t, 0, countDroidProcessCalls(calls, droidMethodAddUserMessage))
	row, ok := fixture.sink.BackgroundTask(droidChildConnectionSessionID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, row.Status, "a failed send does not revive a finished child")
}

func TestChildProcessExitClosesTheRunningRegistryRow(t *testing.T) {
	fixture := newDroidProcessFixture(t, "", "LEAPMUX_DROID_CHILD_EXIT_ON_ADD=1")
	require.NoError(t, fixture.root.SendChildInput(droidChildConnectionSessionID, "follow up", nil))
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
	held := heldDroidReviveSink{
		ProviderServices: fixture.root.sink,
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	fixture.root.sink = held
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	defer release()
	result := make(chan error, 1)
	go func() { result <- fixture.root.SendChildInput(droidChildConnectionSessionID, "follow up", nil) }()
	select {
	case <-held.entered:
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
	release()
	require.NoError(t, <-result)
	row, ok := fixture.sink.BackgroundTask(droidChildConnectionSessionID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusFailed, row.Status, "an exited child cannot be revived after its watcher finished")
}

func TestStoppingRootRemovesChildRuntimeSettings(t *testing.T) {
	fixture := newDroidProcessFixture(t, "custom:Droid-0")
	require.NoError(t, fixture.root.SendChildInput(droidChildConnectionSessionID, "follow up", nil))
	pattern := filepath.Join(fixture.home, ".factory", "leapmux-runtime-settings-*.json")
	files, err := filepath.Glob(pattern)
	require.NoError(t, err)
	require.Len(t, files, 2, "the root and child own separate runtime settings files")
	fixture.root.Stop()
	files, err = filepath.Glob(pattern)
	require.NoError(t, err)
	assert.Empty(t, files, "Stop waits for both processes before removing their settings")
	assert.ErrorIs(t, fixture.root.SendChildInput(droidChildConnectionSessionID, "later", nil), errAgentStopped)
}
