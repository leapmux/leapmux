package ohmypi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOhMyPiProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
}

func TestForkOhMyPiSessionFile(t *testing.T) {
	t.Parallel()

	t.Run("forks across worktrees preserving line 1 title slot and body verbatim", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		srcDir := existingDir(t, filepath.Join(home, "repo"))
		targetDir := existingDir(t, filepath.Join(home, "repo-worktree"))
		q := sessionQuery(targetDir, home, nil)

		srcBucket := sessionDir(q, srcDir)
		titleSlot := `{"type":"title","v":1,"title":"Implement fork","pad":"        "}`
		uMsg := userMessage("Implement native forking.")
		srcPath := writeSession(t, srcBucket, "src-session-1", time.Now(),
			titleSlot,
			sessionHeader("src-session-1", srcDir),
			uMsg,
			assistantMessage,
		)
		// Fork using the full file path (Oh My Pi's standard handle).
		dstPath, err := forkOhMyPiSessionFile(q, srcPath, srcDir, targetDir)
		require.NoError(t, err)
		assert.Equal(t, sessionDir(q, targetDir), filepath.Dir(dstPath))
		assert.NotEqual(t, srcPath, dstPath)

		raw, err := os.ReadFile(dstPath)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		require.Len(t, lines, 4)

		// Line 1 fixed-size title slot is preserved verbatim.
		assert.Equal(t, titleSlot, lines[0])

		// Line 2 session header gets fresh UUID, timestamp, canonical target cwd, and parentSession.
		var header map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[1]), &header))
		assert.Equal(t, "session", header["type"])
		assert.Equal(t, float64(3), header["version"])
		newID, _ := header["id"].(string)
		assert.NotEqual(t, "src-session-1", newID)
		_, parseErr := uuid.Parse(newID)
		require.NoError(t, parseErr)
		assert.Equal(t, canonicalPath(targetDir), header["cwd"])
		assert.Equal(t, srcPath, header["parentSession"])
		assert.True(t, strings.HasSuffix(filepath.Base(dstPath), "_"+newID+".jsonl"))

		// Subsequent entry lines stay verbatim.
		assert.Equal(t, uMsg, lines[2])
		assert.Equal(t, assistantMessage, lines[3])

		// Discoverable via Oh My Pi's stored-session reader under targetDir.
		listed, err := storedSessions(context.Background(), sessionQuery(targetDir, home, nil))
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, dstPath, listed[0].Handle)
		assert.Equal(t, "Implement fork", listed[0].Title)
	})

	t.Run("forks by bare session ID from source working directory", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		srcDir := existingDir(t, filepath.Join(home, "src"))
		targetDir := existingDir(t, filepath.Join(home, "dst"))
		q := sessionQuery(targetDir, home, nil)

		srcBucket := sessionDir(q, srcDir)
		writeSession(t, srcBucket, "01a0cf77", time.Now(),
			sessionHeader("01a0cf77", srcDir),
			userMessage("Check tests"),
			assistantMessage,
		)

		dstPath, err := forkOhMyPiSessionFile(q, "01a0cf77", srcDir, targetDir)
		require.NoError(t, err)
		assert.Equal(t, sessionDir(q, targetDir), filepath.Dir(dstPath))

		raw, err := os.ReadFile(dstPath)
		require.NoError(t, err)
		assert.Contains(t, string(raw), "Check tests")
	})

	t.Run("fails on missing session, invalid handle, or corrupt header", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		dir := existingDir(t, filepath.Join(home, "repo"))
		q := sessionQuery(dir, home, nil)

		_, err := forkOhMyPiSessionFile(q, "absent-id", dir, dir)
		require.Error(t, err)

		_, err = forkOhMyPiSessionFile(q, "--bad-flag", dir, dir)
		require.Error(t, err)

		badPath := writeSession(t, sessionDir(q, dir), "bad-hdr", time.Now(),
			`{"type":"title","v":1,"title":"Broken","pad":""}`,
			`{"type":"message","message":{"role":"user","content":"missing header"}}`,
		)
		_, err = forkOhMyPiSessionFile(q, badPath, dir, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "session header")
	})
}

func TestOhMyPiForkRejectsEmptyTargetDir(t *testing.T) {
	t.Parallel()
	_, err := forkOhMyPiSessionFile(agent.StoredSessionQuery{HomeDir: t.TempDir()}, "01a0cf77", "/src", "   ")
	require.Error(t, err)
}
