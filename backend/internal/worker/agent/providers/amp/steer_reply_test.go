package amp

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestAmpRecoversExactReplyForConsecutiveNativeSteers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("initial")
	first, err := h.agent.SteerInputWithReceipt(agent.SteerRequest{InputID: "first", AttemptID: "attempt-1", Content: "Identical steer"})
	require.NoError(t, err)
	second, err := h.agent.SteerInputWithReceipt(agent.SteerRequest{InputID: "second", AttemptID: "attempt-2", Content: "Identical steer"})
	require.NoError(t, err)
	require.NotEqual(t, first.InputID, second.InputID)
	exported, err := parseThreadExport([]byte(fmt.Sprintf(`{"id":"T-1","messages":[
		{"protocolMessageID":"another-steer","role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"Identical steer"}]},
		{"protocolMessageID":%q,"role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"Identical steer"}]},
		{"protocolMessageID":%q,"role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"Identical steer"}]},
		{"protocolMessageID":"native-answer","role":"assistant","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"text","text":"Reply to both steers."}]}
	]}`, first.InputID, second.InputID)), "T-1")
	require.NoError(t, err)
	require.True(t, h.agent.applyRecoveredSteers(exported))
	assert.Len(t, h.rows(), 1)
	assert.Equal(t, []agent.SteerResolution{
		{InputID: "first", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeEchoed, Receipt: first},
		{InputID: "second", AttemptID: "attempt-2", Outcome: agent.SteerOutcomeEchoed, Receipt: second},
	}, h.sink.SteerResolutions())
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "T-1", TurnID: first.InputID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "amp-export:T-1:native-answer:0"}},
		Inputs: []agent.NativeReplyBoundary{{InputID: first.InputID, AfterOrdinal: 1}, {InputID: second.InputID, AfterOrdinal: 1}},
	}}, h.sink.NativeReplyTurns())
	assert.True(t, h.agent.applyRecoveredSteers(exported))
	assert.Len(t, h.rows(), 1, "recovery must retain one transcript row for the native answer")
	assert.Len(t, h.sink.NativeReplyTurns(), 1)
}

func TestAmpCarriesAnUnreadSteerPastThePriorNativeEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	sink := &replyRecordingSink{ProviderServices: h.agent.sink, ready: make(chan agent.NativeReplyTurn, 2)}
	h.agent.sink = sink
	original, err := h.agent.SendInputWithReceipt("Initial prompt", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	steer, err := h.agent.SteerInputWithReceipt(agent.SteerRequest{InputID: "queued", AttemptID: "attempt", Content: "Identical prompt"})
	require.NoError(t, err)
	var consumed atomic.Bool
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		more := ""
		if consumed.Load() {
			more = fmt.Sprintf(`,
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true},"content":[{"type":"text","text":"Identical prompt"}]},
				{"protocolMessageID":"steer-answer","role":"assistant","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"text","text":"Steered reply."}]}`, steer.InputID)
		}
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[
			{"protocolMessageID":"foreign-user","role":"user","meta":{"steer":true},"content":[{"type":"text","text":"Identical prompt"}]},
			{"protocolMessageID":"foreign-answer","role":"assistant","state":{"type":"complete","stopReason":"end_turn"}},
			{"protocolMessageID":%q,"role":"user"},
			{"protocolMessageID":"root-answer","role":"assistant","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"text","text":"Original reply."}]}%s
		]}`, threadID, original.InputID, more)), nil
	}
	echo := `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"Identical prompt"}]}}`
	h.feed(proc, echo)
	assert.Empty(t, h.sink.SteerResolutions(), "equal text without the submitted ID proves no steer")
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Original reply."}],"stop_reason":"end_turn"}}`)
	assert.False(t, h.turnActive())
	ctx := testutil.DeadlineContext(t)
	select {
	case turn := <-sink.ready:
		assert.Equal(t, original.InputID, turn.TurnID)
		assert.Equal(t, []agent.NativeReplyBoundary{{InputID: original.InputID, AfterOrdinal: 1}}, turn.Inputs)
	case <-ctx.Done():
		t.Fatal("Amp did not publish the original turn")
	}

	consumed.Store(true)
	h.feed(proc, echo)
	assert.True(t, h.turnActive())
	assert.Equal(t, []agent.SteerResolution{{InputID: "queued", AttemptID: "attempt", Outcome: agent.SteerOutcomeEchoed, Receipt: steer}}, h.sink.SteerResolutions())
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Steered reply."}],"stop_reason":"end_turn"}}`)
	select {
	case turn := <-sink.ready:
		assert.Equal(t, steer.InputID, turn.TurnID)
		assert.Equal(t, []agent.NativeReplyBoundary{{InputID: steer.InputID, AfterOrdinal: 1}}, turn.Inputs)
		assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: replyTextMessageKey(steer, 0, 0)}}, turn.Items)
	case <-ctx.Done():
		t.Fatal("Amp did not publish the steer's later turn")
	}
}

func TestAmpReportsUncertainSteerByAttemptWhenRecoveryExpires(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("initial")
	_, err := h.agent.SteerInputWithReceipt(agent.SteerRequest{InputID: "queued", AttemptID: "attempt-1", Content: "Pending steer"})
	require.NoError(t, err)
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	started := make(chan struct{})
	h.agent.exportFn = func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	trap := h.clock.Trap().NewTimer(AmpExportBudgetTag)
	defer trap.Close()
	done := make(chan struct{})
	go func() { defer close(done); h.agent.reconcileStoppedSteers("T-1") }()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, ampExportBudget, testutil.WaitForTimer(t, ctx, trap))
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("Amp did not start the recovery export")
	}
	h.clock.Advance(ampExportBudget).MustWait(ctx)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Amp recovery exceeded its budget")
	}
	assert.Equal(t, []agent.SteerResolution{{InputID: "queued", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeUncertain}}, h.sink.SteerResolutions())
	assert.Empty(t, h.sink.NativeReplyTurns())
	assert.False(t, h.turnActive())
}

func TestAmpRecoversAReplyAfterItsVisibleSteerPrefix(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("initial")
	steer, err := h.agent.SteerInputWithReceipt(agent.SteerRequest{InputID: "queued", AttemptID: "attempt", Content: "Adjust this"})
	require.NoError(t, err)
	var completed atomic.Bool
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		hidden := ""
		if completed.Load() {
			hidden = `,{"protocolMessageID":"hidden","role":"assistant","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"text","text":"Recovered reply."}]}`
		}
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
			{"protocolMessageID":"visible","role":"assistant","state":{"type":"complete","stopReason":"tool_use"},"content":[{"type":"text","text":"Visible prefix."}]}%s
		]}`, threadID, steer.InputID, hidden)), nil
	}
	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"Adjust this"}]}}`)
	require.Len(t, h.sink.SteerResolutions(), 1)
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Visible prefix."}],"stop_reason":"tool_use"}}`)
	require.Len(t, h.rows(), 1)
	completed.Store(true)
	proc.exit()
	proc.awaitHandled(t)

	rows := h.rows()
	require.Len(t, rows, 2)
	for i, expected := range []string{"Visible prefix.", "Recovered reply."} {
		text, ok := agent.ReplyText(ampProvider{}, agent.MessageContent{Original: rows[i].Content})
		require.True(t, ok)
		assert.Equal(t, expected, text)
	}
	replies := h.sink.NativeReplyTurns()
	require.Len(t, replies, 1)
	assert.Equal(t, []agent.NativeReplyItem{
		{Ordinal: 2, MessageKey: rows[0].IdempotencyKey},
		{Ordinal: 3, MessageKey: rows[1].IdempotencyKey},
	}, replies[0].Items)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: steer.InputID, AfterOrdinal: 1}}, replies[0].Inputs)
}

func TestAmpStopRetiresAnEchoedSteerThatHasNoReply(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("initial")
	steer, err := h.agent.SteerInputWithReceipt(agent.SteerRequest{InputID: "queued", AttemptID: "attempt", Content: "Stop before the answer"})
	require.NoError(t, err)
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}]}`, threadID, steer.InputID)), nil
	}
	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"Stop before the answer"}]}}`)
	require.Len(t, h.sink.SteerResolutions(), 1)
	h.agent.Stop()
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		steer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, h.sink.NativeReplyRetirements())
	assert.Empty(t, h.sink.NativeReplyTurns())
}
