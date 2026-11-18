package antigravity

import (
	"runtime"
	"testing"
	"time"

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
