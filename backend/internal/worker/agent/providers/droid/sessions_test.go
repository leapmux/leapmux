package droid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestDroidStoredSessionsUnderFactoryHomeOverride(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	work := filepath.Join(home, "workspace")
	dir := filepath.Join(home, ".factory", "sessions", droidSanitizeCwd(work))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	const sessionID = "e2b678f5-0bef-4f1f-8a10-39c542ea031d"
	require.NoError(t, os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(
		`{"type":"session_start","id":"`+sessionID+`","title":"Native Droid session","cwd":"`+work+`"}`+"\n",
	), 0o600))

	sessions, err := droidStoredSessions(t.Context(), agent.StoredSessionQuery{
		WorkingDir: work,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(map[string]string{droidHomeEnv: home}),
	})
	require.NoError(t, err)
	require.Len(t, sessions, 1, "the override is the parent directory of .factory")
	assert.Equal(t, sessionID, sessions[0].Handle)
	assert.Equal(t, "Native Droid session", sessions[0].Title)
}
