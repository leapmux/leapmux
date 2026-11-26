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

func TestCodexInputReplyBindsOnlyTheCorrelatedStartResult(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	pending := newCodexTurnStart("main-thread", nil, turnSettings{})
	pending.reply = newCodexTrackedReply(agent.NativeInputReceipt{SessionID: "main-thread", InputID: "sent-input"})
	a.pendingStart = pending
	a.replyStarts = map[string]*codexTrackedReply{pending.reply.receipt.InputID: pending.reply}

	a.handleMainTurnStarted("unrelated-turn")
	require.Same(t, pending, a.pendingStart)
	select {
	case <-pending.reply.done:
		t.Fatal("an unrelated notification settled exact delivery")
	default:
	}
	a.recordCodexReplyEnd("main-thread", "unrelated-turn", true)
	assert.Empty(t, sink.NativeInputTurnBindings())
	a.recordCodexReplyEnd("main-thread", "own-turn", true)
	a.handleCodexTurnStartResult(pending, json.RawMessage(`{"turn":{"id":"own-turn","status":"completed","items":[]}}`), nil)
	assert.Equal(t, map[agent.NativeInputReceipt]string{pending.reply.receipt: "own-turn"}, sink.NativeInputTurnBindings())
	assert.Empty(t, sink.NativeReplyRetirements())
	assert.Empty(t, a.replyStarts, "an already ended turn needs no retirement")
	select {
	case <-pending.reply.done:
		require.NoError(t, pending.reply.err)
	default:
		t.Fatal("a correlated result did not confirm exact delivery")
	}

	missing := newCodexTurnStart("main-thread", nil, turnSettings{})
	missing.reply = newCodexTrackedReply(agent.NativeInputReceipt{SessionID: "main-thread", InputID: "unconfirmed"})
	a.replyStarts[missing.reply.receipt.InputID] = missing.reply
	a.handleCodexTurnStartResult(missing, json.RawMessage(`{"turn":{"id":"unrelated-turn"}}`), nil)
	require.NotContains(t, sink.NativeInputTurnBindings(), missing.reply.receipt)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[missing.reply.receipt])
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
	sink := &agenttest.ControlSink{}
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
	assert.Empty(t, sink.NativeInputTurnBindings())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		pending.reply.receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
	assert.Nil(t, a.pendingStart)
	assert.Empty(t, a.replyStarts)
	assert.Equal(t, "unrelated-turn", a.mainTurn.id)
	_, err := a.SendRequest("test/barrier", json.RawMessage(`{}`), 0)
	require.NoError(t, err)
	require.Len(t, requests(), 2)
	assert.Equal(t, "turn/start", requests()[0].Method)
}

func TestCodexTrackedTurnStartUncertainErrorWithoutActiveTurnKeepsPendingStartAttachable(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	pending := newCodexTurnStart("main-thread", []map[string]interface{}{{"type": "text", "text": "hello"}}, turnSettings{})
	pending.reply = newCodexTrackedReply(agent.NativeInputReceipt{SessionID: "main-thread", InputID: "sent-input"})
	a.pendingStart = pending
	a.replyStarts = map[string]*codexTrackedReply{pending.reply.receipt.InputID: pending.reply}

	// Non-timeout ErrDeliveryUncertain while process is alive and no turn started yet.
	a.handleCodexTurnStartResult(pending, nil, agent.ErrDeliveryUncertain)

	// Delayed turn/started must still match and clear a.pendingStart, attaching pending.input.
	a.handleMainTurnStarted("delayed-turn")
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Nil(t, a.pendingStart, "delayed turn/started must clear pendingStart after non-timeout uncertain error")
	assert.Equal(t, []map[string]interface{}{{"type": "text", "text": "hello"}}, a.mainTurn.input)
}

func TestCodexTrackedTurnStartWithoutTurnIDKeepsPendingStartUntilTurnStarted(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	pending := newCodexTurnStart("main-thread", []map[string]interface{}{{"type": "text", "text": "hello"}}, turnSettings{})
	pending.reply = newCodexTrackedReply(agent.NativeInputReceipt{SessionID: "main-thread", InputID: "sent-input"})
	a.pendingStart = pending
	a.replyStarts = map[string]*codexTrackedReply{pending.reply.receipt.InputID: pending.reply}

	// turn/start succeeds without a recognized turnID before turn/started arrives.
	a.PublishTurnActive()
	a.handleCodexTurnStartResult(pending, json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil)

	a.Mu.Lock()
	stillPending := a.pendingStart
	a.Mu.Unlock()
	assert.Same(t, pending, stillPending, "confirmed turn/start without reply turnID must keep pendingStart active until turn/started")
	active, published := sink.LastTurnActive()
	require.True(t, published)
	assert.True(t, active, "confirmed turn/start must not publish idle before turn/started")

	a.handleMainTurnStarted("turn-1")
	a.Mu.Lock()
	assert.Nil(t, a.pendingStart)
	assert.Equal(t, []map[string]interface{}{{"type": "text", "text": "hello"}}, a.mainTurn.input)
	a.Mu.Unlock()
}

func TestCodexLateBindDoesNotRestoreTurnInputAfterAgentMessageStarted(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	pending := newCodexTurnStart("main-thread", []map[string]interface{}{{"type": "text", "text": "hello"}}, turnSettings{})
	pending.reply = newCodexTrackedReply(agent.NativeInputReceipt{SessionID: "main-thread", InputID: "sent-input"})
	a.pendingStart = pending
	a.replyStarts = map[string]*codexTrackedReply{pending.reply.receipt.InputID: pending.reply}

	a.handleMainTurnStarted("turn-1")
	a.markCodexTurnAgentMessage()
	a.handleCodexTurnStartResult(pending, json.RawMessage(`{"turn":{"id":"turn-1","status":"inProgress","items":[]}}`), nil)

	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Nil(t, a.mainTurn.input, "late handleCodexTurnStartResult must not restore input after agentMessage already started")
}

func TestCodexTurnCompletedWithoutTurnIDStillSettlesUnechoedSteers(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	a.mainTurn = codexMainTurn{id: "turn-1", ended: make(chan struct{})}
	a.steerLedger.Add(agent.SteerResolution{
		InputID:   "steer-1",
		AttemptID: "attempt-1",
		Receipt:   agent.NativeInputReceipt{SessionID: "main-thread", TurnID: "turn-1", InputID: "client-1"},
	})

	a.handleTurnCompleted(json.RawMessage(`{"turn":{"status":"completed"}}`))
	assert.Equal(t, []agent.SteerResolution{{
		InputID:   "steer-1",
		AttemptID: "attempt-1",
		Outcome:   agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions())
}

func TestCodexTrackedTurnStartEmptyResultAfterProcessExitRetiresAndClearsPendingStart(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	pending := newCodexTurnStart("main-thread", []map[string]interface{}{{"type": "text", "text": "hello"}}, turnSettings{})
	pending.reply = newCodexTrackedReply(agent.NativeInputReceipt{SessionID: "main-thread", InputID: "sent-input"})
	a.pendingStart = pending
	a.replyStarts = map[string]*codexTrackedReply{pending.reply.receipt.InputID: pending.reply}

	a.SimulateExitForTest()
	a.handleCodexTurnStartResult(pending, json.RawMessage(`{}`), nil)

	a.Mu.Lock()
	assert.Nil(t, a.pendingStart, "empty turn/start result on exited process must clear pendingStart")
	assert.Empty(t, a.replyStarts)
	a.Mu.Unlock()
	select {
	case <-pending.reply.done:
		require.Error(t, pending.reply.err, "empty turn/start result after process exit must report the exit error")
	default:
		t.Fatal("tracked reply did not settle after process exit")
	}
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		pending.reply.receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
}

func TestCodexTrackedTurnStartWithoutTurnIDAttachesImmediatelyWhenTurnAlreadyActive(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	pending := newCodexTurnStart("main-thread", []map[string]interface{}{{"type": "text", "text": "hello"}}, turnSettings{})
	pending.reply = newCodexTrackedReply(agent.NativeInputReceipt{SessionID: "main-thread", InputID: "sent-input"})
	a.pendingStart = pending
	a.replyStarts = map[string]*codexTrackedReply{pending.reply.receipt.InputID: pending.reply}

	a.handleMainTurnStarted("active-turn")
	require.Same(t, pending, a.pendingStart, "unrelated turn/started does not attach before turn/start answers")

	a.handleCodexTurnStartResult(pending, json.RawMessage(`{"turn":{"id":"active-turn"}}`), nil)

	a.Mu.Lock()
	assert.Nil(t, a.pendingStart)
	assert.Equal(t, []map[string]interface{}{{"type": "text", "text": "hello"}}, a.mainTurn.input)
	a.Mu.Unlock()
	select {
	case <-pending.reply.done:
		require.NoError(t, pending.reply.err)
	default:
		t.Fatal("tracked reply did not settle")
	}
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		pending.reply.receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
}
