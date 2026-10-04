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
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
// TestHelperProcessKimi. The helper answers `--version`, and `web` starts the
// same fake kap-server the other tests use, on a real loopback port, and prints
// the ready line the real server prints.
//
// InstallFakeCLI sets PATH with t.Setenv, which the testing package refuses in a
// parallel test, so these tests run serially.

const (
	kimiHelperEnv         = "LEAPMUX_TEST_KIMI_HELPER"
	kimiHelperVersionEnv  = "LEAPMUX_TEST_KIMI_VERSION"
	kimiHelperScenarioEnv = "LEAPMUX_TEST_KIMI_SCENARIO"
	// kimiHelperChildPIDEnv gives the file where the helper writes the PID of a
	// process that it leaves behind.
	kimiHelperChildPIDEnv = "LEAPMUX_TEST_KIMI_CHILD_PID_FILE"
)

func installFakeKimi(t *testing.T, env ...string) string {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: kimiBinaryName, HelperRun: "TestHelperProcessKimi", WantEnv: kimiHelperEnv,
		Env: env, ArgsFile: argsFile, ForwardArgs: true,
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
	assert.Equal(t, strings.Join(kimiServerArgs, " "), strings.TrimSpace(string(args)),
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

// The two tests below count the event stream's goroutines in the whole process.
// They are serial, as every launch test in this file is, so no other stream
// changes the count.

func TestKimiStopEndsTheEventStream(t *testing.T) {
	installFakeKimi(t)
	readers, dispatchers := kimiStreamLoops()
	started, _, err := startKimiForTest(t, agent.Options{})
	require.NoError(t, err)
	r, d := kimiStreamLoops()
	require.Equal(t, readers+1, r, "the agent runs one stream reader")
	require.Equal(t, dispatchers+1, d, "the agent runs one stream dispatcher")

	// The server exits inside the stop's grace period, so the stop never cancels
	// the process context. The stream must end without it.
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
	group, err := syscall.Getpgid(kimi.Cmd().Process.Pid)
	require.NoError(t, err)
	require.NoError(t, syscall.Kill(-group, syscall.SIGKILL))
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

func TestKimiStartRefusesTheLegacyCLI(t *testing.T) {
	argsFile := installFakeKimi(t, kimiHelperVersionEnv+"=kimi, version 1.5.0")
	_, _, err := startKimiForTest(t, agent.Options{})
	require.ErrorIs(t, err, errKimiLegacyCLI)
	assert.Contains(t, err.Error(), "1.5.0")
	args, readErr := os.ReadFile(argsFile)
	require.NoError(t, readErr)
	assert.Equal(t, "--version", strings.TrimSpace(string(args)), "the legacy CLI's own web command never runs")
}

func TestKimiStartReportsAServerThatExitsBeforeItIsReady(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=exit-early")
	_, _, err := startKimiForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Kimi Code 2.0 or later")
}

func TestKimiStartRefusesAServerWithNoModel(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=no-models")
	_, _, err := startKimiForTest(t, agent.Options{})
	// The startup error keeps the reason's text, not its chain.
	require.ErrorContains(t, err, errKimiNoModel.Error())
}

func TestKimiStartReportsAVersionProbeThatFails(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=version-fails")
	_, _, err := startKimiForTest(t, agent.Options{})
	require.ErrorContains(t, err, "run `kimi --version`")
	assert.Contains(t, err.Error(), "kimi: the installation is broken", "the error quotes what the program printed on stderr")
	assert.NotContains(t, err.Error(), "second line", "the error quotes only the first line")
}

func TestKimiStartReportsAVersionProbeThatNeverAnswers(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=version-hangs")
	// The fake never answers, so only the probe's own limit ends the wait. The
	// startup timeout sets that limit.
	_, _, err := startKimiForTest(t, agent.Options{StartupTimeout: 2 * time.Second})
	require.ErrorContains(t, err, "`kimi --version` did not answer within 2s")
}

// A background job that the login shell's profile starts inherits the shell's
// stdout, and it keeps the pipe open after `kimi --version` exits. The probe
// must not wait for that job, which can run for as long as the user's session.
func TestKimiStartIsNotHeldByAProcessTheVersionProbeLeftBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	installFakeKimi(t, kimiHelperScenarioEnv+"=version-leaves-child", kimiHelperChildPIDEnv+"="+pidFile)
	opts := agent.Options{
		AgentID: "kimi-launch", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: 30 * time.Second, StartupTimeout: 60 * time.Second,
	}

	type startResult struct {
		agent agent.Agent
		err   error
	}
	done := make(chan startResult, 1)
	go func() {
		started, err := Start(context.Background(), opts, agent.NewProviderServices(&agenttest.ControlSink{}))
		done <- startResult{agent: started, err: err}
	}()
	settle := func(result startResult) {
		if result.err == nil {
			result.agent.Stop()
			_ = result.agent.Wait()
		}
	}
	var result startResult
	select {
	case result = <-done:
	case <-time.After(45 * time.Second):
		// End the process that holds the probe, so the start returns and its
		// server does not outlive the test.
		_ = syscall.Kill(readKimiHelperChildPID(t, pidFile), syscall.SIGKILL)
		settle(<-done)
		t.Fatal("the start waits for a process that the version probe left behind")
	}
	childPID := readKimiHelperChildPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	require.NoError(t, result.err, "the probe read the version the program printed before it exited")
	t.Cleanup(func() { settle(result) })
	require.NoError(t, syscall.Kill(childPID, 0), "the process that holds the pipe still runs, so the start did not wait for it")
}

// readKimiHelperChildPID reads the PID that the helper wrote.
func readKimiHelperChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	var pid int
	_, err = fmt.Sscan(string(data), &pid)
	require.NoError(t, err)
	require.Positive(t, pid)
	return pid
}

func TestKimiStartReportsAServerThatStatesNoVersion(t *testing.T) {
	installFakeKimi(t, kimiHelperScenarioEnv+"=legacy-server")
	_, _, err := startKimiForTest(t, agent.Options{})
	require.ErrorContains(t, err, errKimiLegacyCLI.Error(), "the server's own version is checked too")
	assert.Contains(t, err.Error(), kimiVersionFromServer)
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
	if slices.Contains(args, "--version") {
		return runFakeKimiVersion(scenario)
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
	case "no-models":
		fake.models = nil
		fake.config = map[string]any{}
	case "stored":
		fake.sessions["session_stored"] = &fakeKapSession{Model: "kimi-text", Permission: "manual"}
	case "legacy-server":
		fake.version = ""
	}
	shutdown := make(chan struct{})
	fake.onShutdown = func() { close(shutdown) }

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	server := &http.Server{Handler: fake, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()

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
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return 1
	}
	return 0
}

// runFakeKimiVersion answers `kimi --version` as the scenario states.
func runFakeKimiVersion(scenario string) int {
	switch scenario {
	case "version-fails":
		fmt.Fprintln(os.Stderr, "kimi: the installation is broken\nsecond line")
		return 1
	case "version-hangs":
		// The fake never answers. The probe's timeout kills it.
		time.Sleep(time.Hour)
		return 0
	case "version-leaves-child":
		// A process that inherits stdout and outlives this one, as a background
		// job of the login shell's profile does.
		child := exec.Command("sleep", "3600")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(os.Getenv(kimiHelperChildPIDEnv), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	version := os.Getenv(kimiHelperVersionEnv)
	if version == "" {
		version = "2.0.2"
	}
	fmt.Println(version)
	return 0
}
