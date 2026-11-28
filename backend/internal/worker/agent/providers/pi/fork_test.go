package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
}

func TestForkPiSessionFile(t *testing.T) {
	t.Parallel()

	t.Run("forks by bare session ID across worktrees and preserves body verbatim", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, ".pi", "agent", "sessions")
		srcDir := testutil.NativeAbsPath("/Users/dev/project")
		targetDir := testutil.NativeAbsPath("/Users/dev/project-worktree")
		srcID := "018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b"

		entry1 := `{"type":"model_change","provider":"anthropic","modelId":"claude-sonnet-4"}`
		entry2 := `{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"build the fork"}]}}`
		entry3 := `{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`
		writePiTranscript(t, root, srcDir, "2026-09-01T12-00-00-000Z_"+srcID+".jsonl", time.Now(),
			`{"type":"session","version":3,"id":"`+srcID+`","timestamp":"2026-09-01T12:00:00.000Z","cwd":`+agenttest.JSONString(srcDir)+`}`,
			entry1, entry2, entry3,
		)
		srcPath := filepath.Join(root, manglePiPath(srcDir), "2026-09-01T12-00-00-000Z_"+srcID+".jsonl")

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		dstPath, err := forkPiSessionFile(q, srcID, srcDir, targetDir)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(root, manglePiPath(targetDir)), filepath.Dir(dstPath))
		assert.True(t, strings.HasSuffix(dstPath, ".jsonl"))

		raw, err := os.ReadFile(dstPath)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		require.Len(t, lines, 4)

		var header map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &header))
		assert.Equal(t, "session", header["type"])
		assert.Equal(t, float64(3), header["version"])
		newID, _ := header["id"].(string)
		assert.NotEqual(t, srcID, newID)
		_, parseErr := uuid.Parse(newID)
		require.NoError(t, parseErr)
		assert.Equal(t, filepath.Clean(targetDir), header["cwd"])
		assert.Equal(t, srcPath, header["parentSession"])
		assert.NotEqual(t, "2026-09-01T12:00:00.000Z", header["timestamp"])
		assert.True(t, strings.HasSuffix(filepath.Base(dstPath), "_"+newID+".jsonl"))

		assert.Equal(t, entry1, lines[1])
		assert.Equal(t, entry2, lines[2])
		assert.Equal(t, entry3, lines[3])

		// The forked transcript must be discoverable by Pi's stored-session reader
		// under the target working directory.
		stored, err := piStoredSessions(context.Background(), agent.StoredSessionQuery{
			WorkingDir: targetDir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
		})
		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, newID, stored[0].Handle)
		assert.Equal(t, "build the fork", stored[0].Title)
	})

	t.Run("forks by explicit session file path", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, ".pi", "agent", "sessions")
		srcDir := testutil.NativeAbsPath("/Users/dev/src")
		targetDir := testutil.NativeAbsPath("/Users/dev/dst")
		fileName := "2026-09-01T12-00-00-000Z_sess-by-path.jsonl"
		writePiTranscript(t, root, srcDir, fileName, time.Now(),
			`{"type":"session","version":3,"id":"sess-by-path","timestamp":"2026-09-01T12:00:00.000Z","cwd":`+agenttest.JSONString(srcDir)+`}`,
			`{"type":"message","message":{"role":"user","content":"hello"}}`,
		)
		srcPath := filepath.Join(root, manglePiPath(srcDir), fileName)

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		dstPath, err := forkPiSessionFile(q, srcPath, srcDir, targetDir)
		require.NoError(t, err)
		assert.NotEqual(t, srcPath, dstPath)
		assert.Equal(t, filepath.Join(root, manglePiPath(targetDir)), filepath.Dir(dstPath))
	})

	t.Run("fails on missing session, invalid handle, or bad header", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, ".pi", "agent", "sessions")
		dir := testutil.NativeAbsPath("/Users/dev/project")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}

		_, err := forkPiSessionFile(q, "nonexistent-id", dir, dir)
		require.Error(t, err)

		_, err = forkPiSessionFile(q, "--bad-flag", dir, dir)
		require.Error(t, err)

		badFile := "2026-09-01T12-00-00-000Z_bad-hdr.jsonl"
		writePiTranscript(t, root, dir, badFile, time.Now(), `{"type":"message","message":{"role":"user","content":"no header"}}`)
		_, err = forkPiSessionFile(q, filepath.Join(root, manglePiPath(dir), badFile), dir, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "session header")
	})
}
