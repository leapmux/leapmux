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
//
// The cwd is a QUOTED scalar. A Windows path holds backslashes and a colon
// after the drive letter, and the rules a plain YAML scalar applies to both are
// subtle enough that a fixture must not depend on them. A JSON string literal
// is a valid YAML double-quoted scalar, so fixtureJSONString states it.
func writeCopilotSession(t *testing.T, home, id, cwd, name, created, updated string) {
	t.Helper()
	requireHostAbsDir(t, cwd)
	body := fmt.Sprintf(
		"id: %s\ncwd: %s\nclient_name: leapmux\nname: %s\nuser_named: false\nsummary_count: 0\ncreated_at: %s\nupdated_at: %s\n",
		id, fixtureJSONString(cwd), name, created, updated)
	writeFixtureFile(t, filepath.Join(home, ".copilot", "session-state", id, "workspace.yaml"), body)
}

func TestCopilotStoredSessions(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()

	writeCopilotSession(t, home, "sess-new", dir, "Newest work", "2026-08-27T20:11:35.263Z", "2026-08-28T06:06:28.114Z")
	writeCopilotSession(t, home, "sess-old", dir, "Older work", "2026-08-26T10:00:00.000Z", "2026-08-26T11:00:00.000Z")
	writeCopilotSession(t, home, "sess-elsewhere", absPath("other", "dir"), "Not mine", "2026-08-29T10:00:00.000Z", "2026-08-29T11:00:00.000Z")

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
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()

	// No updated_at: created_at answers instead.
	writeCopilotSession(t, home, "created-only", dir, "n", "2026-08-26T10:00:00.000Z", "")
	// Neither parses: the SIDECAR's modification time answers, because that is
	// the file Copilot writes. The session directory's own time is not the
	// session's -- see TestCopilotStoredSessions_OrdersByTheSidecarNotTheDirectory.
	writeCopilotSession(t, home, "no-times", dir, "n", "", "")
	touchFixture(t, filepath.Join(home, ".copilot", "session-state", "no-times", "workspace.yaml"),
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

// A session DIRECTORY's modification time changes only when a file appears in
// it or leaves it, so it tracks the session's creation and never its use -- and
// the migration out of the XDG location stamped whole groups of directories
// with the single time of the move. `workspace.yaml` is the file Copilot
// rewrites, so it is what orders the walk; ordering by the directory dropped a
// session used yesterday in favour of one created yesterday and never used.
func TestCopilotStoredSessions_OrdersByTheSidecarNotTheDirectory(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	stateDir := filepath.Join(home, ".copilot", "session-state")

	// No parseable times in either sidecar, so the file's own time decides.
	writeCopilotSession(t, home, "sess-recent", dir, "used yesterday", "", "")
	writeCopilotSession(t, home, "sess-stale", dir, "created yesterday", "", "")
	touchFixture(t, filepath.Join(stateDir, "sess-recent", "workspace.yaml"),
		time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	touchFixture(t, filepath.Join(stateDir, "sess-stale", "workspace.yaml"),
		time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC))
	// The DIRECTORY times say the opposite, which is what a migration produces.
	touchFixture(t, filepath.Join(stateDir, "sess-recent"), time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC))
	touchFixture(t, filepath.Join(stateDir, "sess-stale"), time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC))

	got, err := copilotStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil), Limit: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-recent"}, handlesOf(got),
		"the cut keeps the session whose sidecar is newest, not whose directory is")
}

func TestCopilotStoredSessions_SkipsUnreadableSidecars(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
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
		WorkingDir: absPath("Users", "dev", "project"), HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// The reader walks every session directory and stops at `limit` ACCEPTED
// sessions, so the cut runs on the sidecar's modification time and the order of
// what survives runs on its `updated_at`. The fixture therefore states both,
// and states them in agreement.
//
// The modification times are set rather than left to the write order. A
// filesystem whose timestamps are coarser than this loop gives several sidecars
// one time, sortAndCapEntries then breaks that tie by NAME, and the cut keeps a
// pair the test never chose -- which is how this read `sess-5, sess-3` on
// Windows while it read `sess-5, sess-4` everywhere else.
func TestCopilotStoredSessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := range 6 {
		id := fmt.Sprintf("sess-%d", i)
		writeCopilotSession(t, home, id, dir, "n",
			"2026-08-26T10:00:00.000Z", fmt.Sprintf("2026-08-2%dT10:00:00.000Z", i))
		touchFixture(t, filepath.Join(home, ".copilot", "session-state", id, copilotWorkspaceFileName),
			base.Add(time.Duration(i)*time.Hour))
	}

	got, err := copilotStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-5", "sess-4"}, handlesOf(got),
		"the two newest sidecars survive the cut, and their own updated_at orders them")
}

func TestCopilotHome(t *testing.T) {
	t.Parallel()
	home := absPath("home", "dev")

	assert.Equal(t, filepath.Join(home, ".copilot"),
		copilotHome(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}))

	assert.Equal(t, filepath.Join(home, "alt"),
		copilotHome(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"COPILOT_HOME": "~/alt"})}))

	// XDG_STATE_HOME is Copilot's MIGRATION source, not where the store lives:
	// the CLI moves session-state out of it into ~/.copilot on startup, so
	// following it would read where the store used to be.
	assert.Equal(t, filepath.Join(home, ".copilot"),
		copilotHome(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"XDG_STATE_HOME": absPath("xdg-state")})}))
}
