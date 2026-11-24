//go:build unix

package goose

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPAgent_Interrupt_SendsSessionCancelNotification(t *testing.T) {
	t.Parallel()

	agent, requests := newGooseAgentForRPCWithResponder(t,
		func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	// Helper sets sessionID="session-1" by default.

	require.NoError(t, agent.Interrupt(agentapi.StopContext{}))

	// session/cancel is a notification, which the writer goroutine flushes.
	var got []agenttest.RecordedRequest
	require.Eventually(t, func() bool {
		got = requests()
		return len(got) > 0
	}, time.Second, 5*time.Millisecond, "the cancel never went out")
	require.Len(t, got, 1)
	assert.Equal(t, "session/cancel", got[0].Method)
	assert.Equal(t, "session-1", got[0].Params["sessionId"])
}

// The protocol asks the CLIENT to answer an outstanding permission request when it
// cancels a session, and Goose is the provider that proves why: it waits for that
// answer, so a bare `session/cancel` left the turn running for the rest of the session
// -- the thinking indicator never stopped, and the card the reader had just dismissed
// was the only thing that could have unblocked it.
func TestACPAgent_Interrupt_AnswersAnOutstandingPermissionRequest(t *testing.T) {
	t.Parallel()

	a, requests := newGooseAgentForRPCWithResponder(t,
		func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.SetSinkForTest(agentapi.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"session-1","options":[{"optionId":"allow_once","kind":"allow_once","name":"allow_once"}]}}`))

	require.NoError(t, a.Interrupt(agentapi.StopContext{}))

	var answer string
	require.Eventually(t, func() bool {
		for _, recorded := range requests() {
			if strings.Contains(recorded.Raw, `"id":7`) && strings.Contains(recorded.Raw, "outcome") {
				answer = recorded.Raw
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond, "the request the turn was blocked on was never answered")
	assert.Contains(t, answer, `"outcome":"cancelled"`, "the reader gave no decision, and none is invented")
}

// A request the reader already answered is not answered twice: the response the
// browser sent clears it, and the cancel then has nothing of its own to send.
func TestACPAgent_Interrupt_SkipsARequestTheReaderAlreadyAnswered(t *testing.T) {
	t.Parallel()

	a, requests := newGooseAgentForRPCWithResponder(t,
		func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	a.SetSinkForTest(agentapi.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"session-1"}}`))
	require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`), agentapi.StopContext{}))

	require.NoError(t, a.Interrupt(agentapi.StopContext{}))

	require.Eventually(t, func() bool {
		for _, recorded := range requests() {
			if recorded.Method == acp.MethodSessionCancel {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond, "the cancel never went out")
	cancelled := 0
	for _, recorded := range requests() {
		if strings.Contains(recorded.Raw, `"outcome":"cancelled"`) {
			cancelled++
		}
	}
	assert.Zero(t, cancelled, "the reader's own answer stands")
}

func TestACPAgent_Interrupt_NoSessionIsNoop(t *testing.T) {
	t.Parallel()

	agent, requests := newGooseAgentForRPCWithResponder(t,
		func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	// Wipe the session so cancelSession would emit a stale id; the
	// interrupt path must short-circuit instead of emitting at all.
	// (Base fields are reachable via the embedding promotion.)
	agent.Mu.Lock()
	agent.SetSessionIDForTest("")
	agent.Mu.Unlock()

	require.NoError(t, agent.Interrupt(agentapi.StopContext{}))
	// The empty-session path returns before it starts any write, so the
	// recorded requests are complete the moment the call returns.
	assert.Empty(t, requests(),
		"Interrupt before session/new completes must be a no-op")
}

func TestACPAgent_Interrupt_AfterStopErrors(t *testing.T) {
	t.Parallel()

	agent, _ := newGooseAgentForRPCWithResponder(t,
		func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	agent.SetStoppedForTest(true)

	err := agent.Interrupt(agentapi.StopContext{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

func TestGooseClearProviderStateResetsDelegationAndOutputState(t *testing.T) {
	t.Parallel()
	a, _ := newGooseAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"s2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetSinkForTest(agentapi.NewProviderServices(&agenttest.Sink{}))
	*a.HooksForTest() = a.hooks()
	a.Mu.Lock()
	a.gooseChildRenames = map[string]string{"spawn-1": "sess-1"}
	a.gooseOutput = map[string]gooseOutputState{"call-1": {sequence: 3, bytes: 10}}
	a.activeDelegations = map[string]struct{}{"sess-1": {}}
	a.promptOutput = goosePromptOutputState{goalArmed: true, lastMessageID: "msg-1"}
	a.Mu.Unlock()

	_, err := a.ClearContext()
	require.NoError(t, err)

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.gooseChildRenames)
	assert.Empty(t, a.gooseOutput)
	assert.Empty(t, a.activeDelegations)
	assert.Equal(t, goosePromptOutputState{}, a.promptOutput)
}

func TestGooseIdleInterruptClosesActiveDelegations(t *testing.T) {
	t.Parallel()
	a, _ := newGooseAgentForRPCWithResponder(t, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agentapi.NewProviderServices(sink))
	*a.HooksForTest() = a.hooks()

	// Register a running delegated subagent row and record it in activeDelegations.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "sess-fg-1",
		Kind:   bgtask.KindSubagent,
		Title:  "Delegated worker",
		Status: bgtask.StatusRunning,
	}))
	a.Mu.Lock()
	a.activeDelegations = map[string]struct{}{"sess-fg-1": {}}
	a.Mu.Unlock()

	require.NoError(t, a.Interrupt(agentapi.StopContext{}))

	a.Mu.Lock()
	remaining := len(a.activeDelegations)
	a.Mu.Unlock()
	assert.Zero(t, remaining, "idle Interrupt must drain activeDelegations")
	row, found := sink.BackgroundTask("sess-fg-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusInterrupted, row.Status, "idle Interrupt must close lingering delegated subagents as interrupted")
}
