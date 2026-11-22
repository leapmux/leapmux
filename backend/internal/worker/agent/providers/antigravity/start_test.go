package antigravity

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityResumePreservesTheMissingProviderCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated POSIX PATH")
	}
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	opts := agent.Options{
		AgentID: "missing-antigravity", WorkingDir: home, HomeDir: home, Shell: "/bin/sh",
		ResumeSessionID: "saved-session", StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second,
		AgentDirs: agentdirtest.NewDirs(t, []agentdir.Spec{{Prefix: "antigravity"}}),
	}
	started, err := Start(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Nil(t, started)
	require.ErrorIs(t, err, launch.ErrNoCandidate)
	assert.ErrorContains(t, err, "(agy)")
}

const resumeAuthenticationPeer = "LEAPMUX_TEST_AGY_RESUME_AUTH"

func TestAntigravityResumeAuthenticationPeer(t *testing.T) {
	scenario := os.Getenv(resumeAuthenticationPeer)
	if scenario == "" {
		return
	}
	var logPath string
	for _, argument := range os.Args {
		if path, found := strings.CutPrefix(argument, "--log-file="); found {
			logPath = path
		}
	}
	require.NotEmpty(t, logPath)
	require.NoError(t, os.WriteFile(logPath, []byte("the runtime recorded both sign-in attempts\n"), 0o600))
	input := bufio.NewScanner(os.Stdin)
	for index, method := range []string{"initialize", "session/list", "authenticate", "authenticate"} {
		require.True(t, input.Scan(), "expected request %s", method)
		var request struct {
			ID     json.RawMessage
			Method string
		}
		require.NoError(t, json.Unmarshal(input.Bytes(), &request))
		require.Equal(t, method, request.Method)
		if index == 1 && scenario == "list-error" {
			_, err := fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32603,\"message\":\"Native catalog unavailable\"}}\n", request.ID)
			require.NoError(t, err)
			return
		}
		if index == 3 {
			fmt.Fprintln(os.Stderr, "the native authentication process exited")
			os.Exit(37)
		}
		response := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{}}", request.ID)
		if index != 0 {
			response = fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32000,\"message\":\"requires sign-in\"}}", request.ID)
		}
		_, err := fmt.Fprintln(os.Stdout, response)
		require.NoError(t, err)
	}
}

func TestAntigravityResumeKeepsSignInDiagnosticsAfterNativeExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated POSIX executable")
	}
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	testutil.WriteExecutable(t, filepath.Join(dir, "agy"), "#!/bin/sh\n"+resumeAuthenticationPeer+
		"=1 exec "+shellquote.POSIX.Quote(executable)+" -test.run=^TestAntigravityResumeAuthenticationPeer$ -- \"$@\"\n")
	t.Setenv("PATH", dir)
	opts := agent.Options{
		AgentID: "resume-authentication", WorkingDir: dir, HomeDir: dir, Shell: "/bin/sh",
		ResumeSessionID: "saved-session", StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second,
		AgentDirs: agentdirtest.NewDirs(t, []agentdir.Spec{{Prefix: "antigravity"}}),
	}
	started, err := Start(testutil.DeadlineContext(t), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Nil(t, started)
	require.ErrorIs(t, err, errAntigravitySignIn)
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 37, exit.ExitCode())
	assert.ErrorContains(t, err, "session: antigravity CLI is not signed in")
	assert.ErrorContains(t, err, "the runtime recorded both sign-in attempts")
	assert.ErrorContains(t, err, "the native authentication process exited")
}

func TestAntigravityBootstrapFailureDoesNotSuggestClearingSavedSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated POSIX executable")
	}
	dir := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	testutil.WriteExecutable(t, filepath.Join(dir, "agy"), "#!/bin/sh\n"+resumeAuthenticationPeer+
		"=list-error exec "+shellquote.POSIX.Quote(executable)+" -test.run=^TestAntigravityResumeAuthenticationPeer$ -- \"$@\"\n")
	t.Setenv("PATH", dir)
	opts := agent.Options{
		AgentID: "resume-with-list-error", WorkingDir: dir, HomeDir: dir, Shell: "/bin/sh",
		ResumeSessionID: "saved-session", StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second,
		AgentDirs: agentdirtest.NewDirs(t, []agentdir.Spec{{Prefix: "antigravity"}}),
	}
	started, err := Start(testutil.DeadlineContext(t), opts, agent.NewProviderServices(&agenttest.Sink{}))
	require.Nil(t, started)
	require.ErrorContains(t, err, "Native catalog unavailable")
	assert.NotContains(t, err.Error(), "send /clear")
}
