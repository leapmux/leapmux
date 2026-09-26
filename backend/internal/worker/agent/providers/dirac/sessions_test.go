package dirac

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestDiracReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		writeDiracHistory(t, filepath.Join(home, ".dirac"), []diracHistoryRecord{{
			ID: "1790338601902", ULID: "6e7e52e6-e009-457f-961f-48e43bcc2cf7",
			TS: 1790338601902, Task: "Arithmetic", CWD: dir,
		}})
		return "6e7e52e6-e009-457f-961f-48e43bcc2cf7"
	})
}

func TestDiracStoredSessionsFiltersByWorkingDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeDiracHistory(t, home, []diracHistoryRecord{
		{ID: "1", ULID: "mine", TS: 1000, Task: "Mine", CWD: "/work/mine"},
		{ID: "2", ULID: "other", TS: 2000, Task: "Other", CWD: "/work/other"},
	})
	got, err := diracStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work/mine",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"DIRAC_DIR": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"mine"}, agenttest.Handles(got))
}

func TestDiracStoredSessionsSortsNewestFirst(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeDiracHistory(t, home, []diracHistoryRecord{
		{ID: "1", ULID: "older", TS: 1000, Task: "Older", CWD: "/work"},
		{ID: "2", ULID: "newer", TS: 2000, Task: "Newer", CWD: "/work"},
	})
	got, err := diracStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"DIRAC_DIR": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"newer", "older"}, agenttest.Handles(got))
}

func TestDiracStoredSessionsSkipsRowsWithNoULID(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeDiracHistory(t, home, []diracHistoryRecord{
		{ID: "1", Task: "No handle", CWD: "/work"},
		{ID: "2", ULID: "real", TS: 1000, Task: "Real", CWD: "/work"},
	})
	got, err := diracStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"DIRAC_DIR": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"real"}, agenttest.Handles(got))
}

func TestDiracTimestampMillis(t *testing.T) {
	t.Parallel()
	want := time.UnixMilli(1790338601902).UTC()
	assert.Equal(t, want, diracTimestampMillis(1790338601902))
	assert.True(t, diracTimestampMillis(0).IsZero())
	assert.True(t, diracTimestampMillis(-1).IsZero())
}

// writeDiracHistory seeds `data/state/taskHistory.json` under the Dirac home.
func writeDiracHistory(t *testing.T, home string, records []diracHistoryRecord) {
	t.Helper()
	state := filepath.Join(home, "data", "state")
	require.NoError(t, os.MkdirAll(state, 0o755))
	raw, err := json.Marshal(records)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(state, "taskHistory.json"), raw, 0o644))
}
