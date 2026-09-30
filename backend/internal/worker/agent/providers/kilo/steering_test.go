package kilo

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A steer is words the reader typed and watched for. A daemon that declines a
// concurrent prompt -- which the protocol does not oblige it to accept -- otherwise
// swallowed them with nothing anywhere the reader could see.
func TestOpenCodeFamilySteerFailureReachesTheReader(t *testing.T) {
	t.Parallel()

	ag, _ := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(agent *Agent) *acp.Base { return &agent.Base },
		func(method string) agenttest.RPCReply {
			if method != acp.MethodSessionPrompt {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			}
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32600,"message":"a turn is already running"}`)}
		},
	)
	sink := &agenttest.ControlSink{}
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	ag.SetPromptActiveForTest(true)

	require.NoError(t, ag.SteerInput("guide the turn", nil))
	require.Eventually(t, func() bool { return len(sink.Notifications()) == 1 }, time.Second, time.Millisecond)
	notice := sink.Notifications()[0]
	assert.Equal(t, contracts.NotificationTypeAgentError, notice["type"])
	assert.Contains(t, notice["error"], "a turn is already running")
}

// Kilo's override keeps the family's steering capability and preserves the
// second prompt as a separate turn when its response arrives later.
func TestKiloSteersThroughTheFamily(t *testing.T) {
	t.Parallel()
	_, steers := any(&Agent{}).(agent.InputSteerer)
	assert.True(t, steers, "Kilo steers through the family's second session/prompt")
	ag := &Agent{}
	*ag.HooksForTest() = opencode.FamilyHooks()
	assert.True(t, ag.SupportsSteering(),
		"every OpenCode-family provider steers with a second session/prompt")
}

func TestKiloSteerPersistsAnAnswerAfterTheFirstPromptEnds(t *testing.T) {
	t.Parallel()

	responseReady := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(responseReady) }) }
	defer release()
	ag, requests := acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method == acp.MethodSessionPrompt {
				<-responseReady
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
		},
	)
	sink := &agenttest.Sink{}
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	ag.SetPromptActiveForTest(true)
	require.NoError(t, ag.SteerInput("guide the turn", nil))
	testutil.RequireEventually(t, func() bool { return len(requests()) == 1 })
	recorded := requests()[0]
	assert.Equal(t, acp.MethodSessionPrompt, recorded.Method)
	assert.Equal(t, "session-1", recorded.Params["sessionId"])
	assert.Contains(t, recorded.Raw, `"text":"guide the turn"`)

	ag.FinishPromptRequestForTest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.True(t, ag.PromptActive(), "the detached steer keeps the Worker turn active after the first response")
	ag.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1",` +
		`"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"The steered answer."}}}}`))
	release()
	testutil.RequireEventually(t, func() bool {
		for _, message := range sink.Messages() {
			var frame struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			}
			if json.Unmarshal(message.Content, &frame) == nil && frame.Kind == contracts.AssembledMessageKindText && frame.Text == "The steered answer." {
				return true
			}
		}
		return false
	})
}

func TestKiloSteerKeepsConcurrentRepliesInOneTurn(t *testing.T) {
	t.Parallel()
	stdin := &agenttest.Stdin{}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a := &Agent{}
	a.AttachPeerForTest(ctx, cancel, stdin, "test-agent", "session-1")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("first steer", nil))
	require.NoError(t, a.SteerInput("second steer", nil))
	var requests []struct {
		ID int64 `json:"id"`
	}
	for _, line := range strings.Split(strings.TrimSpace(stdin.String()), "\n") {
		var request struct {
			ID int64 `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &request))
		requests = append(requests, request)
	}
	require.Len(t, requests, 2)
	a.FinishPromptRequestForTest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1",` +
		`"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"First. "}}}}`))
	require.True(t, a.Deliver(requests[1].ID, agenttest.JSONRPCResponse(requests[1].ID,
		agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)})))
	assert.True(t, a.PromptActive(), "the first detached reply does not close the other prompt")
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1",` +
		`"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Second."}}}}`))
	require.True(t, a.Deliver(requests[0].ID, agenttest.JSONRPCResponse(requests[0].ID,
		agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)})))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
	var texts []string
	for _, message := range sink.Messages() {
		var frame struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &frame) == nil && frame.Kind == contracts.AssembledMessageKindText {
			texts = append(texts, frame.Text)
		}
	}
	assert.Equal(t, []string{"First. Second."}, texts)
}

func TestKiloSteerFailedWriteReleasesItsTurn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a := &Agent{}
	a.AttachPeerForTest(ctx, cancel, agenttest.FailingStdin{}, "test-agent", "session-1")
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetPromptActiveForTest(true)

	require.Error(t, a.SteerInput("guide the turn", nil))
	a.FinishPromptRequestForTest("session-1", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.False(t, a.PromptActive(), "a failed detached write leaves no turn behind")
}
