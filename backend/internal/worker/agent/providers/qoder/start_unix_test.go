//go:build unix

package qoder

import (
	"context"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestStartUsesTheSDKControlTransport(t *testing.T) {
	runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{Binary: "qodercli", Start: startWithPrivateDirs(t)})
	require.Len(t, runs, 1)
	args := runs[0].Args
	assert.Contains(t, args, "--sdk")
	assert.NotContains(t, args, "-p")
	assert.NotContains(t, args, "--input-format")
	// Qoder 1.1.86's ordinary headless stream ignores control requests.
	// Its SDK daemon supplies session controls and permission requests.
	assert.NotContains(t, args, "--permission-prompt-tool")
}

func TestStartPinsTheSDKEnvironmentAfterTheLoginProfile(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: "qodercli", Start: startWithPrivateDirs(t),
		Pins: []string{"QODER_SDK_CUSTOM_BASE_URL_BYOK=1", "QODER_SITE=GLOBAL", "QODER_NO_RC=1", "QODER_FORCE_FILE_STORAGE=1", "QODER_HTTPDNS=0"},
	})
}

func startWithPrivateDirs(t *testing.T) agent.StartFunc {
	t.Helper()
	dirs := agentdirtest.NewDirs(t, []agentdir.Spec{agentDirSpec()}, t.TempDir())
	return func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
		opts.AgentDirs = dirs
		return Start(ctx, opts, sink)
	}
}
