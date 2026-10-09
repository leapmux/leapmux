package droid

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const droidTaskCall = `{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"tool_call","toolUse":{"id":"task-1","name":"Task","input":{"subagent_type":"explorer","description":"Inspect the note","prompt":"Read the child note."}}}}}`
const droidChildAvailable = `{"type":"notification","params":{"sessionId":"main-session","notification":{"type":"child_session_available","childSessionId":"child-1","toolUseId":"task-1","subagentType":"explorer","description":"Inspect the note"}}}`

func TestChildSessionAvailableCreatesRegistryAndPrompt(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidChildAvailable))
	a.HandleOutput([]byte(droidChildAvailable))

	row, ok := sink.BackgroundTask("child-1")
	require.True(t, ok, "the native child session appears in the task registry")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "worker-agent-1", row.ParentAgentID)
	assert.Equal(t, "Inspect the note", row.Title)
	require.NotEmpty(t, row.ChildAgentID)
	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 1, "a replay does not duplicate the child's prompt")
	var prompt map[string]string
	require.NoError(t, json.Unmarshal(childRows[0].Content, &prompt))
	assert.Equal(t, "Read the child note.", prompt["content"])
}

func TestChildSessionMessagesAndTurnEndStayInChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidTaskCall))
	a.HandleOutput([]byte(droidChildAvailable))
	// Isolate output routing from the separate missing-registration failure.
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "droid-tool-task-1", ProviderChildKey: "child-1", Title: "Inspect the note"})
	require.NoError(t, err)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "child-1", Kind: bgtask.KindSubagent, ChildAgentID: childID,
		ParentAgentID: a.AgentID(), Title: "Inspect the note", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.PersistChildPrompt(childID, "Read the child note."))
	rootCount := len(sink.Messages())

	for _, frame := range []string{
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"create_message","message":{"id":"msg-1","role":"assistant"}}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"assistant_text_delta","messageId":"msg-1","blockIndex":0,"textDelta":"CHILD_EARLY"}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"assistant_text_complete","messageId":"msg-1","blockIndex":0}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"tool_call","toolUse":{"id":"read-1","name":"Read","input":{"file_path":"/note.txt"}}}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"tool_result","toolUseId":"read-1","content":"CHILD_READ_MARKER","isError":false}}}`,
		`{"type":"notification","params":{"sessionId":"child-1","notification":{"type":"agent_turn_completed","turnId":"turn-1","reason":"end_turn"}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}

	childRows := sink.Child(childID).Messages()
	require.GreaterOrEqual(t, len(childRows), 5, "the child keeps its prompt, answer, tool call, result, and turn end")
	assert.Equal(t, rootCount, len(sink.Messages()), "child output does not enter the root transcript")
	assert.Contains(t, string(childRows[2].Content), "CHILD_EARLY")
	assert.Contains(t, string(childRows[4].Content), "CHILD_READ_MARKER")
	assert.True(t, childRows[len(childRows)-1].TurnEnd)
	assert.False(t, a.ActiveChildTurnState("child-1").Active)
	finished, ok := sink.BackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, finished.Status)
}

func TestUnknownNativeSessionCannotWriteIntoTheRoot(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	for _, frame := range []string{
		`{"type":"notification","params":{"sessionId":"unknown-session","notification":{"type":"create_message","message":{"id":"msg-1","role":"assistant"}}}}`,
		`{"type":"notification","params":{"sessionId":"unknown-session","notification":{"type":"droid_working_state_changed","newState":"thinking"}}}`,
		`{"type":"notification","params":{"sessionId":"unknown-session","notification":{"type":"agent_turn_completed","turnId":"turn-1","reason":"end_turn"}}}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.TurnActives(), "an unknown session cannot move the root turn")
}
