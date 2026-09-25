//go:build unix

package cline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The launch tests put a fake `cline` on PATH that runs this test binary as
// TestHelperProcessCline. The helper is the daemon: it serves the fake hub on
// the port the worker chose, publishes its discovery record where the worker
// said, and exits on the authenticated shutdown.
//
// InstallFakeCLI sets PATH with t.Setenv, which the testing package refuses in a
// parallel test, so these tests run serially.

const (
	// clineHelperScenarioEnv selects a fault of the fake daemon.
	clineHelperScenarioEnv = "LEAPMUX_TEST_CLINE_SCENARIO"
	// clineHelperDumpEnv is where the fake daemon writes its arguments and the
	// variables the worker set for it.
	clineHelperDumpEnv = "LEAPMUX_TEST_CLINE_DUMP"
)

// fakeDaemonDump is what the fake daemon saw, and its own process id.
type fakeDaemonDump struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	PID  int               `json:"pid"`
}

// installFakeCline installs the fake and returns the file the daemon writes
// what it saw into.
func installFakeCline(t *testing.T, env ...string) string {
	t.Helper()
	dump := filepath.Join(t.TempDir(), "dump.json")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "cline", HelperRun: "TestHelperProcessCline", WantEnv: clineHelperEnv,
		Env: append([]string{clineHelperDumpEnv + "=" + dump}, env...), ForwardArgs: true,
	})
	return dump
}

// writeClineSettings writes providers.json into a data directory of the test
// and points CLINE_DATA_DIR at it.
func writeClineSettings(t *testing.T, content string) {
	t.Helper()
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, settingsDirName), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, settingsDirName, providersFileName), []byte(content), 0o600))
	t.Setenv(envClineDataDir, dataDir)
}

func startClineForTest(t *testing.T, opts agent.Options) (*Agent, *agenttest.ControlSink, string, error) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	if opts.AgentID == "" {
		opts.AgentID = "cline-launch"
	}
	if opts.WorkingDir == "" {
		opts.WorkingDir = t.TempDir()
	}
	opts.Shell = testutil.TestShell()
	opts.APITimeout = 30 * time.Second
	opts.StartupTimeout = 60 * time.Second
	if opts.AgentDirs == nil {
		opts.AgentDirs = agentdirtest.NewDirs(t, []agentdir.Spec{agentDirSpec()})
	}
	// dir records the path of the directory that the start creates, so a test
	// can check it after a start that failed.
	var dir string
	started, err := start(context.Background(), opts, agent.NewProviderServices(sink), startDeps{
		getenv: os.Getenv,
		clock:  quartz.NewReal(),
		newDir: func(ctx context.Context, dirs *agentdir.Dirs) (*agentdir.Dir, error) {
			created, err := newClineAgentDir(ctx, dirs)
			if err == nil {
				dir = created.Path()
			}
			return created, err
		},
	})
	if err != nil {
		return nil, sink, dir, err
	}
	a := started.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	return a, sink, dir, nil
}

func readDump(t *testing.T, path string) fakeDaemonDump {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var dump fakeDaemonDump
	require.NoError(t, json.Unmarshal(data, &dump))
	return dump
}

func TestClineStartRunsAPrivateHub(t *testing.T) {
	dump := installFakeCline(t)
	writeClineSettings(t, `{"version":1,"lastUsedProvider":"anthropic","providers":{"anthropic":{"settings":{"provider":"anthropic","model":"claude-opus-5","apiKey":"secret"},"updatedAt":"2026-09-24T13:46:05.991Z","tokenSource":"manual"}}}`)
	a, sink, dir, err := startClineForTest(t, agent.Options{})
	require.NoError(t, err)

	seen := readDump(t, dump)
	port := seen.Env[envHubPort]
	require.NotEmpty(t, port)
	assert.Equal(t, []string{"--cwd", a.opts.WorkingDir, "--host", daemonListen.host, "--port", port, "--pathname", hubPathname, "--no-connectors"}, seen.Args)
	assert.Equal(t, "1", seen.Env[envRunAsHubDaemon])
	assert.Equal(t, filepath.Join(dir, discoveryFileName), seen.Env[envHubDiscoveryPath], "the record and its lock are private")
	assert.Equal(t, filepath.Join(dir, tasksDBFileName), seen.Env[envTasksDBPath], "the agenda database is private")
	assert.Equal(t, sessionBackendLocal, seen.Env[envSessionBackendMode])
	assert.Equal(t, "1", seen.Env[envNoAutoUpdate])
	// The daemon publishes its record in the agent's directory alone. Cline's
	// own hubs publish theirs under `<data dir>/locks/hub`, and a daemon that
	// wrote there would be one that the user's Cline finds and attaches to.
	record, ok := readDiscovery(filepath.Join(dir, discoveryFileName))
	require.True(t, ok, "the daemon's record lies in the agent's directory")
	assert.Equal(t, port, strconv.Itoa(record.Port))
	assert.Equal(t, seen.PID, record.PID)
	assert.NoDirExists(t, filepath.Join(os.Getenv(envClineDataDir), "locks"), "the user's discovery records stay untouched")
	assert.NoDirExists(t, filepath.Join(isolatedHome, ".cline"), "nothing reaches the default Cline directory")

	assert.NotEmpty(t, sink.LastSessionID())
	assert.Equal(t, []string{sink.LastSessionID()}, sink.StatusActives())
	assert.Equal(t, "claude-opus-5", sink.LastSettingsRefresh().Model, "the user's Cline settings choose the model")
	assert.Equal(t, "anthropic", a.selection.Provider)

	require.NoError(t, a.SendInput("Hello.", nil), "the hub takes a prompt")

	pid := a.record.PID
	a.Stop()
	select {
	case <-a.ProcessDone():
	case <-time.After(30 * time.Second):
		t.Fatal("the daemon did not exit on stop")
	}
	assert.NoDirExists(t, dir, "the stop removes the agent's directory")
	waitFor(t, func() bool { return !providerkit.ProcessRuns(pid) }, "the daemon's own process ends")
}

// A daemon that opens a connection with no token for a local Origin lets any
// local process drive the agent, so the start stops it and refuses.
func TestClineStartRefusesAHubThatTrustsALocalOrigin(t *testing.T) {
	dump := installFakeCline(t, clineHelperScenarioEnv+"=trusts-local-origin")
	_, _, dir, err := startClineForTest(t, agent.Options{})
	require.ErrorIs(t, err, errHubTrustsLocalOrigin)
	assert.Contains(t, err.Error(), "without its token")
	pid := readDump(t, dump).PID
	require.Positive(t, pid)
	waitFor(t, func() bool { return !providerkit.ProcessRuns(pid) }, "the refused daemon ends")
	assert.NoDirExists(t, dir)
}

func TestClineStartRefusesAHubItCannotDrive(t *testing.T) {
	installFakeCline(t, clineHelperScenarioEnv+"=v2")
	_, _, _, err := startClineForTest(t, agent.Options{})
	require.ErrorIs(t, err, errProtocolMismatch)
	assert.Contains(t, err.Error(), "Cline 3.0.64")
}

func TestClineStartReportsAHubThatExitsBeforeItIsReady(t *testing.T) {
	installFakeCline(t, clineHelperScenarioEnv+"=exit-early")
	_, _, dir, err := startClineForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot start the hub", "the error carries the daemon's stderr")
	assert.Contains(t, err.Error(), "Cline 3.0.64")
	assert.NoDirExists(t, dir)
}

func TestClineStartTriesAnotherPortWhenOneIsTaken(t *testing.T) {
	dump := installFakeCline(t, clineHelperScenarioEnv+"=in-use-once")
	_, _, _, err := startClineForTest(t, agent.Options{})
	require.NoError(t, err)
	assert.NotEmpty(t, readDump(t, dump).Env[envHubPort])
}

// A resume that fails states its own reason, which the service shows as it is,
// and the start leaves no daemon and no directory.
func TestClineStartReportsAFailedResume(t *testing.T) {
	dump := installFakeCline(t)
	_, _, dir, err := startClineForTest(t, agent.Options{ResumeSessionID: "missing_1"})
	require.Error(t, err)
	assert.Equal(t, "the Cline session missing_1 is not in Cline's session store", err.Error())
	assertStartLeftNothing(t, dump, dir)
}

// assertStartLeftNothing checks that a start that failed after its daemon
// started ended that daemon and removed the agent's directory.
func assertStartLeftNothing(t *testing.T, dump, dir string) {
	t.Helper()
	pid := readDump(t, dump).PID
	require.Positive(t, pid)
	waitFor(t, func() bool { return !providerkit.ProcessRuns(pid) }, "the daemon of the failed start ends")
	require.NotEmpty(t, dir, "the start created its directory")
	assert.NoDirExists(t, dir, "the failed start removes the agent's directory")
}

// A daemon that refuses the client cannot run the agent.
func TestClineStartReportsAHubThatRefusesTheClient(t *testing.T) {
	dump := installFakeCline(t, clineHelperScenarioEnv+"=refuse-register")
	_, _, dir, err := startClineForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "register with the Cline hub")
	assertStartLeftNothing(t, dump, dir)
}

// A new session that the daemon cannot create fails the start with the phase
// that failed.
func TestClineStartReportsASessionThatCannotOpen(t *testing.T) {
	dump := installFakeCline(t, clineHelperScenarioEnv+"=refuse-session")
	_, _, dir, err := startClineForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create the Cline session")
	assert.Contains(t, err.Error(), "no provider is configured")
	assertStartLeftNothing(t, dump, dir)
}

// A record whose address is not a loopback address gets no connection and no
// token: the start stops the daemon's process instead.
func TestClineStartRefusesAHubAddressThatIsNotLoopback(t *testing.T) {
	dump := installFakeCline(t, clineHelperScenarioEnv+"=remote-url")
	_, _, dir, err := startClineForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a loopback address")
	assertStartLeftNothing(t, dump, dir)
}

// A start without the worker's agent directories starts no daemon: the
// directory holds the daemon's record and its token.
func TestClineStartRefusesWithoutAnAgentDirectory(t *testing.T) {
	dump := installFakeCline(t)
	started, err := start(context.Background(), agent.Options{
		AgentID: "cline-no-dir", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: 30 * time.Second, StartupTimeout: 60 * time.Second,
	}, agent.NewProviderServices(&agenttest.ControlSink{}), startDeps{
		getenv: os.Getenv,
		clock:  quartz.NewReal(),
		newDir: newClineAgentDir,
	})
	require.Error(t, err)
	assert.Nil(t, started)
	assert.Contains(t, err.Error(), "prepare the Cline agent directory")
	assert.NoFileExists(t, dump, "no daemon starts")
}

func TestClineStartRefusesUnreadableSettings(t *testing.T) {
	dump := installFakeCline(t)
	writeClineSettings(t, `{"lastUsedProvider":`)
	_, _, _, err := startClineForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
	assert.NoFileExists(t, dump, "no daemon starts on a guess")
}

// TestHelperProcessCline is the fake `cline`. It does nothing in the test
// process itself.
func TestHelperProcessCline(t *testing.T) {
	if os.Getenv(clineHelperEnv) != "1" {
		return
	}
	args := os.Args
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	os.Exit(runFakeDaemon(args))
}

// runFakeDaemon is the body of the fake daemon.
func runFakeDaemon(args []string) int {
	env := map[string]string{}
	for _, key := range []string{envRunAsHubDaemon, envHubDiscoveryPath, envHubPort, envTasksDBPath, envSessionBackendMode, envNoAutoUpdate} {
		env[key] = os.Getenv(key)
	}
	if path := os.Getenv(clineHelperDumpEnv); path != "" {
		data, _ := json.Marshal(fakeDaemonDump{Args: args, Env: env, PID: os.Getpid()})
		_ = os.WriteFile(path, data, 0o600)
	}
	scenario := os.Getenv(clineHelperScenarioEnv)
	switch scenario {
	case "exit-early":
		fmt.Fprintln(os.Stderr, "boom: cannot start the hub")
		return 1
	case "in-use-once":
		marker := os.Getenv(envHubDiscoveryPath) + ".tried"
		if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(marker, nil, 0o600)
			fmt.Fprintln(os.Stderr, "Error: listen EADDRINUSE: address already in use 127.0.0.1")
			return 1
		}
	}
	port := ""
	if i := slices.Index(args, flagPort); i >= 0 && i+1 < len(args) {
		port = args[i+1]
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(daemonListen.address, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		return 1
	}
	hub := newFakeHubState()
	hub.trustsLocalOrigin = scenario == "trusts-local-origin"
	switch scenario {
	case "refuse-register":
		hub.handle(commandClientRegister, func(fakeCommand) fakeReply {
			return fakeReply{Code: "protocol_error", Message: "unsupported client"}
		})
	case "refuse-session":
		hub.handle(commandSessionCreate, func(fakeCommand) fakeReply {
			return fakeReply{Code: "invalid_config", Message: "no provider is configured"}
		})
	}
	var once sync.Once
	done := make(chan struct{})
	hub.onShutdown = func() { once.Do(func() { close(done) }) }
	server := &http.Server{Handler: hub, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()

	record := fakeRecord("http://" + listener.Addr().String())
	record.PID = os.Getpid()
	record.Host, record.Port = daemonListen.host, listener.Addr().(*net.TCPAddr).Port
	switch scenario {
	case "v2":
		record.ProtocolVersion, record.MinClientProtocolVersion, record.MaxClientProtocolVersion = "v2", "v2", "v2"
	case "remote-url":
		record.URL = "ws://192.0.2.1:" + strconv.Itoa(record.Port) + hubPathname
	}
	data, _ := json.Marshal(record)
	path := os.Getenv(envHubDiscoveryPath)
	// The daemon renames a whole file into place, so a reader never sees half.
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write the record:", err)
		return 1
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		fmt.Fprintln(os.Stderr, "publish the record:", err)
		return 1
	}
	select {
	case <-done:
	case <-time.After(5 * time.Minute):
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	hub.closeConnections()
	_ = server.Shutdown(ctx)
	_ = os.Remove(path)
	return 0
}
