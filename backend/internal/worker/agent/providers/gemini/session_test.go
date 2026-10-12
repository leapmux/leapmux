package gemini

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func geminiStoreFixture(t *testing.T) (agent.StoredSessionQuery, string, string) {
	t.Helper()
	home := t.TempDir()
	work := filepath.Join(t.TempDir(), "native-workspace")
	require.NoError(t, os.MkdirAll(work, 0o700))
	root := filepath.Join(home, ".gemini")
	project := filepath.Join(root, "tmp", "native-project")
	chats := filepath.Join(project, "chats")
	require.NoError(t, os.MkdirAll(chats, 0o700))
	registry, err := json.Marshal(map[string]any{"projects": map[string]string{geminiRegistryPath(work): "native-project"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "projects.json"), registry, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".project_root"), []byte(geminiRegistryPath(work)), 0o600))
	query := agent.StoredSessionQuery{HomeDir: home, WorkingDir: work, Getenv: func(string) string { return "" }}
	_, hash := geminiProjectIdentity(work)
	return query, chats, hash
}

func geminiFixtureSessionPath(directory, sessionID string) string {
	return filepath.Join(directory, "session-2026-10-02T16-00-"+sessionID[:min(8, len(sessionID))]+".jsonl")
}

func TestGeminiSessionLimitAppliesAfterOwnedRecordsAreValidated(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	query.Limit = 1
	owned := geminiFixtureSessionPath(directory, "owned")
	foreign := geminiFixtureSessionPath(directory, "foreign")
	writeGeminiSession(t, owned, "owned", hash, "main", "owned prompt")
	writeGeminiSession(t, foreign, "foreign", "other-project", "main", "foreign prompt")
	require.NoError(t, os.Chtimes(owned, time.Unix(100, 0), time.Unix(100, 0)))
	require.NoError(t, os.Chtimes(foreign, time.Unix(200, 0), time.Unix(200, 0)))
	sessions, err := geminiStoredSessions(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "owned", sessions[0].Handle)
}

func writeGeminiSession(t *testing.T, path, sessionID, hash, kind, text string) {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{"sessionId": sessionID, "projectHash": hash, "kind": kind, "startTime": "2026-10-02T16:00:00Z", "lastUpdated": "2026-10-02T17:00:00Z"})
	require.NoError(t, err)
	message, err := json.Marshal(map[string]any{"id": "native-user", "type": "user", "content": []map[string]string{{"text": text}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(append(metadata, '\n'), append(message, '\n')...), 0o600))
}

func TestGeminiStoredSessionsListsOnlyOwnedNativeRootSessions(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiSession(t, geminiFixtureSessionPath(directory, "root-native-session"), "root-native-session", hash, "main", "native user prompt")
	writeGeminiSession(t, geminiFixtureSessionPath(directory, "foreign-native-session"), "foreign-native-session", "different-project", "main", "foreign prompt")
	writeGeminiSession(t, geminiFixtureSessionPath(directory, "child-native-session"), "child-native-session", hash, "subagent", "child prompt")
	sessions, err := geminiStoredSessions(context.Background(), query)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "root-native-session", sessions[0].Handle)
	assert.Equal(t, "native user prompt", sessions[0].Title)
	assert.Equal(t, "2026-10-02T17:00:00Z", sessions[0].UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	path, err := locateGeminiSession(query, "root-native-session")
	require.NoError(t, err)
	assert.Equal(t, geminiFixtureSessionPath(directory, "root-native-session"), path)
}

func TestGeminiProjectDirectoryUsesTheConfiguredHomeAndMarker(t *testing.T) {
	t.Parallel()
	query, directory, _ := geminiStoreFixture(t)
	home := query.HomeDir
	query.HomeDir = t.TempDir()
	query.Getenv = func(key string) string {
		if key == "GEMINI_CLI_HOME" {
			return home
		}
		return ""
	}
	actual, err := geminiProjectDirectory(query)
	require.NoError(t, err)
	assert.Equal(t, directory, actual)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(directory), ".project_root"), []byte("/another/workspace"), 0o600))
	_, err = geminiProjectDirectory(query)
	assert.ErrorContains(t, err, "another directory")
}

func TestGeminiStoredSessionsHandlesAbsentStoresAndCancellation(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiSession(t, geminiFixtureSessionPath(directory, "root-native-session"), "root-native-session", hash, "main", "native user prompt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := geminiStoredSessions(ctx, query)
	assert.ErrorIs(t, err, context.Canceled)
	query.HomeDir = t.TempDir()
	sessions, err := geminiStoredSessions(context.Background(), query)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestGeminiSessionIdentitiesRejectPathsAndEmptyValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", ".", "..", "../session", "native/session", `native\session`, "native\x00session", "native session"} {
		assert.False(t, validGeminiSessionID(value), "value %q", value)
	}
	for _, value := range []string{"native-session", "19e08b9c-7ff9-4b36-b8c3-c775979a832e", "native_session_1"} {
		assert.True(t, validGeminiSessionID(value), "value %q", value)
	}
}

func TestGeminiToolSourceRejectsSymlinkedTranscriptDirectories(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	external := t.TempDir()
	path := geminiFixtureSessionPath(external, "native-session")
	writeGeminiSession(t, path, "native-session", hash, "main", "external transcript")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	data = append(data, []byte(`{"id":"result","type":"gemini","content":"","toolCalls":[{"id":"run_shell_command__c1","name":"run_shell_command","args":{"command":"printf external"},"result":[{"text":"external result"}],"status":"success"}]}`+"\n")...)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, os.Remove(directory))
	require.NoError(t, os.Symlink(external, directory))
	source := &geminiToolSource{query: query}
	output, err := source.ReadSupplements(context.Background(), "native-session", map[string]agent.MessageContent{
		"run_shell_command__c1": {Original: []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"run_shell_command__c1","status":"completed"}`)},
	}, true)
	assert.Error(t, err)
	assert.Empty(t, output)
}

func TestLocateGeminiSessionUsesNativeUpdatedTimeBeforeFilesystemOrder(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	const sessionID = "bd824478-fa1b-4f2a-8b61-11c118a4dfb8"
	nativeLatest := filepath.Join(directory, "session-2026-10-03T05-07-bd824478.jsonl")
	filesystemLatest := filepath.Join(directory, "session-2026-10-02T05-07-bd824478.jsonl")
	write := func(path, updated, text string) {
		metadata, err := json.Marshal(map[string]any{
			"sessionId": sessionID, "projectHash": hash, "kind": "main",
			"startTime": "2026-10-01T00:00:00Z", "lastUpdated": updated,
		})
		require.NoError(t, err)
		message, err := json.Marshal(map[string]any{"id": "native-user", "type": "user", "content": []map[string]string{{"text": text}}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(append(metadata, '\n'), append(message, '\n')...), 0o600))
	}
	write(nativeLatest, "2026-10-04T00:00:00Z", "The native selector chooses this archive.")
	write(filesystemLatest, "2026-10-03T00:00:00Z", "Filesystem order chooses the wrong archive.")
	// A same-resolution filesystem tie selects the second path by its smaller name.
	// No sleep or timestamp change creates the mismatch.
	firstInfo, err := os.Stat(nativeLatest)
	require.NoError(t, err)
	secondInfo, err := os.Stat(filesystemLatest)
	require.NoError(t, err)
	require.False(t, secondInfo.ModTime().Before(firstInfo.ModTime()), "the fixture must preserve its write order")
	actual, err := locateGeminiSession(query, sessionID)
	require.NoError(t, err)
	assert.Equal(t, nativeLatest, actual)
}

func writeGeminiSelectionCandidate(t *testing.T, path, sessionID, hash string, fields map[string]any) {
	t.Helper()
	metadata := map[string]any{"sessionId": sessionID, "projectHash": hash, "kind": "main"}
	for key, value := range fields {
		metadata[key] = value
	}
	encoded, err := json.Marshal(metadata)
	require.NoError(t, err)
	message := []byte(`{"id":"native-user","type":"user","content":[{"text":"Native selection context."}]}`)
	require.NoError(t, os.WriteFile(path, append(append(encoded, '\n'), append(message, '\n')...), 0o600))
}

func TestLocateGeminiSessionUsesNativeStartTimeWhenUpdatedTimeIsAbsent(t *testing.T) {
	t.Parallel()
	for _, missing := range []struct {
		name   string
		fields map[string]any
	}{
		{name: "absent", fields: map[string]any{"startTime": "2026-10-04T00:00:00Z"}},
		{name: "null", fields: map[string]any{"startTime": "2026-10-04T00:00:00Z", "lastUpdated": nil}},
	} {
		t.Run(missing.name, func(t *testing.T) {
			t.Parallel()
			query, directory, hash := geminiStoreFixture(t)
			const sessionID = "bd824478-fa1b-4f2a-8b61-11c118a4dfb8"
			selected := filepath.Join(directory, "session-2026-10-03T05-07-bd824478.jsonl")
			other := filepath.Join(directory, "session-2026-10-02T05-07-bd824478.jsonl")
			writeGeminiSelectionCandidate(t, selected, sessionID, hash, missing.fields)
			writeGeminiSelectionCandidate(t, other, sessionID, hash, map[string]any{"startTime": "2026-10-05T00:00:00Z", "lastUpdated": "2026-10-03T00:00:00Z"})
			actual, err := locateGeminiSession(query, sessionID)
			require.NoError(t, err)
			assert.Equal(t, selected, actual)
		})
	}
}

func TestLocateGeminiSessionRejectsEqualNativeTimesWithoutFilesystemAuthority(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		first map[string]any
		last  map[string]any
	}{
		{name: "equal", first: map[string]any{"lastUpdated": "2026-10-04T00:00:00Z"}, last: map[string]any{"lastUpdated": "2026-10-04T00:00:00Z"}},
		{name: "timezone", first: map[string]any{"lastUpdated": "2026-10-04T00:00:00Z"}, last: map[string]any{"lastUpdated": "2026-10-04T01:00:00+01:00"}},
		{name: "unstated", first: map[string]any{}, last: map[string]any{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query, directory, hash := geminiStoreFixture(t)
			const sessionID = "bd824478-fa1b-4f2a-8b61-11c118a4dfb8"
			writeGeminiSelectionCandidate(t, filepath.Join(directory, "session-2026-10-03T05-07-bd824478.jsonl"), sessionID, hash, test.first)
			writeGeminiSelectionCandidate(t, filepath.Join(directory, "session-2026-10-02T05-07-bd824478.jsonl"), sessionID, hash, test.last)
			actual, err := locateGeminiSession(query, sessionID)
			assert.ErrorContains(t, err, "multiple archives")
			assert.Empty(t, actual)
		})
	}
}

func TestLocateGeminiSessionKeepsAnUnambiguousNewerCandidateAboveOlderTies(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	const sessionID = "bd824478-fa1b-4f2a-8b61-11c118a4dfb8"
	selected := filepath.Join(directory, "session-2026-10-04T05-07-bd824478.jsonl")
	writeGeminiSelectionCandidate(t, selected, sessionID, hash, map[string]any{"lastUpdated": "2026-10-04T00:00:00Z"})
	for _, name := range []string{"session-2026-10-03T05-07-bd824478.jsonl", "session-2026-10-02T05-07-bd824478.jsonl"} {
		writeGeminiSelectionCandidate(t, filepath.Join(directory, name), sessionID, hash, map[string]any{"lastUpdated": "2026-10-03T00:00:00Z"})
	}
	actual, err := locateGeminiSession(query, sessionID)
	require.NoError(t, err)
	assert.Equal(t, selected, actual)
}

func TestLocateGeminiSessionRejectsTheSelectedForeignProjectInsteadOfAnOlderOwnedArchive(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	const sessionID = "bd824478-fa1b-4f2a-8b61-11c118a4dfb8"
	writeGeminiSelectionCandidate(t, filepath.Join(directory, "session-2026-10-03T05-07-bd824478.jsonl"), sessionID, hash, map[string]any{"lastUpdated": "2026-10-03T00:00:00Z"})
	writeGeminiSelectionCandidate(t, filepath.Join(directory, "session-2026-10-04T05-07-bd824478.jsonl"), sessionID, "foreign-project", map[string]any{"lastUpdated": "2026-10-04T00:00:00Z"})
	actual, err := locateGeminiSession(query, sessionID)
	assert.ErrorContains(t, err, "another project")
	assert.Empty(t, actual)
}

func TestLocateGeminiSessionUsesExactFilenameAndFullNativeIdentity(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	const sessionID = "bd824478-fa1b-4f2a-8b61-11c118a4dfb8"
	selected := filepath.Join(directory, "session-2026-10-02T05-07-bd824478.jsonl")
	writeGeminiSelectionCandidate(t, selected, sessionID, hash, map[string]any{"lastUpdated": "2026-10-02T00:00:00Z"})
	writeGeminiSelectionCandidate(t, filepath.Join(directory, "session-2026-10-03T05-07-bd824478.jsonl"), "bd824478-fa1b-4f2a-8b61-11c118a4dfb9", hash, map[string]any{"lastUpdated": "2026-10-03T00:00:00Z"})
	writeGeminiSelectionCandidate(t, filepath.Join(directory, "session-2026-10-04T05-07-foreign.jsonl"), sessionID, hash, map[string]any{"lastUpdated": "2026-10-04T00:00:00Z"})
	writeGeminiSelectionCandidate(t, filepath.Join(directory, "session-2026-10-05T05-07-bd824478.jsonl"), sessionID, hash, map[string]any{"lastUpdated": "2026-10-05T00:00:00Z", "kind": "subagent"})
	actual, err := locateGeminiSession(query, sessionID)
	require.NoError(t, err)
	assert.Equal(t, selected, actual)
}

func TestLocateGeminiSessionAcceptsNativeJSONAndShortTokenSuffixes(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	path := filepath.Join(directory, "session-2026-10-02T05-07-root.json")
	data, err := json.Marshal(map[string]any{"sessionId": "root", "projectHash": hash, "kind": "main", "lastUpdated": "9999-12-31T23:59:59Z", "messages": []any{}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	actual, err := locateGeminiSession(query, "root")
	require.NoError(t, err)
	assert.Equal(t, path, actual)
}

func TestLocateGeminiSessionRejectsSymlinkedMatchingFiles(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	const sessionID = "bd824478-fa1b-4f2a-8b61-11c118a4dfb8"
	external := filepath.Join(t.TempDir(), "external.jsonl")
	writeGeminiSelectionCandidate(t, external, sessionID, hash, map[string]any{"lastUpdated": "2026-10-04T00:00:00Z"})
	require.NoError(t, os.Symlink(external, filepath.Join(directory, "session-2026-10-04T05-07-bd824478.jsonl")))
	actual, err := locateGeminiSession(query, sessionID)
	assert.Error(t, err)
	assert.Empty(t, actual)
}
