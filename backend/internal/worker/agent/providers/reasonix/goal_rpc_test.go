//go:build unix

package reasonix

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reasonixGoalRPCFixture(t *testing.T, respond func(agenttest.RecordedRequest) agenttest.RPCReply) (*Agent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	if respond == nil {
		var objective string
		respond = func(request agenttest.RecordedRequest) agenttest.RPCReply {
			if request.Method == acp.MethodSessionPrompt {
				blocks := request.Params["prompt"].([]interface{})
				objective = blocks[0].(map[string]interface{})["text"].(string)
			}
			if request.Method == reasonixMethodSessionStatus {
				status, err := json.Marshal(map[string]interface{}{
					"sessionId": request.Params["sessionId"], "mode": "goal",
					"goal": map[string]interface{}{"status": "running", "objective": objective},
				})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: status}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	}
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{{Id: "normal"}, {Id: "goal"}})
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.WireTurnActiveForTest()
	return a, sink, requests
}

func TestReasonixGoalSetDeliversTheObjectiveAfterBothModeChanges(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	objective := "Keep the original bytes.\n한글 and ASCII stay intact."
	outcome, err := a.PerformGoalAction(agent.GoalActionSet, objective)
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	require.Len(t, requests(), 4)
	actual := requests()
	assert.Equal(t, acp.MethodSessionSetMode, actual[0].Method)
	assert.Equal(t, map[string]interface{}{"sessionId": "session-1", "modeId": "normal"}, actual[0].Params)
	assert.Equal(t, acp.MethodSessionSetMode, actual[1].Method)
	assert.Equal(t, map[string]interface{}{"sessionId": "session-1", "modeId": "goal"}, actual[1].Params)
	assert.Equal(t, acp.MethodSessionPrompt, actual[2].Method)
	assert.Equal(t, "session-1", actual[2].Params["sessionId"])
	assert.Equal(t, []interface{}{map[string]interface{}{"type": "text", "text": objective}}, actual[2].Params["prompt"])
	assert.Equal(t, reasonixMethodSessionStatus, actual[3].Method)
	goal, present := sink.LastGoal()
	assert.True(t, present)
	assert.Equal(t, objective, goal.Objective)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Equal(t, 1, sink.GoalClears())
}

func TestReasonixGoalClearDoesNotSendAnObjective(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	outcome, err := a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput)
	require.Len(t, requests(), 1)
	assert.Equal(t, acp.MethodSessionSetMode, requests()[0].Method)
	assert.Equal(t, "normal", requests()[0].Params["modeId"])
	assert.Equal(t, 1, sink.GoalClears())
	assert.Empty(t, sink.TurnActives())
}

func TestReasonixGoalSetRejectsBusyAndEmptyInputsBeforeChangingModes(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	_, err := a.PerformGoalAction(agent.GoalActionSet, " \n\t")
	require.ErrorContains(t, err, "empty")
	a.Mu.Lock()
	a.SetPromptActiveForTest(true)
	a.Mu.Unlock()
	_, err = a.PerformGoalAction(agent.GoalActionSet, "New objective")
	require.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.Empty(t, requests())
	assert.Zero(t, sink.GoalClears())
}

func TestReasonixGoalRejectsUnsupportedOperationsWithoutSendingInput(t *testing.T) {
	t.Parallel()
	a, sink, requests := reasonixGoalRPCFixture(t, nil)
	for _, action := range []agent.GoalAction{agent.GoalActionPause, agent.GoalActionResume, agent.GoalAction(-1)} {
		_, err := a.PerformGoalAction(action, "")
		require.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
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
			a, sink, requests := reasonixGoalRPCFixture(t, func(request agenttest.RecordedRequest) agenttest.RPCReply {
				if request.Params["modeId"] == mode {
					return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"Mode change failed"}`)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			_, err := a.PerformGoalAction(agent.GoalActionSet, "Keep the objective")
			require.ErrorContains(t, err, "Mode change failed")
			for _, request := range requests() {
				assert.NotEqual(t, acp.MethodSessionPrompt, request.Method)
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
	a, sink, _ := reasonixGoalRPCFixture(t, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == reasonixMethodSessionStatus {
			if reads.Add(1) == 1 {
				return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"none"}}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"running","objective":"Native objective"}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Native objective")
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
			a, sink, _ := reasonixGoalRPCFixture(t, func(request agenttest.RecordedRequest) agenttest.RPCReply {
				if request.Method == reasonixMethodSessionStatus {
					return agenttest.RPCReply{Result: json.RawMessage(response)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			_, err := a.PerformGoalAction(agent.GoalActionSet, "Native objective")
			require.Error(t, err)
			assert.Empty(t, sink.Goals())
		})
	}
}

func TestReasonixGoalSetKeepsANewerNativeNotification(t *testing.T) {
	t.Parallel()
	var current atomic.Pointer[Agent]
	var reads atomic.Int32
	blocked := json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"blocked","objective":"Native objective"}}`)
	a, sink, _ := reasonixGoalRPCFixture(t, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == reasonixMethodSessionStatus {
			if reads.Add(1) == 1 {
				current.Load().handleReasonixStatusUpdate(blocked)
				return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-1","mode":"goal","goal":{"status":"running","objective":"Native objective"}}`)}
			}
			return agenttest.RPCReply{Result: blocked}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	current.Store(a)
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Native objective")
	require.NoError(t, err)
	require.NotEmpty(t, sink.Goals())
	for _, goal := range sink.Goals() {
		assert.Equal(t, agent.GoalStatusBlocked, goal.Status)
	}
}
