package deepseekharness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderReadsTheNativeSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, workingDir string) string {
		const id = "native-conformance-session"
		dir := filepath.Join(home, ".dsh", "sessions", projectDirectory(workingDir), id)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		header, err := json.Marshal(map[string]any{"type": "session", "version": 4, "id": id, "cwd": workingDir})
		require.NoError(t, err)
		log := append(header, '\n')
		log = append(log, []byte(`{"type":"session/title","time":2000,"data":{"title":"Native conformance"}}`)...)
		log = append(log, '\n')
		require.NoError(t, os.WriteFile(filepath.Join(dir, "session.v4.jsonl"), log, 0o600))
		return id
	})
}

func TestProjectDirectoryMatchesNativeEscaping(t *testing.T) {
	for _, tc := range []struct{ cwd, want string }{{"/a/b", "--a-b--"}, {`C:\work\a`, "--C-work-a--"}, {"/a/~/한😀", "--a-~007E-~D55C~D83D~DE00--"}, {"/", "--root--"}} {
		assert.Equal(t, tc.want, projectDirectory(tc.cwd))
	}
	assert.Len(t, projectDirectory(strings.Repeat("a", 300)), 255)
}

func TestStoredSessionLogRequiresExactRootAndWorkspace(t *testing.T) {
	log := []byte("{\"type\":\"session\",\"version\":4,\"id\":\"native-session\",\"cwd\":\"/workspace\"}\n{\"type\":\"session/title\",\"time\":2000,\"data\":{\"title\":\"Saved native task\"}}\n")
	item, ok := storedSessionFromLog(log, "/workspace", time.UnixMilli(1000))
	require.True(t, ok)
	assert.Equal(t, "native-session", item.Handle)
	assert.Equal(t, "Saved native task", item.Title)
	assert.Equal(t, time.UnixMilli(2000), item.UpdatedAt)
	_, ok = storedSessionFromLog(log, "/other", time.Time{})
	assert.False(t, ok)
	_, ok = storedSessionFromLog([]byte(strings.Replace(string(log), `"cwd":"/workspace"`, `"cwd":"/workspace","parentSession":"root"`, 1)), "/workspace", time.Time{})
	assert.False(t, ok)
	_, ok = storedSessionFromLog([]byte(strings.Replace(string(log), `"version":4`, `"version":5`, 1)), "/workspace", time.Time{})
	assert.False(t, ok)
}

func TestStoredSessionsReadsPlainAndZstdWithoutWriting(t *testing.T) {
	home := t.TempDir()
	project := projectDirectory("/workspace")
	log := []byte("{\"type\":\"session\",\"version\":4,\"id\":\"native\",\"cwd\":\"/workspace\"}\n{\"type\":\"session/title\",\"data\":{\"title\":\"Native title\"}}\n")
	for _, compressed := range []bool{false, true} {
		session := filepath.Join(home, "sessions", project, "entry")
		require.NoError(t, os.MkdirAll(session, 0o700))
		leaf := "session.v4.jsonl"
		data := log
		if compressed {
			encoder, err := zstd.NewWriter(nil)
			require.NoError(t, err)
			data = encoder.EncodeAll(log, nil)
			require.NoError(t, encoder.Close())
			leaf += ".zstd"
		}
		path := filepath.Join(session, leaf)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		before, err := os.Stat(path)
		require.NoError(t, err)
		items, err := storedSessions(context.Background(), agent.StoredSessionQuery{WorkingDir: "/workspace", HomeDir: home, Getenv: func(key string) string {
			if key == "DSH_HOME" {
				return home
			}
			return ""
		}})
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "Native title", items[0].Title)
		assert.Equal(t, "native", items[0].Handle)
		after, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, before.ModTime(), after.ModTime())
	}
}

func TestStoredSessionsRejectsForeignSymlinkAndMissingHome(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, "sessions", projectDirectory("/workspace"), "session")
	require.NoError(t, os.MkdirAll(parent, 0o700))
	foreign := filepath.Join(t.TempDir(), "foreign.jsonl")
	require.NoError(t, os.WriteFile(foreign, []byte(`{"type":"session","version":4,"id":"foreign","cwd":"/workspace"}`), 0o600))
	require.NoError(t, os.Symlink(foreign, filepath.Join(parent, "session.v4.jsonl")))
	q := agent.StoredSessionQuery{WorkingDir: "/workspace", Getenv: func(string) string { return home }}
	items, err := storedSessions(context.Background(), q)
	require.NoError(t, err)
	assert.Empty(t, items)
	q.Getenv = func(string) string { return "relative" }
	items, err = storedSessions(context.Background(), q)
	require.NoError(t, err)
	assert.Empty(t, items)
}
