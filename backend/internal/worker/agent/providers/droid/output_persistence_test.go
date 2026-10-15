package droid

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestProcessEndRetriesRetainedDroidOutput(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%t", stop), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"assistant_text_delta","messageId":"partial","textDelta":"retained"}`)))
			sink.PersistErr = errors.New("write refused")
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
			attempts := len(sink.Messages())
			sink.PersistErr = nil
			done := make(chan struct{})
			close(done)
			a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: a.AgentID(), ProviderName: "droid", Ctx: t.Context(), ProcessDone: done})
			if stop {
				a.Stop()
			} else {
				require.NoError(t, a.Wait())
			}
			rows := sink.Messages()[attempts:]
			require.Len(t, rows, 2)
			_, text, completion, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
			require.True(t, ok)
			assert.Equal(t, "retained", text)
			assert.Equal(t, agent.MessageCompletionInterrupted, completion)
			assert.True(t, rows[1].TurnEnd)
		})
	}
}

func TestNewChildToolRetainsStateAfterPendingOutputRecovers(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput([]byte(droidChildAvailable))
	task, found := sink.BackgroundTask("child-1")
	require.True(t, found)
	child := sink.Child(task.ChildAgentID)
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"assistant_text_delta","messageId":"old","textDelta":"old answer"}`)))
	child.PersistErr = errors.New("write refused")
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"agent_turn_completed","reason":"cancelled"}`)))
	attempts := len(child.Messages())
	child.PersistErr = nil
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"tool_call","toolUse":{"id":"new-read","name":"Read","input":{"file_path":"note.txt"}}}`)))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"tool_result","toolUseId":"new-read","content":"New result","isError":false}`)))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	assert.Equal(t, []int{0, 1}, agenttest.TurnToolUseCounts(t, child.Messages()[attempts:]))
}

func TestFailedGenerationWriteDoesNotKeepEndedTurnActive(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		for _, boundary := range []struct {
			name       string
			payload    string
			status     bgtask.Status
			completion agent.MessageCompletion
		}{
			{"cancelled", `{"type":"agent_turn_completed","reason":"cancelled"}`, bgtask.StatusInterrupted, agent.MessageCompletionInterrupted},
			{"error", `{"type":"error","message":"model connection failed"}`, bgtask.StatusFailed, agent.MessageCompletionError},
		} {
			t.Run(fmt.Sprintf("child=%t/%s", child, boundary.name), func(t *testing.T) {
				t.Parallel()
				a, root, _ := newSteerAgent(t)
				a.armTurn()
				sessionID := "main-session"
				destination := root
				if child {
					a.HandleOutput([]byte(droidChildAvailable))
					task, found := root.BackgroundTask("child-1")
					require.True(t, found)
					sessionID = "child-1"
					destination = root.Child(task.ChildAgentID)
				}
				a.HandleOutput(droidAuditNotification(t, sessionID, []byte(`{"type":"assistant_text_delta","messageId":"old","textDelta":"retained old answer"}`)))
				destination.PersistErr = errors.New("write refused")
				a.HandleOutput(droidAuditNotification(t, sessionID, []byte(boundary.payload)))
				if child {
					assert.False(t, a.ActiveChildTurnState(sessionID).Active)
					task, found := root.BackgroundTask(sessionID)
					require.True(t, found)
					assert.Equal(t, boundary.status, task.Status)
				} else {
					a.Mu.Lock()
					assert.False(t, a.turnActive)
					a.Mu.Unlock()
				}
				failedAttempts := len(destination.Messages())
				destination.PersistErr = nil
				a.HandleOutput(droidAuditNotification(t, sessionID, []byte(`{"type":"assistant_text_delta","messageId":"new","textDelta":"new answer"}`)))
				a.HandleOutput(droidAuditNotification(t, sessionID, []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
				rows := destination.Messages()[failedAttempts:]
				newIndex := 2
				if boundary.name == "error" {
					newIndex = 1
					require.Len(t, destination.PersistedNotifications(), 1)
				}
				require.Len(t, rows, newIndex+2, "retain both text rows and native turn boundaries")
				_, text, completion, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
				require.True(t, ok)
				assert.Equal(t, "retained old answer", text)
				assert.Equal(t, boundary.completion, completion)
				if boundary.name != "error" {
					assert.JSONEq(t, boundary.payload, string(rows[1].Content))
				}
				_, text, completion, ok = agenttest.DecodeAssembledMessage(rows[newIndex].Content)
				require.True(t, ok)
				assert.Equal(t, "new answer", text)
				assert.Equal(t, agent.MessageCompletionComplete, completion)
				assert.True(t, rows[newIndex+1].TurnEnd)
			})
		}
	}
}

func TestCompletedTextRetainsOutputAfterPersistenceFailure(t *testing.T) {
	t.Parallel()
	for _, end := range []string{
		`{"type":"assistant_text_complete","messageId":"partial","blockIndex":0}`,
		`{"type":"agent_turn_completed","reason":"cancelled"}`,
	} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"assistant_text_delta","messageId":"partial","textDelta":"retained"}`)))
			sink.PersistErr = errors.New("write refused")
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(end)))
			failedAttempts := len(sink.Messages())
			sink.PersistErr = nil
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"droid_working_state_changed","newState":"idle"}`)))
			rows := sink.Messages()[failedAttempts:]
			require.NotEmpty(t, rows)
			_, text, _, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
			require.True(t, ok)
			assert.Equal(t, "retained", text)
			assert.Empty(t, a.outputStates["main-session"].pending)
			count := len(sink.Messages())
			a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"assistant_text_complete","messageId":"partial","blockIndex":0}`)))
			assert.Len(t, sink.Messages(), count, "a late text completion does not duplicate the retained output")
		})
	}
}
