package kimi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// writeKimiState writes one session's state.json in the shape 2.0.2 writes it.
func writeKimiState(t *testing.T, root, workDir, id string, fields map[string]any) string {
	t.Helper()
	state := map[string]any{
		"id": id, "version": 2, "cwd": workDir, "createdAt": 1790187218480, "updatedAt": 1790187218596,
		"archived": false, "agents": map[string]any{"main": map[string]any{"type": "main"}}, "custom": map[string]any{},
		"isCustomTitle": false, "title": "Hello there.", "titleKind": "prompt", "lastPrompt": "Hello there.", "lastTurnReason": "completed",
	}
	for key, value := range fields {
		if value == nil {
			delete(state, key)
			continue
		}
		state[key] = value
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	dir := filepath.Join(root, "sessions", kimiWorkDirKey(workDir), id)
	agenttest.WriteFixtureFile(t, filepath.Join(dir, kimiStateFile), string(data))
	return dir
}

func listKimiSessions(t *testing.T, home, workDir string, env map[string]string) []agent.StoredSession {
	t.Helper()
	sessions, err := kimiProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: workDir, HomeDir: home, Getenv: agenttest.FixtureEnv(env),
	})
	require.NoError(t, err)
	return sessions
}

func TestKimiReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, kimiProvider{}, func(t *testing.T, home, workingDir string) string {
		writeKimiState(t, filepath.Join(home, ".kimi-code"), workingDir, "session_86f0469d-6a78-4738-9823-b1c3b0f38bbc", nil)
		return "session_86f0469d-6a78-4738-9823-b1c3b0f38bbc"
	})
}

func TestKimiWorkDirKey(t *testing.T) {
	t.Parallel()

	// Keys the 2.0.2 CLI wrote for these two directories.
	assert.Equal(t, "wd_work_52414f32c22a", kimiWorkDirKey("/Users/trustin/Workspaces/leapmux/.tmp/probe/kimi-code/work"))
	assert.Equal(t, "wd_kimi-code_ba976c43c2a8", kimiWorkDirKey("/Users/trustin/Workspaces/leapmux/.tmp/probe/kimi-code"))
	assert.Equal(t, kimiWorkDirKey("/Users/trustin/Workspaces/leapmux/.tmp/probe/kimi-code"),
		kimiWorkDirKey("/Users/trustin/Workspaces/leapmux/.tmp/probe/kimi-code/"), "a trailing slash is trimmed")
	assert.True(t, strings.HasPrefix(kimiWorkDirKey(`C:\Users\me\app`), "wd_app_"), "a backslash separates as a slash")
	assert.Equal(t, kimiWorkDirKey(`C:\Users\me\app`), kimiWorkDirKey("C:/Users/me/app"))
}

func TestKimiSlug(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"My Project":                   "my-project",
		"--weird__name--":              "weird__name",
		"a.b-c_d":                      "a.b-c_d",
		"日本語":                          "workspace",
		"":                             "workspace",
		".":                            "workspace",
		"..":                           "workspace",
		strings.Repeat("x", 50):        strings.Repeat("x", 40),
		strings.Repeat("y", 39) + " z": strings.Repeat("y", 39),
	} {
		assert.Equal(t, want, kimiSlug(input), "%q", input)
	}
}

func TestKimiStoredSessions(t *testing.T) {
	t.Parallel()

	t.Run("lists the sessions of the directory, newest first", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, ".kimi-code")
		workDir := filepath.Join(home, "project")
		writeKimiState(t, root, workDir, "session_old", map[string]any{"updatedAt": 1790000000000, "title": "Old work"})
		writeKimiState(t, root, workDir, "session_new", map[string]any{"updatedAt": 1790100000000, "title": "", "lastPrompt": "Fix the parser."})

		sessions := listKimiSessions(t, home, workDir, nil)
		require.Len(t, sessions, 2)
		assert.Equal(t, "session_new", sessions[0].Handle)
		assert.Equal(t, "Fix the parser.", sessions[0].Title, "a session with no title is titled by its last prompt")
		assert.Equal(t, time.UnixMilli(1790100000000).UTC(), sessions[0].UpdatedAt.UTC())
		assert.Equal(t, "session_old", sessions[1].Handle)
		assert.Equal(t, "Old work", sessions[1].Title)
	})

	t.Run("skips what the picker must not offer", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, ".kimi-code")
		workDir := filepath.Join(home, "project")
		writeKimiState(t, root, workDir, "session_ok", nil)
		writeKimiState(t, root, workDir, "session_archived", map[string]any{"archived": true})
		writeKimiState(t, root, workDir, "session_empty", map[string]any{"lastPrompt": nil, "title": nil})
		writeKimiState(t, root, workDir, "session_blank", map[string]any{"lastPrompt": "  "})
		writeKimiState(t, root, workDir, "session_elsewhere", map[string]any{"cwd": filepath.Join(home, "other")})
		writeKimiState(t, root, workDir, "session_mismatch", map[string]any{"id": "not_a_session"})
		agenttest.WriteFixtureFile(t, filepath.Join(root, "sessions", kimiWorkDirKey(workDir), "session_broken", kimiStateFile), "{not json")
		agenttest.WriteFixtureFile(t, filepath.Join(root, "sessions", kimiWorkDirKey(workDir), "notes", kimiStateFile), `{"id":"session_notes","cwd":"x"}`)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "sessions", kimiWorkDirKey(workDir), "session_nostate"), 0o755))

		assert.Equal(t, []string{"session_ok"}, agenttest.Handles(listKimiSessions(t, home, workDir, nil)))
	})

	t.Run("reads the data root KIMI_CODE_HOME states", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		custom := filepath.Join(home, "kimi-data")
		workDir := filepath.Join(home, "project")
		writeKimiState(t, custom, workDir, "session_custom", nil)
		writeKimiState(t, filepath.Join(home, ".kimi-code"), workDir, "session_default", nil)

		assert.Equal(t, []string{"session_custom"}, agenttest.Handles(listKimiSessions(t, home, workDir, map[string]string{kimiHomeEnv: custom})))
		assert.Equal(t, []string{"session_default"}, agenttest.Handles(listKimiSessions(t, home, workDir, nil)))
	})

	t.Run("a session with no update time is timed by its state file", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		workDir := filepath.Join(home, "project")
		dir := writeKimiState(t, filepath.Join(home, ".kimi-code"), workDir, "session_untimed", map[string]any{"updatedAt": nil})
		modified := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
		require.NoError(t, os.Chtimes(filepath.Join(dir, kimiStateFile), modified, modified))

		sessions := listKimiSessions(t, home, workDir, nil)
		require.Len(t, sessions, 1)
		assert.True(t, modified.Equal(sessions[0].UpdatedAt), "got %s", sessions[0].UpdatedAt)
	})

	t.Run("an absent store lists nothing", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		assert.Empty(t, listKimiSessions(t, home, filepath.Join(home, "project"), nil))
		assert.Empty(t, listKimiSessions(t, home, "", nil), "no working directory, no key to read")
	})

	t.Run("an unreadable store is an error", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		workDir := filepath.Join(home, "project")
		// A file where the directory of sessions belongs.
		agenttest.WriteFixtureFile(t, filepath.Join(home, ".kimi-code", "sessions", kimiWorkDirKey(workDir)), "not a directory")
		_, err := kimiProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
			WorkingDir: workDir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
		})
		assert.Error(t, err)
	})

	t.Run("the listing is capped", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, ".kimi-code")
		workDir := filepath.Join(home, "project")
		for i := range 5 {
			writeKimiState(t, root, workDir, "session_"+string(rune('a'+i)), map[string]any{"updatedAt": 1790000000000 + i})
		}
		sessions, err := kimiProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
			WorkingDir: workDir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 2,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"session_e", "session_d"}, agenttest.Handles(sessions))
	})
}
