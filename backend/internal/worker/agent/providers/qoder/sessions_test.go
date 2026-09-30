package qoder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestQoderProjectSlugMatchesTheInstalledCLI(t *testing.T) {
	t.Parallel()
	longPathHash := "corjik"
	if filepath.Separator == '\\' {
		longPathHash = "cy58vg"
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/repo/.tmp/my_project", want: "-repo--tmp-my-project"},
		{path: "/repo/space and#hash", want: "-repo-space-and-hash"},
		{path: "/repo/😀", want: "-repo---"},
		{path: "/repo/" + strings.Repeat("a", 194), want: "-repo-" + strings.Repeat("a", 194)},
		{path: "/repo/" + strings.Repeat("a", 195), want: "-repo-" + strings.Repeat("a", 194) + "-" + longPathHash},
	} {
		assert.Equal(t, tc.want, qoderProjectSlug(tc.path), tc.path)
	}
}

func TestQoderStoredSessionsFindsNativeDotPath(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	workingDir := filepath.FromSlash("/repo/.tmp/my_project")
	const nativeSlug = "-repo--tmp-my-project"
	const sessionID = "3c33fac3-77c3-4511-8aba-502ed720fdb8"
	projectDir := filepath.Join(home, ".qoder", "projects", nativeSlug)
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	transcript := filepath.Join(projectDir, sessionID+".jsonl")
	cwdJSON, err := json.Marshal(workingDir)
	require.NoError(t, err)
	content := `{"type":"user","cwd":` + string(cwdJSON) + `,"sessionId":"` + sessionID + `","message":{"role":"user","content":"hello qoder"}}` + "\n"
	require.NoError(t, os.WriteFile(transcript, []byte(content), 0o600))
	sessions, err := qoderStoredSessions(context.Background(), agent.StoredSessionQuery{HomeDir: home, WorkingDir: workingDir, Getenv: func(string) string { return "" }})
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, sessionID, sessions[0].Handle)
}
