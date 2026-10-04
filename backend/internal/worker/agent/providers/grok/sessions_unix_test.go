//go:build unix

package grok

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// A symlink needs a privilege on Windows that a test runner lacks, so this test
// runs on unix alone.
func TestGrokSkipsASymlinkedSessionDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := filepath.Join(home, ".grok", "sessions", grokEncodeCwd(dir))
	writeGrokSession(t, filepath.Join(home, "store"), "outside", grokSummaryFixture("linked", dir, "Linked", time.Now()))
	require.NoError(t, os.MkdirAll(group, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(home, "store", "outside"), filepath.Join(group, "linked")))

	assert.Empty(t, listGrokSessions(t, home, dir, nil), "Grok writes real directories, so a link is not its session")
}

// A group that the reader cannot list fails the listing only when no other
// group lists a session. A partial list is better than none, and an empty list
// must not hide a failure.
func TestGrokReportsAnUnreadableGroupWhenItListsNothing(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root lists a directory whatever its mode")
	}
	home := t.TempDir()
	dir := longGrokCwd(t, home)
	sessions := filepath.Join(home, ".grok", "sessions")
	locked := filepath.Join(sessions, "project-locked")
	agenttest.WriteFixtureFile(t, filepath.Join(locked, grokCwdMarkerFile), dir)
	writeGrokSession(t, locked, "hidden-by-mode", grokSummaryFixture("hidden-by-mode", dir, "Locked", time.Now()))
	// Search permission alone: the marker opens, and the listing fails.
	require.NoError(t, os.Chmod(locked, 0o100))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := grokStoredSessions(context.Background(), agent.StoredSessionQuery{WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil)})
	require.Error(t, err, "a store that could not be read is not an empty store")

	readable := filepath.Join(sessions, "project-readable")
	agenttest.WriteFixtureFile(t, filepath.Join(readable, grokCwdMarkerFile), dir)
	writeGrokSession(t, readable, "listed", grokSummaryFixture("listed", dir, "Listed", time.Now()))
	assert.Equal(t, []string{"listed"}, agenttest.Handles(listGrokSessions(t, home, dir, nil)))
}
