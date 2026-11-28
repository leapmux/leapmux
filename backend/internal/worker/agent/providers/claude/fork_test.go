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
		args, activeID, err := claudeSessionArgs(agent.ForkSession(srcID, ""))
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
			args, activeID, err := claudeSessionArgs(agent.ForkSession(id, ""))
			require.Errorf(t, err, "%q must be rejected before reaching argv", id)
			assert.Nil(t, args)
			assert.Empty(t, activeID)
		}
	})

	t.Run("rejects invalid SessionInit states", func(t *testing.T) {
		for _, bad := range []agent.SessionInit{
			{Kind: agent.SessionInitFresh, SessionID: "unexpected"},
			{Kind: agent.SessionInitResume},
			{Kind: agent.SessionInitResume, SessionID: "sess-a", SourceWorkingDir: "/src"},
			{Kind: agent.SessionInitFork},
			{Kind: agent.SessionInitKind(99), SessionID: "sess-a"},
		} {
			args, activeID, err := claudeSessionArgs(bad)
			require.Error(t, err)
			assert.Nil(t, args)
			assert.Empty(t, activeID)
		}
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
		stagedPath, err := stageClaudeForkTranscript(q, srcDir, worktreeDir, sessionID)
		require.NoError(t, err)

		targetPath := filepath.Join(home, ".claude", "projects", mangleClaudePath(worktreeDir), sessionID+".jsonl")
		assert.Equal(t, targetPath, stagedPath)
		got, err := os.ReadFile(targetPath)
		require.NoError(t, err)
		assert.Equal(t, content, string(got))
	})

	t.Run("refreshes existing target transcript atomically when source updates", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/feat-y")
		sessionID := "sess-existing"

		srcProjectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(srcDir))
		targetProjectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(worktreeDir))
		writeClaudeTranscript(t, srcProjectDir, sessionID, time.Now(), claudeUserRecord(srcDir, sessionID, "updated source version"))
		writeClaudeTranscript(t, targetProjectDir, sessionID, time.Now(), claudeUserRecord(worktreeDir, sessionID, "stale target version"))

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		stagedPath, err := stageClaudeForkTranscript(q, srcDir, worktreeDir, sessionID)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(targetProjectDir, sessionID+".jsonl"), stagedPath)

		got, err := os.ReadFile(filepath.Join(targetProjectDir, sessionID+".jsonl"))
		require.NoError(t, err)
		assert.Contains(t, string(got), "updated source version")
		assert.NotContains(t, string(got), "stale target version")
	})

	t.Run("no-ops cleanly when source transcript is missing", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/missing")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}

		stagedPath, err := stageClaudeForkTranscript(q, srcDir, worktreeDir, "sess-absent")
		require.NoError(t, err)
		assert.Empty(t, stagedPath)
		_, err = os.Stat(filepath.Join(home, ".claude", "projects", mangleClaudePath(worktreeDir), "sess-absent.jsonl"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("no-ops when source and target share the same mangled directory", func(t *testing.T) {
		home := t.TempDir()
		dir := testutil.NativeAbsPath("/Users/dev/repo")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		stagedPath, err := stageClaudeForkTranscript(q, dir, dir, "sess-same")
		require.NoError(t, err)
		assert.Empty(t, stagedPath)
	})

	t.Run("skips session IDs containing path separators", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		worktreeDir := testutil.NativeAbsPath("/Users/dev/repo-worktrees/traversal")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}

		stagedPath, err := stageClaudeForkTranscript(q, srcDir, worktreeDir, "../escape")
		require.NoError(t, err)
		assert.Empty(t, stagedPath)
		_, err = os.Stat(filepath.Join(home, ".claude", "projects", "escape.jsonl"))
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
		stagedPath, err := stageClaudeForkTranscript(q, srcDir, worktreeDir, sessionID)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(alt, "projects", mangleClaudePath(worktreeDir), sessionID+".jsonl"), stagedPath)

		got, err := os.ReadFile(filepath.Join(alt, "projects", mangleClaudePath(worktreeDir), sessionID+".jsonl"))
		require.NoError(t, err)
		assert.Contains(t, string(got), "custom config")
	})

	t.Run("returns an explicit error for >200-byte mangled target dir without a unique existing directory", func(t *testing.T) {
		home := t.TempDir()
		srcDir := testutil.NativeAbsPath("/Users/dev/repo")
		longTarget := testutil.NativeAbsPath("/Users/dev/" + strings.Repeat("worktree-segment/", 15) + "leaf")
		require.Greater(t, len(mangleClaudePath(longTarget)), claudeMangleMaxLength)
		sessionID := "sess-long-target"

		srcProjectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(srcDir))
		writeClaudeTranscript(t, srcProjectDir, sessionID, time.Now(),
			claudeUserRecord(srcDir, sessionID, "long target test"))

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}

		// Case 1: no existing project directory matches the 200-byte prefix.
		stagedPath, err := stageClaudeForkTranscript(q, srcDir, longTarget, sessionID)
		require.Error(t, err)
		assert.Empty(t, stagedPath)
		assert.Contains(t, err.Error(), "exceeds 200 bytes")
		assert.Contains(t, err.Error(), "no existing project directory")

		// Case 2: two existing project directories share the 200-byte prefix (ambiguous).
		prefix := mangleClaudePath(longTarget)[:claudeMangleMaxLength]
		projectsRoot := filepath.Join(home, ".claude", "projects")
		dirA := filepath.Join(projectsRoot, prefix+"-aaaa")
		dirB := filepath.Join(projectsRoot, prefix+"-bbbb")
		require.NoError(t, os.MkdirAll(dirA, 0o700))
		require.NoError(t, os.MkdirAll(dirB, 0o700))

		stagedPath, err = stageClaudeForkTranscript(q, srcDir, longTarget, sessionID)
		require.Error(t, err)
		assert.Empty(t, stagedPath)
		assert.Contains(t, err.Error(), "exceeds 200 bytes")
		assert.Contains(t, err.Error(), "found 2 matching project directories")

		// Case 3: exactly one existing project directory matches -> succeeds.
		require.NoError(t, os.Remove(dirB))
		stagedPath, err = stageClaudeForkTranscript(q, srcDir, longTarget, sessionID)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dirA, sessionID+".jsonl"), stagedPath)
		got, readErr := os.ReadFile(stagedPath)
		require.NoError(t, readErr)
		assert.Contains(t, string(got), "long target test")
	})
}
