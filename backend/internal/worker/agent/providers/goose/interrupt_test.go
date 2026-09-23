//go:build unix

package goose

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPAgent_Interrupt_SendsSessionCancelNotification(t *testing.T) {
	t.Parallel()

	agent, requests := newGooseAgentForRPCWithResponder(t,
		func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	// Helper sets sessionID="session-1" by default.

	require.NoError(t, agent.Interrupt())

	// session/cancel is a notification — drain briefly.
	deadline := time.Now().Add(time.Second)
	var got []agenttest.RecordedRequest
	for time.Now().Before(deadline) {
		got = requests()
		if len(got) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
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
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"session-1","options":[{"optionId":"allow_once","kind":"allow_once","name":"allow_once"}]}}`))

	require.NoError(t, a.Interrupt())

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
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"session-1"}}`))
	require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`)))

	require.NoError(t, a.Interrupt())

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

	require.NoError(t, agent.Interrupt())
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, requests(),
		"Interrupt before session/new completes must be a no-op")
}

func TestACPAgent_Interrupt_AfterStopErrors(t *testing.T) {
	t.Parallel()

	agent, _ := newGooseAgentForRPCWithResponder(t,
		func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
	agent.SetStoppedForTest(true)

	err := agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}
