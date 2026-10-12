package muse

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseIdleInterruptDoesNotWriteANativeCommand(t *testing.T) {
	a, sink := testAgent(t)
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(string, json.RawMessage) agenttest.RPCReply {
		calls++
		return agenttest.RPCReply{}
	}})
	require.NoError(t, a.Interrupt(agent.NewStopContext(nil)))
	assert.Zero(t, calls)
	assert.Empty(t, sink.TurnLifecycle())
}

func TestMuseInterruptTargetsTheExactNativeSessionAndTurn(t *testing.T) {
	a, _ := testAgent(t)
	a.sessions["session"].turnID = "native-turn"
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		calls++
		require.Equal(t, methodTurnInterrupt, method)
		var params struct {
			SessionID string `json:"sessionId"`
			TurnID    string `json:"turnId"`
			CommandID string `json:"commandId"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "session", params.SessionID)
		assert.Equal(t, "native-turn", params.TurnID)
		assert.NotEmpty(t, params.CommandID)
		result, err := json.Marshal(map[string]any{"commandId": params.CommandID, "status": "accepted"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.NoError(t, a.Interrupt(agent.NewStopContext(nil)))
	assert.Equal(t, 1, calls)
	assert.Equal(t, "native-turn", a.sessions["session"].turnID)
}

func TestMuseInterruptRefusalKeepsTheNativeTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.sessions["session"].turnID = "native-turn"
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodTurnInterrupt, method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"native interruption refused"}`)}
	}})
	require.ErrorContains(t, a.Interrupt(agent.NewStopContext(nil)), "native interruption refused")
	assert.Equal(t, "native-turn", a.sessions["session"].turnID)
	assert.Empty(t, sink.TurnLifecycle())
}

func TestMuseRawInputRejectsAnUnsupportedMethodBeforeWriting(t *testing.T) {
	a, _ := testAgent(t)
	calls := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(string, json.RawMessage) agenttest.RPCReply {
		calls++
		return agenttest.RPCReply{}
	}})
	require.ErrorContains(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":"control","method":"future/method","params":{}}`), agent.NewStopContext(nil)), "unsupported")
	assert.Zero(t, calls)
}

func TestMuseLateInterruptAcknowledgementKeepsTheReplacementTurnActive(t *testing.T) {
	a, sink := testAgent(t)
	a.startTurn("session", "old-turn")
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodTurnInterrupt, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "old-turn", params["turnId"])
		feed(t, a, contracts.MuseMethodTurnCompleted, map[string]any{"sessionId": "session", "turnId": "old-turn", "terminal": contracts.MuseTurnOutcomeCompleted})
		a.startTurn("session", "replacement-turn")
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Equal(t, "replacement-turn", a.sessions["session"].turnID)
	assert.Equal(t, []string{"turn_active:true", "turn_end", "turn_active:false", "turn_active:true"}, sink.TurnLifecycle())
}

func TestMuseInterruptUsesItsOriginalProviderInstance(t *testing.T) {
	original, _ := testAgent(t)
	replacement, _ := testAgent(t)
	original.sessions["session"].turnID = "old-turn"
	replacement.sessions["session"].turnID = "replacement-turn"
	writes := 0
	replacement.SetStdinForTest(&museTestPeer{agent: replacement, reply: func(string, json.RawMessage) agenttest.RPCReply {
		writes++
		return agenttest.RPCReply{}
	}})
	original.SetStdinForTest(&museTestPeer{agent: original, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "old-turn", params["turnId"])
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.NoError(t, original.Interrupt(agent.StopContext{}))
	assert.Zero(t, writes)
	assert.Equal(t, "replacement-turn", replacement.sessions["session"].turnID)
}

func TestMuseOverlappingInterruptsKeepTheirExactCommandIdentities(t *testing.T) {
	a, _ := testAgent(t)
	a.sessions["session"].turnID = "native-turn"
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var commands []string
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, raw json.RawMessage) agenttest.RPCReply {
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "native-turn", params["turnId"])
		commands = append(commands, params["commandId"].(string))
		once.Do(func() { close(started); <-release })
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	results := make(chan error, 2)
	go func() { results <- a.Interrupt(agent.StopContext{}) }()
	ctx := museRoleContext(t)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the first native interrupt did not reach its controlled stop point")
	}
	go func() { results <- a.Interrupt(agent.StopContext{}) }()
	close(release)
	for range 2 {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("the overlapping native interrupts did not complete")
		}
	}
	require.Len(t, commands, 2)
	assert.NotEqual(t, commands[0], commands[1])
	assert.Equal(t, "native-turn", a.sessions["session"].turnID)
}

type museStopWriteFailure struct {
	count int
	err   error
}

func (w museStopWriteFailure) Write(raw []byte) (int, error) { return min(w.count, len(raw)), w.err }
func (museStopWriteFailure) Close() error                    { return nil }

func TestMuseInterruptPreservesFailedAndUncertainWrites(t *testing.T) {
	for _, count := range []int{0, 1} {
		t.Run(map[int]string{0: "zero bytes", 1: "partial write"}[count], func(t *testing.T) {
			a, sink := testAgent(t)
			a.sessions["session"].turnID = "native-turn"
			writeErr := errors.New("the native input pipe failed")
			a.SetStdinForTest(museStopWriteFailure{count: count, err: writeErr})
			err := a.Interrupt(agent.StopContext{})
			require.ErrorIs(t, err, writeErr)
			assert.Equal(t, "native-turn", a.sessions["session"].turnID)
			assert.Empty(t, sink.TurnLifecycle())
		})
	}
	var _ io.WriteCloser = museStopWriteFailure{}
}

func TestMuseChildInterruptTargetsOnlyTheChildAndKeepsItsParentActive(t *testing.T) {
	a, sink := testAgent(t)
	a.sessions["session"].turnID = "parent-turn"
	a.sessions["child-session"] = &sessionState{childKey: "child-key", childID: "child-agent", parentSessionID: "session", subagentID: "native-child", turnID: "child-turn"}
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodChildInterrupt, method)
		var params map[string]any
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "session", params["sessionId"])
		assert.Equal(t, "native-child", params["subagentId"])
		assert.NotContains(t, params, "turnId")
		result, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: result}
	}})
	require.NoError(t, a.InterruptChild("child-key", agent.StopContext{}))
	assert.Equal(t, "parent-turn", a.sessions["session"].turnID)
	assert.Equal(t, "child-turn", a.sessions["child-session"].turnID)
	assert.Empty(t, sink.TurnLifecycle())
	require.ErrorIs(t, a.InterruptChild("absent", agent.StopContext{}), agent.ErrChildRouteNotReady)
}
