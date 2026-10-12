package droid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

func TestHandshakeLoadsAnExistingSession(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	options := agent.Options{ResumeSessionID: "saved-session", WorkingDir: "/work"}
	returned := make(chan error, 1)
	go func() { returned <- a.handshake(options) }()
	select {
	case <-stdin.written:
	case <-time.After(30 * time.Second):
		t.Fatal("the handshake wrote no request")
	}

	frames := stdin.frames()
	require.Len(t, frames, 1)
	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &request))
	assert.Equal(t, droidMethodLoadSession, request.Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(request.Params, &params))
	assert.Equal(t, map[string]any{"sessionId": "saved-session"}, params)
	a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"session":{"messages":[]},"settings":{"modelId":"custom:Droid-0"},"availableModels":[]}}`))
	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the native load response did not settle the handshake")
	}
	a.Mu.Lock()
	sessionID, model := a.sessionID, a.settings.model
	a.Mu.Unlock()
	assert.Equal(t, "saved-session", sessionID)
	assert.Equal(t, "custom:Droid-0", model)
}

func TestHandshakeReportsNativeLoadError(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	returned := make(chan error, 1)
	go func() { returned <- a.handshake(agent.Options{ResumeSessionID: "missing-session"}) }()
	select {
	case <-stdin.written:
	case <-time.After(30 * time.Second):
		t.Fatal("the handshake wrote no request")
	}
	a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","error":{"code":-32000,"message":"saved transcript is absent"}}`))
	select {
	case err := <-returned:
		require.ErrorContains(t, err, "saved transcript is absent")
	case <-time.After(30 * time.Second):
		t.Fatal("the native error did not settle the handshake")
	}
}

func TestHandshakeCreatesANewSession(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()
	returned := make(chan error, 1)
	go func() { returned <- a.handshake(agent.Options{WorkingDir: "/work"}) }()
	select {
	case <-stdin.written:
	case <-time.After(30 * time.Second):
		t.Fatal("the handshake wrote no request")
	}
	frames := stdin.frames()
	require.Len(t, frames, 1)
	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(frames[0]), &request))
	assert.Equal(t, droidMethodInitializeSession, request.Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(request.Params, &params))
	assert.Equal(t, "/work", params["cwd"])
	assert.NotEmpty(t, params["machineId"])
	assert.NotContains(t, params, "sessionId")

	a.HandleOutput([]byte(`{"type":"response","id":"leapmux-init","result":{"sessionId":"fresh-session","settings":{},"availableModels":[]}}`))
	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the native initialize response did not settle the handshake")
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	assert.Equal(t, "fresh-session", sessionID)
}

func TestDroidOutputPathStartupDoesNotRequireAWorkerReaderRoot(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.Mkdir(work, 0o700))
	logPath := filepath.Join(home, "native-invocations.log")
	binary, err := os.Executable()
	require.NoError(t, err)
	spec := launch.Spec{
		Program:    binary,
		PrefixArgs: []string{"-test.run=^TestDroidChildProcessHelper$", "--"},
		Env: []string{
			droidChildProcessTestEnv + "=1",
			"LEAPMUX_DROID_CHILD_PROCESS_LOG=" + logPath,
		},
	}
	options := agent.Options{
		AgentID: "native-path-startup", WorkingDir: work, HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), StartupTimeout: 30 * time.Second,
	}
	recorded := &agenttest.Sink{}
	sink := agent.NewProviderServices(recorded)
	started, startErr := startProcess(t.Context(), options, sink, droidLaunch{spec: spec, shutdownGrace: Registration().ShutdownGrace})
	if started != nil {
		// Test.Context ends before Cleanup runs. Stop the peer before that boundary.
		defer func() {
			assert.NoError(t, t.Context().Err(), "the controlled peer stops while its test context remains live")
			started.Stop()
			assert.NoError(t, started.Wait(), "this controlled peer exits with code zero when Stop closes stdin")
		}()
	}
	require.NoError(t, startErr, "native startup must not require a directory for a removed text reader")
	require.NotNil(t, started)
	a, ok := started.(*Agent)
	require.True(t, ok)
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	assert.Equal(t, "main-session", sessionID)
	assert.Equal(t, work, a.Cmd().Dir)
	calls := readDroidProcessCalls(logPath)
	require.Len(t, calls, 1, "the controlled native peer must receive one startup request")
	assert.Equal(t, droidMethodInitializeSession, calls[0].method)
	assert.NotEmpty(t, calls[0].pid)
}
