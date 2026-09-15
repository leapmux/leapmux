//go:build unix

package agent

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reasonixGoalRPCFixture(t *testing.T, respond func(recordedRequest) jsonrpcResponsePayload) (*ReasonixAgent, *testSink, func() []recordedRequest) {
	t.Helper()
	if respond == nil {
		var objective string
		respond = func(request recordedRequest) jsonrpcResponsePayload {
			if request.Method == acpMethodSessionPrompt {
				blocks := request.Params["prompt"].([]interface{})
				objective = blocks[0].(map[string]interface{})["text"].(string)
			}
			if request.Method == reasonixMethodSessionStatus {
				status, err := json.Marshal(map[string]interface{}{
					"sessionId": request.Params["sessionId"], "mode": "goal",
					"goal": map[string]interface{}{"status": "running", "objective": objective},
				})
				require.NoError(t, err)
				return jsonrpcResponsePayload{Result: status}
			}
			return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
		}
	}
	a, requests := newACPAgentForRPCWithRequestResponder(t,
		func() *ReasonixAgent { return &ReasonixAgent{} },
		func(a *ReasonixAgent) *acpBase { return &a.acpBase },
		respond,
	)
	sink := &testSink{}
	a.sink = sink
	a.availableModes = []*leapmuxv1.AvailableOption{{Id: "normal"}, {Id: "goal"}}
	a.modeChannel = modeChannelPermissionMode
	a.wireTurnActive()
	return a, sink, requests
}

func TestReasonixGoalSetDeliversTheObjectiveAfterBothModeChanges(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	objective := "Keep the original bytes.\n한글 and ASCII stay intact."
	outcome, err := a.PerformGoalAction(GoalActionSet, objective)
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	require.Len(t, requests(), 4)
	actual := requests()
	assert.Equal(t, acpMethodSessionSetMode, actual[0].Method)
	assert.Equal(t, map[string]interface{}{"sessionId": "session-1", "modeId": "normal"}, actual[0].Params)
	assert.Equal(t, acpMethodSessionSetMode, actual[1].Method)
	assert.Equal(t, map[string]interface{}{"sessionId": "session-1", "modeId": "goal"}, actual[1].Params)
	assert.Equal(t, acpMethodSessionPrompt, actual[2].Method)
	assert.Equal(t, "session-1", actual[2].Params["sessionId"])
	assert.Equal(t, []interface{}{map[string]interface{}{"type": "text", "text": objective}}, actual[2].Params["prompt"])
	assert.Equal(t, reasonixMethodSessionStatus, actual[3].Method)
	goal, present := sink.LastGoal()
	assert.True(t, present)
	assert.Equal(t, objective, goal.Objective)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, 1, sink.GoalClears())
}

func TestReasonixGoalClearDoesNotSendAnObjective(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	outcome, err := a.PerformGoalAction(GoalActionClear, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	require.Len(t, requests(), 1)
	assert.Equal(t, acpMethodSessionSetMode, requests()[0].Method)
	assert.Equal(t, "normal", requests()[0].Params["modeId"])
	assert.Equal(t, 1, sink.GoalClears())
	assert.Empty(t, sink.TurnActives())
}

func TestReasonixGoalSetRejectsBusyAndEmptyInputsBeforeChangingModes(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	_, err := a.PerformGoalAction(GoalActionSet, " \n\t")
	require.ErrorContains(t, err, "empty")
	a.mu.Lock()
	a.promptActive = true
	a.mu.Unlock()
	_, err = a.PerformGoalAction(GoalActionSet, "New objective")
	require.ErrorIs(t, err, ErrAgentBusy)
	assert.Empty(t, requests())
	assert.Zero(t, sink.GoalClears())
}

func TestReasonixGoalRejectsUnsupportedOperationsWithoutSendingInput(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	for _, action := range []GoalAction{GoalActionPause, GoalActionResume, GoalAction(-1)} {
		_, err := a.PerformGoalAction(action, "")
		require.ErrorIs(t, err, ErrGoalControlUnsupported)
	}
	assert.Empty(t, requests())
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

func TestReasonixGoalSetStopsWhenAModeRequestFails(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"normal", "goal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a, sink, requests := reasonixGoalRPCFixture(t, func(request recordedRequest) jsonrpcResponsePayload {
				if request.Params["modeId"] == mode {
					return jsonrpcResponsePayload{Error: json.RawMessage(`{"code":-32603,"message":"Mode change failed"}`)}
				}
				return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
			})
			_, err := a.PerformGoalAction(GoalActionSet, "Keep the objective")
			require.ErrorContains(t, err, "Mode change failed")
			for _, request := range requests() {
				assert.NotEqual(t, acpMethodSessionPrompt, request.Method)
			}
			last, published := sink.LastTurnActive()
			assert.True(t, published)
			assert.False(t, last)
			assert.Empty(t, sink.Goals())
			if mode == "normal" {
				assert.Zero(t, sink.GoalClears())
			} else {
				assert.Equal(t, 1, sink.GoalClears(), "the first mode request confirmed removal of the previous objective")
			}
		})
	}
}

func TestReasonixGoalSetWaitsForTheNativeObjective(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	a, sink, _ := reasonixGoalRPCFixture(t, func(request recordedRequest) jsonrpcResponsePayload {
		if request.Method == reasonixMethodSessionStatus {
			if reads.Add(1) == 1 {
				return jsonrpcResponsePayload{Result: json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"none"}}`)}
			}
			return jsonrpcResponsePayload{Result: json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"running","objective":"Native objective"}}`)}
		}
		return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
	})
	_, err := a.PerformGoalAction(GoalActionSet, "Native objective")
	require.NoError(t, err)
	assert.EqualValues(t, 2, reads.Load())
	goal, present := sink.LastGoal()
	assert.True(t, present)
	assert.Equal(t, "Native objective", goal.Objective)
}

func TestReasonixGoalSetRejectsUnrelatedAndInvalidSnapshots(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`{"sessionId":"other-session","mode":"goal","goal":{"status":"running","objective":"Native objective"}}`,
		`{"sessionId":"session-1","mode":"goal","goal":{"status":"running","objective":"Another objective"}}`,
		`{"sessionId":"session-1","mode":"goal","goal":{}}`,
		`{"sessionId":"session-1","mode":"goal"}`,
	} {
		t.Run(response, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := reasonixGoalRPCFixture(t, func(request recordedRequest) jsonrpcResponsePayload {
				if request.Method == reasonixMethodSessionStatus {
					return jsonrpcResponsePayload{Result: json.RawMessage(response)}
				}
				return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
			})
			_, err := a.PerformGoalAction(GoalActionSet, "Native objective")
			require.Error(t, err)
			assert.Empty(t, sink.Goals())
		})
	}
}

func TestReasonixGoalSetKeepsANewerNativeNotification(t *testing.T) {
	t.Parallel()
	var current atomic.Pointer[ReasonixAgent]
	var reads atomic.Int32
	blocked := json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"blocked","objective":"Native objective"}}`)
	a, sink, _ := reasonixGoalRPCFixture(t, func(request recordedRequest) jsonrpcResponsePayload {
		if request.Method == reasonixMethodSessionStatus {
			if reads.Add(1) == 1 {
				current.Load().handleReasonixStatusUpdate(blocked)
				return jsonrpcResponsePayload{Result: json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"running","objective":"Native objective"}}`)}
			}
			return jsonrpcResponsePayload{Result: blocked}
		}
		return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
	})
	current.Store(a)
	_, err := a.PerformGoalAction(GoalActionSet, "Native objective")
	require.NoError(t, err)
	require.NotEmpty(t, sink.Goals())
	for _, goal := range sink.Goals() {
		assert.Equal(t, GoalStatusBlocked, goal.Status)
	}
}
