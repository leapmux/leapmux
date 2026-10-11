package agent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
)

// isolateAgentDirBases points $XDG_RUNTIME_DIR and the system's temporary
// directory at directories of the test, and returns the first. The default
// bases then lie in the test, apart from /tmp on Unix, where the test's spec
// finds nothing of its own. It uses t.Setenv, so the test cannot run in
// parallel.
func isolateAgentDirBases(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	temp := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", temp)
		t.Setenv("TEMP", temp)
	} else {
		t.Setenv("TMPDIR", temp)
	}
	return xdg
}

// agentDirParent is the parent of the agent directories under base.
func agentDirParent(base string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(base, "leapmux-agents")
	}
	return filepath.Join(base, "leapmux-agents-"+strconv.Itoa(os.Getuid()))
}

// staleAgentDir makes a directory that an ended worker left under base: its
// lock file exists, and nothing holds the lock.
func staleAgentDir(t *testing.T, base, prefix string, n int) string {
	t.Helper()
	dir := filepath.Join(agentDirParent(base), fmt.Sprintf("%s-1-%d", prefix, n))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".owner.lock"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "record.json"), []byte("{}"), 0o600))
	return dir
}

// C-M5: the worker sweeps at its start, for every provider that states a spec,
// not at the first agent of that provider. An agent's start then finds the
// directories through its options, and its directory waits for the sweep.
func TestPrepareAgentDirsSweepsAtStartAndHandsTheDirectoriesToEachStart(t *testing.T) {
	xdg := isolateAgentDirBases(t)
	dataDir := t.TempDir()
	inXDG := staleAgentDir(t, xdg, "test", 1)
	inDataDir := staleAgentDir(t, filepath.Join(dataDir, "run"), "test", 2)

	var mu sync.Mutex
	var hooked []string
	spec := agentdir.Spec{Prefix: "test", OnStale: func(_ context.Context, dir string) error {
		mu.Lock()
		defer mu.Unlock()
		hooked = append(hooked, dir)
		return nil
	}}
	reg := testRegistration(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	reg.AgentDir = &spec
	m := agent.NewManager(agenttest.MustNewRegistry(reg), nil)
	require.NoError(t, m.PrepareAgentDirs(context.Background(), dataDir))
	require.ErrorContains(t, m.PrepareAgentDirs(context.Background(), dataDir), "prepared already")

	var created *agentdir.Dir
	started := newLivingAgent()
	t.Cleanup(started.Stop)
	_, err := m.StartAgentWith(context.Background(), agent.Options{AgentID: "a-1"}, agent.NewProviderServices(&agenttest.ControlSink{}),
		func(ctx context.Context, opts agent.Options, _ agent.ProviderServices) (agent.Agent, error) {
			require.NotNil(t, opts.AgentDirs, "the manager hands the worker's directories to the start")
			dir, err := opts.AgentDirs.New(ctx, spec)
			if err != nil {
				return nil, err
			}
			assert.NoDirExists(t, inXDG, "the directory waits for the sweep of its parent")
			created = dir
			return started, nil
		})
	require.NoError(t, err)
	require.NotNil(t, created)
	t.Cleanup(func() { _ = created.Close() })
	assert.Equal(t, agentDirParent(xdg), filepath.Dir(created.Path()), "$XDG_RUNTIME_DIR is the first base")

	require.Eventually(t, func() bool {
		_, err := os.Stat(inDataDir)
		return os.IsNotExist(err)
	}, 30*time.Second, 10*time.Millisecond, "the sweep of the base under the data directory ends by itself")
	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{inXDG, inDataDir}, hooked)
}

// A caller that states its own directories keeps them, and a worker that
// prepared none gives a start nothing, so a provider that needs a directory
// refuses to start.
func TestStartAgentHandsNoDirectoriesWhenTheWorkerPreparedNone(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	own := &agentdir.Dirs{}
	for _, tc := range []struct {
		name string
		opts agent.Options
		want *agentdir.Dirs
	}{
		{"none", agent.Options{AgentID: "none"}, nil},
		{"the caller's own", agent.Options{AgentID: "own", AgentDirs: own}, own},
	} {
		var seen *agentdir.Dirs
		_, err := m.StartAgentWith(context.Background(), tc.opts, agent.NewProviderServices(&agenttest.ControlSink{}),
			func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (agent.Agent, error) {
				seen = opts.AgentDirs
				return nil, errTestStart
			})
		require.ErrorIs(t, err, errTestStart, tc.name)
		assert.True(t, tc.want == seen, "%s: the start gets the directories that the caller stated", tc.name)
	}
	var none *agentdir.Dirs
	_, err := none.New(context.Background(), agentdir.Spec{Prefix: "test"})
	require.ErrorContains(t, err, "prepared no agent directories")
}
