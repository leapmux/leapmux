package dirac

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const diracStartupHelperEnv = "LEAPMUX_DIRAC_STARTUP_HELPER"
const diracStartupLogEnv = "LEAPMUX_DIRAC_STARTUP_LOG"
const diracStartupHoldPromptEnv = "LEAPMUX_DIRAC_STARTUP_HOLD_PROMPT"
const diracStartupSessionID = "dirac-test-session"

// TestMain runs the fake ACP peer before the Go test runner parses CLI flags.
// The provider's real `--acp` flag then reaches this test binary.
func TestMain(m *testing.M) {
	if code, handled := envprobe.Run(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	if os.Getenv(diracStartupHelperEnv) == "1" {
		runDiracStartupPeer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runDiracStartupPeer() {
	log, err := os.OpenFile(os.Getenv(diracStartupLogEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(2)
	}
	if err := json.NewEncoder(log).Encode(map[string]any{
		"method": "test/launch", "args": os.Args[1:], "profile": os.Getenv("DIRAC_DIR"), "cwd": cwd,
	}); err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if _, err := log.Write(append(append([]byte(nil), scanner.Bytes()...), '\n')); err != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 || (request.Method == acp.MethodSessionPrompt && os.Getenv(diracStartupHoldPromptEnv) == "1") {
			continue
		}
		result := `{}`
		switch request.Method {
		case "initialize":
			result = `{"protocolVersion":1,"agentInfo":{"name":"dirac-test","version":"1"},"agentCapabilities":{}}`
		case acp.MethodSessionNew:
			result = fmt.Sprintf(`{"sessionId":%q,"modes":{"currentModeId":"act","availableModes":[{"id":"plan","name":"Plan"},{"id":"act","name":"Act"}]}}`, diracStartupSessionID)
		}
		if _, err := fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", request.ID, result); err != nil {
			os.Exit(2)
		}
		if writer.Flush() != nil {
			os.Exit(2)
		}
	}
	if scanner.Err() != nil {
		os.Exit(2)
	}
	if err := json.NewEncoder(log).Encode(map[string]string{"method": "test/eof"}); err != nil {
		os.Exit(2)
	}
	if log.Close() != nil {
		os.Exit(2)
	}
}

func TestStartAppliesTheRequestedNativePlanMode(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	logPath := filepath.Join(home, "requests.jsonl")
	executable, err := os.Executable()
	require.NoError(t, err)

	previous := diracLocator
	diracLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable, Env: []string{
			diracStartupHelperEnv + "=1",
			diracStartupLogEnv + "=" + logPath,
		}}, launch.Found
	})
	t.Cleanup(func() { diracLocator = previous })

	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{
		AgentID: "dirac-startup-test", WorkingDir: work, HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), StartupTimeout: 10 * time.Second,
		Options: optionmap.Map{agent.OptionIDPermissionMode: "plan"},
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(started.Stop)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var nativeMode string
	var arguments []string
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var request struct {
			Method string   `json:"method"`
			Args   []string `json:"args"`
			Params struct {
				ModeID string `json:"modeId"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &request) == nil {
			switch request.Method {
			case "test/launch":
				arguments = request.Args
			case "session/set_mode":
				nativeMode = request.Params.ModeID
			}
		}
	}
	assert.Equal(t, []string{"--acp"}, arguments, "ACP reads the native subagentsEnabled preference; the CLI flag is ignored")
	assert.Equal(t, "plan", nativeMode, "the native session must enter Plan before its first prompt")
}

func TestDiracLaunchObservationRequiresExactExportedInputs(t *testing.T) {
	t.Parallel()
	valid := envprobe.Snapshot{
		Values:     map[string]string{"DIRAC_DIR": "", "HOME": "", "USERPROFILE": ""},
		WorkingDir: t.TempDir(),
		Paths:      map[string]string{"DIRAC_DIR": "", "HOME": "", "USERPROFILE": ""},
		Present:    map[string]bool{"DIRAC_DIR": false, "HOME": true, "USERPROFILE": false},
	}
	raw, err := json.Marshal(valid)
	require.NoError(t, err)
	snapshot, ok := diracProfileProbe.DecodeObservation(base64.RawURLEncoding.EncodeToString(raw))
	require.True(t, ok)
	assert.Equal(t, valid, snapshot)

	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{name: "missing value", field: "values", value: map[string]string{"HOME": "", "DIRAC_DIR": ""}},
		{name: "unrequested value", field: "values", value: map[string]string{"HOME": "", "DIRAC_DIR": "", "USERPROFILE": "", "EXTRA": ""}},
		{name: "unrequested path", field: "paths", value: map[string]string{"HOME": "", "USERPROFILE": "", "EXTRA": "/tmp"}},
		{name: "unrequested field", field: "other", value: 1},
		{name: "null value", field: "values", value: map[string]any{"HOME": nil, "DIRAC_DIR": "", "USERPROFILE": ""}},
		{name: "null path", field: "paths", value: map[string]any{"HOME": "", "DIRAC_DIR": nil, "USERPROFILE": ""}},
		{name: "missing presence flag", field: "present", value: map[string]bool{"HOME": true, "DIRAC_DIR": true}},
		{name: "null presence flag", field: "present", value: map[string]any{"HOME": true, "DIRAC_DIR": false, "USERPROFILE": nil}},
		{name: "inconsistent presence", field: "values", value: map[string]string{"HOME": "", "DIRAC_DIR": "relative", "USERPROFILE": ""}},
		{name: "empty cwd", field: "workingDir", value: ""},
		{name: "relative cwd", field: "workingDir", value: "relative"},
		{name: "invalid cwd", field: "workingDir", value: "invalid\x00path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := map[string]any{
				"values": valid.Values, "paths": valid.Paths,
				"workingDir": valid.WorkingDir, "present": valid.Present,
			}
			wire[tc.field] = tc.value
			raw, err := json.Marshal(wire)
			require.NoError(t, err)
			_, ok := diracProfileProbe.DecodeObservation(base64.RawURLEncoding.EncodeToString(raw))
			assert.False(t, ok, string(raw))
		})
	}
}

type diracStartupEvent struct {
	Method  string          `json:"method"`
	ID      json.RawMessage `json:"id"`
	Profile string          `json:"profile"`
	CWD     string          `json:"cwd"`
	Params  struct {
		SessionID string `json:"sessionId"`
	} `json:"params"`
}

func diracStartupEvents(t *testing.T, logPath, method string) []diracStartupEvent {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var events []diracStartupEvent
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		var event diracStartupEvent
		if json.Unmarshal(line, &event) == nil && event.Method == method {
			events = append(events, event)
		}
	}
	return events
}

func selectDiracStartupPeer(t *testing.T, logPath string, choose func(string) (launch.Spec, launch.Resolution)) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv(diracStartupHelperEnv, "1")
	t.Setenv(diracStartupLogEnv, logPath)
	t.Setenv(diracStartupHoldPromptEnv, "1")
	previous := diracLocator
	diracLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return choose(executable)
	})
	t.Cleanup(func() { diracLocator = previous })
}

func diracCheckpointShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the launch fixture uses Bash's Unix profile and permission semantics")
	}
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	return path
}

func startDiracTestStop(t *testing.T, started *Agent, count int) <-chan struct{} {
	t.Helper()
	var stopped sync.WaitGroup
	for range count {
		stopped.Go(started.Stop)
	}
	finished := make(chan struct{})
	go func() {
		stopped.Wait()
		close(finished)
	}()
	return finished
}

func awaitDiracTestStop(t *testing.T, finished <-chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the Dirac Stop call never finished")
	}
}

func TestDiracStopConfirmsTheLaunchedProfileBeforeEOF(t *testing.T) {
	shell := diracCheckpointShell(t)
	home := t.TempDir()
	work := t.TempDir()
	workerProfile := filepath.Join(home, "worker-profile")
	logPath := filepath.Join(home, "requests.jsonl")
	profileScript := filepath.Join(home, "profile.sh")
	t.Setenv("DIRAC_DIR", workerProfile)
	t.Setenv("BASH_ENV", profileScript)
	require.NoError(t, os.WriteFile(profileScript, []byte("export DIRAC_DIR=launch-profile-$$\nprintf 'profile banner'\n"), 0o600))
	selectDiracStartupPeer(t, logPath, func(executable string) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable}, launch.Found
	})
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	started, err := startWithClock(ctx, agent.Options{
		AgentID: "dirac-stop", WorkingDir: work, HomeDir: home, Shell: shell,
	}, agent.NewProviderServices(&agenttest.Sink{}), clock)
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(a.Stop)

	events := diracStartupEvents(t, logPath, "test/launch")
	require.Len(t, events, 1)
	require.Regexp(t, "^launch-profile-[0-9]+$", events[0].Profile)
	cwd, err := filepath.EvalSymlinks(events[0].CWD)
	require.NoError(t, err)
	profile := filepath.Join(cwd, events[0].Profile)
	require.Equal(t, profile, a.checkpointProfile())
	assert.Equal(t, "profile banner", a.PreambleOutput())

	// Only the archive selected by the launch shell has this tasklet.
	writeDiracTaskletStore(t, profile)
	writeDiracCheckpointStore(t, profile, diracStartupSessionID,
		runtimeConfig("native-root", "act"), matchingHistory("native-root", diracStartupSessionID), "native-root", nil)
	metadata, err := readDiracTaskletMetadata(a.root, diracStartupSessionID)
	require.NoError(t, err)
	require.Len(t, metadata.Tasklets, 1)
	assert.Equal(t, "Witten", metadata.Tasklets[0].Record.ID)
	require.NoError(t, os.Remove(filepath.Join(profile, "data", "acp-session-runtime-configs", diracStartupSessionID+".json")))

	require.NoError(t, a.SendInput("hold the model response", nil))

	// A valid old checkpoint in the Worker's assumed archive cannot satisfy this stop.
	writeDiracCheckpointStore(t, workerProfile, diracStartupSessionID, runtimeConfig("task", "act"), matchingHistory("task", diracStartupSessionID), "task", map[string]string{
		"api_conversation_history.jsonl": diracUserMessageHistory,
	})
	// Observe a plausible index entry before the native runtime file exists.
	writeDiracCheckpointStore(t, profile, diracStartupSessionID, "", matchingHistory("task", diracStartupSessionID), "task", nil)

	poll := clock.Trap().NewTimer(diracStopPollTimerTag)
	t.Cleanup(poll.Close)
	finished := startDiracTestStop(t, a, 2)
	require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
	require.Eventually(t, func() bool {
		return len(diracStartupEvents(t, logPath, acp.MethodSessionCancel)) == 1
	}, 2*time.Second, time.Millisecond)
	assert.Empty(t, diracStartupEvents(t, logPath, "test/eof"), "stdin stays open until the exact session has a recoverable native task")
	cancellations := diracStartupEvents(t, logPath, acp.MethodSessionCancel)
	require.Len(t, cancellations, 1)
	assert.Empty(t, cancellations[0].ID)
	assert.Equal(t, diracStartupSessionID, cancellations[0].Params.SessionID)

	// A valid duplicate index still cannot release EOF.
	writeDiracCheckpointStore(t, profile, diracStartupSessionID, runtimeConfig("task", "act"), duplicateDiracHistory("task", diracStartupSessionID), "task", map[string]string{
		"api_conversation_history.jsonl": diracUserMessageHistory,
	})
	clock.Advance(diracStopPollInterval).MustWait(ctx)
	require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
	assert.Empty(t, diracStartupEvents(t, logPath, "test/eof"))

	// The index writer can leave a partial record. Recovery must wait for a
	// complete replacement while the same native process keeps running.
	writeDiracCheckpointStore(t, profile, diracStartupSessionID, runtimeConfig("task", "act"), "[{", "task", nil)
	clock.Advance(diracStopPollInterval).MustWait(ctx)
	require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
	assert.Empty(t, diracStartupEvents(t, logPath, "test/eof"))

	writeDiracCheckpointStore(t, profile, diracStartupSessionID, runtimeConfig("task", "act"), matchingHistory("task", diracStartupSessionID), "task", map[string]string{
		"api_conversation_history.jsonl": diracUserMessageHistory,
	})
	clock.Advance(diracStopPollInterval).MustWait(ctx)
	awaitDiracTestStop(t, finished)
	assert.Len(t, diracStartupEvents(t, logPath, "test/eof"), 1)
	assert.Len(t, diracStartupEvents(t, logPath, acp.MethodSessionCancel), 1)
}

func TestDiracStopWithoutExternalObservationUsesOrdinaryTeardown(t *testing.T) {
	shell := diracCheckpointShell(t)
	home, work, redirect := t.TempDir(), t.TempDir(), t.TempDir()
	fallback := filepath.Join(home, "worker")
	logPath := filepath.Join(home, "requests.jsonl")
	profileScript := filepath.Join(home, "profile.sh")
	t.Setenv("DIRAC_DIR", fallback)
	t.Setenv("BASH_ENV", profileScript)
	t.Setenv("LEAPMUX_TEST_DIRAC_REDIRECT", redirect)
	require.NoError(t, os.WriteFile(profileScript, []byte(`
dirac_test_callable() {
  export DIRAC_DIR="$LEAPMUX_TEST_DIRAC_REDIRECT"
  cd "$DIRAC_DIR" || return
  "$LEAPMUX_TEST_DIRAC_PEER" "$@"
}
`), 0o600))
	selectDiracStartupPeer(t, logPath, func(executable string) (launch.Spec, launch.Resolution) {
		return launch.ShellCommands("dirac_test_callable"), launch.Found
	})
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("LEAPMUX_TEST_DIRAC_PEER", executable)

	clock := testutil.NewQuartzMock(t)
	started, err := startWithClock(t.Context(), agent.Options{
		AgentID: "dirac-callable", WorkingDir: work, HomeDir: home, Shell: shell,
	}, agent.NewProviderServices(&agenttest.Sink{}), clock)
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(a.Stop)
	events := diracStartupEvents(t, logPath, "test/launch")
	require.Len(t, events, 1)
	assert.Equal(t, redirect, events[0].Profile)
	assert.Empty(t, a.checkpointProfile())
	assert.Equal(t, fallback, a.root, "child archive history keeps the Worker fallback")
	require.NoError(t, a.SendInput("hold the model response", nil))
	writeDiracCheckpointStore(t, fallback, diracStartupSessionID, runtimeConfig("task", "act"), `[]`, "task", nil)

	awaitDiracTestStop(t, startDiracTestStop(t, a, 2))
	assert.Len(t, diracStartupEvents(t, logPath, "test/eof"), 1)
	assert.Empty(t, diracStartupEvents(t, logPath, acp.MethodSessionCancel))
}

func TestDiracStopRequiresTheObservedProfileToLinkItsSession(t *testing.T) {
	shell := diracCheckpointShell(t)
	home, work, redirect := t.TempDir(), t.TempDir(), t.TempDir()
	observed := filepath.Join(home, "observed")
	logPath := filepath.Join(home, "requests.jsonl")
	t.Setenv("DIRAC_DIR", observed)
	t.Setenv("LEAPMUX_TEST_DIRAC_REDIRECT", redirect)
	wrapper := filepath.Join(home, "dirac-wrapper")
	testutil.WriteExecutable(t, wrapper, `#!/bin/sh
export DIRAC_DIR="$LEAPMUX_TEST_DIRAC_REDIRECT"
cd "$DIRAC_DIR" || exit
exec "$LEAPMUX_TEST_DIRAC_PEER" "$@"
`)
	selectDiracStartupPeer(t, logPath, func(executable string) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: wrapper}, launch.Found
	})
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("LEAPMUX_TEST_DIRAC_PEER", executable)

	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	started, err := startWithClock(ctx, agent.Options{
		AgentID: "dirac-wrapper", WorkingDir: work, HomeDir: home, Shell: shell,
	}, agent.NewProviderServices(&agenttest.Sink{}), clock)
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(a.Stop)
	events := diracStartupEvents(t, logPath, "test/launch")
	require.Len(t, events, 1)
	assert.Equal(t, redirect, events[0].Profile)
	assert.Equal(t, observed, a.checkpointProfile())
	require.NoError(t, a.SendInput("hold the model response", nil))
	writeDiracCheckpointStore(t, observed, "old-session", runtimeConfig("task", "act"),
		matchingHistory("task", diracStartupSessionID), "task", map[string]string{
			"api_conversation_history.jsonl": diracUserMessageHistory,
		})

	poll := clock.Trap().NewTimer(diracStopPollTimerTag)
	t.Cleanup(poll.Close)
	finished := startDiracTestStop(t, a, 2)
	require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
	assert.Empty(t, diracStartupEvents(t, logPath, "test/eof"))
	for elapsed := diracStopPollInterval; elapsed < diracStopBudget; elapsed += diracStopPollInterval {
		clock.Advance(diracStopPollInterval).MustWait(ctx)
		require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
	}
	clock.Advance(diracStopPollInterval).MustWait(ctx)
	awaitDiracTestStop(t, finished)
	assert.Len(t, diracStartupEvents(t, logPath, "test/eof"), 1)
	assert.Len(t, diracStartupEvents(t, logPath, acp.MethodSessionCancel), 1)
}

func TestDiracLaunchUsesThePlatformHomeWhenTheOverrideIsUnexported(t *testing.T) {
	shell := diracCheckpointShell(t)
	home, work := t.TempDir(), t.TempDir()
	logPath := filepath.Join(home, "requests.jsonl")
	profile := filepath.Join(home, "profile.sh")
	t.Setenv("BASH_ENV", profile)
	t.Setenv("LEAPMUX_TEST_DIRAC_HOME", home)
	t.Setenv("DIRAC_DIR", work)
	require.NoError(t, os.WriteFile(profile, []byte(`
unset DIRAC_DIR
DIRAC_DIR=unexported
export HOME="$LEAPMUX_TEST_DIRAC_HOME"
export USERPROFILE="$HOME/wrong-profile"
`), 0o600))
	selectDiracStartupPeer(t, logPath, func(executable string) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable}, launch.Found
	})
	started, err := Start(t.Context(), agent.Options{
		AgentID: "dirac-default-profile", WorkingDir: work, HomeDir: home, Shell: shell,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(a.Stop)
	events := diracStartupEvents(t, logPath, "test/launch")
	require.Len(t, events, 1)
	assert.Empty(t, events[0].Profile)
	assert.Equal(t, filepath.Join(home, ".dirac"), a.checkpointProfile())
}

type closeNotifyingDiracArchive struct {
	sessionstore.ArchiveRoot
	closed chan<- struct{}
}

func (r closeNotifyingDiracArchive) Close() error {
	err := r.ArchiveRoot.Close()
	close(r.closed)
	return err
}

func TestDiracStopDisposesAReadThatOutlivesTheDeadline(t *testing.T) {
	shell := diracCheckpointShell(t)
	profile, work := t.TempDir(), t.TempDir()
	logPath := filepath.Join(work, "requests.jsonl")
	t.Setenv("DIRAC_DIR", profile)
	selectDiracStartupPeer(t, logPath, func(executable string) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable}, launch.Found
	})
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	started, err := startWithClock(ctx, agent.Options{
		AgentID: "dirac-held-checkpoint", WorkingDir: work, Shell: shell,
	}, agent.NewProviderServices(&agenttest.Sink{}), clock)
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(a.Stop)
	require.NoError(t, a.SendInput("hold the model response", nil))
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseRead := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseRead)
	a.openCheckpointArchive = func(profile string) (sessionstore.ArchiveRoot, error) {
		root, err := sessionstore.OpenArchiveRoot(profile)
		if err != nil {
			return nil, err
		}
		close(entered)
		<-release
		return closeNotifyingDiracArchive{ArchiveRoot: root, closed: closed}, nil
	}

	finished := startDiracTestStop(t, a, 2)
	awaitDiracTestStop(t, entered)
	assert.Error(t, a.SteerInput("whisper after stop admission closes", nil))
	assert.Empty(t, diracStartupEvents(t, logPath, diracSteerMethod))
	assert.Empty(t, diracStartupEvents(t, logPath, "test/eof"))
	clock.Advance(diracStopBudget).MustWait(ctx)
	awaitDiracTestStop(t, finished)
	assert.Len(t, diracStartupEvents(t, logPath, "test/eof"), 1)
	releaseRead()
	awaitDiracTestStop(t, closed)
	assert.Empty(t, diracStartupEvents(t, logPath, acp.MethodSessionCancel))
}

func TestDiracLaunchProfileMatchesTheNativePathRules(t *testing.T) {
	shell := diracCheckpointShell(t)
	for _, tc := range []struct {
		name    string
		profile string
		want    string
	}{
		{name: "exported empty home", profile: "export HOME=''\n", want: ".dirac"},
		{name: "relative home", profile: "export HOME=relhome\n", want: "relhome/.dirac"},
		{name: "spaces in override", profile: "export DIRAC_DIR=' state dir '\n", want: " state dir "},
		{name: "dot segments before native filesystem lookup", profile: "export DIRAC_DIR=link/../state\n", want: "state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, work, remote := t.TempDir(), t.TempDir(), t.TempDir()
			physicalWork, err := filepath.EvalSymlinks(work)
			require.NoError(t, err)
			target := filepath.Join(remote, "child")
			require.NoError(t, os.Mkdir(target, 0o700))
			require.NoError(t, os.Symlink(target, filepath.Join(work, "link")))
			logPath := filepath.Join(home, "requests.jsonl")
			profile := filepath.Join(home, "profile.sh")
			require.NoError(t, os.WriteFile(profile, []byte("unset DIRAC_DIR\n"+tc.profile), 0o600))
			t.Setenv("BASH_ENV", profile)
			selectDiracStartupPeer(t, logPath, func(executable string) (launch.Spec, launch.Resolution) {
				return launch.Spec{Program: executable}, launch.Found
			})
			started, err := Start(t.Context(), agent.Options{
				AgentID: "dirac-native-profile", WorkingDir: work, HomeDir: home, Shell: shell,
			}, agent.NewProviderServices(&agenttest.Sink{}))
			require.NoError(t, err)
			t.Cleanup(started.Stop)
			require.Len(t, diracStartupEvents(t, logPath, acp.MethodSessionNew), 1)
			assert.Equal(t, filepath.Join(physicalWork, filepath.FromSlash(tc.want)), started.(*Agent).checkpointProfile())
		})
	}
}

type heldDiracStdin struct {
	io.WriteCloser
	firstWrite sync.Once
	entered    chan struct{}
	closed     chan struct{}
}

func (w *heldDiracStdin) Write([]byte) (int, error) {
	w.firstWrite.Do(func() { close(w.entered) })
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *heldDiracStdin) Close() error {
	err := w.WriteCloser.Close()
	close(w.closed)
	return err
}

func TestDiracStopReleasesBlockedStdinAtTheCheckpointDeadline(t *testing.T) {
	shell := diracCheckpointShell(t)
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue full=%t", full), func(t *testing.T) {
			profile, work := t.TempDir(), t.TempDir()
			logPath := filepath.Join(work, "requests.jsonl")
			t.Setenv("DIRAC_DIR", profile)
			selectDiracStartupPeer(t, logPath, func(executable string) (launch.Spec, launch.Resolution) {
				return launch.Spec{Program: executable}, launch.Found
			})
			logs := testutil.CaptureDefaultLogger(t)
			clock := testutil.NewQuartzMock(t)
			ctx := testutil.DeadlineContext(t)
			started, err := startWithClock(ctx, agent.Options{
				AgentID: "dirac-stalled-checkpoint", WorkingDir: work, Shell: shell,
			}, agent.NewProviderServices(&agenttest.Sink{}), clock)
			require.NoError(t, err)
			a := started.(*Agent)
			t.Cleanup(a.Stop)
			require.NoError(t, a.SendInput("hold the model response", nil))
			writeDiracCheckpointStore(t, profile, diracStartupSessionID, runtimeConfig("task", "act"), matchingHistory("task", diracStartupSessionID), "task", map[string]string{
				"api_conversation_history.jsonl": "{not a record}\n",
			})
			stdin := &heldDiracStdin{
				WriteCloser: a.StdinForTest(),
				entered:     make(chan struct{}),
				closed:      make(chan struct{}),
			}
			a.SetStdinForTest(stdin)
			blocked, err := a.EnqueueStdin([]byte("{}\n"))
			require.NoError(t, err)
			awaitDiracTestStop(t, stdin.entered)
			if full {
				for {
					if _, err := a.EnqueueStdin([]byte("{}\n")); err != nil {
						require.ErrorContains(t, err, "queue is full")
						break
					}
				}
			}

			budget := clock.Trap().NewTimer(diracStopBudgetTimerTag)
			poll := clock.Trap().NewTimer(diracStopPollTimerTag)
			t.Cleanup(budget.Close)
			t.Cleanup(poll.Close)
			finished := startDiracTestStop(t, a, 2)
			require.Equal(t, diracStopBudget, testutil.WaitForTimer(t, ctx, budget))
			require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
			queueRefused := "could not queue ACP cancellation before shutdown"
			assert.Equal(t, full, strings.Contains(logs.String(), queueRefused))
			for elapsed := diracStopPollInterval; elapsed < diracStopBudget; elapsed += diracStopPollInterval {
				clock.Advance(diracStopPollInterval).MustWait(ctx)
				require.Equal(t, diracStopPollInterval, testutil.WaitForTimer(t, ctx, poll))
			}
			clock.Advance(diracStopPollInterval - time.Nanosecond).MustWait(ctx)
			const deadlineDiagnostic = "the Dirac resume checkpoint could not be confirmed before shutdown"
			assert.NotContains(t, logs.String(), deadlineDiagnostic)
			assert.Empty(t, diracStartupEvents(t, logPath, "test/eof"))
			select {
			case err := <-blocked:
				t.Errorf("the stdin write returned before the checkpoint deadline: %v", err)
			default:
			}

			clock.Advance(time.Nanosecond).MustWait(ctx)
			awaitDiracTestStop(t, finished)
			assert.Equal(t, 1, strings.Count(logs.String(), deadlineDiagnostic))
			assert.Contains(t, logs.String(), "context deadline exceeded")
			assert.Len(t, diracStartupEvents(t, logPath, "test/eof"), 1)
			assert.Empty(t, diracStartupEvents(t, logPath, acp.MethodSessionCancel))
			select {
			case err := <-blocked:
				assert.ErrorIs(t, err, io.ErrClosedPipe)
			case <-ctx.Done():
				t.Error("ordinary teardown did not release the blocked write")
			}
		})
	}
}
