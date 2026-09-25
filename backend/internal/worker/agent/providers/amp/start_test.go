package amp

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
)

// startAgent runs the real Start. It starts no `amp` process: the first message
// would, and these tests send none.
func startAgent(t *testing.T, resume string) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	started, err := Start(context.Background(), agent.Options{
		AgentID:         "agent-start",
		WorkingDir:      t.TempDir(),
		HomeDir:         t.TempDir(),
		Shell:           "/bin/sh",
		ResumeSessionID: resume,
		AgentDirs:       agentdirtest.NewDirs(t, []agentdir.Spec{agentDirSpec()}),
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a, ok := started.(*Agent)
	require.True(t, ok)
	t.Cleanup(a.Stop)
	return a, sink
}

func TestStartPreparesTheAgentDirectoryAndNoProcess(t *testing.T) {
	t.Parallel()
	a, sink := startAgent(t, "")
	assert.Nil(t, a.proc, "no thread exists until the first message")
	assert.Equal(t, 1, sink.StatusActiveCount())
	assert.Zero(t, sink.SessionIDCount())

	info, err := os.Stat(a.launch.stateDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	specPath := filepath.Join(a.launch.stateDir, helperSpecFileName)
	assert.Equal(t, contracts.EnvAgentHelper+"="+specPath, a.launch.helperEnv)
	assert.NotContains(t, a.launch.helperEnv, a.bridge.secretText(), "the secret reaches neither argv nor the environment")
	spec, err := agent.ReadHelperSpec(specPath)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP.String(), spec.Provider)
	assert.Equal(t, helperPermission, spec.Helper)
	var config helperConfig
	require.NoError(t, json.Unmarshal(spec.Config, &config))
	assert.Equal(t, a.bridge.endpoint(), config.Endpoint)
	assert.Equal(t, a.bridge.secretText(), config.Secret)
	if runtime.GOOS != "windows" {
		specInfo, err := os.Stat(specPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), specInfo.Mode().Perm())
	}

	assert.True(t, filepath.IsAbs(a.launch.helperProgram), "Amp starts the helper with no shell and no PATH search")
	executable, err := os.Executable()
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(executable)
	require.NoError(t, err)
	assert.Equal(t, resolved, a.launch.helperProgram, "the helper is the worker's own executable")
}

func TestStartWithAResumeStatesTheThreadAndFixesTheMode(t *testing.T) {
	t.Parallel()
	a, sink := startAgent(t, "T-5d6a2b8e-0000-4000-8000-000000000001")
	assert.Equal(t, "T-5d6a2b8e-0000-4000-8000-000000000001", sink.LastSessionID())
	assert.True(t, a.modeLocked)
	assert.Equal(t, "T-5d6a2b8e-0000-4000-8000-000000000001", a.threadID)
}

// A worker that prepared no agent directories cannot give the bridge a private
// place, so the agent does not start.
func TestStartRefusesWithoutTheWorkersAgentDirectories(t *testing.T) {
	t.Parallel()
	_, err := Start(context.Background(), agent.Options{
		AgentID:    "agent-no-dirs",
		WorkingDir: t.TempDir(),
		HomeDir:    t.TempDir(),
		Shell:      "/bin/sh",
	}, agent.NewProviderServices(&agenttest.ControlSink{}))
	require.ErrorContains(t, err, "prepare the Amp permission bridge")
	assert.ErrorContains(t, err, "prepared no agent directories")
}

func TestStopRemovesTheAgentDirectoryAndClosesTheBridge(t *testing.T) {
	t.Parallel()
	a, _ := startAgent(t, "")
	a.Stop()
	_, err := os.Stat(a.launch.stateDir)
	assert.True(t, os.IsNotExist(err))
	_, err = a.bridge.listener.Accept()
	assert.ErrorIs(t, err, net.ErrClosed, "no helper reaches a stopped agent")
}

func TestCheckHelperProgram(t *testing.T) {
	t.Parallel()
	program, err := checkHelperProgram("/opt/leapmux/leapmux")
	require.NoError(t, err)
	assert.Equal(t, "/opt/leapmux/leapmux", program)
	for _, path := range []string{"/home/me/~bin/leapmux", `C:\Users\%USERNAME%\leapmux.exe`} {
		_, err := checkHelperProgram(path)
		assert.ErrorContainsf(t, err, "which Amp's permission rule expands", "%s", path)
		assert.Truef(t, strings.Contains(err.Error(), path), "the error states the path %s", path)
	}
}
