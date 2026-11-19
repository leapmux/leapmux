package qoder

import (
	"runtime"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQoderPermissionFlagCoversEveryMode pins the launch word of each offered
// mode. Qoder CLI 1.1.78 admits these words for --permission-mode: default,
// accept_edits, bypass_permissions, dont_ask, plan and auto. A mode without its
// word would restart the agent in Default.
func TestQoderPermissionFlagCoversEveryMode(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		contracts.QoderModeDefault:           "default",
		contracts.QoderModeAcceptEdits:       "accept_edits",
		contracts.QoderModeAuto:              "auto",
		contracts.QoderModeDontAsk:           "dont_ask",
		contracts.QoderModePlan:              "plan",
		contracts.QoderModeBypassPermissions: "bypass_permissions",
	}
	offered := make(map[string]string, len(qoderPermissionModes))
	for _, mode := range qoderPermissionModes {
		offered[mode.Id] = qoderPermissionFlag(mode.Id)
	}
	assert.Equal(t, want, offered, "each offered mode launches with its own native word")
	assert.Equal(t, "default", qoderPermissionFlag(""))
	assert.Equal(t, "default", qoderPermissionFlag("yolo"), "an unknown mode launches in Default")
}

func TestQoderAgentEnvDoesNotRestoreTheRemovedSDKWorkerMode(t *testing.T) {
	t.Parallel()
	env := qoderAgentEnv([]string{
		"QODER_AGENT_SDK_ENTRYPOINT=1",
		"QODER_SDK_AUTH_PAYLOAD_FILE=/controlled/obsolete-payload.json",
		`QODER_JOB_TOKEN={"token":"controlled-key"}`,
	})
	assert.NotContains(t, env, "QODER_AGENT_SDK_ENTRYPOINT=1")
	assert.Contains(t, env, `QODER_JOB_TOKEN={"token":"controlled-key"}`)
}

func TestQoderStartupReportsTheMissingCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated POSIX PATH")
	}
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	dirs := agentdirtest.NewDirs(t, []agentdir.Spec{agentDirSpec()})
	started, err := Start(t.Context(), agent.Options{
		AgentID: "missing-qoder", HomeDir: home, WorkingDir: home, Shell: "/bin/sh",
		AgentDirs: dirs, StartupTimeout: 30 * time.Second, APITimeout: 30 * time.Second,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.Nil(t, started)
	require.ErrorIs(t, err, launch.ErrNoCandidate)
	assert.ErrorContains(t, err, "(qodercli)")
}
