package mimo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore/opencodestoretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mimoStockDB is the database of a MiMo installation with no override.
func mimoStockDB(home string) string {
	return filepath.Join(home, ".local", "share", mimoAppName, mimoDBName)
}

func TestMiMoDBPath(t *testing.T) {
	t.Parallel()

	home := testutil.NativeAbsPath("/home/dev")
	query := func(env map[string]string) agent.StoredSessionQuery {
		return agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(env)}
	}
	mimoHome := testutil.NativeAbsPath("/opt/mimo")
	xdg := testutil.NativeAbsPath("/xdg/data")
	absoluteDB := testutil.NativeAbsPath("/elsewhere/sessions.db")

	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "the stock layout", want: mimoStockDB(home)},
		{name: "XDG_DATA_HOME moves the data directory", env: map[string]string{"XDG_DATA_HOME": xdg},
			want: filepath.Join(xdg, mimoAppName, mimoDBName)},
		{name: "MIMOCODE_HOME wins over XDG_DATA_HOME", env: map[string]string{"MIMOCODE_HOME": mimoHome, "XDG_DATA_HOME": xdg},
			want: filepath.Join(mimoHome, "data", mimoDBName)},
		{name: "a relative MIMOCODE_HOME has no store, because MiMo refuses to start with it",
			env: map[string]string{"MIMOCODE_HOME": "relative/home"}},
		{name: "a blank MIMOCODE_HOME states no home", env: map[string]string{"MIMOCODE_HOME": "  ", "XDG_DATA_HOME": xdg},
			want: filepath.Join(xdg, mimoAppName, mimoDBName)},
		{name: "an in-memory MIMOCODE_DB with spaces around it has no store", env: map[string]string{"MIMOCODE_DB": " :memory: "}},
		{name: "an absolute MIMOCODE_DB wins", env: map[string]string{"MIMOCODE_DB": absoluteDB, "MIMOCODE_HOME": mimoHome},
			want: absoluteDB},
		{name: "a relative MIMOCODE_DB names a file in the data directory", env: map[string]string{"MIMOCODE_DB": "custom.db"},
			want: filepath.Join(home, ".local", "share", mimoAppName, "custom.db")},
		{name: "an in-memory MIMOCODE_DB has no store", env: map[string]string{"MIMOCODE_DB": ":memory:"}},
		{name: "a relative MIMOCODE_DB with no data directory has no store",
			env: map[string]string{"MIMOCODE_DB": "custom.db", "MIMOCODE_HOME": "relative/home"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, mimoDBPath(query(tc.env)))
		})
	}
}

// importDDL adds the tables in which MiMo marks the sessions it imported:
// external_import in current releases, and claude_import in the releases
// before the table was renamed.
const importDDL = `
CREATE TABLE external_import (source text NOT NULL, source_key text NOT NULL, session_id text NOT NULL);
CREATE TABLE claude_import (source_uuid text PRIMARY KEY NOT NULL, session_id text NOT NULL);`

func TestMiMoStoredSessionsHidesImportedSessions(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	path := mimoStockDB(home)
	opencodestoretest.SeedFamilyDB(t, path, dir)
	db := agenttest.NewFixtureDB(t, path, importDDL)
	for _, stmt := range []string{
		`INSERT INTO external_import VALUES ('cc', 'k1', 'ses_old')`,
		`INSERT INTO claude_import VALUES ('u1', 'ses_no_updated')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}

	got, err := mimoProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new"}, agenttest.Handles(got),
		"a session MiMo imported from another program stays in that program's picker")
}

func TestMiMoStoredSessionsWithoutImportTables(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	opencodestoretest.SeedFamilyDB(t, mimoStockDB(home), dir)

	got, err := mimoProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got),
		"a store that never imported a session lists every session of the directory")
}

func TestMiMoStoredSessionsAbsentStore(t *testing.T) {
	t.Parallel()
	got, err := mimoProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: testutil.NativeAbsPath("/Users/dev/project"),
		HomeDir:    t.TempDir(),
		Getenv:     agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMiMoStoredSessionsInMemoryStore(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	opencodestoretest.SeedFamilyDB(t, mimoStockDB(home), dir)

	got, err := mimoProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"MIMOCODE_DB": ":memory:"}),
	})
	require.NoError(t, err)
	assert.Empty(t, got, "an in-memory store holds nothing another process can read")
}

func TestMiMoReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		opencodestoretest.SeedFamilyDB(t, mimoStockDB(home), dir)
		return "ses_new"
	})
}
