package junie

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func junieSessionUpdate(t *testing.T, sessionID string, update map[string]any) json.RawMessage {
	t.Helper()
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "update": update})
	require.NoError(t, err)
	return params
}

func newJunieChildTestAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, sink := newJunieGoalAgent(t)
	a.SetSessionIDForTest("junie-main")
	a.HooksForTest().SessionMetadataHandler = a.handleSessionMetadata
	return a, sink
}

func TestJunieNativeChildSessionRoutesItsMessages(t *testing.T) {
	t.Parallel()
	a, sink := newJunieChildTestAgent(t)
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": "junie-child-1",
		"name": "junie-cli-docs", "task": "Read the local file.",
	}))

	row, ok := sink.BackgroundTask("junie-child-1")
	require.True(t, ok, "the native child announcement opens a registry row")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	require.NotEmpty(t, row.ChildAgentID, "the native session opens a child tab")
	child := sink.Child(row.ChildAgentID)
	require.Len(t, child.Messages(), 1, "the child task opens the child transcript")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, child.Messages()[0].Source)
	assert.JSONEq(t, `{"content":"Read the local file."}`, string(child.Messages()[0].Content))

	a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-child-1", map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "The child read the file."},
	}))
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
		"sessionUpdate": "subagent_state_update", "subagentSessionId": "junie-child-1", "state": "completed",
	}))

	row, ok = sink.BackgroundTask("junie-child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Len(t, sink.Messages(), 0, "the child's answer stays out of the root transcript")
	require.Len(t, child.Messages(), 2)
	assert.Contains(t, string(child.Messages()[1].Content), "The child read the file.")
}

func TestJunieNativeChildStateClosesTheRegistryRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state string
		want  bgtask.Status
	}{
		{state: "completed", want: bgtask.StatusCompleted},
		{state: "failed", want: bgtask.StatusFailed},
		{state: "cancelled", want: bgtask.StatusStopped},
		{state: "disconnected", want: bgtask.StatusFailed},
	} {
		t.Run(tc.state, func(t *testing.T) {
			t.Parallel()
			a, sink := newJunieChildTestAgent(t)
			a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
				"sessionUpdate": "subagent_spawned", "subagentSessionId": "junie-child-1",
				"name": "junie-cli-docs", "task": "Answer the question.",
			}))
			a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
				"sessionUpdate": "subagent_state_update", "subagentSessionId": "junie-child-1", "state": tc.state,
			}))
			row, ok := sink.BackgroundTask("junie-child-1")
			require.True(t, ok)
			assert.Equal(t, tc.want, row.Status)
		})
	}
}

func TestJunieNativeChildRejectsInvalidIDsAndDoesNotRepeatThePrompt(t *testing.T) {
	t.Parallel()
	a, sink := newJunieChildTestAgent(t)
	for _, childID := range []string{"", "junie-main"} {
		a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
			"sessionUpdate": "subagent_spawned", "subagentSessionId": childID,
			"name": "junie-cli-docs", "task": "Answer the question.",
		}))
	}
	assert.Empty(t, sink.BackgroundTasks(), "an invalid child session opens no row")

	spawn := junieSessionUpdate(t, "junie-main", map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": "junie-child-1",
		"name": "junie-cli-docs", "task": "Answer the question.",
	})
	a.HandleSessionUpdateForTest(spawn)
	a.HandleSessionUpdateForTest(spawn)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].ChildAgentID)
	assert.Len(t, sink.Child(rows[0].ChildAgentID).Messages(), 1, "a replay opens one prompt")
}

func TestJunieNativeChildKeepsAnUnknownStateOpen(t *testing.T) {
	t.Parallel()
	a, sink := newJunieChildTestAgent(t)
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": "junie-child-1",
		"name": "junie-cli-docs", "task": "Answer the question.",
	}))
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, "junie-main", map[string]any{
		"sessionUpdate": "subagent_state_update", "subagentSessionId": "junie-child-1", "state": "future_state",
	}))
	row, ok := sink.BackgroundTask("junie-child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status, "an unknown state supplies no final outcome")
}

func TestJunieNativeStateProjectsAChildToolResult(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUNIE_HOME", home)
	const sessionID = "session-260928-061937-xr13"
	const prompt = "Read /work/note.txt and report the marker."
	const marker = "JUNIE_CHILD_NATIVE_RESULT"
	writeJunieChildState(t, home, sessionID, prompt, marker)

	a, sink := newJunieChildTestAgent(t)
	a.SetContextForTest(t.Context())
	a.SetSessionIDForTest(sessionID)
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, sessionID, map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": junieTestChildSessionID,
		"name": "leapmux-e2e-child", "task": prompt,
	}))

	row, ok := sink.BackgroundTask(junieTestChildSessionID)
	require.True(t, ok)
	require.NotEmpty(t, row.ChildAgentID)
	rows := junieChildMessageContents(sink, row.ChildAgentID)
	contents := strings.Join(rows, "\n")
	assert.GreaterOrEqual(t, len(rows), 3, "the prompt and the tool request/result use separate rows")
	assert.Contains(t, contents, "open_entire_file")
	assert.Contains(t, contents, marker)
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), marker, "the child result stays out of the root transcript")
	}
}

func TestJunieNativeStateRejectsAnotherSessionSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JUNIE_HOME", home)
	const sessionID = "session-260928-061937-xr13"
	const prompt = "Read /work/note.txt and report the marker."
	const marker = "JUNIE_WRONG_SESSION_RESULT"
	writeJunieChildState(t, home, sessionID, prompt, marker)
	summary, err := json.Marshal(map[string]any{
		"sessionId":  "session-260928-061938-other",
		"projectDir": "/work",
		"subagents":  []any{map[string]any{"id": "agent-1", "name": "leapmux-e2e-child", "status": "Running"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, "sessions", sessionID, "summary.json"), summary, 0o600))

	a, sink := newJunieChildTestAgent(t)
	a.SetContextForTest(t.Context())
	a.SetSessionIDForTest(sessionID)
	a.HandleSessionUpdateForTest(junieSessionUpdate(t, sessionID, map[string]any{
		"sessionUpdate": "subagent_spawned", "subagentSessionId": junieTestChildSessionID,
		"name": "leapmux-e2e-child", "task": prompt,
	}))
	row, ok := sink.BackgroundTask(junieTestChildSessionID)
	require.True(t, ok)
	require.NotEmpty(t, row.ChildAgentID)
	assert.NotContains(t, strings.Join(junieChildMessageContents(sink, row.ChildAgentID), "\n"), marker,
		"a state file under a different native session cannot feed this child")
}

func junieChildMessageContents(sink *agenttest.Sink, childAgentID string) []string {
	var rows []string
	for _, message := range sink.Child(childAgentID).Messages() {
		rows = append(rows, string(message.Content))
	}
	return rows
}

const (
	junieTestStepID         = "29ea4167-0030-4a55-a11b-05178ab9ed3f"
	junieTestChildSessionID = "subagent-" + junieTestStepID
)

func writeJunieChildState(t *testing.T, home, sessionID, prompt, marker string) {
	t.Helper()
	child := map[string]any{
		"handle": "agent-1", "typeId": "leapmux-e2e-child", "displayName": "leapmux-e2e-child",
		"status": "RUNNING", "tasks": []string{prompt},
		"resume": map[string]any{"state": map[string]any{"observations": []any{
			map[string]any{"records": []any{}},
			map[string]any{"records": []any{map[string]any{
				"request": map[string]any{
					"type": "com.intellij.ml.llm.matterhorn.agent.actions.ToolActionRequest",
					"toolCallId": map[string]any{
						"id": "junie-child-read", "callId": "callid_child_read", "name": "open_entire_file",
					},
					"inputParams": map[string]any{"rawJsonObject": map[string]any{"path": "/work/note.txt"}},
				},
				"result": map[string]any{"content": marker, "text": marker, "images": []any{}},
			}}},
		}}},
	}
	blob, err := json.Marshal(map[string]any{
		"lastAgentState": map[string]any{"subagents": map[string]any{"runs": []any{child}}},
	})
	require.NoError(t, err)
	snapshot, err := json.Marshal(map[string]any{
		"kind": "SessionA2uxEvent", "taskId": "task-1",
		"event": map[string]any{"state": "IN_PROGRESS", "agentEvent": map[string]any{
			"kind": "AgentStateUpdatedEvent", "agent": map[string]any{"kind": "MainAgent", "id": "main"},
			"blob": string(blob),
		}},
	})
	require.NoError(t, err)
	directory := filepath.Join(home, "sessions", sessionID)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "state.json"), snapshot, 0o600))
	summary, err := json.Marshal(map[string]any{
		"sessionId":  sessionID,
		"projectDir": "/work",
		"subagents":  []any{map[string]any{"id": "agent-1", "name": "leapmux-e2e-child", "status": "Running"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "summary.json"), summary, 0o600))
	spawn, err := json.Marshal(map[string]any{
		"kind": "SessionA2uxEvent", "taskId": "task-1",
		"event": map[string]any{"agentEvent": map[string]any{
			"kind":   "SubagentSpawnedEvent",
			"agent":  map[string]any{"kind": "MainAgent", "id": "main"},
			"stepId": junieTestStepID + "-spawned", "name": "leapmux-e2e-child", "task": prompt,
		}},
	})
	require.NoError(t, err)
	updated, err := json.Marshal(map[string]any{
		"kind": "SessionA2uxEvent", "taskId": "task-1",
		"event": map[string]any{"agentEvent": map[string]any{
			"kind":   "CustomAgentBlockUpdatedEvent",
			"agent":  map[string]any{"kind": "CustomAgent", "id": "agent-1", "name": "leapmux-e2e-child"},
			"stepId": junieTestStepID, "name": "leapmux-e2e-child", "status": "STARTED",
		}},
	})
	require.NoError(t, err)
	events := append(append(spawn, '\n'), updated...)
	events = append(events, '\n')
	require.NoError(t, os.WriteFile(filepath.Join(directory, "events.jsonl"), events, 0o600))
}
