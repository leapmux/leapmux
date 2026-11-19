//go:build unix

package kimi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The launch tests put a fake `kimi` on PATH that re-runs this test binary as
// TestHelperProcessKimi. The helper records every invocation and rejects `--version`.
// `doctor config` validates a synthetic configuration, and `web` starts the
// same fake kap-server the other tests use, on a real loopback port, and prints
// the ready line the real server prints.
//
// InstallFakeCLI sets PATH with t.Setenv, which the testing package refuses in a
// parallel test, so these tests run serially.

const (
	kimiHelperEnv         = "LEAPMUX_TEST_KIMI_HELPER"
	kimiHelperScenarioEnv = "LEAPMUX_TEST_KIMI_SCENARIO"
)

func installFakeKimi(t *testing.T, env ...string) string {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: kimiBinaryName, HelperRun: "TestHelperProcessKimi", WantEnv: kimiHelperEnv,
		Env: append(env, "LEAPMUX_TEST_KIMI_ARGS_FILE="+argsFile), ForwardArgs: true,
	})
	return argsFile
}

func startKimiForTest(t *testing.T, opts agent.Options) (agent.Agent, *agenttest.ControlSink, error) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	if opts.AgentID == "" {
		opts.AgentID = "kimi-launch"
	}
	if opts.WorkingDir == "" {
		opts.WorkingDir = t.TempDir()
	}
	opts.Shell = testutil.TestShell()
	if opts.APITimeout == 0 {
		opts.APITimeout = 30 * time.Second
	}
	if opts.StartupTimeout == 0 {
		opts.StartupTimeout = 60 * time.Second
	}
	started, err := Start(context.Background(), opts, agent.NewProviderServices(sink))
	if err == nil {
		t.Cleanup(func() {
			started.Stop()
			_ = started.Wait()
		})
	}
	return started, sink, err
}

func TestKimiStartLaunchesTheServer(t *testing.T) {
	argsFile := installFakeKimi(t)
	started, sink, err := startKimiForTest(t, agent.Options{})
	require.NoError(t, err)

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, strings.Join(kimiServerArgs, " "), strings.Split(strings.TrimSpace(string(args)), "\n")[0],
		"the server runs with --no-open, a free loopback port and the quiet log level")
	assert.Equal(t, "session_1", sink.LastSessionID())
	assert.Equal(t, []string{"session_1"}, sink.StatusActives())
	assert.Equal(t, "kimi-k2", sink.LastSettingsRefresh().Model)

	require.NoError(t, started.SendInput("Hello.", nil), "the server takes a prompt")
	kimi, ok := started.(*Agent)
	require.True(t, ok)
	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume},
		kimi.SupportedGoalActions(), "GET /meta states the goal feature")

	started.Stop()
	select {
	case <-started.(*Agent).ProcessDone():
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not exit on stop")
	}
}

func TestKimiFreshRuntimeRetainsOptionsAndAttributesSessionWarnings(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=session-warning")
	running, sink, err := startKimiForTest(t, agent.Options{Options: options(agent.OptionIDModel, "kimi-text", kimiOptionSwarmMode, kimiSwarmOn)})
	require.NoError(t, err)
	require.NotNil(t, running)
	assert.Equal(t, "session_2", sink.MessageSessionID())
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1)
	assert.Equal(t, "session_2", rows[0].AgentSessionID)
	assert.Contains(t, string(rows[0].Content), "AGENTS.md total exceeds the recommended size.")
	opts := agent.CurrentOptions(running.OptionGroups())
	assert.Equal(t, "kimi-text", opts[agent.OptionIDModel])
	assert.Equal(t, kimiSwarmOn, opts[kimiOptionSwarmMode])
}

// A session that opens on a model of the Kimi Code account shows the windows
// of the plan before its first turn. A session on a model of another provider
// shows none.
func TestKimiStartReadsThePlanUsage(t *testing.T) {
	for scenario, want := range map[string]string{"account-plan": kimiTestPlanWindows, "": `{}`} {
		installFakeKimi(t, kimiHelperScenarioEnv+"="+scenario)
		started, sink, err := startKimiForTest(t, agent.Options{})
		require.NoError(t, err)
		kimi, ok := started.(*Agent)
		require.True(t, ok)
		waitFor(t, kimi.usageWork.Idle, "the plan usage read finishes")
		assert.JSONEq(t, want, lastKimiRateLimitState(t, sink), "scenario %q", scenario)
		started.Stop()
		_ = started.Wait()
	}
}

// The two tests below count the event stream's goroutines in the whole process.
// They are serial, as every launch test in this file is, so no other stream
// changes the count.

func TestKimiStopEndsTheEventStream(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=unused-http-connection")
	readers, dispatchers := kimiStreamLoops()
	started, _, err := startKimiForTest(t, agent.Options{})
	require.NoError(t, err)
	r, d := kimiStreamLoops()
	require.Equal(t, readers+1, r, "the agent runs one stream reader")
	require.Equal(t, dispatchers+1, d, "the agent runs one stream dispatcher")

	// The stream owns a separate context, so Stop must close it explicitly and
	// Wait must join its goroutines after native exit.
	started.Stop()
	require.NoError(t, started.Wait())
	waitForKimiStreamLoops(t, readers, dispatchers, "a stopped agent keeps no stream goroutine")
}

func TestKimiWaitEndsTheEventStreamOfAServerThatDied(t *testing.T) {
	installFakeKimi(t)
	readers, dispatchers := kimiStreamLoops()
	started, _, err := startKimiForTest(t, agent.Options{})
	require.NoError(t, err)
	kimi, ok := started.(*Agent)
	require.True(t, ok)

	// The server dies, and nobody calls Stop. The manager's exit goroutine calls
	// Wait, and nothing else.
	require.NoError(t, kimi.SignalGroup(syscall.SIGKILL))
	_ = started.Wait()
	waitForKimiStreamLoops(t, readers, dispatchers,
		"the reader stops redialing the dead server's port with the token, and the dispatcher returns")
	assert.False(t, kimi.IsStopped(), "the exit was not a stop")
}

func TestKimiStartResumesAStoredSession(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=stored")
	_, sink, err := startKimiForTest(t, agent.Options{ResumeSessionID: "session_stored"})
	require.NoError(t, err)
	assert.Equal(t, "session_stored", sink.LastSessionID())
	assert.Equal(t, "kimi-text", sink.LastSettingsRefresh().Model, "the stored session keeps its model")
}

func TestKimiStartReportsAFailedResume(t *testing.T) {
	installFakeKimi(t)
	_, _, err := startKimiForTest(t, agent.Options{ResumeSessionID: "session_missing"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session_missing")
}

func TestKimiStartDoesNotProbeTheRelease(t *testing.T) {
	argsFile := installFakeKimi(t, kimiHelperScenarioEnv+"=version-fails")
	started, _, err := startKimiForTest(t, agent.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.NotContains(t, string(args), "--version")
	assert.Contains(t, string(args), "web")
	assert.Contains(t, string(args), "doctor config")
}

func TestKimiHelperRecordsARefusedVersionProbe(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("LEAPMUX_TEST_KIMI_ARGS_FILE", argsFile)
	assert.Equal(t, 91, runFakeKimi([]string{"--version"}))
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, "--version\n", string(args))
}

func TestKimiStartDoesNotRequireServerReleaseMetadata(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=legacy-server")
	started, _, err := startKimiForTest(t, agent.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { started.Stop(); _ = started.Wait() })
}

func TestKimiStartReportsAServerThatExitsBeforeItIsReady(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=exit-early")
	_, _, err := startKimiForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server start")
}

func TestKimiStartRefusesAServerWithNoModel(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=no-models")
	_, _, err := startKimiForTest(t, agent.Options{})
	// The startup error keeps the reason's text, not its chain.
	require.ErrorContains(t, err, errKimiNoModel.Error())
}

// TestHelperProcessKimi is the fake `kimi`. It does nothing in the test process
// itself.
func TestHelperProcessKimi(t *testing.T) {
	if os.Getenv(kimiHelperEnv) != "1" {
		return
	}
	args := os.Args
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	os.Exit(runFakeKimi(args))
}

func runFakeKimi(args []string) int {
	scenario := os.Getenv(kimiHelperScenarioEnv)
	if argsFile := os.Getenv("LEAPMUX_TEST_KIMI_ARGS_FILE"); argsFile != "" {
		file, err := os.OpenFile(argsFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_, writeErr := fmt.Fprintln(file, strings.Join(args, " "))
		if err := errors.Join(writeErr, file.Close()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if slices.Contains(args, "--version") {
		fmt.Fprintln(os.Stderr, "version probes are forbidden")
		return 91
	}
	if slices.Equal(args, []string{"doctor", "config"}) {
		fmt.Print("Kimi doctor\n\nSKIP config.toml  synthetic-config.toml\n\nAll checked config files are valid.\n")
		return 0
	}
	if len(args) == 0 || args[0] != "web" {
		fmt.Fprintln(os.Stderr, "unknown command")
		return 2
	}
	if scenario == "exit-early" {
		fmt.Fprintln(os.Stderr, "Error: unknown option '--no-open'")
		return 3
	}

	fake := newFakeKapState()
	switch scenario {
	case "session-warning":
		fake.nextSession = 1
		fake.reply("GET "+kimiSessionPath("session_2", "/warnings"), fakeKapReply{Data: map[string]any{
			"warnings": []map[string]string{{"code": "agents-md-oversized", "message": "AGENTS.md total exceeds the recommended size.", "severity": "warning"}},
		}})
	case "no-models":
		fake.models = nil
		fake.config = map[string]any{}
	case "stored":
		fake.sessions["session_stored"] = &fakeKapSession{Model: "kimi-text", Permission: "manual"}
	case "legacy-server":
		fake.version = ""
	case "account-plan":
		fake.models = append(fake.models, kimiTestManagedModel())
		fake.config["default_model"] = kimiTestManagedModelID
		fake.usage = kimiTestPlanUsage()
	}
	shutdown := make(chan struct{})
	fake.onShutdown = func() { close(shutdown) }

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	connections := &fakeKimiHTTPConnections{waiting: make(map[net.Conn]struct{})}
	var accepted chan struct{}
	if scenario == "unused-http-connection" {
		accepted = make(chan struct{}, 1)
	}
	server := &http.Server{
		Handler: fake, ReadHeaderTimeout: 10 * time.Second,
		ConnState: func(conn net.Conn, state http.ConnState) {
			connections.observe(conn, state)
			if state == http.StateNew {
				select {
				case accepted <- struct{}{}:
				default:
				}
			}
		},
	}
	go func() { _ = server.Serve(listener) }()
	if accepted != nil {
		conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 30*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = conn.Close() }()
		select {
		case <-accepted:
		case <-time.After(30 * time.Second):
			fmt.Fprintln(os.Stderr, "the fixture server did not accept its unused connection")
			return 1
		}
	}

	// A log line first, as pino prints at the warn level, then the ready line.
	fmt.Printf("{\"level\":40,\"msg\":\"a warning\"}\n")
	fmt.Printf("Kimi server: http://%s/#token=%s\n", listener.Addr().String(), fakeKapToken)

	stdinClosed := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString(0)
		close(stdinClosed)
	}()
	select {
	case <-shutdown:
	case <-stdinClosed:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fake.closeConnections()
	connections.closeBeforeHeaders()
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

type fakeKimiHTTPConnections struct {
	mu      sync.Mutex
	waiting map[net.Conn]struct{}
	closing bool
}

func (connections *fakeKimiHTTPConnections) observe(conn net.Conn, state http.ConnState) {
	connections.mu.Lock()
	if state != http.StateNew {
		delete(connections.waiting, conn)
		connections.mu.Unlock()
		return
	}
	if connections.closing {
		connections.mu.Unlock()
		_ = conn.Close()
		return
	}
	connections.waiting[conn] = struct{}{}
	connections.mu.Unlock()
}

// Go's graceful shutdown waits for new connections to send headers or
// age out. Close those sockets so the fixture can finish within the
// Worker's grace period while active requests complete.
func (connections *fakeKimiHTTPConnections) closeBeforeHeaders() {
	connections.mu.Lock()
	connections.closing = true
	waiting := connections.waiting
	connections.waiting = nil
	connections.mu.Unlock()
	for conn := range waiting {
		_ = conn.Close()
	}
}

// The server launch disables automatic updates after the shell profile runs.
func TestLaunchPinsTheUpdaterOffForTheServer(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: kimiBinaryName,
		Start:  Start,
		Pins:   []string{"KIMI_CODE_NO_AUTO_UPDATE=1"},
		Runs:   1,
	})
}
