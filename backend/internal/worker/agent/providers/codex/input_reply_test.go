package codex

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type codexReplyProofSink struct {
	agenttest.ControlSink
	bound   map[agent.NativeInputReceipt]string
	retired map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome
}

func (s *codexReplyProofSink) BindNativeInputTurn(receipt agent.NativeInputReceipt, turnID string) error {
	s.bound[receipt] = turnID
	return nil
}

func (s *codexReplyProofSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.retired[receipt] = outcome
	return nil
}

func TestCodexInputReplyBindsOnlyTheCorrelatedStartResult(t *testing.T) {
	t.Parallel()
	sink := &codexReplyProofSink{bound: make(map[agent.NativeInputReceipt]string), retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome)}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	pending := newCodexTurnStart("main-thread", nil, turnSettings{})
	pending.receipt = agent.NativeInputReceipt{SessionID: "main-thread", InputID: "sent-input"}
	pending.replyDone = make(chan struct{})
	pending.endedBeforeAck = make(map[string]bool)
	a.pendingStart = pending
	a.replyStarts = map[string]*codexPendingTurnStart{pending.receipt.InputID: pending}

	a.handleMainTurnStarted("unrelated-turn")
	require.Same(t, pending, a.pendingStart)
	select {
	case <-pending.replyDone:
		t.Fatal("an unrelated notification settled exact delivery")
	default:
	}
	a.recordCodexReplyEnd("main-thread", "unrelated-turn", true)
	assert.Empty(t, sink.bound)
	a.recordCodexReplyEnd("main-thread", "own-turn", true)
	a.bindCodexInputReply(pending, json.RawMessage(`{"turn":{"id":"own-turn","status":"completed","items":[]}}`), nil)
	assert.Equal(t, map[agent.NativeInputReceipt]string{pending.receipt: "own-turn"}, sink.bound)
	assert.Empty(t, sink.retired)
	assert.Empty(t, a.replyStarts, "an already ended turn needs no retirement")
	select {
	case <-pending.replyDone:
		require.NoError(t, pending.replyErr)
	default:
		t.Fatal("a correlated result did not confirm exact delivery")
	}

	missing := newCodexTurnStart("main-thread", nil, turnSettings{})
	missing.receipt = agent.NativeInputReceipt{SessionID: "main-thread", InputID: "unconfirmed"}
	missing.replyDone = make(chan struct{})
	missing.endedBeforeAck = make(map[string]bool)
	a.replyStarts[missing.receipt.InputID] = missing
	a.bindCodexInputReply(missing, json.RawMessage(`{"turn":{"id":"unrelated-turn"}}`), nil)
	require.NotContains(t, sink.bound, missing.receipt)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.retired[missing.receipt])
}

func TestCodexInputReplyTimesOutWithoutTheCorrelatedStartResult(t *testing.T) {
	t.Parallel()
	ctx := codexLifecycleTestContext(t)
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, "turn/start")
	t.Cleanup(trap.Close)
	a, _, requests := newCodexAgentForAsyncRPCRequests(t, func(request codexRecordedRequest, reply func(agenttest.RPCReply)) {
		if request.Method != "turn/start" {
			reply(agenttest.RPCReply{Result: json.RawMessage(`{}`)})
		}
	}, clock)
	sink := &codexReplyProofSink{bound: make(map[agent.NativeInputReceipt]string), retired: make(map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome)}
	a.sink = agent.NewProviderServices(sink)
	a.threadID = "main-thread"
	done := make(chan error, 1)
	go func() {
		_, err := a.SendInput("The submitted prompt", nil)
		done <- err
	}()
	require.Equal(t, turnStartAckTimeout, testutil.WaitForTimer(t, ctx, trap))

	a.Mu.Lock()
	pending := a.pendingStart
	a.Mu.Unlock()
	require.NotNil(t, pending)
	a.handleMainTurnStarted("unrelated-turn")
	select {
	case err := <-done:
		t.Fatalf("a notification settled exact delivery: %v", err)
	default:
	}

	clock.Advance(turnStartAckTimeout).MustWait(ctx)
	select {
	case err := <-done:
		require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	case <-ctx.Done():
		t.Fatal("the start request did not report delivery uncertainty")
	}
	assert.Empty(t, sink.bound)
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		pending.receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.retired)
	assert.Nil(t, a.pendingStart)
	assert.Empty(t, a.replyStarts)
	assert.Equal(t, "unrelated-turn", a.mainTurn.id)
	_, err := a.SendRequest("test/barrier", json.RawMessage(`{}`), 0)
	require.NoError(t, err)
	require.Len(t, requests(), 2)
	assert.Equal(t, "turn/start", requests()[0].Method)
}
