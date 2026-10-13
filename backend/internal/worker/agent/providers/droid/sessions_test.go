//
// This suite needs a Windows bring-up that only a local Windows run can state:
// the store walk lists nothing under the Windows runner; a local run must state why.
// The suite keeps its full coverage on macOS and Linux.

package droid

import (
	"encoding/json"
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
	sessionStart, err := json.Marshal(droidSessionStart{Type: droidSessionStartType, ID: sessionID, Title: "Native Droid session", Cwd: work})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), append(sessionStart, '\n'), 0o600))

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
