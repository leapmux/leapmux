package commandcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func commandCodeReplyAgent(t *testing.T, handler func(string, json.RawMessage) agenttest.RPCReply) (*Agent, *agenttest.Sink) {
	t.Helper()
	if handler == nil {
		handler = func(string, json.RawMessage) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1"}`)}
		}
	}
	a := agentWithPeer(t, handler)
	sink := &agenttest.Sink{}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	return a, sink
}

func feedCommandCodeReplyText(t *testing.T, a *Agent, seq uint64, text string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": contracts.CommandCodeFrameKindEvent, "seq": seq,
		"event": map[string]any{
			"type": contracts.CommandCodeEventMessageEnd, "content": []map[string]string{{"type": "text", "text": text}},
		},
	})
	require.NoError(t, err)
	a.HandleOutput(raw)
}

func finishCommandCodeReply(t *testing.T, a *Agent, turnID string) {
	t.Helper()
	feedMethod(t, a, contracts.CommandCodeMethodTurnCompleted, map[string]any{"turnId": turnID, "stopReason": "end_turn"})
}

func TestCommandCodeReplyUsesTheAcknowledgedTurnAfterItsEarlyEnd(t *testing.T) {
	t.Parallel()
	var a *Agent
	var sink *agenttest.Sink
	a, sink = commandCodeReplyAgent(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodTurnStart, method)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_autonomous"})
		feedCommandCodeReplyText(t, a, 8, "An independent turn.")
		finishCommandCodeReply(t, a, "turn_autonomous")

		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedCommandCodeReplyText(t, a, 21, "First result.")
		feedCommandCodeReplyText(t, a, 25, "Second result.")
		feedCommandCodeReplyText(t, a, 25, "Second result.")
		finishCommandCodeReply(t, a, "turn_1")
		return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1"}`)}
	})

	receipt, err := a.SendInputWithReceipt("Summarize the checks.", nil)
	require.NoError(t, err)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "native-session", InputID: "turn_1", TurnID: "turn_1"}, receipt)
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, "turn_1", turns[0].TurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: "turn_1", AfterOrdinal: 1}}, turns[0].Inputs)
	require.Len(t, turns[0].Items, 2)
	assert.Equal(t, int64(2), turns[0].Items[0].Ordinal)
	assert.Equal(t, int64(3), turns[0].Items[1].Ordinal)

	texts := map[string]string{}
	for _, row := range sink.Messages() {
		if row.NativeTurnID == "turn_1" {
			text, ok := agent.ReplyText(commandcodeProvider{}, agent.MessageContent{Original: row.Content})
			require.True(t, ok)
			texts[row.IdempotencyKey] = text
		}
	}
	assert.Equal(t, map[string]string{
		turns[0].Items[0].MessageKey: "First result.",
		turns[0].Items[1].MessageKey: "Second result.",
	}, texts)
	assert.Empty(t, a.nativeReplies)
	assert.False(t, a.PublishTurnActive().Active)
}

func TestCommandCodeReplyRetainsTextFlushedByNativeCompletion(t *testing.T) {
	t.Parallel()
	a, sink := commandCodeReplyAgent(t, nil)
	receipt, err := a.SendInputWithReceipt("Summarize the checks.", nil)
	require.NoError(t, err)
	feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": receipt.TurnID})
	feedEvent(t, a, map[string]any{"type": "thinking_delta", "delta": "Reasoning."})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "The rendered result."})
	finishCommandCodeReply(t, a, receipt.TurnID)

	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Items, 1)
	var items []agenttest.Message
	for _, row := range sink.Messages() {
		if _, _, _, assembled := agenttest.DecodeAssembledMessage(row.Content); assembled {
			items = append(items, row)
		}
	}
	require.Len(t, items, 2)
	assert.Equal(t, receipt.TurnID, items[1].NativeTurnID)
	assert.Equal(t, turns[0].Items[0].MessageKey, items[1].IdempotencyKey)
	text, ok := agent.ReplyText(commandcodeProvider{}, agent.MessageContent{Original: items[1].Content})
	require.True(t, ok)
	assert.Equal(t, "The rendered result.", text)
}

func TestCommandCodeReplyKeepsOldBufferedTextOutOfANewerTurn(t *testing.T) {
	t.Parallel()
	var a *Agent
	var sink *agenttest.Sink
	a, sink = commandCodeReplyAgent(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodTurnStart, method)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_previous"})
		feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "Text from the older turn."})
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		finishCommandCodeReply(t, a, "turn_1")
		return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1"}`)}
	})

	receipt, err := a.SendInputWithReceipt("Start the new turn.", nil)
	require.NoError(t, err)
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, receipt.TurnID, turns[0].TurnID)
	assert.Empty(t, turns[0].Items)
	for _, row := range sink.Messages() {
		_, text, completion, assembled := agenttest.DecodeAssembledMessage(row.Content)
		if assembled && text == "Text from the older turn." {
			assert.Equal(t, agent.MessageCompletionInterrupted, completion)
			assert.NotEqual(t, receipt.TurnID, row.NativeTurnID)
			return
		}
	}
	t.Fatal("the older text must stay visible as interrupted output")
}

func TestCommandCodeReplyNeedsANativeStartAndCompletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		end  func(*testing.T, *Agent, string)
	}{
		{"no native start", finishCommandCodeReply},
		{"process exit", func(t *testing.T, a *Agent, turnID string) {
			feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": turnID})
			feedCommandCodeReplyText(t, a, 10, "A reply before exit.")
			a.finishStream()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := commandCodeReplyAgent(t, nil)
			receipt, err := a.SendInputWithReceipt("Summarize the checks.", nil)
			require.NoError(t, err)

			tc.end(t, a, receipt.TurnID)

			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		})
	}
}
