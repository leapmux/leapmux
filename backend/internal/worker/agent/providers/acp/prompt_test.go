package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPPromptCanStartAfterTheSessionChanges(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, _ := newACPTurnBase(t, agenttest.NopStdin(&output))
	require.NoError(t, a.SendInput("Old session input", nil))
	cleared := make(chan error, 1)
	go func() {
		_, err := a.ClearContext()
		cleared <- err
	}()
	testutil.RequireEventually(t, func() bool {
		return a.IsPendingForTest(int64(2))
	})
	a.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"new-session"}}`)))
	require.NoError(t, <-cleared)
	require.NoError(t, a.SendInput("New session input", nil))
}

func TestACPPromptIgnoresAResponseFromThePreviousSession(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	require.NoError(t, a.SendInput("Current session input", nil))
	a.finishPromptRequest("previous-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Empty(t, sink.Messages())
	active, _ := sink.LastTurnActive()
	assert.True(t, active)
	require.ErrorIs(t, a.SendInput("Later input", nil), agent.ErrAgentBusy)
}

func TestACPPromptPreservesTheCompleteNativeResultWrapper(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	a, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
	original := json.RawMessage(` {"id":"native-result", "role":"result", "future":9007199254740993, "content":{"stopReason":"end_turn","usage":{"totalTokens":0}}} `)
	a.handleACPPromptResponse(original)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, []byte(original), sink.Messages()[0].Content)
	assert.True(t, sink.Messages()[0].TurnEnd)
}

// promptEnd is one call of Hooks.PromptEnded.
type promptEnd struct {
	err     error
	stopped bool
}

// recordPromptEnds sets Hooks.PromptEnded to record each call.
func recordPromptEnds(b *Base) *[]promptEnd {
	var ends []promptEnd
	b.hooks.PromptEnded = func(err error, stopped bool) {
		ends = append(ends, promptEnd{err: err, stopped: stopped})
	}
	return &ends
}

// The hook reads each end of a prompt of the current session before the base
// writes its row or its note, so a provider can settle what it holds for the
// prompt.
func TestACPPromptEndedReadsEachEndBeforeTheBaseWritesIt(t *testing.T) {
	t.Parallel()
	failure := &providerkit.JSONRPCResponseError{Code: -32000, Message: "Rate exceeded"}
	for _, tc := range []struct {
		name        string
		response    json.RawMessage
		err         error
		interrupted bool
		// agentStopped states that the agent process stopped, as a Stop leaves it.
		agentStopped bool
		want         promptEnd
	}{
		{name: "a result", response: json.RawMessage(`{"stopReason":"end_turn"}`), want: promptEnd{}},
		{name: "an error", err: failure, want: promptEnd{err: failure}},
		{name: "an error of a stopped prompt", err: failure, interrupted: true, want: promptEnd{err: failure, stopped: true}},
		{name: "a result of a stopped prompt", response: json.RawMessage(`{"stopReason":"cancelled"}`), interrupted: true, want: promptEnd{stopped: true}},
		{name: "an error of a stopped agent", err: failure, agentStopped: true, want: promptEnd{err: failure, stopped: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			b, sink := newACPTurnBase(t, agenttest.NopStdin(&output))
			var ends []promptEnd
			written := -1
			b.hooks.PromptEnded = func(err error, stopped bool) {
				written = len(sink.Messages()) + sink.NotificationCount()
				ends = append(ends, promptEnd{err: err, stopped: stopped})
			}
			b.promptActive = true
			if tc.interrupted {
				b.noteACPInterruptRequested()
			}
			b.SetStoppedForTest(tc.agentStopped)

			b.finishPromptRequest("session-1", tc.response, tc.err)

			assert.Equal(t, []promptEnd{tc.want}, ends)
			assert.Zero(t, written, "the hook runs before the base writes the end")
		})
	}
}

func TestACPPromptEndedSkipsAResponseOfThePreviousSession(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	b, _ := newACPTurnBase(t, agenttest.NopStdin(&output))
	ends := recordPromptEnds(b)

	b.finishPromptRequest("previous-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)

	assert.Empty(t, *ends, "the prompt of a replaced session is no prompt of the current one")
}
