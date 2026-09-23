package kilo

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
	q := agent.StoredSessionQuery{Getenv: agenttest.FixtureEnv(map[string]string{"KILO_DB": "beta.db"})}
	assert.Empty(t, kiloDBPath(q), "a bare override name has nothing to resolve against")
}

func TestKiloDBPath(t *testing.T) {
	t.Parallel()

	t.Run("prefers the current name when it exists", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		agenttest.WriteFixtureFile(t, filepath.Join(data, "kilo", "kilo.db"), "")
		agenttest.WriteFixtureFile(t, filepath.Join(data, "kilo", "opencode.db"), "")

		q := agent.StoredSessionQuery{HomeDir: testutil.NativeAbsPath("/home/dev"), Getenv: agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
		assert.Equal(t, filepath.Join(data, "kilo", "kilo.db"), kiloDBPath(q),
			"a machine holding both names must read the live store")
	})

	t.Run("falls back to the pre-fork name", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		agenttest.WriteFixtureFile(t, filepath.Join(data, "kilo", "opencode.db"), "")

		q := agent.StoredSessionQuery{HomeDir: testutil.NativeAbsPath("/home/dev"), Getenv: agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
		assert.Equal(t, filepath.Join(data, "kilo", "opencode.db"), kiloDBPath(q))
	})

	t.Run("names the current file when neither exists", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		q := agent.StoredSessionQuery{HomeDir: testutil.NativeAbsPath("/home/dev"), Getenv: agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
		assert.Equal(t, filepath.Join(data, "kilo", "kilo.db"), kiloDBPath(q))
	})

	t.Run("honours KILO_DB", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		// Absolute for the HOST, because that is the question sessionstore.OverridePath
		// asks the override.
		custom := testutil.NativeAbsPath("/custom/kilo.db")
		abs := agent.StoredSessionQuery{HomeDir: testutil.NativeAbsPath("/home/dev"), Getenv: agenttest.FixtureEnv(map[string]string{
			"XDG_DATA_HOME": data, "KILO_DB": custom,
		})}
		assert.Equal(t, custom, kiloDBPath(abs))

		rel := agent.StoredSessionQuery{HomeDir: testutil.NativeAbsPath("/home/dev"), Getenv: agenttest.FixtureEnv(map[string]string{
			"XDG_DATA_HOME": data, "KILO_DB": "beta.db",
		})}
		assert.Equal(t, filepath.Join(data, "kilo", "beta.db"), kiloDBPath(rel))
	})

	t.Run("defaults to the XDG data directory on every platform", func(t *testing.T) {
		t.Parallel()
		home := testutil.NativeAbsPath("/home/dev")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, ".local", "share", "kilo", "kilo.db"), kiloDBPath(q))
	})
}

func TestKiloStoredSessions_EndToEnd(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	data := t.TempDir()
	opencodestoretest.SeedFamilyDB(t, filepath.Join(data, "kilo", "kilo.db"), dir)

	got, err := kiloStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    testutil.NativeAbsPath("/unused"),
		Getenv:     agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": data}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got),
		"Kilo ships OpenCode's table, so the shared reader answers it unchanged")
}

func TestKiloReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		opencodestoretest.SeedFamilyDB(t, filepath.Join(home, ".local", "share", "kilo", "kilo.db"), dir)
		return "ses_new"
	})
}
