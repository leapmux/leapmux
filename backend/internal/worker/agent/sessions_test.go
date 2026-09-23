package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSortAndCapSessions(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	t.Run("orders newest first", func(t *testing.T) {
		t.Parallel()
		got := agent.SortAndCapSessions([]agent.StoredSession{
			{Handle: "old", UpdatedAt: base.Add(-2 * time.Hour)},
			{Handle: "new", UpdatedAt: base},
			{Handle: "mid", UpdatedAt: base.Add(-time.Hour)},
		}, 0)
		assert.Equal(t, []string{"new", "mid", "old"}, agenttest.Handles(got))
	})

	t.Run("breaks a timestamp tie by handle", func(t *testing.T) {
		t.Parallel()
		got := agent.SortAndCapSessions([]agent.StoredSession{
			{Handle: "b", UpdatedAt: base},
			{Handle: "a", UpdatedAt: base},
		}, 0)
		assert.Equal(t, []string{"a", "b"}, agenttest.Handles(got),
			"one stable order, so two calls cannot disagree")
	})

	t.Run("sorts an unknown time last, not to the epoch", func(t *testing.T) {
		t.Parallel()
		got := agent.SortAndCapSessions([]agent.StoredSession{
			{Handle: "unknown"},
			{Handle: "ancient", UpdatedAt: time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)},
		}, 0)
		assert.Equal(t, []string{"ancient", "unknown"}, agenttest.Handles(got))
	})

	t.Run("drops a record with no handle", func(t *testing.T) {
		t.Parallel()
		got := agent.SortAndCapSessions([]agent.StoredSession{
			{Handle: "", Title: "unresumable", UpdatedAt: base},
			{Handle: "  ", UpdatedAt: base},
			{Handle: "real", UpdatedAt: base.Add(-time.Hour)},
		}, 0)
		assert.Equal(t, []string{"real"}, agenttest.Handles(got))
	})

	t.Run("caps to the limit, keeping the newest", func(t *testing.T) {
		t.Parallel()
		got := agent.SortAndCapSessions([]agent.StoredSession{
			{Handle: "a", UpdatedAt: base.Add(-3 * time.Hour)},
			{Handle: "b", UpdatedAt: base.Add(-time.Hour)},
			{Handle: "c", UpdatedAt: base.Add(-2 * time.Hour)},
		}, 2)
		assert.Equal(t, []string{"b", "c"}, agenttest.Handles(got))
	})

	t.Run("keeps every record when the limit is not positive or not reached", func(t *testing.T) {
		t.Parallel()
		sessions := []agent.StoredSession{
			{Handle: "a", UpdatedAt: base},
			{Handle: "b", UpdatedAt: base.Add(-time.Hour)},
		}
		for _, limit := range []int{-1, 0, 2, 3} {
			assert.Equalf(t, []string{"a", "b"}, agenttest.Handles(agent.SortAndCapSessions(sessions, limit)),
				"limit %d", limit)
		}
	})

	t.Run("does not rewrite the slice of the caller", func(t *testing.T) {
		t.Parallel()
		sessions := []agent.StoredSession{
			{Handle: "", UpdatedAt: base},
			{Handle: "old", UpdatedAt: base.Add(-time.Hour)},
			{Handle: "new", UpdatedAt: base},
		}
		want := append([]agent.StoredSession(nil), sessions...)
		got := agent.SortAndCapSessions(sessions, 1)
		assert.Equal(t, []string{"new"}, agenttest.Handles(got))
		assert.Equal(t, want, sessions, "a filter in place would overwrite the array that the caller passed")
	})

	t.Run("handles the empty input", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, agent.SortAndCapSessions(nil, 10))
	})
}

func TestStoredSessionQueryDefaults(t *testing.T) {
	t.Parallel()

	q := agent.StoredSessionQuery{Getenv: agenttest.FixtureEnv(map[string]string{"FOO": "bar"})}
	assert.Equal(t, "bar", q.Env("FOO"))
	assert.Empty(t, q.Env("MISSING"))
	assert.Equal(t, agent.DefaultStoredSessionLimit, q.EffectiveLimit())
	assert.Equal(t, agent.DefaultStoredSessionLimit, agent.StoredSessionQuery{Limit: -3}.EffectiveLimit(),
		"a negative limit is no limit of the caller's own")
	assert.Equal(t, 7, agent.StoredSessionQuery{Limit: 7}.EffectiveLimit())

	assert.Equal(t, "/home/u", agent.StoredSessionQuery{HomeDir: "/home/u"}.Home())

	// XDG_DATA_HOME wins; otherwise `~/.local/share` on EVERY platform,
	// because the CLIs this serves use the xdg-basedir package rather than the
	// platform-native layout.
	xdg := agent.StoredSessionQuery{HomeDir: "/home/u", Getenv: agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": "/xdg"})}
	assert.Equal(t, "/xdg", xdg.XDGDataHome())
	plain := agent.StoredSessionQuery{HomeDir: "/home/u", Getenv: agenttest.FixtureEnv(nil)}
	assert.Equal(t, filepath.Join("/home/u", ".local", "share"), plain.XDGDataHome())
	blank := agent.StoredSessionQuery{HomeDir: "/home/u", Getenv: agenttest.FixtureEnv(map[string]string{"XDG_DATA_HOME": "  "})}
	assert.Equal(t, filepath.Join("/home/u", ".local", "share"), blank.XDGDataHome(),
		"a blank XDG_DATA_HOME is unset, as xdg-basedir reads it")
}

// With no seam, the query reads the environment and the home directory of the
// worker process.
func TestStoredSessionQueryReadsTheProcessWithoutASeam(t *testing.T) {
	t.Setenv("LEAPMUX_TEST_STORED_SESSION_QUERY", "from-process")
	q := agent.StoredSessionQuery{}
	assert.Equal(t, "from-process", q.Env("LEAPMUX_TEST_STORED_SESSION_QUERY"))

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, home, q.Home())
}
