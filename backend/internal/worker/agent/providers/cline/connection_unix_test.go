//go:build unix

package cline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
)

// C-M5 end to end: a start finds the agent directory of a crashed worker,
// whose daemon still runs. The new directory, and with it the new daemon,
// exist only after the old daemon ended, so the two never write the session's
// files at the same time. The fake `cline` of start_unix_test.go is the new
// daemon, so this test cannot run in parallel.
func TestClineStartWaitsUntilTheDaemonOfACrashedWorkerEnded(t *testing.T) {
	installFakeCline(t)
	base := t.TempDir()
	orphan := orphanAgentDir(t, base)
	old := newFakeDaemon(t)
	oldDaemon := old.proc.identity(t)
	old.hub.mu.Lock()
	// Cline's daemon ends after an accepted shutdown.
	old.hub.onShutdown = func() { _ = old.proc.stdin.Close() }
	old.hub.mu.Unlock()
	writeRecord(t, filepath.Join(orphan, discoveryFileName), old.record)
	dirs, err := agentdir.Start(context.Background(), agentdir.Config{Specs: []agentdir.Spec{agentDirSpec()}, Bases: []string{base}})
	require.NoError(t, err)

	sink := &agenttest.ControlSink{}
	opts := agent.Options{
		AgentID:        "cline-after-crash",
		WorkingDir:     t.TempDir(),
		Shell:          testutil.TestShell(),
		APITimeout:     30 * time.Second,
		StartupTimeout: 60 * time.Second,
		AgentDirs:      dirs,
	}
	oldRan := true
	started, err := start(context.Background(), opts, agent.NewProviderServices(sink), startDeps{
		getenv: os.Getenv,
		clock:  quartz.NewReal(),
		newDir: func(ctx context.Context, d *agentdir.Dirs) (*agentdir.Dir, error) {
			dir, err := newClineAgentDir(ctx, d)
			oldRan = oldDaemon.Runs()
			return dir, err
		},
	})
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	assert.False(t, oldRan, "the old daemon ended before the new directory existed")
	assert.NoDirExists(t, orphan, "the sweep removed the old directory")
	assert.Equal(t, 1, old.hub.shutdownCount())
	assert.NotEmpty(t, sink.LastSessionID(), "the new daemon opened the session")
	old.proc.waitExit(t)
	assert.True(t, old.proc.cmd.ProcessState.Success(), "the old daemon ended through its shutdown, with no kill")
}
