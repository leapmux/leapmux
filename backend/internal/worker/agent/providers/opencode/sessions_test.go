package opencode

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
	q := agent.StoredSessionQuery{Getenv: agenttest.FixtureEnv(map[string]string{"OPENCODE_DB": "beta.db"})}
	assert.Empty(t, opencodeDBPath(q), "a bare override name has nothing to resolve against")
}

func TestOpencodeDBPath(t *testing.T) {
	t.Parallel()
	home := testutil.NativeAbsPath("/home/dev")

	base := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
	assert.Equal(t, filepath.Join(home, ".local", "share", "opencode", "opencode.db"), opencodeDBPath(base))

	data := testutil.NativeAbsPath("/data")
	xdg := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": data})}
	assert.Equal(t, filepath.Join(data, "opencode", "opencode.db"), opencodeDBPath(xdg))

	// OPENCODE_DB accepts host-absolute paths and paths relative to the data directory.
	custom := testutil.NativeAbsPath("/custom/store.db")
	abs := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(map[string]string{"OPENCODE_DB": custom})}
	assert.Equal(t, custom, opencodeDBPath(abs))

	rel := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(map[string]string{"OPENCODE_DB": "beta.db"})}
	assert.Equal(t, filepath.Join(home, ".local", "share", "opencode", "beta.db"), opencodeDBPath(rel))
}

func TestOpencodeStoredSessions_EndToEnd(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	data := t.TempDir()
	seedOpenCodeStore(t, filepath.Join(data, "opencode", "opencode.db"), dir)

	got, err := opencodeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    testutil.NativeAbsPath("/unused"),
		Getenv:     agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": data}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got))
}

func TestOpenCodeReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		seedOpenCodeStore(t, filepath.Join(home, ".local", "share", "opencode", "opencode.db"), dir)
		return "ses_new"
	})
}

func seedOpenCodeStore(t *testing.T, path, dir string) {
	t.Helper()
	// The queried columns are shared, but OpenCode v2 keeps them in session_v2.
	opencodestoretest.SeedFamilyDB(t, path, dir)
	agenttest.NewFixtureDB(t, path, `ALTER TABLE session RENAME TO session_v2`)
}

func TestOpenCodeDBPathPreservesNativeOverrideSpaces(t *testing.T) {
	t.Parallel()
	data := testutil.NativeAbsPath("/data")
	for _, value := range []string{" custom.db", "custom.db ", "   ", "nested/ custom.db"} {
		query := agent.StoredSessionQuery{EnvEntries: []string{"XDG_DATA_HOME=" + data, "OPENCODE_DB=" + value}}
		assert.Equal(t, filepath.Join(data, "opencode", value), opencodeDBPath(query), "override %q", value)
	}
}

func TestOpenCodeDBPathDoesNotSubstituteTheDefaultForANonPersistentOverride(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	dir := testutil.NativeAbsPath("/project")
	seedOpenCodeStore(t, filepath.Join(data, "opencode", "opencode.db"), dir)
	for _, entries := range [][]string{
		{"OPENCODE_DB="},
		{"OPENCODE_DB=:memory:"},
		{"OPENCODE_DB=another.db", "OPENCODE_DB="},
	} {
		query := agent.StoredSessionQuery{WorkingDir: dir, EnvEntries: append([]string{"XDG_DATA_HOME=" + data}, entries...)}
		assert.Empty(t, opencodeDBPath(query), "override entries: %v", entries)
		records, err := opencodeStoredSessions(t.Context(), query)
		require.NoError(t, err)
		assert.Empty(t, records, "the picker must not read the normal database for an explicit nonpersistent override")
	}
}

func TestOpenCodeStoredSessionsReadTheUntrimmedNativeDatabasePath(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	dir := testutil.NativeAbsPath("/project")
	path := filepath.Join(data, "opencode", " custom.db")
	opencodestoretest.SeedFamilyDB(t, path, dir)
	agenttest.NewFixtureDB(t, path, `ALTER TABLE session RENAME TO session_v2`)
	records, err := opencodeStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: dir, EnvEntries: []string{"XDG_DATA_HOME=" + data, "OPENCODE_DB= custom.db"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(records))
}
