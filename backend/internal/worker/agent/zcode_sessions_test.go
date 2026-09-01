package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":"/moved/zcode"}}`)

		q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(nil)}
		assert.Equal(t, filepath.Join("/moved/zcode", "cli", "db", "db.sqlite"), zcodeSessionDBPath(q))
	})

	t.Run("ZCODE_STORAGE_DIR wins over the configuration", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":"/moved/zcode"}}`)

		q := StoredSessionQuery{HomeDir: home, Getenv: fixtureEnv(map[string]string{"ZCODE_STORAGE_DIR": "/env/zcode"})}
		assert.Equal(t, filepath.Join("/env/zcode", "cli", "db", "db.sqlite"), zcodeSessionDBPath(q))
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
	dir := "/Users/dev/project"
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
