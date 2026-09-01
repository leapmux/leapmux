package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCopilotSession writes one `session-state/<id>/workspace.yaml`, in the
// shape Copilot's own writer produces.
func writeCopilotSession(t *testing.T, home, id, cwd, name, created, updated string) {
	t.Helper()
	body := fmt.Sprintf(
		"id: %s\ncwd: %s\nclient_name: leapmux\nname: %s\nuser_named: false\nsummary_count: 0\ncreated_at: %s\nupdated_at: %s\n",
		id, cwd, name, created, updated)
	writeFixtureFile(t, filepath.Join(home, ".copilot", "session-state", id, "workspace.yaml"), body)
}

func TestCopilotStoredSessions(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()

	writeCopilotSession(t, home, "sess-new", dir, "Newest work", "2026-08-27T20:11:35.263Z", "2026-08-28T06:06:28.114Z")
	writeCopilotSession(t, home, "sess-old", dir, "Older work", "2026-08-26T10:00:00.000Z", "2026-08-26T11:00:00.000Z")
	writeCopilotSession(t, home, "sess-elsewhere", "/other/dir", "Not mine", "2026-08-29T10:00:00.000Z", "2026-08-29T11:00:00.000Z")

	got, err := copilotStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"sess-new", "sess-old"}, handlesOf(got),
		"only the sessions whose sidecar records this working directory")
	assert.Equal(t, "Newest work", got[0].Title)
	assert.Equal(t, time.Date(2026, 8, 28, 6, 6, 28, 114000000, time.UTC), got[0].UpdatedAt)
}

func TestCopilotStoredSessions_FallsBackWhenTimesAreMissing(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()

	// No updated_at: created_at answers instead.
	writeCopilotSession(t, home, "created-only", dir, "n", "2026-08-26T10:00:00.000Z", "")
	// Neither parses: the directory's modification time answers.
	writeCopilotSession(t, home, "no-times", dir, "n", "", "")
	touchFixture(t, filepath.Join(home, ".copilot", "session-state", "no-times"),
		time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC))

	got, err := copilotStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "created-only", got[0].Handle)
	assert.Equal(t, time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC), got[0].UpdatedAt)
	assert.Equal(t, "no-times", got[1].Handle)
	assert.False(t, got[1].UpdatedAt.IsZero())
}

func TestCopilotStoredSessions_SkipsUnreadableSidecars(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	root := filepath.Join(home, ".copilot", "session-state")

	writeCopilotSession(t, home, "good", dir, "readable", "2026-08-26T10:00:00.000Z", "2026-08-26T11:00:00.000Z")
	// A session directory with no sidecar at all.
	writeFixtureFile(t, filepath.Join(root, "no-sidecar", "events.jsonl"), "{}\n")
	// A sidecar that is not YAML.
	writeFixtureFile(t, filepath.Join(root, "broken", "workspace.yaml"), "\tid: [unclosed\n  cwd:::\n")

	got, err := copilotStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"good"}, handlesOf(got))
}

func TestCopilotStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := copilotStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: "/Users/dev/project", HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCopilotStoredSessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	home := t.TempDir()
	for i := range 6 {
		id := fmt.Sprintf("sess-%d", i)
		writeCopilotSession(t, home, id, dir, "n",
			"2026-08-26T10:00:00.000Z", fmt.Sprintf("2026-08-2%dT10:00:00.000Z", i))
	}

	got, err := copilotStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-5", "sess-4"}, handlesOf(got))
}

func TestCopilotHome(t *testing.T) {
	t.Parallel()
	home := "/home/dev"

	assert.Equal(t, filepath.Join(home, ".copilot"),
		copilotHome(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}))

	assert.Equal(t, filepath.Join(home, "alt"),
		copilotHome(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"COPILOT_HOME": "~/alt"})}))

	// XDG_STATE_HOME is Copilot's MIGRATION source, not where the store lives:
	// the CLI moves session-state out of it into ~/.copilot on startup, so
	// following it would read where the store used to be.
	assert.Equal(t, filepath.Join(home, ".copilot"),
		copilotHome(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"XDG_STATE_HOME": "/xdg-state"})}))
}
