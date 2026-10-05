package junie

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func junieEventLine(t *testing.T, taskID, kind, stepID, agentKind, agentID, name, prompt string) []byte {
	t.Helper()
	event, err := json.Marshal(map[string]any{
		"kind": "SessionA2uxEvent", "taskId": taskID,
		"event": map[string]any{"agentEvent": map[string]any{
			"kind": kind, "stepId": stepID, "name": name, "task": prompt,
			"agent": map[string]any{"kind": agentKind, "id": agentID, "name": name},
		}},
	})
	require.NoError(t, err)
	return event
}

func junieEventPair(t *testing.T, stepID, handle, taskID, name, prompt string) []byte {
	t.Helper()
	spawn := junieEventLine(t, taskID, "SubagentSpawnedEvent", stepID+"-spawned", "MainAgent", "main", name, prompt)
	update := junieEventLine(t, taskID, "CustomAgentBlockUpdatedEvent", stepID, "CustomAgent", handle, name, "")
	return bytes.Join([][]byte{spawn, update, nil}, []byte{'\n'})
}

func TestJunieChildEventLinkKeepsEqualLabelsWithDistinctStepsApart(t *testing.T) {
	t.Parallel()
	const name = "leapmux-e2e-child"
	const prompt = "Read the same file."
	const otherStep = "2f6f9b38-2cd8-43fc-a2b5-65c3f3c45d22"
	events := append(
		junieEventPair(t, junieTestStepID, "agent-1", "task-1", name, prompt),
		junieEventPair(t, otherStep, "agent-2", "task-1", name, prompt)...,
	)

	first, err := junieChildEventLink(events, junieTestChildSessionID, name, prompt)
	require.NoError(t, err)
	assert.Equal(t, junieChildLink{handle: "agent-1", taskID: "task-1"}, first)
	second, err := junieChildEventLink(events, "subagent-"+otherStep, name, prompt)
	require.NoError(t, err)
	assert.Equal(t, junieChildLink{handle: "agent-2", taskID: "task-1"}, second)
}

func TestJunieChildEventLinkWaitsForACompleteUpdateRecord(t *testing.T) {
	t.Parallel()
	const name = "leapmux-e2e-child"
	const prompt = "Read a file."
	events := junieEventPair(t, junieTestStepID, "agent-1", "task-1", name, prompt)
	partial := bytes.TrimSuffix(events, []byte{'\n'})

	_, err := junieChildEventLink(partial, junieTestChildSessionID, name, prompt)
	require.ErrorIs(t, err, errJunieStateNotReady)
	link, err := junieChildEventLink(events, junieTestChildSessionID, name, prompt)
	require.NoError(t, err)
	assert.Equal(t, "agent-1", link.handle)
}

func TestJunieChildEventLinkRejectsAChangedTaskOrHandle(t *testing.T) {
	t.Parallel()
	const name = "leapmux-e2e-child"
	const prompt = "Read a file."
	spawn := junieEventLine(t, "task-1", "SubagentSpawnedEvent", junieTestStepID+"-spawned", "MainAgent", "main", name, prompt)
	wrongTask := junieEventLine(t, "task-2", "CustomAgentBlockUpdatedEvent", junieTestStepID, "CustomAgent", "agent-1", name, "")
	_, err := junieChildEventLink(bytes.Join([][]byte{spawn, wrongTask, nil}, []byte{'\n'}), junieTestChildSessionID, name, prompt)
	require.ErrorContains(t, err, "different tasks")

	first := junieEventLine(t, "task-1", "CustomAgentBlockUpdatedEvent", junieTestStepID, "CustomAgent", "agent-1", name, "")
	second := junieEventLine(t, "task-1", "CustomAgentBlockUpdatedEvent", junieTestStepID, "CustomAgent", "agent-2", name, "")
	_, err = junieChildEventLink(bytes.Join([][]byte{spawn, first, second, nil}, []byte{'\n'}), junieTestChildSessionID, name, prompt)
	require.ErrorContains(t, err, "ambiguous run handle")
}

func TestJunieChildEventLinkRejectsInvalidIdentity(t *testing.T) {
	t.Parallel()
	events := junieEventPair(t, junieTestStepID, "agent-1", "task-1", "leapmux-e2e-child", "Read a file.")
	for _, sessionID := range []string{"", "subagent-../agent-1", "subagent-agent-1", "subagent-" + strings.ToUpper(junieTestStepID)} {
		_, err := junieChildEventLink(events, sessionID, "leapmux-e2e-child", "Read a file.")
		require.ErrorContains(t, err, "identity is incomplete", sessionID)
	}
	_, err := junieChildEventLink(events, junieTestChildSessionID, "another-child", "Read a file.")
	require.ErrorContains(t, err, "differs from the ACP child")
	_, err = junieChildEventLink(events, junieTestChildSessionID, "leapmux-e2e-child", "another prompt")
	require.ErrorContains(t, err, "differs from the ACP child")
}

func TestJunieChildToolRecordsRejectsAnotherTaskSnapshot(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	const sessionID = "session-260928-061937-xr13"
	const prompt = "Read a file."
	writeJunieChildState(t, home, sessionID, prompt, "MARKER")
	data, err := readJunieState(home, sessionID)
	require.NoError(t, err)
	_, err = junieChildToolRecords(data, junieChildLink{handle: "agent-1", taskID: "task-other"}, "leapmux-e2e-child", prompt)
	require.ErrorContains(t, err, "not a main-agent snapshot")
}

func TestReadJunieEventsRejectsASymlink(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	const sessionID = "session-260928-061937-xr13"
	writeJunieChildState(t, home, sessionID, "Read a file.", "MARKER")
	path := filepath.Join(home, "sessions", sessionID, "events.jsonl")
	outside := filepath.Join(home, "outside-events.jsonl")
	require.NoError(t, os.Rename(path, outside))
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	_, err := readJunieEvents(home, sessionID)
	require.ErrorContains(t, err, "not a regular file")
}

func TestReadJunieEventsRejectsAnOversizedFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	const sessionID = "session-260928-061937-xr13"
	writeJunieChildState(t, home, sessionID, "Read a file.", "MARKER")
	path := filepath.Join(home, "sessions", sessionID, "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(junieEventsReadLimit+1))
	require.NoError(t, file.Close())
	_, err = readJunieEvents(home, sessionID)
	require.ErrorContains(t, err, "size cap")
}

func TestJunieChildTailIgnoresRepeatedSnapshotsAndReadsAReplacement(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUNIE_HOME", home)
	const sessionID = "session-260928-061937-xr13"
	const prompt = "Read a file."
	writeJunieChildState(t, home, sessionID, prompt, "FIRST_MARKER")
	a, sink := newJunieChildTestAgent(t)
	a.SetContextForTest(t.Context())
	a.SetSessionIDForTest(sessionID)
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, sessionID, map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": junieTestChildSessionID,
		"name": "leapmux-e2e-child", "task": prompt,
	}))
	defer a.stopChildTails()
	row, ok := sink.BackgroundTask(junieTestChildSessionID)
	require.True(t, ok)
	tail := a.childTails[junieTestChildSessionID]
	require.NotNil(t, tail)
	firstCount := len(sink.Child(row.ChildAgentID).Messages())
	assert.GreaterOrEqual(t, firstCount, 3)
	tail.poll()
	assert.Len(t, sink.Child(row.ChildAgentID).Messages(), firstCount)

	path := filepath.Join(home, "sessions", sessionID, "state.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	data = bytes.ReplaceAll(data, []byte("callid_child_read"), []byte("callid_second_read"))
	data = bytes.ReplaceAll(data, []byte("junie-child-read"), []byte("junie-second-read"))
	data = bytes.ReplaceAll(data, []byte("FIRST_MARKER"), []byte("SECOND_MARKER"))
	replacement := filepath.Join(home, "replacement-state.json")
	require.NoError(t, os.WriteFile(replacement, data, 0o600))
	require.NoError(t, os.Rename(replacement, path))
	tail.poll()
	rows := junieChildMessageContents(sink, row.ChildAgentID)
	assert.Contains(t, strings.Join(rows, "\n"), "SECOND_MARKER")
	assert.Len(t, rows, firstCount+2)
	tail.poll()
	assert.Len(t, sink.Child(row.ChildAgentID).Messages(), firstCount+2)
}

// Junie stores the text "The action is in progress." as the result of a tool
// call that still runs, and replaces it with the real result when the call
// ends (ACTION_IN_PROGRESS_PLACEHOLDER of AbstractIssueSingleStepAgentWorker).
// A probe read both values from state.json of the real CLI, with 5 s between
// them for a child Read. The tail must wait for the real result. A placeholder
// that counts as the result closes the row for good: the first poll sees the
// placeholder, and the real result never reaches the child tab.
func TestJunieChildTailWaitsForTheRealResultOfARunningCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUNIE_HOME", home)
	const sessionID = "session-260928-061937-xr13"
	const prompt = "Read a file."
	const placeholder = "The action is in progress."
	writeJunieChildState(t, home, sessionID, prompt, placeholder)
	a, sink := newJunieChildTestAgent(t)
	a.SetContextForTest(t.Context())
	a.SetSessionIDForTest(sessionID)
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, sessionID, map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": junieTestChildSessionID,
		"name": "leapmux-e2e-child", "task": prompt,
	}))
	defer a.stopChildTails()
	row, ok := sink.BackgroundTask(junieTestChildSessionID)
	require.True(t, ok)
	tail := a.childTails[junieTestChildSessionID]
	require.NotNil(t, tail)

	rows := junieChildMessageContents(sink, row.ChildAgentID)
	assert.Len(t, rows, 2, "the task and the request of the running call, and no result")
	assert.NotContains(t, strings.Join(rows, "\n"), placeholder)
	tail.poll()
	assert.Len(t, sink.Child(row.ChildAgentID).Messages(), 2, "a repeated placeholder adds no row")

	path := filepath.Join(home, "sessions", sessionID, "state.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	replacement := filepath.Join(home, "replacement-state.json")
	require.NoError(t, os.WriteFile(replacement, bytes.ReplaceAll(data, []byte(placeholder), []byte("REAL_RESULT")), 0o600))
	require.NoError(t, os.Rename(replacement, path))
	tail.poll()
	rows = junieChildMessageContents(sink, row.ChildAgentID)
	assert.Len(t, rows, 3, "the real result is the one result row")
	assert.Contains(t, strings.Join(rows, "\n"), "REAL_RESULT")
	assert.NotContains(t, strings.Join(rows, "\n"), placeholder)
}

// The two other placeholders of the jar state a call that ended without a real
// result. They are final, so the child tab shows them as the result.
func TestJunieChildTailShowsTheFinalPlaceholdersAsResults(t *testing.T) {
	for _, placeholder := range []string{
		"The action was cancelled.",
		"The action was interrupted because the user sent a real-time follow-up message.",
	} {
		t.Run(placeholder, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("JUNIE_HOME", home)
			const sessionID = "session-260928-061937-xr13"
			const prompt = "Read a file."
			writeJunieChildState(t, home, sessionID, prompt, placeholder)
			a, sink := newJunieChildTestAgent(t)
			a.SetContextForTest(t.Context())
			a.SetSessionIDForTest(sessionID)
			a.HandleSessionUpdateForTest(junieSessionUpdate(t, sessionID, map[string]any{
				"sessionUpdate": "subagent_spawned", "subagentSessionId": junieTestChildSessionID,
				"name": "leapmux-e2e-child", "task": prompt,
			}))
			defer a.stopChildTails()
			row, ok := sink.BackgroundTask(junieTestChildSessionID)
			require.True(t, ok)
			rows := junieChildMessageContents(sink, row.ChildAgentID)
			assert.Len(t, rows, 3)
			assert.Contains(t, strings.Join(rows, "\n"), placeholder)
		})
	}
}

func TestJunieChildEventLinkIgnoresUnrelatedFrames(t *testing.T) {
	t.Parallel()
	events := junieEventPair(t, junieTestStepID, "agent-1", "task-1", "leapmux-e2e-child", "Read a file.")
	unrelated := []byte(`{"kind":"SessionA2uxEvent","taskId":"task-1","event":{"agentEvent":{"kind":"MarkdownBlockUpdatedEvent","stepId":"different","text":"SubagentSpawnedEvent"}}}`)
	events = append(unrelated, append([]byte{'\n'}, events...)...)
	link, err := junieChildEventLink(events, junieTestChildSessionID, "leapmux-e2e-child", "Read a file.")
	require.NoError(t, err)
	assert.Equal(t, "agent-1", link.handle)
}
