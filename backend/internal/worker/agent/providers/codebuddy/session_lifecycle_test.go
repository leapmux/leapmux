package codebuddy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

const codebuddyReplayInit = `{"type":"system","subtype":"init","session_id":"session-1","model":"unknown","permissionMode":"plan","tools":[],"mcp_servers":[],"slash_commands":[]}`

func TestCodebuddyResumeFailureDetails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		errors []string
		want   string
	}{
		{name: "absent details", want: "CodeBuddy could not restore the session."},
		{name: "blank details", errors: []string{" ", "\t"}, want: "CodeBuddy could not restore the session."},
		{name: "separate failures", errors: []string{"First failure.", "Second failure.", "Third failure."}, want: "CodeBuddy could not restore the session.\nFirst failure.\nSecond failure.\nThird failure."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			state := &codebuddyResumeState{done: make(chan error, 1)}
			a.resumeState = state
			frame, err := json.Marshal(map[string]any{"type": "result", "session_id": "session-1", "is_error": true, "errors": tc.errors})
			require.NoError(t, err)
			a.HandleOutput(frame)
			select {
			case err := <-state.done:
				require.EqualError(t, err, tc.want)
			default:
				t.Fatal("the native restore failure did not complete startup")
			}
			assert.Nil(t, a.resumeState)
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.SessionIDs())
		})
	}
}

func TestCodebuddyResumeReplayPreservesCurrentStateAndStoredHistory(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.model = "custom-local:current"
	state := &codebuddyResumeState{done: make(chan error, 1)}
	a.resumeState = state
	a.HandleOutput([]byte(codebuddyReplayInit))
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"text","text":"Old answer."},{"type":"tool_use","id":"old-plan","name":"ExitPlanMode"},{"type":"tool_use","id":"old-child","name":"Agent","input":{"prompt":"Old task."}}]}}`))
	a.HandleOutput([]byte(`{"type":"user","session_id":"session-1","message":{"content":[{"type":"tool_result","tool_use_id":"old-plan","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet."}]}]}}`))
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","is_error":true,"errors":["Historical failure."]}`))
	select {
	case err := <-state.done:
		require.NoError(t, err, "a previous turn's failure must not fail restoration")
	default:
		t.Fatal("the native replay did not complete")
	}
	assert.Nil(t, a.resumeState)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.SessionIDs())
	assert.Empty(t, sink.BackgroundTasks())
	assert.Empty(t, a.turnToolCalls)
	assert.Empty(t, a.pendingPlanModeTools)
	assert.False(t, a.active)
	assert.False(t, a.NativeTurnRestartRequired())
	assert.Equal(t, "custom-local:current", a.model)
	assert.Equal(t, "default", a.permissionMode)
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"text","text":"New answer."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","_requestId":"new-request","is_error":false}`))
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[0].Content), "New answer.")
}

func TestCodebuddyResumeReplayRejectsUnrelatedCompletion(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"result","session_id":"foreign","is_error":false}`,
		`{"type":"result","session_id":"session-1","parent_tool_use_id":"child","is_error":false}`,
		`{"type":"result","session_id":"session-1","_requestId":"live-request","is_error":false}`,
		`{"type":"result","session_id":"session-1"}`,
		`{"type":"result","session_id":"session-1","is_error":"invalid"}`,
	} {
		sink := &agenttest.Sink{}
		a := newOfflineAgent(t, sink)
		state := &codebuddyResumeState{done: make(chan error, 1)}
		a.resumeState = state
		a.HandleOutput([]byte(codebuddyReplayInit))
		a.HandleOutput([]byte(frame))
		assert.Same(t, state, a.resumeState)
		assert.Empty(t, state.done)
		assert.Empty(t, sink.Messages())
	}
}

func TestCodebuddyResumeReplayKeepsControlsFunctional(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newOfflineAgent(t, &sink.Sink)
	a.sink = agent.NewProviderServices(sink)
	a.resumeState = &codebuddyResumeState{done: make(chan error, 1)}
	a.HandleOutput([]byte(codebuddyReplayInit))
	reply := make(chan codebuddyControlResult, 1)
	a.pendingControl["catalog"] = reply
	a.HandleOutput([]byte(codebuddyControlAnswer(`{"availableModels":[]}`)("catalog")))
	select {
	case result := <-reply:
		assert.True(t, result.Success)
		assert.True(t, result.HasModelCatalog)
	default:
		t.Fatal("the replay swallowed a pending control response")
	}
	a.HandleOutput(canUseTool("live-permission"))
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Empty(t, a.resumeState.done)
}

func TestCodebuddyResumeReplayRequiresItsOwnInit(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"system","subtype":"init","session_id":"foreign","model":"unknown"}`,
		`{"type":"system","subtype":"init","session_id":"session-1","model":"custom-local:current"}`,
		`{"type":"system","subtype":"init","session_id":"session-1","model":"unknown","parent_tool_use_id":"child"}`,
	} {
		a := newOfflineAgent(t, &agenttest.Sink{})
		a.resumeState = &codebuddyResumeState{done: make(chan error, 1)}
		a.HandleOutput([]byte(frame))
		assert.False(t, a.resumeState.started)
	}
}
