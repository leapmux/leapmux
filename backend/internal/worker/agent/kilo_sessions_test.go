package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The branch the shared resolver exists to keep distinguishable: the override
// is SET, but the data directory it would resolve against is not. Falling
// through to the default name under that same unresolvable directory would be a
// second empty answer reached by accident.
//
// No t.Parallel: t.Setenv panics under it, and an unresolvable home needs the
// process environment rather than the query's Getenv seam.
func TestKiloDBPath_OverrideWithNoDataDirIsEmpty(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	q := StoredSessionQuery{Getenv: fixtureEnv(map[string]string{"KILO_DB": "beta.db"})}
	assert.Empty(t, kiloDBPath(q), "a bare override name has nothing to resolve against")
}

func TestKiloDBPath(t *testing.T) {
	t.Parallel()

	t.Run("prefers the current name when it exists", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		writeFixtureFile(t, filepath.Join(data, "kilo", "kilo.db"), "")
		writeFixtureFile(t, filepath.Join(data, "kilo", "opencode.db"), "")

		q := StoredSessionQuery{HomeDir: "/home/dev", Getenv: fixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
		assert.Equal(t, filepath.Join(data, "kilo", "kilo.db"), kiloDBPath(q),
			"a machine holding both names must read the live store")
	})

	t.Run("falls back to the pre-fork name", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		writeFixtureFile(t, filepath.Join(data, "kilo", "opencode.db"), "")

		q := StoredSessionQuery{HomeDir: "/home/dev", Getenv: fixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
		assert.Equal(t, filepath.Join(data, "kilo", "opencode.db"), kiloDBPath(q))
	})

	t.Run("names the current file when neither exists", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		q := StoredSessionQuery{HomeDir: "/home/dev", Getenv: fixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
		assert.Equal(t, filepath.Join(data, "kilo", "kilo.db"), kiloDBPath(q))
	})

	t.Run("honours KILO_DB", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		abs := StoredSessionQuery{HomeDir: "/home/dev", Getenv: fixtureEnv(map[string]string{
			"XDG_DATA_HOME": data, "KILO_DB": "/custom/kilo.db",
		})}
		assert.Equal(t, "/custom/kilo.db", kiloDBPath(abs))

		rel := StoredSessionQuery{HomeDir: "/home/dev", Getenv: fixtureEnv(map[string]string{
			"XDG_DATA_HOME": data, "KILO_DB": "beta.db",
		})}
		assert.Equal(t, filepath.Join(data, "kilo", "beta.db"), kiloDBPath(rel))
	})

	t.Run("defaults to the XDG data directory on every platform", func(t *testing.T) {
		t.Parallel()
		q := StoredSessionQuery{HomeDir: "/home/dev", Getenv: fixtureEnv(nil)}
		assert.Equal(t, filepath.Join("/home/dev", ".local", "share", "kilo", "kilo.db"), kiloDBPath(q))
	})
}

func TestKiloStoredSessions_EndToEnd(t *testing.T) {
	t.Parallel()
	dir := "/Users/dev/project"
	data := t.TempDir()
	seedOpenCodeFamilyDB(t, filepath.Join(data, "kilo", "kilo.db"), dir)

	got, err := kiloStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    "/unused",
		Getenv:     fixtureEnv(map[string]string{"XDG_DATA_HOME": data}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, handlesOf(got),
		"Kilo ships OpenCode's table, so the shared reader answers it unchanged")
}
