package junie

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

func TestJunieReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		writeJunieIndex(t, filepath.Join(home, ".junie"), []junieIndexRecord{{
			SessionID: "session-260925-201309-19zf",
			CreatedAt: 1790334789400, UpdatedAt: 1790334807396,
			Project: dir, TaskName: "Create a Friendly Greeting Message",
		}})
		return "session-260925-201309-19zf"
	})
}

func TestJunieStoredSessionsUsesProcessEnvWhenGetenvIsUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUNIE_HOME", home)
	writeJunieIndex(t, home, []junieIndexRecord{{
		SessionID: "resumable", Project: "/work", TaskName: "Resume", UpdatedAt: 1000,
	}})

	got, err := junieStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work",
		HomeDir:    t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"resumable"}, agenttest.Handles(got))
}

func TestJunieStoredSessionsFiltersByWorkingDir(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeJunieIndex(t, home, []junieIndexRecord{
		{SessionID: "mine", Project: "/work/mine", TaskName: "Mine", UpdatedAt: 1000},
		{SessionID: "other", Project: "/work/other", TaskName: "Other", UpdatedAt: 2000},
	})
	got, err := junieStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work/mine",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"JUNIE_HOME": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"mine"}, agenttest.Handles(got))
}

func TestJunieStoredSessionsSortsNewestFirst(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	writeJunieIndex(t, home, []junieIndexRecord{
		{SessionID: "older", Project: "/work", TaskName: "Older", UpdatedAt: 1000},
		{SessionID: "newer", Project: "/work", TaskName: "Newer", UpdatedAt: 2000},
	})
	got, err := junieStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"JUNIE_HOME": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"newer", "older"}, agenttest.Handles(got))
}

func TestJunieStoredSessionsSkipsMalformedLines(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessions, 0o755))
	body := `{"sessionId":"good","projectDir":"/work","taskName":"Good","updatedAt":1000}` + "\n" +
		`{"sessionId":` + "\n" +
		`{"sessionId":"","projectDir":"/work"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "index.jsonl"), []byte(body), 0o644))

	got, err := junieStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: "/work",
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{"JUNIE_HOME": home}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"good"}, agenttest.Handles(got))
}

func TestJunieTimestampMillis(t *testing.T) {
	t.Parallel()
	want := time.UnixMilli(1790334807396).UTC()
	assert.Equal(t, want, junieTimestampMillis(1790334807396, 1790334789400))
	assert.Equal(t, time.UnixMilli(1790334789400).UTC(), junieTimestampMillis(0, 1790334789400))
	assert.True(t, junieTimestampMillis(0, 0).IsZero())
}

// writeJunieIndex seeds `sessions/index.jsonl` under the Junie home.
func writeJunieIndex(t *testing.T, home string, records []junieIndexRecord) {
	t.Helper()
	sessions := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessions, 0o755))
	var body []byte
	for _, record := range records {
		raw, err := json.Marshal(record)
		require.NoError(t, err)
		body = append(body, raw...)
		body = append(body, '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "index.jsonl"), body, 0o644))
}
