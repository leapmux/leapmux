//go:build unix

package codewhale

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// startHome makes a home whose Codewhale state the test owns, so a start never
// writes into the developer's own `~/.codewhale`.
func startHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(envHome, filepath.Join(home, ".codewhale"))
	// An inherited sandbox marker must not reach the runtime.
	t.Setenv("CODEWHALE_SANDBOX", "seatbelt")
	return home
}

func startOptions(t *testing.T, home string, options optionmap.Map) agent.Options {
	t.Helper()
	workingDir := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(workingDir, 0o755))
	return agent.Options{
		AgentID:        "cw-start",
		WorkingDir:     workingDir,
		HomeDir:        home,
		Shell:          testutil.TestShell(),
		APITimeout:     10 * time.Second,
		StartupTimeout: 30 * time.Second,
		Options:        options,
	}
}

// startAgent starts one agent and stops it when the test ends.
func startAgent(t *testing.T, opts agent.Options) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	started, err := startWith(t.Context(), opts, agent.NewProviderServices(sink), quartz.NewReal())
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	return a, sink
}

// awaitReply waits for the fake runtime's reply and the completion of its turn.
func awaitReply(t *testing.T, sink *agenttest.ControlSink) {
	t.Helper()
	require.Eventually(t, func() bool {
		var sawReply, sawTurnEnd bool
		for _, message := range sink.Messages() {
			if strings.Contains(string(message.Content), "Hello from the fake runtime.") {
				sawReply = true
			}
			sawTurnEnd = sawTurnEnd || message.TurnEnd
		}
		return sawReply && sawTurnEnd
	}, 30*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		state, published := sink.LatestTurnState()
		return published && !state.Active
	}, 30*time.Second, 10*time.Millisecond, "the turn end clears the flag")
}

// successfulLaunchRecord reads the last attempt after one successful start.
// The startup stops retrying as soon as a runtime is ready.
func successfulLaunchRecord(t *testing.T, record string) fakeLaunchRecord {
	t.Helper()
	launches := readLaunchRecords(t, record)
	require.NotEmpty(t, launches)
	require.LessOrEqual(t, len(launches), launchAttempts)
	return launches[len(launches)-1]
}

func TestStartOpensAThreadOnAFreshStore(t *testing.T) {
	record := installFakeCodewhale(t, "")
	home := startHome(t)
	a, sink := startAgent(t, startOptions(t, home, optionmap.Map{}))

	require.NotEmpty(t, a.threadID)
	assert.Equal(t, []string{a.threadID}, sink.SessionIDs())
	assert.Equal(t, filepath.Join(home, ".codewhale", codewhaleStoresDirName), filepath.Dir(a.store.dir))

	launch := successfulLaunchRecord(t, record)
	assert.Equal(t, []string{"app-server", "--http", "--host", "127.0.0.1", "--port"}, launch.Args[:5])
	assert.Equal(t, a.store.tasksDir(), launch.TasksDir)
	assert.Equal(t, a.store.runtimeDir(), launch.RuntimeDir)
	assert.True(t, launch.HasToken, "the token reaches the runtime through its environment")
	assert.Equal(t, "1", launch.WorkerFlag)
	assert.Empty(t, launch.SandboxFlag, "an inherited sandbox marker is scrubbed")
	for _, arg := range launch.Args {
		assert.NotContains(t, arg, "token", "the token never reaches argv")
	}

	// The thread's settings and the runtime's catalog reach the option groups.
	options := agent.CurrentOptions(a.OptionGroups())
	assert.Equal(t, "deepseek-flash", options[agent.OptionIDModel])
	assert.Equal(t, "agent", options["codewhale_mode"])
	assert.Equal(t, "ask", options[agent.OptionIDPermissionMode])
	require.Len(t, a.models, 2)
	assert.True(t, a.models[0].IsDefault, "the model the runtime chose for a thread opened with no model is the default")

	// The owner record lets a later start reclaim an orphan.
	owner, err := os.ReadFile(filepath.Join(a.store.dir, ownerFileName))
	require.NoError(t, err)
	var parsed storeOwner
	require.NoError(t, json.Unmarshal(owner, &parsed))
	assert.Equal(t, int32(os.Getpid()), parsed.WorkerPID)
	assert.Equal(t, int32(launch.PID), parsed.RuntimePID)

	require.NoError(t, agenttest.SendInput(a, "Say hello.", nil))
	awaitReply(t, sink)
}

func TestStartRecordsTheNativeListenerBehindAProfileFunction(t *testing.T) {
	record := installFakeCodewhale(t, "")
	runtimePath, err := exec.LookPath("codewhale")
	require.NoError(t, err)
	shell, err := exec.LookPath("bash")
	require.NoError(t, err)
	profile := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.WriteFile(profile, []byte("codewhale() { "+shellquote.POSIX.Quote(runtimePath)+" \"$@\"; }\n"), 0o600))
	t.Setenv("BASH_ENV", profile)
	opts := startOptions(t, startHome(t), optionmap.Map{})
	opts.Shell = shell
	a, _ := startAgent(t, opts)
	run := successfulLaunchRecord(t, record)
	require.NotEqual(t, a.Cmd().Process.Pid, run.PID, "the shell stays alive around the runtime")

	bytes, err := os.ReadFile(filepath.Join(a.store.dir, ownerFileName))
	require.NoError(t, err)
	var owner storeOwner
	require.NoError(t, json.Unmarshal(bytes, &owner))
	assert.Equal(t, int32(run.PID), owner.RuntimePID, "the persisted identity must name the native listener")
}

func TestRuntimeDoesNotRetryExhaustionAfterABindWarning(t *testing.T) {
	shell, err := exec.LookPath("bash")
	require.NoError(t, err)
	dir := t.TempDir()
	calls := filepath.Join(dir, "profile-calls")
	profile := filepath.Join(dir, "profile")
	require.NoError(t, os.WriteFile(profile, []byte("printf 'attempt\\n' >> "+shellquote.POSIX.Quote(calls)+
		"\nprintf 'Failed to bind during profile setup\\n' >&2\n"), 0o600))
	t.Setenv("BASH_ENV", profile)
	t.Setenv("PATH", dir)
	opts := agent.Options{AgentID: "cw-bind-warning", Shell: shell, WorkingDir: dir, StartupTimeout: 30 * time.Second}
	started, err := launchRuntime(t.Context(), agent.NewProviderServices(&agenttest.ControlSink{}), runtimeLaunch{
		opts: opts, spec: launch.ShellCommands("leapmux-codewhale-missing"), store: codewhaleStore{dir: dir},
		token: "test-token", clock: quartz.NewReal(),
	})
	require.Nil(t, started)
	require.ErrorIs(t, err, launch.ErrNoCandidate)
	assert.ErrorContains(t, err, "leapmux-codewhale-missing")
	assert.ErrorContains(t, err, "Failed to bind during profile setup")
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 127, exit.ExitCode())
	history, err := os.ReadFile(calls)
	require.NoError(t, err)
	assert.Equal(t, "attempt\n", string(history))
}

func TestStartRestoresOutputStyleOnTheSameThread(t *testing.T) {
	record := installFakeCodewhale(t, "bind-once")
	home := startHome(t)
	t.Setenv("CODEWHALE_VERBOSITY", "concise")
	var threadID, storeDir string
	previousAttempts := 0
	starts := []struct {
		style       string
		verbosity   string
		minAttempts int
	}{
		{style: "concise", verbosity: "concise", minAttempts: 2},
		{style: "normal", verbosity: "normal", minAttempts: 1},
		{style: "auto", verbosity: "concise", minAttempts: 1},
	}
	for _, start := range starts {
		style := start.style
		options := optionmap.Map{OptionOutputStyle: style, agent.OptionIDModel: "deepseek-flash", agent.OptionIDEffort: "high", agent.OptionIDPermissionMode: "ask"}
		if style == "auto" {
			delete(options, OptionOutputStyle)
		}
		originalStyle := options.Get(OptionOutputStyle)
		opts := startOptions(t, home, options)
		opts.Session = agent.ResumeSession(threadID)
		a, sink := startAgent(t, opts)
		launches := readLaunchRecords(t, record)
		require.GreaterOrEqual(t, len(launches)-previousAttempts, start.minAttempts, "launch attempts for %q", style)
		attempts := launches[previousAttempts:]
		require.LessOrEqual(t, len(attempts), launchAttempts)
		for i, attempt := range attempts {
			assert.Equal(t, start.verbosity, attempt.Verbosity, "output style %q on attempt %d", style, i+1)
		}
		previousAttempts = len(launches)
		if threadID == "" {
			threadID, storeDir = a.threadID, a.store.dir
		}
		assert.Equal(t, threadID, a.threadID)
		assert.Equal(t, storeDir, a.store.dir)
		current := a.SettingsSnapshot().SurfacedOptions
		assert.Equal(t, style, current[OptionOutputStyle])
		for key, value := range options {
			assert.Equal(t, value, current[key], key)
		}
		require.NoError(t, agenttest.SendInput(a, "Keep the native conversation.", nil))
		awaitReply(t, sink)
		a.Stop()
		// Stop cancels the launch context even when the fake process exits cleanly.
		require.ErrorIs(t, a.Wait(), context.Canceled)
		assert.Equal(t, originalStyle, options.Get(OptionOutputStyle), "startup leaves the caller's option map unchanged")
	}
}

func TestStartResumesTheStoredThread(t *testing.T) {
	installFakeCodewhale(t, "")
	home := startHome(t)
	sink := &agenttest.ControlSink{}
	opts := startOptions(t, home, optionmap.Map{})
	first, err := startWith(t.Context(), opts, agent.NewProviderServices(sink), quartz.NewReal())
	require.NoError(t, err)
	fresh := first.(*Agent)
	threadID, store := fresh.threadID, fresh.store.dir
	fresh.Stop()
	_ = fresh.Wait()

	resume := startOptions(t, home, optionmap.Map{agent.OptionIDModel: "deepseek-pro", "codewhale_mode": "plan"})
	resume.Session = agent.ResumeSession(threadID)
	a, _ := startAgent(t, resume)
	assert.Equal(t, threadID, a.threadID)
	assert.Equal(t, store, a.store.dir, "a resume runs on the store that holds its thread")
	options := agent.CurrentOptions(a.OptionGroups())
	assert.Equal(t, "deepseek-pro", options[agent.OptionIDModel], "the launch's settings are applied to the resumed thread")
	assert.Equal(t, "plan", options["codewhale_mode"])
	assert.False(t, a.models[0].IsDefault, "a resumed thread states no default")
}

func TestStartRetriesAPortAnotherProcessTook(t *testing.T) {
	record := installFakeCodewhale(t, "bind-once")
	home := startHome(t)
	a, _ := startAgent(t, startOptions(t, home, optionmap.Map{}))
	assert.NotEmpty(t, a.threadID)
	attempts := readLaunchRecords(t, record)
	assert.GreaterOrEqual(t, len(attempts), 2, "the first launch lost its port and a later one ran")
	assert.LessOrEqual(t, len(attempts), launchAttempts)
}

func TestStartReportsARuntimeThatExits(t *testing.T) {
	installFakeCodewhale(t, "exit")
	home := startHome(t)
	_, err := startWith(t.Context(), startOptions(t, home, optionmap.Map{}), agent.NewProviderServices(&agenttest.ControlSink{}), quartz.NewReal())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refused to start")
	// A fresh store that never held a thread is removed.
	entries, _ := os.ReadDir(filepath.Join(home, ".codewhale", codewhaleStoresDirName))
	assert.Empty(t, entries)
}

func TestStartRefusesAResumeOfAThreadNoStoreHolds(t *testing.T) {
	installFakeCodewhale(t, "")
	home := startHome(t)
	opts := startOptions(t, home, optionmap.Map{})
	opts.Session = agent.ResumeSession("thr_missing")
	_, err := startWith(t.Context(), opts, agent.NewProviderServices(&agenttest.ControlSink{}), quartz.NewReal())
	require.Error(t, err)
	assert.True(t, errors.Is(err, errThreadNotStored), err)

	assert.Contains(t, err.Error(), "send /clear", "a failed resume is fatal and states the way out")

	// A handle the token rule refuses never reaches a path.
	opts.Session = agent.ResumeSession("--dangerously-skip-permissions")
	_, err = startWith(t.Context(), opts, agent.NewProviderServices(&agenttest.ControlSink{}), quartz.NewReal())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid")
}

func TestStartGivesUpWhenEveryPortIsTaken(t *testing.T) {
	record := installFakeCodewhale(t, "bind-always")
	home := startHome(t)
	_, err := startWith(t.Context(), startOptions(t, home, optionmap.Map{}), agent.NewProviderServices(&agenttest.ControlSink{}), quartz.NewReal())
	require.ErrorIs(t, err, errPortTaken)
	assert.Len(t, readLaunchRecords(t, record), launchAttempts, "each attempt ran on a fresh port, and no more ran")
	entries, _ := os.ReadDir(filepath.Join(home, ".codewhale", codewhaleStoresDirName))
	assert.Empty(t, entries, "the start removes the fresh store")
}

// A thread that a dead worker left with a running turn is busy until that turn
// ends: the input queue must wait for it rather than draw a 409.
func TestStartStatesATurnThatTheRuntimeStillRuns(t *testing.T) {
	installFakeCodewhale(t, "busy-turn")
	home := startHome(t)
	a, sink := startAgent(t, startOptions(t, home, optionmap.Map{}))
	assert.Equal(t, fakeRecoveredTurnID, a.turnID)
	last, published := sink.LastTurnActive()
	require.True(t, published)
	assert.True(t, last)
	assert.ErrorIs(t, agenttest.SendInput(a, "Hi.", nil), agent.ErrAgentBusy)
}
