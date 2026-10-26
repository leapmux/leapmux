package fastagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

func writeFastagentRootArchive(t *testing.T, home, sessionID, workingDir string, messages []fastagentHistoryMessage) string {
	t.Helper()
	if messages == nil {
		messages = []fastagentHistoryMessage{}
	}
	dir := filepath.Join(home, "sessions", sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	snapshot := map[string]any{
		"session_id":   sessionID,
		"execution":    map[string]any{"resumable": true, "child_link": nil},
		"continuation": map[string]any{"cwd": workingDir, "active_agent": "agent", "agents": map[string]any{"agent": map[string]string{"history_file": "history_agent.json"}}},
	}
	for file, value := range map[string]any{"session.json": snapshot, "history_agent.json": map[string]any{"messages": messages}} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), mustJSON(t, value), 0o600))
	}
	return dir
}

func TestReadFastagentRootArchiveValidatesTheNativeOwner(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any, string)
	}{
		{name: "different session", change: func(s map[string]any, _ string) { s["session_id"] = "other" }},
		{name: "child session", change: func(s map[string]any, _ string) {
			s["execution"].(map[string]any)["child_link"] = map[string]string{"parent_session_id": "parent"}
		}},
		{name: "different directory", change: func(s map[string]any, other string) { s["continuation"].(map[string]any)["cwd"] = other }},
		{name: "missing directory", change: func(s map[string]any, _ string) { delete(s["continuation"].(map[string]any), "cwd") }},
		{name: "hostile history", change: func(s map[string]any, _ string) {
			s["continuation"].(map[string]any)["agents"] = map[string]any{"agent": map[string]string{"history_file": "../history_agent.json"}}
		}},
		{name: "no history", change: func(s map[string]any, _ string) { s["continuation"].(map[string]any)["agents"] = map[string]any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd, other := t.TempDir(), t.TempDir(), t.TempDir()
			dir := writeFastagentRootArchive(t, home, "root", cwd, nil)
			raw, err := os.ReadFile(filepath.Join(dir, "session.json"))
			require.NoError(t, err)
			var snapshot map[string]any
			require.NoError(t, json.Unmarshal(raw, &snapshot))
			tc.change(snapshot, other)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "session.json"), mustJSON(t, snapshot), 0o600))
			histories, err := readFastagentRootArchive(home, "root", cwd)
			require.Error(t, err)
			assert.Nil(t, histories)
		})
	}
}

func TestReadFastagentRootArchiveHandlesEmptyAndInvalidHistories(t *testing.T) {
	t.Parallel()
	home, cwd := t.TempDir(), t.TempDir()
	dir := writeFastagentRootArchive(t, home, "root", cwd, []fastagentHistoryMessage{})
	histories, err := readFastagentRootArchive(home, "root", cwd)
	require.NoError(t, err)
	require.Len(t, histories, 1)
	assert.Empty(t, histories[0].messages)
	for _, raw := range []string{"", "{", `{}`, `{"messages":null}`, `{"messages":"bad"}`} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "history_agent.json"), []byte(raw), 0o600))
		histories, err = readFastagentRootArchive(home, "root", cwd)
		require.Error(t, err)
		assert.Nil(t, histories)
	}
	require.NoError(t, os.Remove(filepath.Join(dir, "history_agent.json")))
	_, err = readFastagentRootArchive(home, "root", cwd)
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, owner := range []string{"", ".", "..", "../root", `root\child`} {
		_, err := readFastagentRootArchive(home, owner, cwd)
		require.Error(t, err)
	}
}

func TestReadFastagentRootArchiveRejectsSymlinksAndOversizedFiles(t *testing.T) {
	t.Parallel()
	home, cwd := t.TempDir(), t.TempDir()
	dir := writeFastagentRootArchive(t, home, "root", cwd, nil)
	path := filepath.Join(dir, "history_agent.json")
	require.NoError(t, os.Remove(path))
	outside := filepath.Join(t.TempDir(), "history.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"messages":[]}`), 0o600))
	require.NoError(t, os.Symlink(outside, path))
	_, err := readFastagentRootArchive(home, "root", cwd)
	require.ErrorContains(t, err, "symlink")
	require.NoError(t, os.Remove(path))
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(fastagentRootHistoryLimit+1))
	require.NoError(t, file.Close())
	_, err = readFastagentRootArchive(home, "root", cwd)
	require.ErrorContains(t, err, "regular file within")
}

func TestReadFastagentRootArchivePreservesCloseFailures(t *testing.T) {
	t.Parallel()
	home, cwd := t.TempDir(), t.TempDir()
	writeFastagentRootArchive(t, home, "root", cwd, nil)
	root, err := sessionstore.OpenArchiveRoot(home)
	require.NoError(t, err)
	closeErr := errors.New("injected root close failure")
	histories, err := readFastagentRootArchiveFromRoot(fastagentCloseFaultRoot{ArchiveRoot: root, closeErr: closeErr}, "root", cwd)
	require.ErrorIs(t, err, closeErr)
	assert.Nil(t, histories)
}

func TestReadFastagentRootArchiveUsesTheWorkingDirectoryIdentity(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	cwd, alias := filepath.Join(parent, "NativeWork"), filepath.Join(parent, "nativework")
	require.NoError(t, os.Mkdir(cwd, 0o700))
	if _, err := os.Stat(alias); err != nil {
		t.Skip("the filesystem distinguishes the case of directory paths")
	}
	home := t.TempDir()
	writeFastagentRootArchive(t, home, "root", alias, nil)
	histories, err := readFastagentRootArchive(home, "root", cwd)
	require.NoError(t, err)
	require.Len(t, histories, 1)
}
