package commandcode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestCommandCodeReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, commandcodeProvider{}, func(t *testing.T, home, dir string) string {
		const id = "a63cfe72-d44b-4f91-9f3a-8b5d2f50ec86"
		project := filepath.Join(home, ".commandcode", "projects", "native-project")
		require.NoError(t, os.MkdirAll(project, 0o700))
		header, err := json.Marshal(map[string]any{"type": "session", "version": 3, "id": id, "cwd": dir})
		require.NoError(t, err)
		content := append(header, []byte("\n"+`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"Native session title"}]}}`+"\n")...)
		require.NoError(t, os.WriteFile(filepath.Join(project, id+".jsonl"), content, 0o600))
		return id
	})
}
