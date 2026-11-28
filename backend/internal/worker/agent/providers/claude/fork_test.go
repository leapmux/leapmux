package claude

import (
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

func TestClaudeProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
}

func TestClaudeSessionArgsFork(t *testing.T) {
	t.Parallel()

	t.Run("builds fork argv with fresh session ID", func(t *testing.T) {
		srcID := "3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f"
		args, activeID, err := claudeSessionArgs("", srcID)
		require.NoError(t, err)
		require.Len(t, args, 5)
		assert.Equal(t, "--resume", args[0])
		assert.Equal(t, srcID, args[1])
		assert.Equal(t, "--fork-session", args[2])
		assert.Equal(t, "--session-id", args[3])
		assert.Equal(t, args[4], activeID)
		assert.NotEqual(t, srcID, activeID)
		_, parseErr := uuid.Parse(activeID)
		require.NoError(t, parseErr)
	})

	t.Run("rejects invalid fork session tokens", func(t *testing.T) {
		for _, id := range []string{
			"-x",
			"--dangerously-skip-permissions",
			"has\x00nul",
			"has\nnewline",
			" leading-space",
			strings.Repeat("a", 129),
		} {
			args, activeID, err := claudeSessionArgs("", id)
			require.Errorf(t, err, "%q must be rejected before reaching argv", id)
			assert.Nil(t, args)
			assert.Empty(t, activeID)
		}
	})

	t.Run("rejects simultaneous resume and fork", func(t *testing.T) {
		args, activeID, err := claudeSessionArgs("sess-a", "sess-b")
		require.Error(t, err)
		assert.Nil(t, args)
		assert.Empty(t, activeID)
	})
}

func TestStageClaudeForkTranscript(t *testing.T) {
	t.Parallel()

	t.Run("copies source transcript into target worktree project directory", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/feat-x")
		sessionID := "3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f"

		srcProjectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(srcDir))
		content := claudeUserRecord(srcDir, sessionID, "initial prompt") + "\n" +
			`{"type":"assistant","sessionId":"` + sessionID + `","message":{"role":"assistant","content":[{"type":"text","text":"reply"}]}}` + "\n"
		writeClaudeTranscript(t, srcProjectDir, sessionID, time.Now(), strings.TrimSuffix(content, "\n"))

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		require.NoError(t, stageClaudeForkTranscript(q, srcDir, worktreeDir, sessionID))

		targetPath := filepath.Join(home, ".claude", "projects", mangleClaudePath(worktreeDir), sessionID+".jsonl")
		got, err := os.ReadFile(targetPath)
		require.NoError(t, err)
		assert.Equal(t, content, string(got))
	})

	t.Run("preserves existing target transcript without overwriting", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/feat-y")
		sessionID := "sess-existing"

		srcProjectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(srcDir))
		targetProjectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(worktreeDir))
		writeClaudeTranscript(t, srcProjectDir, sessionID, time.Now(), claudeUserRecord(srcDir, sessionID, "source version"))
		writeClaudeTranscript(t, targetProjectDir, sessionID, time.Now(), claudeUserRecord(worktreeDir, sessionID, "target version"))

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		require.NoError(t, stageClaudeForkTranscript(q, srcDir, worktreeDir, sessionID))

		got, err := os.ReadFile(filepath.Join(targetProjectDir, sessionID+".jsonl"))
		require.NoError(t, err)
		assert.Contains(t, string(got), "target version")
	})

	t.Run("no-ops cleanly when source transcript is missing", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/missing")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}

		require.NoError(t, stageClaudeForkTranscript(q, srcDir, worktreeDir, "sess-absent"))
		_, err := os.Stat(filepath.Join(home, ".claude", "projects", mangleClaudePath(worktreeDir), "sess-absent.jsonl"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("no-ops when source and target share the same mangled directory", func(t *testing.T) {
		home := t.TempDir()
		dir := testutil.NativeAbsPath("/Users/dev/repo")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		require.NoError(t, stageClaudeForkTranscript(q, dir, dir, "sess-same"))
	})

	t.Run("skips session IDs containing path separators", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/traversal")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}

		require.NoError(t, stageClaudeForkTranscript(q, srcDir, worktreeDir, "../escape"))
		_, err := os.Stat(filepath.Join(home, ".claude", "projects", "escape.jsonl"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("honors CLAUDE_CONFIG_DIR override", func(t *testing.T) {
		home := t.TempDir()
		alt := filepath.Join(home, "custom-claude")
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/alt")
		sessionID := "sess-alt"

		writeClaudeTranscript(t, filepath.Join(alt, "projects", mangleClaudePath(srcDir)), sessionID, time.Now(),
			claudeUserRecord(srcDir, sessionID, "custom config"))

		q := agent.StoredSessionQuery{
			HomeDir: home,
			Getenv:  agenttest.FixtureEnv(map[string]string{"CLAUDE_CONFIG_DIR": "~/custom-claude"}),
		}
		require.NoError(t, stageClaudeForkTranscript(q, srcDir, worktreeDir, sessionID))

		got, err := os.ReadFile(filepath.Join(alt, "projects", mangleClaudePath(worktreeDir), sessionID+".jsonl"))
		require.NoError(t, err)
		assert.Contains(t, string(got), "custom config")
	})
}
