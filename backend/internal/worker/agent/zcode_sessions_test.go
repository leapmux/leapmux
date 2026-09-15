package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeStorePathsFor resolves the store location for a home directory with no
// environment override.
func zcodeStorePathsFor(home string) zcodeToolStoreLocation {
	return zcodeToolStorePaths(StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)})
}

// writeZCodeSessionDBPath states storage.sessionDbPath in the CLI configuration.
func writeZCodeSessionDBPath(t *testing.T, home, databasePath string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
		`{"storage":{"sessionDbPath":`+fixtureJSONString(databasePath)+`}}`)
}

func TestZCodeToolStorePaths(t *testing.T) {
	t.Parallel()

	t.Run("the stock layout keeps the database and the artifacts as siblings", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".zcode", "cli", "artifacts"), 0o700))
		location := zcodeStorePathsFor(home)
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), location.databasePath)
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), location.artifactRoot,
			"a live installation holds cli/db/db.sqlite beside cli/artifacts")
	})

	t.Run("an absent artifact directory still reports the stock path", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(home).artifactRoot,
			"the fallback is the grandparent of the database, which is the same cli directory")
	})

	t.Run("sessionDbPath moves the artifact root with the database", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(moved, "cli", "artifacts"), 0o700))
		writeZCodeSessionDBPath(t, home, filepath.Join(moved, "cli", "db", "db.sqlite"))
		location := zcodeStorePathsFor(home)
		assert.Equal(t, filepath.Join(moved, "cli", "db", "db.sqlite"), location.databasePath)
		assert.Equal(t, filepath.Join(moved, "cli", "artifacts"), location.artifactRoot,
			"the setting states one file, so the artifacts follow the layout around it")
	})

	t.Run("an existing storage-root artifact directory stays the first choice", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".zcode", "cli", "artifacts"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(moved, "cli", "artifacts"), 0o700))
		writeZCodeSessionDBPath(t, home, filepath.Join(moved, "cli", "db", "db.sqlite"))
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(home).artifactRoot)
	})

	t.Run("a regular file at the artifact path is not an artifact directory", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := t.TempDir()
		writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "artifacts"), "not a directory")
		require.NoError(t, os.MkdirAll(filepath.Join(moved, "cli", "artifacts"), 0o700))
		writeZCodeSessionDBPath(t, home, filepath.Join(moved, "cli", "db", "db.sqlite"))
		assert.Equal(t, filepath.Join(moved, "cli", "artifacts"), zcodeStorePathsFor(home).artifactRoot)
	})

	t.Run("a database with no grandparent directory keeps the storage-root path", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeZCodeSessionDBPath(t, home, "db.sqlite")
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(home).artifactRoot)
	})
}

func TestZCodeArtifactRootBesideDatabase(t *testing.T) {
	t.Parallel()
	assert.Empty(t, zcodeArtifactRootBesideDatabase(""))
	assert.Empty(t, zcodeArtifactRootBesideDatabase("db.sqlite"), "a bare file name has no grandparent directory")
	root := absPath("moved", "zcode", "cli")
	assert.Equal(t, filepath.Join(root, "artifacts"),
		zcodeArtifactRootBesideDatabase(filepath.Join(root, "db", "db.sqlite")))
}

func TestZCodeSessionDBPath(t *testing.T) {
	t.Parallel()

	t.Run("defaults to the stock installation", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), zcodeSessionDBPath(q))
	})

	t.Run("follows storage.sessionDbPath from the CLI configuration", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":"~/.zcode","sessionDbPath":"~/elsewhere/db.sqlite"}}`)

		q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, "elsewhere", "db.sqlite"), zcodeSessionDBPath(q),
			"the explicit database path wins, and its leading ~ expands")
	})

	t.Run("follows storage.dir when no database path is stated", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := absPath("moved", "zcode")
		writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":`+fixtureJSONString(moved)+`}}`)

		q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
		assert.Equal(t, filepath.Join(moved, "cli", "db", "db.sqlite"), zcodeSessionDBPath(q))
	})

	t.Run("ZCODE_STORAGE_DIR wins over the configuration", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":`+fixtureJSONString(absPath("moved", "zcode"))+`}}`)

		fromEnv := absPath("env", "zcode")
		q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"ZCODE_STORAGE_DIR": fromEnv})}
		assert.Equal(t, filepath.Join(fromEnv, "cli", "db", "db.sqlite"), zcodeSessionDBPath(q))
	})

	t.Run("survives a malformed configuration", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"), "{ not json")

		q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), zcodeSessionDBPath(q),
			"an unreadable configuration falls back to the stock layout rather than reporting no sessions")
	})
}

func TestZCodeStoredSessions_EndToEnd(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	seedOpenCodeFamilyDB(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), dir)

	got, err := zcodeProvider{}.ListStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    home,
		Getenv:     fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, handlesOf(got),
		"ZCode's session table is OpenCode's, so parent_id already excludes its subagent_child rows")
}
