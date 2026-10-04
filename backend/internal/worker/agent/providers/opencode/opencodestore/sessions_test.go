package opencodestore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore/opencodestoretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeFamilySessions(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "opencode.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)

	got, err := ListSessions(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir})
	require.NoError(t, err)

	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got),
		"newest first; the subagent, the archived row and the other directory are all excluded")
	assert.Equal(t, "Newest session", got[0].Title)
	assert.Equal(t, time.UnixMilli(3_000).UTC(), got[0].UpdatedAt)
	// time_updated 0 falls back to time_created rather than becoming the epoch.
	assert.Equal(t, time.UnixMilli(500).UTC(), got[2].UpdatedAt)
}

func TestOpenCodeFamilySessions_MatchesTheDirectoryExactly(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	parent := filepath.Dir(dir)
	sep := string(filepath.Separator)
	path := filepath.Join(t.TempDir(), "opencode.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)

	// A trailing separator and a `.` segment name the same directory, and the
	// reader cleans the query before it binds it. Both are composed from `dir`
	// rather than written out, so each stays a spelling of the host's own path.
	dotted := parent + sep + "." + sep + filepath.Base(dir)
	for _, query := range []string{dir, dir + sep, dotted} {
		got, err := ListSessions(context.Background(), path, agent.StoredSessionQuery{WorkingDir: query})
		require.NoError(t, err)
		assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got), "query=%q", query)
	}

	// A different directory, and a prefix of the real one, both answer nothing.
	for _, query := range []string{filepath.Join(parent, "other"), parent} {
		got, err := ListSessions(context.Background(), path, agent.StoredSessionQuery{WorkingDir: query})
		require.NoError(t, err)
		assert.Empty(t, got, "query=%q must not match by prefix", query)
	}
}

func TestOpenCodeFamilySessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := agenttest.NewFixtureDB(t, path, opencodestoretest.FamilyDDL)
	for i := range 10 {
		_, err := db.Exec(
			`INSERT INTO session (id, directory, title, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
			"ses_"+string(rune('a'+i)), dir, "t", 1_000, int64(1_000+i))
		require.NoError(t, err)
	}

	got, err := ListSessions(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir, Limit: 3})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"ses_j", "ses_i", "ses_h"}, agenttest.Handles(got), "the newest three")
}

func TestOpenCodeFamilySessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := ListSessions(context.Background(),
		filepath.Join(t.TempDir(), "never-created.db"),
		agent.StoredSessionQuery{WorkingDir: testutil.NativeAbsPath("/Users/dev/project")})
	require.NoError(t, err, "a CLI the user never ran is not a failure")
	assert.Empty(t, got)
}

func TestOpenCodeFamilySessions_ForeignSchemaIsAnError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "wrong.db")
	agenttest.NewFixtureDB(t, path, `CREATE TABLE unrelated (id text)`)

	_, err := ListSessions(context.Background(), path,
		agent.StoredSessionQuery{WorkingDir: testutil.NativeAbsPath("/Users/dev/project")})
	// Reported rather than swallowed: the CALLER decides that a provider-store
	// failure degrades to the worker's own records, and it can only log what it
	// is told.
	assert.Error(t, err)
}

func TestOpenCodeFamilySessions_EmptyWorkingDirListsNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "opencode.db")
	opencodestoretest.SeedFamilyDB(t, path, testutil.NativeAbsPath("/Users/dev/project"))

	got, err := ListSessions(context.Background(), path, agent.StoredSessionQuery{WorkingDir: "  "})
	require.NoError(t, err)
	assert.Empty(t, got, "no directory means no question to answer, not every session")
}

func TestOpenCodeFamilySessions_LeavesTheStoreAlone(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "opencode.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	// The fixture is built with sqlitedb.Open, which chmods to 0600 and sets
	// WAL -- the two mutations this test exists to refuse. Put the file back in
	// the state a foreign CLI leaves it in, or the assertion compares a store
	// that is ALREADY mutated against itself and passes for either reader.
	require.NoError(t, os.Chmod(path, 0o644))

	before := statFixture(t, path)
	_, err := ListSessions(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir})
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

// importDDL adds the two tables a fork uses to mark the sessions it imported.
// One column is nullable on purpose: a row that recorded no session must not
// empty the listing.
const importDDL = `
CREATE TABLE external_import (source text, source_key text, session_id text);
CREATE TABLE claude_import (source_uuid text, session_id text);`

var forkImportExclusions = []Exclusion{
	{Table: "external_import", Column: "session_id"},
	{Table: "claude_import", Column: "session_id"},
}

func TestListSessionsExcept_DropsTheSessionsAnExclusionLists(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "fork.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	db := agenttest.NewFixtureDB(t, path, importDDL)
	for _, stmt := range []string{
		`INSERT INTO external_import VALUES ('cc', 'k1', 'ses_new')`,
		`INSERT INTO external_import VALUES ('cc', 'k2', NULL)`,
		`INSERT INTO claude_import VALUES ('u1', 'ses_no_updated')`,
		`INSERT INTO claude_import VALUES ('u2', NULL)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	got, err := ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir}, forkImportExclusions...)
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_old"}, agenttest.Handles(got),
		"both tables exclude their sessions, and a NULL id in either excludes nothing else")
}

func TestListSessionsExcept_AnAbsentTableExcludesNothing(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "fork.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)

	got, err := ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir}, forkImportExclusions...)
	require.NoError(t, err, "a release that predates the import tables still lists its sessions")
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got))
}

func TestListSessionsExcept_AnAbsentColumnExcludesNothing(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "fork.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	db := agenttest.NewFixtureDB(t, path, `CREATE TABLE external_import (source text, imported_as text)`)
	_, err := db.Exec(`INSERT INTO external_import VALUES ('cc', 'ses_new')`)
	require.NoError(t, err)

	got, err := ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir}, forkImportExclusions...)
	require.NoError(t, err, "a table whose shape changed must not fail the listing")
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got))
}

func TestListSessionsExcept_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := ListSessionsExcept(context.Background(),
		filepath.Join(t.TempDir(), "never-created.db"),
		agent.StoredSessionQuery{WorkingDir: testutil.NativeAbsPath("/Users/dev/project")},
		forkImportExclusions...)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListSessionsExcept_EmptyWorkingDirListsNothing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fork.db")
	opencodestoretest.SeedFamilyDB(t, path, testutil.NativeAbsPath("/Users/dev/project"))

	for _, workingDir := range []string{"", "  "} {
		got, err := ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: workingDir}, forkImportExclusions...)
		require.NoError(t, err, "workingDir=%q", workingDir)
		assert.Empty(t, got, "workingDir=%q", workingDir)
	}
}

// Each exclusion stands alone. A store that holds one of the two tables drops
// the sessions of that one, and the absent one excludes nothing.
func TestListSessionsExcept_AppliesThePresentExclusionBesideAnAbsentOne(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "fork.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	db := agenttest.NewFixtureDB(t, path, `CREATE TABLE claude_import (source_uuid text, session_id text)`)
	_, err := db.Exec(`INSERT INTO claude_import VALUES ('u1', 'ses_new')`)
	require.NoError(t, err)

	got, err := ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir}, forkImportExclusions...)
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_old", "ses_no_updated"}, agenttest.Handles(got))
}

// The limit applies to the sessions that remain. An excluded session takes no
// place in the answer, so the newest session that remains still fills it.
func TestListSessionsExcept_LimitsTheSessionsThatRemain(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "fork.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	db := agenttest.NewFixtureDB(t, path, importDDL)
	_, err := db.Exec(`INSERT INTO external_import VALUES ('cc', 'k1', 'ses_new')`)
	require.NoError(t, err)

	got, err := ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir, Limit: 1}, forkImportExclusions...)
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_old"}, agenttest.Handles(got))
}

// A file that is not a SQLite database is a store that LeapMux cannot read.
// The caller decides what a failure of a provider store degrades to, so the
// failure is reported, not swallowed.
func TestListSessionsExcept_UnreadableStoreIsAnError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fork.db")
	agenttest.WriteFixtureFile(t, path, "this is not a SQLite database, and it is long enough to hold a header")

	_, err := ListSessionsExcept(context.Background(), path,
		agent.StoredSessionQuery{WorkingDir: testutil.NativeAbsPath("/Users/dev/project")}, forkImportExclusions...)
	assert.Error(t, err)
}

// The check for each exclusion opens the store a second time. That read must
// leave another program's store alone, as the listing itself does.
func TestListSessionsExcept_LeavesTheStoreAlone(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "fork.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	agenttest.NewFixtureDB(t, path, importDDL)
	// See TestOpenCodeFamilySessions_LeavesTheStoreAlone: the fixture writer
	// changes the mode, so the file goes back to the state that a foreign CLI
	// leaves it in.
	require.NoError(t, os.Chmod(path, 0o644))

	before := statFixture(t, path)
	_, err := ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir}, forkImportExclusions...)
	require.NoError(t, err)
	assert.Equal(t, before, statFixture(t, path), "reading another program's store must not change it")
}

func TestListSessionsExcept_WithNoExclusionMatchesListSessions(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "opencode.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	q := agent.StoredSessionQuery{WorkingDir: dir}

	want, err := ListSessions(context.Background(), path, q)
	require.NoError(t, err)
	got, err := ListSessionsExcept(context.Background(), path, q)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestListSessionsExcept_RefusesAnIdentifierThatIsNotPlain(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	path := filepath.Join(t.TempDir(), "opencode.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)

	for _, exclusion := range []Exclusion{
		{Table: "x; DROP TABLE session", Column: "session_id"},
		{Table: "external_import", Column: "session_id) OR (1"},
		{Table: "", Column: "session_id"},
	} {
		assert.Panics(t, func() {
			_, _ = ListSessionsExcept(context.Background(), path, agent.StoredSessionQuery{WorkingDir: dir}, exclusion)
		}, "%+v", exclusion)
	}
}
