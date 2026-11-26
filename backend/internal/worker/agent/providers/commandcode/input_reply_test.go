package commandcode

import (
	"encoding/json"
	"sync"
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

func TestCommandCodeReplyKeysAreDistinctAcrossProcessRestarts(t *testing.T) {
	t.Parallel()
	var receipts []agent.NativeInputReceipt
	var replyKeys []string
	for _, text := range []string{"The original reply.", "The resumed reply."} {
		a, sink := commandCodeReplyAgent(t, nil)
		receipt, err := a.SendInput("Summarize the checks.", nil)
		require.NoError(t, err)
		require.Equal(t, "native-session", receipt.SessionID)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedCommandCodeReplyText(t, a, 10, text)
		finishCommandCodeReply(t, a, "turn_1")
		turns := sink.NativeReplyTurns()
		require.Len(t, turns, 1)
		assert.Equal(t, receipt.TurnID, turns[0].TurnID)
		assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
		require.Len(t, turns[0].Items, 1)
		receipts = append(receipts, receipt)
		replyKeys = append(replyKeys, turns[0].Items[0].MessageKey)
	}
	assert.NotEqual(t, receipts[0].InputID, receipts[1].InputID)
	assert.NotEqual(t, receipts[0].TurnID, receipts[1].TurnID)
	assert.NotEqual(t, replyKeys[0], replyKeys[1])
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

	receipt, err := a.SendInput("Summarize the checks.", nil)
	require.NoError(t, err)
	durableTurnID := a.commandCodeDurableTurnID("turn_1")
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "native-session", InputID: durableTurnID, TurnID: durableTurnID}, receipt)
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, durableTurnID, turns[0].TurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: durableTurnID, AfterOrdinal: 1}}, turns[0].Inputs)
	require.Len(t, turns[0].Items, 2)
	assert.Equal(t, int64(2), turns[0].Items[0].Ordinal)
	assert.Equal(t, int64(3), turns[0].Items[1].Ordinal)

	texts := map[string]string{}
	for _, row := range sink.Messages() {
		if row.NativeTurnID == durableTurnID {
			text, ok := agent.ReplyText(commandcodeProvider{}, agent.MessageContent{Original: row.Content})
			require.True(t, ok)
			texts[row.IdempotencyKey] = text
		}
	}
	assert.Equal(t, map[string]string{
		turns[0].Items[0].MessageKey: "First result.",
		turns[0].Items[1].MessageKey: "Second result.",
	}, texts)
	assert.Zero(t, a.replies.Len())
	assert.False(t, a.PublishTurnActive().Active)
}

func TestCommandCodeReplyRetainsTextFlushedByNativeCompletion(t *testing.T) {
	t.Parallel()
	a, sink := commandCodeReplyAgent(t, nil)
	receipt, err := a.SendInput("Summarize the checks.", nil)
	require.NoError(t, err)
	feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
	feedEvent(t, a, map[string]any{"type": "thinking_delta", "delta": "Reasoning."})
	feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "The rendered result."})
	finishCommandCodeReply(t, a, "turn_1")

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

	receipt, err := a.SendInput("Start the new turn.", nil)
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
			receipt, err := a.SendInput("Summarize the checks.", nil)
			require.NoError(t, err)

			tc.end(t, a, "turn_1")

			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		})
	}
}

func TestCommandCodeSendInputForSessionReturnsReceiptAndTracksReply(t *testing.T) {
	t.Parallel()
	a, sink := commandCodeReplyAgent(t, nil)
	receipt, err := a.SendInputForSession("native-session", "Summarize the checks.", nil)
	require.NoError(t, err)
	durableTurnID := a.commandCodeDurableTurnID("turn_1")
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "native-session", InputID: durableTurnID, TurnID: durableTurnID}, receipt)

	feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
	feedCommandCodeReplyText(t, a, 10, "Session-checked reply.")
	finishCommandCodeReply(t, a, "turn_1")

	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, receipt.TurnID, turns[0].TurnID)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
	require.Len(t, turns[0].Items, 1)
}

type commandCodeLifecycleOrderSink struct {
	*agenttest.Sink
	mu           sync.Mutex
	order        []string
	retiredTurns []string
}

func (s *commandCodeLifecycleOrderSink) record(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, event)
}

func (s *commandCodeLifecycleOrderSink) events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

func (s *commandCodeLifecycleOrderSink) retiredTurnIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.retiredTurns))
	copy(out, s.retiredTurns)
	return out
}

func (s *commandCodeLifecycleOrderSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	s.record("persist_turn_end")
	return s.Sink.PersistTurnEnd(content, span)
}

func (s *commandCodeLifecycleOrderSink) PublishNativeReplyTurn(turn agent.NativeReplyTurn) error {
	s.record("publish_native_reply_turn")
	return s.Sink.PublishNativeReplyTurn(turn)
}

func (s *commandCodeLifecycleOrderSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.mu.Lock()
	s.order = append(s.order, "retire_native_input_turn")
	s.retiredTurns = append(s.retiredTurns, receipt.TurnID)
	s.mu.Unlock()
	return s.Sink.RetireNativeInputTurn(receipt, outcome)
}

func (s *commandCodeLifecycleOrderSink) SetTurnState(state agent.TurnState, seq uint64) {
	if !state.Active {
		s.record("set_turn_inactive")
	}
	s.Sink.SetTurnState(state, seq)
}

func (s *commandCodeLifecycleOrderSink) CaptureSteerResolution(resolution agent.SteerResolution) func() error {
	run := s.Sink.CaptureSteerResolution(resolution)
	return func() error {
		label := "steer:other"
		switch resolution.Outcome {
		case agent.SteerOutcomePending:
			label = "steer:pending"
		case agent.SteerOutcomeEchoed:
			label = "steer:echoed"
		case agent.SteerOutcomeUncertain:
			label = "steer:uncertain"
		case agent.SteerOutcomeDropped:
			label = "steer:dropped"
		default:
			label = "steer:other"
		}
		s.record(label)
		return run()
	}
}

func TestCommandCodeReplySettlementPrecedesIdleTurnState(t *testing.T) {
	t.Parallel()
	t.Run("turn/completed end_turn publishes reply before idle", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		recorder := &commandCodeLifecycleOrderSink{Sink: sink}
		a.sink = agent.NewOrderedProviderServices(nil, agent.NewModelProgressResetSink(agent.NewProviderServices(recorder)))

		_, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedCommandCodeReplyText(t, a, 10, "Done.")
		finishCommandCodeReply(t, a, "turn_1")

		assert.Equal(t, []string{"persist_turn_end", "publish_native_reply_turn", "set_turn_inactive"}, recorder.events())
	})

	t.Run("turn/completed run_error retires reply before idle", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		recorder := &commandCodeLifecycleOrderSink{Sink: sink}
		a.sink = agent.NewOrderedProviderServices(nil, agent.NewModelProgressResetSink(agent.NewProviderServices(recorder)))

		_, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedMethod(t, a, contracts.CommandCodeMethodTurnCompleted, map[string]any{"turnId": "turn_1", "stopReason": "run_error"})

		assert.Equal(t, []string{"persist_turn_end", "retire_native_input_turn", "set_turn_inactive"}, recorder.events())
	})

	t.Run("finishStream retires reply before idle", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		recorder := &commandCodeLifecycleOrderSink{Sink: sink}
		a.sink = agent.NewOrderedProviderServices(nil, agent.NewModelProgressResetSink(agent.NewProviderServices(recorder)))

		receipt, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		a.finishStream()

		assert.Equal(t, []string{"persist_turn_end", "retire_native_input_turn", "set_turn_inactive"}, recorder.events())
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	})

	t.Run("discarding finishStream retires reply before idle without persisting turn end", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		recorder := &commandCodeLifecycleOrderSink{Sink: sink}
		a.sink = agent.NewOrderedProviderServices(nil, agent.NewModelProgressResetSink(agent.NewProviderServices(recorder)))

		receipt, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		a.DiscardOutput()
		a.finishStream()

		assert.Equal(t, []string{"retire_native_input_turn", "set_turn_inactive"}, recorder.events())
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	})
}

func TestCommandCodeDiscardingFinishStreamRetiresRepliesDropsSteersAndFinishesCompaction(t *testing.T) {
	t.Parallel()
	t.Run("retires tracked replies and drops pending steers silently", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, func(method string, _ json.RawMessage) agenttest.RPCReply {
			if method == methodTurnStart {
				return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"turnId":"turn_1","queued":1}`)}
		})
		receipt, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		require.NoError(t, agenttest.SteerInput(a, testCommandCodeSteer(1, "pending")))
		countBeforeDiscard := sink.MessageCount()

		a.DiscardOutput()
		a.finishStream()

		assert.Equal(t, countBeforeDiscard, sink.MessageCount())
		assert.Empty(t, sink.SteerResolutions(), "discarding exit drops pending steers without emitting resolutions")
		assert.Empty(t, a.pendingSteers)
		assert.Zero(t, a.replies.Len())
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		assert.False(t, a.PublishTurnActive().Active)
	})

	t.Run("finishes in-flight manual compaction on discarding exit", func(t *testing.T) {
		t.Parallel()
		a, sink := testAgent(t)
		attempt := &compactionAttempt{done: make(chan struct{})}
		a.Mu.Lock()
		a.manualCompaction = attempt
		a.compacting = true
		a.compactionLive = true
		a.Mu.Unlock()

		a.DiscardOutput()
		a.finishStream()

		select {
		case <-attempt.done:
		default:
			t.Fatal("manual compaction attempt must be finished on discarding exit")
		}
		assert.Nil(t, a.manualCompaction)
		assert.False(t, a.PublishTurnActive().Active)
		assert.Empty(t, sink.LeapMuxNotifications(), "discarding exit must not persist compaction_ended notification")
	})
}

func TestCommandCodeReplyCleanupRetiresInDeterministicKeyOrder(t *testing.T) {
	t.Parallel()
	t.Run("retireCommandCodeRepliesAfterExit", func(t *testing.T) {
		t.Parallel()
		for range 20 {
			a, sink := commandCodeReplyAgent(t, nil)
			recorder := &commandCodeLifecycleOrderSink{Sink: sink}
			a.sink = agent.NewOrderedProviderServices(nil, agent.NewModelProgressResetSink(agent.NewProviderServices(recorder)))
			a.replyProcessID = "proc"
			for _, turnID := range []string{"turn_c", "turn_a", "turn_b"} {
				durableTurnID := a.commandCodeDurableTurnID(turnID)
				a.beginCommandCodeReplyStart()
				a.finishCommandCodeReplyStart(turnID, agent.NativeInputReceipt{
					SessionID: "native-session",
					InputID:   durableTurnID,
					TurnID:    durableTurnID,
				})
			}
			a.finishStream()
			assert.Equal(t, []string{"proc:turn_a", "proc:turn_b", "proc:turn_c"}, recorder.retiredTurnIDs())
		}
	})

	t.Run("finishCommandCodeReplyStart", func(t *testing.T) {
		t.Parallel()
		for range 20 {
			a, sink := commandCodeReplyAgent(t, nil)
			recorder := &commandCodeLifecycleOrderSink{Sink: sink}
			a.sink = agent.NewOrderedProviderServices(nil, agent.NewModelProgressResetSink(agent.NewProviderServices(recorder)))
			a.replyProcessID = "proc"
			a.beginCommandCodeReplyStart()
			a.dispatchMu.Lock()
			for _, turnID := range []string{"turn_c", "turn_a", "turn_b"} {
				durableTurnID := a.commandCodeDurableTurnID(turnID)
				require.True(t, a.replies.RecordSteerBoundary(turnID, agent.NativeInputReceipt{
					SessionID: "native-session",
					InputID:   "steer:" + turnID,
					TurnID:    durableTurnID,
				}))
				a.replies.Finish(turnID, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, a.sink)
			}
			a.dispatchMu.Unlock()
			a.finishCommandCodeReplyStart("", agent.NativeInputReceipt{})
			assert.Equal(t, []string{"proc:turn_a", "proc:turn_b", "proc:turn_c"}, recorder.retiredTurnIDs())
		}
	})
}

type failCommandCodePersistSink struct {
	agent.ProviderServices
	messageErr error
	turnEndErr error
}

func (s *failCommandCodePersistSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.messageErr != nil {
		return s.messageErr
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func (s *failCommandCodePersistSink) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	if s.turnEndErr != nil {
		return s.turnEndErr
	}
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func TestCommandCodeReplyEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("turn/completed for inactive turn settles tracked native reply", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		receipt, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedCommandCodeReplyText(t, a, 0, "Zero-seq reply.")
		// Simulate a newer turn becoming active before turn_1 completes.
		a.Mu.Lock()
		a.turnID = "turn_newer"
		a.Mu.Unlock()
		finishCommandCodeReply(t, a, "turn_1")

		turns := sink.NativeReplyTurns()
		require.Len(t, turns, 1)
		assert.Equal(t, receipt.TurnID, turns[0].TurnID)
		require.Len(t, turns[0].Items, 1)
		assert.Contains(t, turns[0].Items[0].MessageKey, ":row:0")
		assert.Zero(t, a.replies.Len())
	})

	t.Run("non-retained PersistTurnEnd error marks turn unproved", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		a.sink = &failCommandCodePersistSink{ProviderServices: a.sink, turnEndErr: assert.AnError}

		receipt, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedCommandCodeReplyText(t, a, 10, "Reply.")
		finishCommandCodeReply(t, a, "turn_1")

		assert.Empty(t, sink.NativeReplyTurns())
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	})

	t.Run("non-retained PersistMessage error marks turn unproved", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		a.sink = &failCommandCodePersistSink{ProviderServices: a.sink, messageErr: assert.AnError}

		receipt, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedCommandCodeReplyText(t, a, 10, "Lost reply.")
		finishCommandCodeReply(t, a, "turn_1")

		assert.Empty(t, sink.NativeReplyTurns())
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	})

	t.Run("unknown stopReason retires turn as unproved", func(t *testing.T) {
		t.Parallel()
		a, sink := commandCodeReplyAgent(t, nil)
		receipt, err := a.SendInput("Summarize.", nil)
		require.NoError(t, err)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedMethod(t, a, contracts.CommandCodeMethodTurnCompleted, map[string]any{"turnId": "turn_1", "stopReason": "unknown_reason"})

		assert.Empty(t, sink.NativeReplyTurns())
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	})

	t.Run("discarding finishStream with nil sink discards buffered generation and ends compaction", func(t *testing.T) {
		t.Parallel()
		a, _ := testAgent(t)
		feedMethod(t, a, methodTurnStarted, map[string]any{"turnId": "turn_1"})
		feedEvent(t, a, map[string]any{"type": "text_delta", "delta": "buffered text"})
		a.Mu.Lock()
		a.compacting = true
		a.compactionLive = true
		a.Mu.Unlock()
		a.sink = nil
		a.DiscardOutput()

		assert.NotPanics(t, func() { a.finishStream() })
		assert.Empty(t, a.generation.ScopeIDs())
		assert.False(t, a.compactionLive)
	})
}
