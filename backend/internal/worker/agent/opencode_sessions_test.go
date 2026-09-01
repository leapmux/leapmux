package agent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
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
	requireHostAbsDir(t, dir)
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
	insert("ses_elsewhere", "", absPath("somewhere", "else"), "Another directory", 1_000, 9_000, nil)
	insert("ses_no_updated", "", dir, "Never updated", 500, 0, nil)
}

func TestOpenCodeFamilySessions(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
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
	dir := absPath("Users", "dev", "project")
	parent := filepath.Dir(dir)
	sep := string(filepath.Separator)
	path := filepath.Join(t.TempDir(), "opencode.db")
	seedOpenCodeFamilyDB(t, path, dir)

	// A trailing separator and a `.` segment name the same directory, and the
	// reader cleans the query before it binds it. Both are composed from `dir`
	// rather than written out, so each stays a spelling of the host's own path.
	dotted := parent + sep + "." + sep + filepath.Base(dir)
	for _, query := range []string{dir, dir + sep, dotted} {
		got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: query})
		require.NoError(t, err)
		assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, handlesOf(got), "query=%q", query)
	}

	// A different directory, and a prefix of the real one, both answer nothing.
	for _, query := range []string{filepath.Join(parent, "other"), parent} {
		got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: query})
		require.NoError(t, err)
		assert.Empty(t, got, "query=%q must not match by prefix", query)
	}
}

func TestOpenCodeFamilySessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
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
		StoredSessionQuery{WorkingDir: absPath("Users", "dev", "project")})
	require.NoError(t, err, "a CLI the user never ran is not a failure")
	assert.Empty(t, got)
}

func TestOpenCodeFamilySessions_ForeignSchemaIsAnError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wrong.db")
	newFixtureDB(t, path, `CREATE TABLE unrelated (id text)`)

	_, err := openCodeFamilySessions(context.Background(), path,
		StoredSessionQuery{WorkingDir: absPath("Users", "dev", "project")})
	// Reported rather than swallowed: the CALLER decides that a provider-store
	// failure degrades to the worker's own records, and it can only log what it
	// is told.
	assert.Error(t, err)
}

func TestOpenCodeFamilySessions_EmptyWorkingDirListsNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "opencode.db")
	seedOpenCodeFamilyDB(t, path, absPath("Users", "dev", "project"))

	got, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: "  "})
	require.NoError(t, err)
	assert.Empty(t, got, "no directory means no question to answer, not every session")
}

func TestOpenCodeFamilySessions_LeavesTheStoreAlone(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	path := filepath.Join(t.TempDir(), "opencode.db")
	seedOpenCodeFamilyDB(t, path, dir)
	// The fixture is built with sqlitedb.Open, which chmods to 0600 and sets
	// WAL -- the two mutations this test exists to refuse. Put the file back in
	// the state a foreign CLI leaves it in, or the assertion compares a store
	// that is ALREADY mutated against itself and passes for either reader.
	require.NoError(t, os.Chmod(path, 0o644))

	before := statFixture(t, path)
	_, err := openCodeFamilySessions(context.Background(), path, StoredSessionQuery{WorkingDir: dir})
	require.NoError(t, err)
	assert.Equal(t, before, statFixture(t, path), "reading another program's store must not change it")
}

// statFixture captures what a read must not change: the store's file MODE and
// its journal mode. Both, because sqlitedb.Open mutates both, and a reader that
// reached for it instead of OpenReadOnly would take away a permission the
// store's owner chose as well as rewrite the journal of a running program.
func statFixture(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var journal string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journal))
	return fmt.Sprintf("mode=%v journal=%s", info.Mode(), journal)
}

// The branch a single-return resolver could not express: the override is SET,
// but the data directory it would resolve against is not. Answering the default
// name under that same unresolvable directory would be a second empty answer
// reached by accident, and the two cases would be indistinguishable.
//
// No t.Parallel: t.Setenv panics under it, and an unresolvable home needs the
// process environment rather than the query's Getenv seam.
func TestOpencodeDBPath_OverrideWithNoDataDirIsEmpty(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	q := StoredSessionQuery{Getenv: fixtureEnv(map[string]string{"OPENCODE_DB": "beta.db"})}
	assert.Empty(t, opencodeDBPath(q), "a bare override name has nothing to resolve against")
}

func TestOpencodeDBPath(t *testing.T) {
	t.Parallel()
	home := absPath("home", "dev")

	base := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
	assert.Equal(t, filepath.Join(home, ".local", "share", "opencode", "opencode.db"), opencodeDBPath(base))

	data := absPath("data")
	xdg := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
	assert.Equal(t, filepath.Join(data, "opencode", "opencode.db"), opencodeDBPath(xdg))

	// OPENCODE_DB takes an absolute path, or a bare name under the data dir.
	// The override has to be absolute for the HOST, because that is the
	// question storeOverridePath asks it.
	custom := absPath("custom", "store.db")
	abs := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"OPENCODE_DB": custom})}
	assert.Equal(t, custom, opencodeDBPath(abs))

	rel := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"OPENCODE_DB": "beta.db"})}
	assert.Equal(t, filepath.Join(home, ".local", "share", "opencode", "beta.db"), opencodeDBPath(rel))
}

func TestOpencodeStoredSessions_EndToEnd(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	data := t.TempDir()
	seedOpenCodeFamilyDB(t, filepath.Join(data, "opencode", "opencode.db"), dir)

	got, err := opencodeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    absPath("unused"),
		Getenv:     fixtureEnv(map[string]string{"XDG_DATA_HOME": data}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, handlesOf(got))
}
