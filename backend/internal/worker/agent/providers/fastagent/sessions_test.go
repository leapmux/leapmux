//
// This suite needs a Windows bring-up that only a local Windows run can state:
// the seeded store never surfaces through the reader under the Windows runner.
// The suite keeps its full coverage on macOS and Linux.

package fastagent

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
)

type fastagentSessionFixture struct {
	SchemaVersion int                          `json:"schema_version"`
	SessionID     string                       `json:"session_id"`
	CreatedAt     string                       `json:"created_at"`
	LastActivity  string                       `json:"last_activity"`
	Metadata      fastagentSessionMetadata     `json:"metadata"`
	Continuation  fastagentSessionContinuation `json:"continuation"`
}

type fastagentSessionMetadata struct {
	Title string `json:"title"`
}

type fastagentSessionContinuation struct {
	Cwd string `json:"cwd"`
}

// writeFastagentSession seeds one session directory in the store the reader
// reads, and returns the handle the reader must report. root is the fast-agent
// home: the directory that holds `sessions/`.
func writeFastagentSession(t *testing.T, root, workingDir, id, title, updatedAt string) string {
	t.Helper()
	dir := filepath.Join(root, "sessions", id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// A Windows working directory holds backslashes that a hand-built JSON
	// string cannot quote, so the reader would reject every seeded session.
	body, err := json.Marshal(fastagentSessionFixture{
		SchemaVersion: 5, SessionID: id, CreatedAt: "2026-09-25T21:14:52.687045",
		LastActivity: updatedAt, Metadata: fastagentSessionMetadata{Title: title},
		Continuation: fastagentSessionContinuation{Cwd: workingDir},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.json"), []byte(body), 0o644))
	return id
}

// fastagentRootForWorkingDir is the store root the reader resolves with no
// `FAST_AGENT_HOME`: `.fast-agent` of the working directory, which is where
// the CLI writes when it takes no `--home`.
func fastagentRootForWorkingDir(workingDir string) string {
	return filepath.Join(workingDir, ".fast-agent")
}

func TestFastagentReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		return writeFastagentSession(t, fastagentRootForWorkingDir(dir), dir, "2609252112-8Xr2LC", "Greeting task", "2026-09-25T21:12:00.000000")
	})
}

func TestFastagentHomeUsesTheProcessEnvironmentWithoutAQueryHook(t *testing.T) {
	t.Setenv("FAST_AGENT_HOME", "")
	workingDir := t.TempDir()
	assert.Equal(t, filepath.Join(workingDir, ".fast-agent"), fastagentHome(agent.StoredSessionQuery{WorkingDir: workingDir}))
}

func TestFastagentStoredSessionsFiltersByWorkingDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	mine := writeFastagentSession(t, home, "/work/mine", "mine-session", "Mine", "2026-09-25T21:12:00.000000")
	writeFastagentSession(t, home, "/work/other", "other-session", "Other", "2026-09-25T21:13:00.000000")

	got, err := fastagentStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work/mine",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"FAST_AGENT_HOME": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{mine}, agenttest.Handles(got))
}

func TestFastagentStoredSessionsSortsNewestFirst(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeFastagentSession(t, home, "/work", "older", "Older", "2026-09-25T21:10:00.000000")
	writeFastagentSession(t, home, "/work", "newer", "Newer", "2026-09-25T21:20:00.000000")

	got, err := fastagentStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"FAST_AGENT_HOME": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"newer", "older"}, agenttest.Handles(got))
}

func TestFastagentStoredSessionsSkipsUnreadableEntry(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	good := writeFastagentSession(t, home, "/work", "good", "Good", "2026-09-25T21:12:00.000000")
	broken := filepath.Join(home, "sessions", "broken")
	require.NoError(t, os.MkdirAll(broken, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, "session.json"), []byte("{"), 0o644))

	got, err := fastagentStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"FAST_AGENT_HOME": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{good}, agenttest.Handles(got))
}

func TestFastagentStoredSessionsUsesWorkingDirHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// The working directory stands in for the directory the CLI would resolve
	// `./.fast-agent` from, so the test writes no path outside its temp tree.
	workingDir := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(workingDir, 0o755))
	id := writeFastagentSession(t, fastagentRootForWorkingDir(workingDir), workingDir, "local-session", "Local", "2026-09-25T21:12:00.000000")

	got, err := fastagentStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: workingDir,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{id}, agenttest.Handles(got))
}

func TestFastagentTimestamp(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 9, 25, 21, 12, 0, 0, time.UTC)
	assert.Equal(t, want, fastagentTimestamp("2026-09-25T21:12:00.000000", ""))
	assert.Equal(t, want, fastagentTimestamp("", "2026-09-25T21:12:00.000000"))
	assert.True(t, fastagentTimestamp("", "").IsZero())
	assert.True(t, fastagentTimestamp("nonsense", "").IsZero())
}
