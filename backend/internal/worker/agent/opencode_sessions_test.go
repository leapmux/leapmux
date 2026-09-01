package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openCodeFamilyDDL is the `session` table as OpenCode, Kilo and ZCode all ship
// it. Only the columns the shared reader touches are declared: a fixture that
// restated the whole table would drift against three CLIs without testing
// anything more.
const openCodeFamilyDDL = `
CREATE TABLE session (
  id text PRIMARY KEY,
  project_id text,
  parent_id text,
  directory text NOT NULL,
  title text NOT NULL DEFAULT '',
  time_created integer NOT NULL,
  time_updated integer,
  time_archived integer
);`

// seedOpenCodeFamilyDB writes the shared fixture: one directory's sessions plus
// the rows every filter has to drop.
func seedOpenCodeFamilyDB(t *testing.T, path, dir string) {
	t.Helper()
	db := newFixtureDB(t, path, openCodeFamilyDDL)
	insert := func(id, parent, directory, title string, created, updated int64, archived any) {
		t.Helper()
		var parentVal any
		if parent != "" {
			parentVal = parent
		}
		_, err := db.Exec(
			`INSERT INTO session (id, parent_id, directory, title, time_created, time_updated, time_archived)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, parentVal, directory, title, created, updated, archived)
		require.NoError(t, err)
	}

	insert("ses_new", "", dir, "Newest session", 1_000, 3_000, nil)
	insert("ses_old", "", dir, "Older session", 1_000, 2_000, nil)
	insert("ses_subagent", "ses_new", dir, "Spawn subagent", 1_000, 9_000, nil)
	insert("ses_archived", "", dir, "Put away", 1_000, 9_000, 8_000)
	insert("ses_elsewhere", "", "/somewhere/else", "Another directory", 1_000, 9_000, nil)
	insert("ses_no_updated", "", dir, "Never updated", 500, 0, nil)
}

func TestOpenCodeFamilySessions(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	path := filepath.Join(t.TempDir(), "opencode.db")
	seedOpenCodeFamilyDB(t, path, dir)

	got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: dir})
	require.NoError(t, err)

	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, handlesOf(got),
		"newest first; the subagent, the archived row and the other directory are all excluded")
	assert.Equal(t, "Newest session", got[0].Title)
	assert.Equal(t, time.UnixMilli(3_000).UTC(), got[0].UpdatedAt)
	// time_updated 0 falls back to time_created rather than becoming the epoch.
	assert.Equal(t, time.UnixMilli(500).UTC(), got[2].UpdatedAt)
}

func TestOpenCodeFamilySessions_MatchesTheDirectoryExactly(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	path := filepath.Join(t.TempDir(), "opencode.db")
	seedOpenCodeFamilyDB(t, path, dir)

	// A trailing slash and a `.` segment name the same directory, and the
	// reader cleans the query before it binds it.
	for _, query := range []string{dir, dir + "/", "/Users/dev/./project"} {
		got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: query})
		require.NoError(t, err)
		assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, handlesOf(got), "query=%q", query)
	}

	// A different directory, and a prefix of the real one, both answer nothing.
	for _, query := range []string{"/Users/dev/other", "/Users/dev"} {
		got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: query})
		require.NoError(t, err)
		assert.Empty(t, got, "query=%q must not match by prefix", query)
	}
}

func TestOpenCodeFamilySessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := newFixtureDB(t, path, openCodeFamilyDDL)
	for i := range 10 {
		_, err := db.Exec(
			`INSERT INTO session (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
			"ses_"+string(rune('a'+i)), dir, "t", 1_000, int64(1_000+i))
		require.NoError(t, err)
	}

	got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: dir, Limit: 3})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"ses_j", "ses_i", "ses_h"}, handlesOf(got), "the newest three")
}

func TestOpenCodeFamilySessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := openCodeFamilySessions(context.Background(),
		filepath.Join(t.TempDir(), "never-created.db"),
		StoredSessionQuery{WorkingDir: "/Users/dev/project"})
	require.NoError(t, err, "a CLI the user never ran is not a failure")
	assert.Empty(t, got)
}

func TestOpenCodeFamilySessions_ForeignSchemaIsAnError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wrong.db")
	newFixtureDB(t, path, `CREATE TABLE unrelated (id text)`)

	_, err := openCodeFamilySessions(context.Background(), path,
		StoredSessionQuery{WorkingDir: "/Users/dev/project"})
	// Reported rather than swallowed: the CALLER decides that a provider-store
	// failure degrades to the worker's own records, and it can only log what it
	// is told.
	assert.Error(t, err)
}

func TestOpenCodeFamilySessions_EmptyWorkingDirListsNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "opencode.db")
	seedOpenCodeFamilyDB(t, path, "/Users/dev/project")

	got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: "  "})
	require.NoError(t, err)
	assert.Empty(t, got, "no directory means no question to answer, not every session")
}

func TestOpenCodeFamilySessions_LeavesTheStoreAlone(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	path := filepath.Join(t.TempDir(), "opencode.db")
	seedOpenCodeFamilyDB(t, path, dir)

	before := statFixture(t, path)
	_, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: dir})
	require.NoError(t, err)
	assert.Equal(t, before, statFixture(t, path), "reading another program's store must not change it")
}

// statFixture captures the file mode of a store, for the assertion that reading
// it changes nothing.
func statFixture(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var journal string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journal))
	return journal
}

func TestOpencodeDBPath(t *testing.T) {
	t.Parallel()
	home := "/home/dev"

	base := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
	assert.Equal(t, filepath.Join(home, ".local", "share", "opencode", "opencode.db"), opencodeDBPath(base))

	xdg := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"XDG_DATA_HOME": "/data"})}
	assert.Equal(t, filepath.Join("/data", "opencode", "opencode.db"), opencodeDBPath(xdg))

	// OPENCODE_DB takes an absolute path, or a bare name under the data dir.
	abs := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"OPENCODE_DB": "/custom/store.db"})}
	assert.Equal(t, "/custom/store.db", opencodeDBPath(abs))

	rel := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"OPENCODE_DB": "beta.db"})}
	assert.Equal(t, filepath.Join(home, ".local", "share", "opencode", "beta.db"), opencodeDBPath(rel))
}

func TestOpencodeStoredSessions_EndToEnd(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	data := t.TempDir()
	seedOpenCodeFamilyDB(t, filepath.Join(data, "opencode", "opencode.db"), dir)

	got, err := opencodeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    "/unused",
		Getenv:     fixtureEnv(map[string]string{"XDG_DATA_HOME": data}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, handlesOf(got))
}
