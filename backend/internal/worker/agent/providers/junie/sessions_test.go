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
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
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

func TestJunieSubagentFromToolCallMapsTheSpawn(t *testing.T) {
	t.Parallel()
	tc := acp.ToolCallEnvelope{
		ToolCallID: "spawn-1",
		Title:      "Explore the tree",
		RawInput:   json.RawMessage(`{"agent":"general_purpose","extraContext":"look around","handle":""}`),
	}
	obs := junieSubagentFromToolCall(tc)
	require.NotNil(t, obs)
	assert.Equal(t, "spawn-1", obs.RowKey)
	assert.Equal(t, "look around", obs.Prompt)
}

func TestJunieSubagentFromToolCallIgnoresOtherTools(t *testing.T) {
	t.Parallel()
	assert.Nil(t, junieSubagentFromToolCall(acp.ToolCallEnvelope{
		ToolCallID: "cmd-1",
		RawInput:   json.RawMessage(`{"command":"ls","cwd":"/work"}`),
	}))
	assert.Nil(t, junieSubagentFromToolCall(acp.ToolCallEnvelope{ToolCallID: "bare"}))
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
