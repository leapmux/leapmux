package droid

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// newDroidControlTestAgent returns an agent whose sink records each control
// request that the agent publishes.
func newDroidControlTestAgent(t *testing.T) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "droid-control-test", Stdin: &bufferStdin{}, Ctx: context.Background(), ProcessDone: processDone,
		}),
		sink: agent.NewProviderServices(sink), sessionID: "native-session",
	}
	t.Cleanup(func() {
		close(processDone)
		a.Process.Stop()
	})
	return a, sink
}

// publishedQuestions sends one droid.ask_user request to the agent and returns
// the questions of the control request that the agent publishes for it.
func publishedQuestions(t *testing.T, questions []map[string]any) []map[string]any {
	t.Helper()
	a, sink := newDroidControlTestAgent(t)
	env := newDroidEnvelope(droidTypeRequest)
	env.ID = "rpc-ask-1"
	env.Method = droidMethodAskUser
	var err error
	env.Params, err = json.Marshal(map[string]any{"toolCallId": "call-1", "questions": questions})
	require.NoError(t, err)
	a.handleServerRequest(nil, &env)

	published := sink.PublishedControls()
	require.Len(t, published, 1)
	var payload struct {
		Questions []map[string]any `json:"questions"`
	}
	require.NoError(t, json.Unmarshal(published[0].Payload, &payload))
	return payload.Questions
}

// Droid identifies each answer by the `index` of its question, and its own
// numbering starts at 1. The published request must keep that number, or the
// reply has nothing to identify a question with.
func TestAskUserPublishesTheNativeIndexOfEachQuestion(t *testing.T) {
	t.Parallel()
	questions := publishedQuestions(t, []map[string]any{
		{"index": 1, "topic": "Color", "question": "Which color?", "options": []string{"Blue", "Red"}},
		{"index": 2, "topic": "Sizes", "question": "Which sizes?", "options": []string{"Small", "Large"}, "multiSelect": true},
	})
	require.Len(t, questions, 2)
	assert.Equal(t, map[string]any{
		"index": float64(1), "question": "Which color?", "options": []any{"Blue", "Red"}, "multiSelect": false,
	}, questions[0])
	assert.Equal(t, map[string]any{
		"index": float64(2), "question": "Which sizes?", "options": []any{"Small", "Large"}, "multiSelect": true,
	}, questions[1])
}

// A question request that the worker cannot read is answered at once with
// Droid's own cancel. Droid's reply schema requires `answers` to be a list, so a
// cancel with an object there fails Droid's parse instead of cancelling.
func TestAskUserCancelsARequestThatItCannotRead(t *testing.T) {
	t.Parallel()
	stdin := &bufferStdin{}
	sink := &agenttest.ControlSink{}
	processDone := make(chan struct{})
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "droid-control-test", Stdin: stdin, Ctx: context.Background(), ProcessDone: processDone,
		}),
		sink: agent.NewProviderServices(sink), sessionID: "native-session",
	}
	t.Cleanup(func() {
		close(processDone)
		a.Process.Stop()
	})

	env := newDroidEnvelope(droidTypeRequest)
	env.ID = "rpc-ask-bad"
	env.Method = droidMethodAskUser
	env.Params = json.RawMessage(`{"toolCallId":"call-1","questions":"not a list"}`)
	a.handleServerRequest(nil, &env)

	assert.Zero(t, sink.PublishedControlCount(), "a request that the worker cannot read publishes no control")
	var reply struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	stdin.mu.Lock()
	written := stdin.buf.Bytes()
	stdin.mu.Unlock()
	require.NoError(t, json.Unmarshal(written, &reply))
	assert.Equal(t, "rpc-ask-bad", reply.ID)
	assert.JSONEq(t, `{"cancelled":true,"answers":[]}`, string(reply.Result))
}

// The published order is Droid's own order, even when Droid's numbers do not
// start at 1 or skip a number: the number identifies the question, and the
// position orders it.
func TestAskUserPublishesTheQuestionsInTheNativeOrder(t *testing.T) {
	t.Parallel()
	questions := publishedQuestions(t, []map[string]any{
		{"index": 7, "topic": "Q7", "question": "Seventh?", "options": []string{"Yes", "No"}},
		{"index": 3, "topic": "Q3", "question": "Third?", "options": []string{"Yes", "No"}},
	})
	require.Len(t, questions, 2)
	assert.Equal(t, float64(7), questions[0]["index"])
	assert.Equal(t, "Seventh?", questions[0]["question"])
	assert.Equal(t, float64(3), questions[1]["index"])
	assert.Equal(t, "Third?", questions[1]["question"])
}
