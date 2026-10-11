//go:build unix

package codewhale

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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

// awaitReply waits for the assistant row a turn of the fake runtime writes.
func awaitReply(t *testing.T, sink *agenttest.ControlSink) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, message := range sink.Messages() {
			if strings.Contains(string(message.Content), "Hello from the fake runtime.") {
				return true
			}
		}
		return false
	}, 30*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		last, published := sink.LastTurnActive()
		return published && !last
	}, 30*time.Second, 10*time.Millisecond, "the turn end clears the flag")
}

func TestStartOpensAThreadOnAFreshStore(t *testing.T) {
	record := installFakeCodewhale(t, "")
	home := startHome(t)
	a, sink := startAgent(t, startOptions(t, home, optionmap.Map{}))

	require.NotEmpty(t, a.threadID)
	assert.Equal(t, []string{a.threadID}, sink.SessionIDs())
	assert.Equal(t, filepath.Join(home, ".codewhale", codewhaleStoresDirName), filepath.Dir(a.store.dir))

	launches := readLaunchRecords(t, record)
	require.Len(t, launches, 1)
	launch := launches[0]
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
	assert.NotZero(t, parsed.RuntimePID)

	require.NoError(t, a.SendInput("Say hello.", nil))
	awaitReply(t, sink)
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
	resume.ResumeSessionID = threadID
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
	assert.Len(t, readLaunchRecords(t, record), 2, "the first launch lost its port and the second one ran")
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
	opts.ResumeSessionID = "thr_missing"
	_, err := startWith(t.Context(), opts, agent.NewProviderServices(&agenttest.ControlSink{}), quartz.NewReal())
	require.Error(t, err)
	assert.True(t, errors.Is(err, errThreadNotStored), err)

	assert.Contains(t, err.Error(), "send /clear", "a failed resume is fatal and states the way out")

	// A handle the token rule refuses never reaches a path.
	opts.ResumeSessionID = "--dangerously-skip-permissions"
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
	assert.ErrorIs(t, a.SendInput("Hi.", nil), agent.ErrAgentBusy)
}
