package amp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func steerReceiptFromLine(t *testing.T, proc *fakeProc, index int, threadID string) agent.NativeInputReceipt {
	t.Helper()
	lines := proc.lines()
	require.Greater(t, len(lines), index)
	key, ok := lines[index]["request_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, key)
	return agent.NativeInputReceipt{SessionID: threadID, InputID: ampNativeInputID(key)}
}

func TestAmpRecoversExactReplyForConsecutiveNativeSteers(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("initial")
	firstResp, err := h.agent.SteerInput(agent.SteerRequest{InputID: "first", AttemptID: "attempt-1", Content: "Identical steer"})
	require.NoError(t, err)
	assert.Empty(t, firstResp)
	secondResp, err := h.agent.SteerInput(agent.SteerRequest{InputID: "second", AttemptID: "attempt-2", Content: "Identical steer"})
	require.NoError(t, err)
	assert.Empty(t, secondResp)
	first := steerReceiptFromLine(t, proc, 1, "T-1")
	second := steerReceiptFromLine(t, proc, 2, "T-1")
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
	original, err := h.agent.SendInput("Initial prompt", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	steerResp, err := h.agent.SteerInput(agent.SteerRequest{InputID: "queued", AttemptID: "attempt", Content: "Identical prompt"})
	require.NoError(t, err)
	assert.Empty(t, steerResp)
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	var consumed atomic.Bool
	var exportCalls atomic.Int32
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		exportCalls.Add(1)
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
	h.agent.waitSteerWork()
	assert.Equal(t, int32(1), exportCalls.Load())
	assert.Empty(t, h.sink.SteerResolutions(), "equal text without the submitted ID proves no steer")
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Original reply."}],"stop_reason":"end_turn"}}`)
	h.agent.waitSteerWork()
	assert.False(t, h.turnActive())
	ctx := testutil.DeadlineContext(t)
	select {
	case turn := <-sink.ready:
		assert.Equal(t, original.InputID, turn.TurnID)
		assert.Equal(t, []agent.NativeReplyBoundary{{InputID: original.InputID, AfterOrdinal: 1}}, turn.Inputs)
	case <-ctx.Done():
		t.Fatal("Amp did not publish the original turn")
	}
	h.agent.replyWorkWG.Wait()
	assert.Equal(t, int32(2), exportCalls.Load(), "publishInputReply must reuse settledHistory without a redundant export")

	consumed.Store(true)
	h.feed(proc, echo)
	h.agent.waitSteerWork()
	assert.Equal(t, int32(3), exportCalls.Load())
	assert.True(t, h.turnActive())
	assert.Equal(t, []agent.SteerResolution{{InputID: "queued", AttemptID: "attempt", Outcome: agent.SteerOutcomeEchoed, Receipt: steer}}, h.sink.SteerResolutions())
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Steered reply."}],"stop_reason":"end_turn"}}`)
	h.agent.waitSteerWork()
	select {
	case turn := <-sink.ready:
		assert.Equal(t, steer.InputID, turn.TurnID)
		assert.Equal(t, []agent.NativeReplyBoundary{{InputID: steer.InputID, AfterOrdinal: 1}}, turn.Inputs)
		assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: replyTextMessageKey(steer, 0, 0)}}, turn.Items)
	case <-ctx.Done():
		t.Fatal("Amp did not publish the steer's later turn")
	}
	h.agent.replyWorkWG.Wait()
	assert.Equal(t, int32(4), exportCalls.Load())
}

func TestAmpReportsUncertainSteerByAttemptWhenRecoveryExpires(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("initial")
	_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "queued", AttemptID: "attempt-1", Content: "Pending steer"})
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
	// Clear the prior turn's reply so the steer starts its own reply when echoed.
	h.agent.mu.Lock()
	h.agent.turn.reply = nil
	h.agent.mu.Unlock()
	steerResp, err := h.agent.SteerInput(agent.SteerRequest{InputID: "queued", AttemptID: "attempt", Content: "Adjust this"})
	require.NoError(t, err)
	assert.Empty(t, steerResp)
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
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
	h.agent.waitSteerWork()
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
	// Clear the prior turn's reply so the steer starts its own reply when echoed.
	h.agent.mu.Lock()
	h.agent.turn.reply = nil
	h.agent.mu.Unlock()
	steerResp, err := h.agent.SteerInput(agent.SteerRequest{InputID: "queued", AttemptID: "attempt", Content: "Stop before the answer"})
	require.NoError(t, err)
	assert.Empty(t, steerResp)
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}]}`, threadID, steer.InputID)), nil
	}
	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"Stop before the answer"}]}}`)
	h.agent.waitSteerWork()
	require.Len(t, h.sink.SteerResolutions(), 1)
	h.agent.Stop()
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		steer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, h.sink.NativeReplyRetirements())
	assert.Empty(t, h.sink.NativeReplyTurns())
}

func TestAmpSteerInputReturnsEmptyReceiptWhenRowAtEcho(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("initial")
	receipt, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
	require.NoError(t, err)
	assert.Empty(t, receipt, "SteerRowAtEcho provider must return empty receipt from SteerInput")
}

func TestAmpNonCompleteTurnRetiresAndFinishesEchoedTrackedSteer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	lead, err := h.agent.SendInput("initial", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
	require.NoError(t, err)
	steerReqID, _ := proc.lines()[1]["request_id"].(string)
	steerID := ampNativeInputID(steerReqID)

	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[
			{"protocolMessageID":%q,"role":"user"},
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
		]}`, threadID, lead.InputID, steerID)), nil
	}
	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
	h.agent.waitSteerWork()
	require.Len(t, h.sink.SteerResolutions(), 1)

	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	steerReceipt := agent.NativeInputReceipt{SessionID: "T-1", InputID: steerID}
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		lead:         leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		steerReceipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, h.sink.NativeReplyRetirements())
	h.agent.mu.Lock()
	remaining := slices.Clone(h.agent.nativeSteers)
	h.agent.mu.Unlock()
	assert.Empty(t, remaining)
	assert.False(t, h.agent.prepareSteerRecovery(), "interrupted echoed steer must not wedge reconcileStoppedSteers in an export loop")
}

type failingSteerResolutionSink struct {
	agent.ProviderServices
}

func (s failingSteerResolutionSink) OutputQueue() *agent.OutputQueue {
	return nil
}

func (s failingSteerResolutionSink) CaptureSteerResolution(agent.SteerResolution) func() error {
	return func() error { return fmt.Errorf("storage refused") }
}

func TestAmpObserveSteeredInputDoesNotInstallReplyWhenEchoResolutionFails(t *testing.T) {
	t.Parallel()
	t.Run("active turn", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		h.agent.mu.Lock()
		h.agent.turn.reply = nil
		h.agent.mu.Unlock()
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steerReqID, _ := proc.lines()[1]["request_id"].(string)
		steerID := ampNativeInputID(steerReqID)
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}]}`, threadID, steerID)), nil
		}
		h.agent.sink = failingSteerResolutionSink{ProviderServices: agent.DirectProviderServices(h.agent.sink)}
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		h.agent.waitSteerWork()
		h.agent.mu.Lock()
		reply := h.agent.turn.reply
		require.Len(t, h.agent.nativeSteers, 1)
		state := h.agent.nativeSteers[0].state
		h.agent.mu.Unlock()
		assert.Nil(t, reply, "a steer whose echo resolution failed must not install a.turn.reply")
		assert.True(t, h.turnActive())
		assert.Equal(t, ampSteerPending, state)
	})

	t.Run("idle turn", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steerReqID, _ := proc.lines()[1]["request_id"].(string)
		steerID := ampNativeInputID(steerReqID)
		h.agent.mu.Lock()
		h.agent.turn.reply = nil
		h.agent.mu.Unlock()
		h.agent.finishTurn(agent.MessageCompletionComplete, nil)
		require.False(t, h.turnActive())

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}]}`, threadID, steerID)), nil
		}
		h.agent.sink = failingSteerResolutionSink{ProviderServices: agent.DirectProviderServices(h.agent.sink)}
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		h.agent.waitSteerWork()
		h.agent.mu.Lock()
		reply := h.agent.turn.reply
		require.Len(t, h.agent.nativeSteers, 1)
		state := h.agent.nativeSteers[0].state
		h.agent.mu.Unlock()
		assert.Nil(t, reply, "a steer whose echo resolution failed must not install a.turn.reply")
		assert.False(t, h.turnActive(), "failed echo resolution while idle must roll back the started turn")
		actives := h.sink.TurnActives()
		require.NotEmpty(t, actives)
		assert.False(t, actives[len(actives)-1], "TurnActives must end with false after rollback")
		assert.Equal(t, ampSteerPending, state)
	})
}

func TestAmpCleanResultWithEchoedTrackedSteerDoesNotRunDuplicateRecovery(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	lead, err := h.agent.SendInput("initial", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
	require.NoError(t, err)
	steerReqID, _ := proc.lines()[1]["request_id"].(string)
	steerID := ampNativeInputID(steerReqID)
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[
			{"protocolMessageID":%q,"role":"user"},
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
			{"protocolMessageID":"asst-1","role":"assistant","content":[{"type":"text","text":"Done."}],"state":{"type":"complete","stopReason":"end_turn"}}
		]}`, threadID, lead.InputID, steerID)), nil
	}
	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
	h.agent.waitSteerWork()
	require.Len(t, h.sink.SteerResolutions(), 1)

	h.feed(proc, `{"type":"result","is_error":false}`)
	h.agent.mu.Lock()
	reconciling := h.agent.reconciling
	remaining := slices.Clone(h.agent.nativeSteers)
	h.agent.mu.Unlock()
	assert.False(t, reconciling)
	assert.Empty(t, remaining)
	assert.False(t, h.agent.prepareSteerRecovery(), "steer already owned by publishInputReply must not also enter recoverTrackedSteers")
}

func TestAmpForgetUnconsumedSteerPreservesUncertainSteer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("initial")
	_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
	require.NoError(t, err)
	h.agent.mu.Lock()
	sub := h.agent.nativeSteers[0]
	sub.state = ampSteerUncertain
	h.agent.mu.Unlock()
	assert.False(t, h.agent.forgetUnconsumedSteer(sub), "an uncertain steer must not be deleted by forgetUnconsumedSteer")
	assert.True(t, h.agent.hasUncertainSteer("T-1"))
}

func TestAmpEndTurnSkipsSynchronousExportAfterSteerEchoed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	sink := &replyRecordingSink{ProviderServices: h.agent.sink, ready: make(chan agent.NativeReplyTurn, 1)}
	h.agent.sink = sink
	lead, err := h.agent.SendInput("initial", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
	require.NoError(t, err)
	steer := steerReceiptFromLine(t, proc, 1, "T-1")

	var checkingEndTurn atomic.Bool
	var exportHeldOutputMu atomic.Bool
	var postEchoExports atomic.Int32
	exportStarted := make(chan struct{})
	releaseExport := make(chan struct{})
	var startOnce sync.Once
	h.agent.exportFn = func(ctx context.Context, threadID string) ([]byte, error) {
		if checkingEndTurn.Load() {
			postEchoExports.Add(1)
			if h.agent.outputMu.TryLock() {
				h.agent.outputMu.Unlock()
			} else {
				exportHeldOutputMu.Store(true)
			}
			startOnce.Do(func() { close(exportStarted) })
			select {
			case <-releaseExport:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
				{"protocolMessageID":"asst-1","role":"assistant","content":[{"type":"text","text":"Done."}],"state":{"type":"complete","stopReason":"end_turn"}}
			]}`, threadID, lead.InputID, steer.InputID)), nil
		}
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[
			{"protocolMessageID":%q,"role":"user"},
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
		]}`, threadID, lead.InputID, steer.InputID)), nil
	}
	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
	h.agent.waitSteerWork()
	require.Len(t, h.sink.SteerResolutions(), 1)

	checkingEndTurn.Store(true)
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`)
	assert.False(t, h.turnActive(), "end_turn with no pending steer must finish the turn synchronously while publishInputReply export is blocked")
	ctx := testutil.DeadlineContext(t)
	select {
	case <-exportStarted:
	case <-ctx.Done():
		t.Fatal("publishInputReply did not start its background export")
	}
	assert.False(t, h.turnActive())
	close(releaseExport)
	select {
	case turn := <-sink.ready:
		assert.Equal(t, lead.InputID, turn.TurnID)
	case <-ctx.Done():
		t.Fatal("Amp did not publish the completed reply")
	}
	h.agent.replyWorkWG.Wait()
	assert.Equal(t, int32(1), postEchoExports.Load(), "only publishInputReply should run an export after steer echo")
	assert.False(t, exportHeldOutputMu.Load(), "end_turn with no pending steer must not run export while holding outputMu")
}

func TestAmpObserveSteeredInputRunsOffOutputMuAndCancelsOnInterrupt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	_, err := h.agent.SendInput("initial", nil)
	require.NoError(t, err)
	proc := h.nextProc()
	_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
	require.NoError(t, err)

	exportStarted := make(chan struct{})
	exportCanceled := make(chan struct{})
	var exportHeldOutputMu atomic.Bool
	h.agent.exportFn = func(ctx context.Context, _ string) ([]byte, error) {
		if h.agent.outputMu.TryLock() {
			h.agent.outputMu.Unlock()
		} else {
			exportHeldOutputMu.Store(true)
		}
		close(exportStarted)
		<-ctx.Done()
		close(exportCanceled)
		return nil, ctx.Err()
	}

	// handleUserLine must return immediately while exportFn is blocked.
	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
	ctx := testutil.DeadlineContext(t)
	select {
	case <-exportStarted:
	case <-ctx.Done():
		t.Fatal("async steer export did not start")
	}
	assert.False(t, exportHeldOutputMu.Load(), "observeSteeredInput must never hold outputMu during export")

	// Subsequent stdout lines (including tool_use) must still process immediately while export is blocked.
	h.feed(proc, assistantLine(`[`+toolUseBlock("TU-live", "shell_command", `{"command":"pwd"}`)+`]`, "tool_use"))
	toolID, _ := h.agent.matchToolCall(ctx, "shell_command", []byte(`{"command":"pwd"}`))
	assert.Equal(t, "TU-live", toolID)

	// Interrupt must cancel the blocked inline steer export promptly.
	require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
	select {
	case <-exportCanceled:
	case <-ctx.Done():
		t.Fatal("Interrupt did not cancel in-flight inline steer export")
	}
	h.agent.waitSteerWork()
}

func TestAmpReadInputExportCoalescesSameGenerationExports(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	var calls atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	h.agent.exportFn = func(ctx context.Context, threadID string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []byte(fmt.Sprintf(`{"id":%q,"messages":[]}`, threadID)), nil
	}

	ctx := testutil.DeadlineContext(t)
	firstCtx, cancelFirst := context.WithCancel(ctx)
	firstErrCh := make(chan error, 1)
	go func() {
		_, err := h.agent.readInputExport(firstCtx, "T-1")
		firstErrCh <- err
	}()
	select {
	case <-firstStarted:
	case <-ctx.Done():
		t.Fatal("first export did not start")
	}

	secondErrCh := make(chan error, 1)
	go func() {
		_, err := h.agent.readInputExport(ctx, "T-1")
		secondErrCh <- err
	}()
	testutil.RequireEventually(t, func() bool {
		h.agent.exportMu.Lock()
		slot := h.agent.activeExports["T-1"]
		h.agent.exportMu.Unlock()
		if slot == nil {
			return false
		}
		slot.mu.Lock()
		waiters := slot.waiters
		slot.mu.Unlock()
		return waiters == 2
	}, "second same-generation caller must join the in-flight export")

	// Canceling the first caller must not cancel the shared export for the second caller.
	cancelFirst()
	require.ErrorIs(t, <-firstErrCh, context.Canceled)
	assert.Equal(t, int32(1), calls.Load(), "same-generation callers must share one export subprocess")

	// A newer generation must start independently without waiting for the older blocked export.
	h.agent.bumpExportGen()
	exported, err := h.agent.readInputExport(ctx, "T-1")
	require.NoError(t, err)
	assert.Equal(t, "T-1", exported.ID)
	assert.Equal(t, int32(2), calls.Load())

	close(releaseFirst)
	require.NoError(t, <-secondErrCh)
}

func TestAmpVerifySteeredTurnEndAsyncPaths(t *testing.T) {
	t.Parallel()

	t.Run("steer boundary at or after frameCount keeps turn active", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"Step 1."}],"state":{"type":"complete","stopReason":"tool_use"}},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
			]}`, threadID, lead.InputID, steer.InputID)), nil
		}
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Step 1."}],"stop_reason":"end_turn"}}`)
		h.agent.waitSteerWork()
		assert.True(t, h.turnActive(), "a steer boundary at afterFrame >= frameCount must keep the turn active")
		assert.Empty(t, h.turnEnds())
	})

	t.Run("assistant count mismatch keeps turn active", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"Step 1."}],"state":{"type":"complete","stopReason":"tool_use"}},
				{"protocolMessageID":"asst-1","role":"assistant","content":[{"type":"text","text":"Step 2."}],"state":{"type":"in-progress"}}
			]}`, threadID, lead.InputID)), nil
		}
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Step 1."}],"stop_reason":"end_turn"}}`)
		h.agent.waitSteerWork()
		assert.True(t, h.turnActive(), "additional assistant frames in history must keep the turn active")
		assert.Empty(t, h.turnEnds())
	})

	t.Run("newer assistant frame skips finishTurn when stillCurrent is false", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)

		exportStarted := make(chan struct{})
		releaseExport := make(chan struct{})
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			close(exportStarted)
			<-releaseExport
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"Step 1."}],"state":{"type":"complete","stopReason":"end_turn"}}
			]}`, threadID, lead.InputID)), nil
		}

		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Step 1."}],"stop_reason":"end_turn"}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-exportStarted:
		case <-ctx.Done():
			t.Fatal("verifySteeredTurnEndAsync export did not start")
		}

		// A newer assistant frame arrives while verification export is in flight.
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Step 2."}],"stop_reason":"tool_use"}}`)
		close(releaseExport)
		h.agent.waitSteerWork()
		assert.True(t, h.turnActive(), "newer assistant frame must make stillCurrent false and skip finishTurn")
		assert.Empty(t, h.turnEnds())
	})

	t.Run("export failure completes turn cleanly", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		_, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)

		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			return nil, errors.New("export failed")
		}
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`)
		h.agent.waitSteerWork()
		assert.False(t, h.turnActive())
		ends := h.turnEnds()
		require.Len(t, ends, 1)
		assert.Equal(t, agent.MessageCompletionComplete, ends[0].Completion)
	})

	t.Run("history conflict completes turn cleanly", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":"M-other","role":"user"}
			]}`, threadID, lead.InputID)), nil
		}
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`)
		h.agent.waitSteerWork()
		assert.False(t, h.turnActive())
		ends := h.turnEnds()
		require.Len(t, ends, 1)
		assert.Equal(t, agent.MessageCompletionComplete, ends[0].Completion)
	})

	for _, trigger := range []string{"result", "interrupt"} {
		t.Run(trigger+" cancels in-flight verifySteeredTurnEndAsync export", func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withResume("T-1"))
			_, err := h.agent.SendInput("initial", nil)
			require.NoError(t, err)
			proc := h.nextProc()
			_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
			require.NoError(t, err)

			exportStarted := make(chan struct{})
			exportCanceled := make(chan struct{})
			var exportCalls atomic.Int32
			h.agent.exportFn = func(ctx context.Context, _ string) ([]byte, error) {
				if exportCalls.Add(1) > 1 {
					return nil, context.Canceled
				}
				close(exportStarted)
				<-ctx.Done()
				close(exportCanceled)
				return nil, ctx.Err()
			}
			h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`)
			ctx := testutil.DeadlineContext(t)
			select {
			case <-exportStarted:
			case <-ctx.Done():
				t.Fatal("verifySteeredTurnEndAsync export did not start")
			}

			if trigger == "result" {
				h.feed(proc, `{"type":"result","subtype":"success","is_error":false,"session_id":"T-1"}`)
			} else {
				require.NoError(t, h.agent.Interrupt(agent.StopContext{}))
			}
			select {
			case <-exportCanceled:
			case <-ctx.Done():
				t.Fatalf("%s did not cancel in-flight verifySteeredTurnEndAsync export", trigger)
			}
			h.agent.waitSteerWork()
		})
	}
}

func TestAmpSteerWorkFIFOSerialization(t *testing.T) {
	t.Parallel()

	t.Run("observeSteeredInputAsync completes before verifySteeredTurnEndAsync finishes the turn", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		sink := &replyRecordingSink{ProviderServices: h.agent.sink, ready: make(chan agent.NativeReplyTurn, 1)}
		h.agent.sink = sink
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		var calls atomic.Int32
		echoStarted := make(chan struct{})
		releaseEcho := make(chan struct{})
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			if calls.Add(1) == 1 {
				close(echoStarted)
				<-releaseEcho
				return []byte(fmt.Sprintf(`{"id":%q,"messages":[
					{"protocolMessageID":%q,"role":"user"},
					{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
				]}`, threadID, lead.InputID, steer.InputID)), nil
			}
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
				{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"Done."}],"state":{"type":"complete","stopReason":"end_turn"}}
			]}`, threadID, lead.InputID, steer.InputID)), nil
		}

		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-echoStarted:
		case <-ctx.Done():
			t.Fatal("observeSteeredInputAsync export did not start")
		}
		assert.True(t, h.agent.hasPendingSteerWork())

		// end_turn arrives while echo export is still running; it must queue behind the echo.
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`)
		assert.True(t, h.turnActive(), "end_turn must wait behind queued steer echo work")
		assert.Empty(t, h.turnEnds())

		close(releaseEcho)
		h.agent.waitSteerWork()
		assert.False(t, h.turnActive())
		assert.Equal(t, []agent.SteerResolution{{InputID: "q-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeEchoed, Receipt: steer}}, h.sink.SteerResolutions())
		select {
		case turn := <-sink.ready:
			assert.Equal(t, []agent.NativeReplyBoundary{
				{InputID: lead.InputID, AfterOrdinal: 1},
				{InputID: steer.InputID, AfterOrdinal: 1},
			}, turn.Inputs, "echo must attach steer to reply before turn finishes")
		case <-ctx.Done():
			t.Fatal("Amp did not publish the reply turn")
		}
	})

	t.Run("hasPendingSteerWork defers end_turn even without pending tracked steer", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")

		workStarted := make(chan struct{})
		releaseWork := make(chan struct{})
		require.True(t, h.agent.enqueueSteerWork(func() {
			close(workStarted)
			<-releaseWork
		}))
		ctx := testutil.DeadlineContext(t)
		select {
		case <-workStarted:
		case <-ctx.Done():
			t.Fatal("steer worker did not start")
		}
		assert.True(t, h.agent.hasPendingSteerWork())

		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}}`)
		assert.True(t, h.turnActive(), "end_turn must defer while hasPendingSteerWork is true")
		assert.Empty(t, h.turnEnds())

		close(releaseWork)
		h.agent.waitSteerWork()
		assert.False(t, h.turnActive())
		assert.Len(t, h.turnEnds(), 1)
	})
}

type orderedSteerRecordSink struct {
	agent.ProviderServices
	mu     sync.Mutex
	events []string
}

func (s *orderedSteerRecordSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if err := s.ProviderServices.PersistMessage(source, content, span); err != nil {
		return err
	}
	text, _ := agent.ReplyText(ampProvider{}, content)
	s.mu.Lock()
	s.events = append(s.events, "assistant:"+text)
	s.mu.Unlock()
	return nil
}

func (s *orderedSteerRecordSink) CaptureSteerResolution(resolution agent.SteerResolution) func() error {
	write := s.ProviderServices.CaptureSteerResolution(resolution)
	return func() error {
		if err := write(); err != nil {
			return err
		}
		s.mu.Lock()
		s.events = append(s.events, "steer:"+resolution.InputID)
		s.mu.Unlock()
		return nil
	}
}

func (s *orderedSteerRecordSink) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

type failingReserveSink struct {
	agent.ProviderServices
	queue agent.OutputQueue
	err   error
}

func (s *failingReserveSink) OutputQueue() *agent.OutputQueue { return &s.queue }

func (s *failingReserveSink) ReserveTranscriptOutput(*agent.OutputQueue, ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	return nil, s.err
}

func TestAmpObserveSteeredInputTranscriptReservationAndOrdering(t *testing.T) {
	t.Parallel()

	t.Run("steer resolution persists ahead of subsequent assistant row via OutputQueue", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		recorder := &orderedSteerRecordSink{ProviderServices: h.agent.sink}
		h.agent.sink = agent.NewOrderedProviderServices(h.sink.OutputQueue(), recorder)

		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		exportStarted := make(chan struct{})
		releaseExport := make(chan struct{})
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			close(exportStarted)
			<-releaseExport
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
			]}`, threadID, lead.InputID, steer.InputID)), nil
		}

		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-exportStarted:
		case <-ctx.Done():
			t.Fatal("steer echo export did not start")
		}

		// Subsequent assistant row must queue behind the reserved steer ticket.
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"After steer."}],"stop_reason":"tool_use"}}`)
		assert.Empty(t, recorder.recorded(), "assistant row must wait behind reserved steer sequence")

		close(releaseExport)
		h.agent.waitSteerWork()
		assert.Equal(t, []string{"steer:q-1", "assistant:After steer."}, recorder.recorded())
	})

	t.Run("early exit still flushes reserved sequence so assistant rows drain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		recorder := &orderedSteerRecordSink{ProviderServices: h.agent.sink}
		h.agent.sink = agent.NewOrderedProviderServices(h.sink.OutputQueue(), recorder)

		_, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)

		exportStarted := make(chan struct{})
		releaseExport := make(chan struct{})
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			close(exportStarted)
			<-releaseExport
			return nil, errors.New("export failed")
		}

		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-exportStarted:
		case <-ctx.Done():
			t.Fatal("steer echo export did not start")
		}

		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Unblocked row."}],"stop_reason":"tool_use"}}`)
		assert.Empty(t, recorder.recorded())

		close(releaseExport)
		h.agent.waitSteerWork()
		assert.Empty(t, h.sink.SteerResolutions())
		assert.Equal(t, []string{"assistant:Unblocked row."}, recorder.recorded())
	})

	t.Run("ReserveTranscriptSequence error aborts cleanly without export", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		h.agent.sink = &failingReserveSink{
			ProviderServices: h.agent.sink,
			err:              errors.New("queue closed"),
		}
		_, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)

		var exportCalls atomic.Int32
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			exportCalls.Add(1)
			return nil, errors.New("unexpected export")
		}
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		h.agent.waitSteerWork()
		assert.Zero(t, exportCalls.Load())
		assert.Empty(t, h.sink.SteerResolutions())
		h.agent.mu.Lock()
		require.Len(t, h.agent.nativeSteers, 1)
		assert.Equal(t, ampSteerPending, h.agent.nativeSteers[0].state)
		h.agent.mu.Unlock()
	})
}

type blockingFailingSteerSink struct {
	agent.ProviderServices
	entered chan struct{}
	release chan struct{}
}

func (s *blockingFailingSteerSink) OutputQueue() *agent.OutputQueue { return nil }

func (s *blockingFailingSteerSink) CaptureSteerResolution(agent.SteerResolution) func() error {
	return func() error {
		close(s.entered)
		<-s.release
		return errors.New("storage refused")
	}
}

func TestAmpUntrackedFramesRaceOnPostTurnSteerEcho(t *testing.T) {
	t.Parallel()

	t.Run("assistant frame arriving during post-turn steer export marks reply unproved and retires UNPROVED", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "post-turn steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")
		h.agent.mu.Lock()
		h.agent.turn.reply = nil
		h.agent.mu.Unlock()
		h.agent.finishTurn(agent.MessageCompletionComplete, nil)
		require.False(t, h.turnActive())

		exportStarted := make(chan struct{})
		releaseExport := make(chan struct{})
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			close(exportStarted)
			<-releaseExport
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
				{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"Early frame."}],"state":{"type":"complete","stopReason":"tool_use"}},
				{"protocolMessageID":"asst-1","role":"assistant","content":[{"type":"text","text":"Final frame."}],"state":{"type":"complete","stopReason":"end_turn"}}
			]}`, threadID, steer.InputID)), nil
		}

		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"post-turn steer"}]}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-exportStarted:
		case <-ctx.Done():
			t.Fatal("post-turn steer export did not start")
		}

		// An assistant frame arrives before the export finishes and installs turn.reply.
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Early frame."}],"stop_reason":"tool_use"}}`)
		close(releaseExport)
		h.agent.waitSteerWork()

		h.agent.mu.Lock()
		reply := h.agent.turn.reply
		untracked := h.agent.turn.untrackedFrames
		h.agent.mu.Unlock()
		require.NotNil(t, reply)
		assert.Equal(t, 1, untracked)
		assert.True(t, reply.unproved, "untracked assistant frames before reply install must mark reply unproved")

		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Final frame."}],"stop_reason":"end_turn"}}`)
		h.agent.replyWorkWG.Wait()
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, h.sink.NativeReplyRetirements()[steer])
		assert.Empty(t, h.sink.NativeReplyTurns())
	})

	t.Run("runReserved failure with untrackedFrames keeps turn active", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "post-turn steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")
		h.agent.mu.Lock()
		h.agent.turn.reply = nil
		h.agent.mu.Unlock()
		h.agent.finishTurn(agent.MessageCompletionComplete, nil)
		require.False(t, h.turnActive())

		entered := make(chan struct{})
		release := make(chan struct{})
		h.agent.sink = &blockingFailingSteerSink{
			ProviderServices: agent.DirectProviderServices(h.agent.sink),
			entered:          entered,
			release:          release,
		}
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}]}`, threadID, steer.InputID)), nil
		}

		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"post-turn steer"}]}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("CaptureSteerResolution did not start")
		}

		// Live assistant output arrives while runReserved is blocked, incrementing untrackedFrames.
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Live frame."}],"stop_reason":"tool_use"}}`)
		close(release)
		h.agent.waitSteerWork()

		h.agent.mu.Lock()
		active := h.agent.turn.active
		reply := h.agent.turn.reply
		untracked := h.agent.turn.untrackedFrames
		state := h.agent.nativeSteers[0].state
		h.agent.mu.Unlock()
		assert.True(t, active, "turn must stay active when untrackedFrames > 0 even if runReserved fails")
		assert.Nil(t, reply)
		assert.Equal(t, 1, untracked)
		assert.Equal(t, ampSteerPending, state)
	})
}

func TestAmpObserveSteeredInputMultiSteerAndMismatchPaths(t *testing.T) {
	t.Parallel()

	t.Run("two consecutive post-turn echoes reuse a.turn.reply", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		sink := &replyRecordingSink{ProviderServices: h.agent.sink, ready: make(chan agent.NativeReplyTurn, 1)}
		h.agent.sink = sink
		proc := h.send("initial")
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "s1"})
		require.NoError(t, err)
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-2", AttemptID: "att-2", Content: "s2"})
		require.NoError(t, err)
		steer1 := steerReceiptFromLine(t, proc, 1, "T-1")
		steer2 := steerReceiptFromLine(t, proc, 2, "T-1")
		h.agent.mu.Lock()
		h.agent.turn.reply = nil
		h.agent.mu.Unlock()
		h.agent.finishTurn(agent.MessageCompletionComplete, nil)
		require.False(t, h.turnActive())

		var call atomic.Int32
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			switch call.Add(1) {
			case 1:
				close(firstStarted)
				<-releaseFirst
				return []byte(fmt.Sprintf(`{"id":%q,"messages":[{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}]}`, threadID, steer1.InputID)), nil
			case 2:
				return []byte(fmt.Sprintf(`{"id":%q,"messages":[
					{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
					{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
				]}`, threadID, steer1.InputID, steer2.InputID)), nil
			default:
				return []byte(fmt.Sprintf(`{"id":%q,"messages":[
					{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
					{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
					{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"Combined reply."}],"state":{"type":"complete","stopReason":"end_turn"}}
				]}`, threadID, steer1.InputID, steer2.InputID)), nil
			}
		}

		// Both echoes arrive before the first export installs a.turn.reply, so echo 2 captures reply == nil and reuses a.turn.reply.
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"s1"}]}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-firstStarted:
		case <-ctx.Done():
			t.Fatal("first post-turn echo export did not start")
		}
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"s2"}]}}`)
		close(releaseFirst)
		h.agent.waitSteerWork()

		assert.Equal(t, []agent.SteerResolution{
			{InputID: "q-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeEchoed, Receipt: steer1},
			{InputID: "q-2", AttemptID: "att-2", Outcome: agent.SteerOutcomeEchoed, Receipt: steer2},
		}, h.sink.SteerResolutions())

		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Combined reply."}],"stop_reason":"end_turn"}}`)
		select {
		case turn := <-sink.ready:
			assert.Equal(t, steer1.InputID, turn.TurnID)
			assert.Equal(t, []agent.NativeReplyBoundary{
				{InputID: steer1.InputID, AfterOrdinal: 1},
				{InputID: steer2.InputID, AfterOrdinal: 1},
			}, turn.Inputs)
		case <-ctx.Done():
			t.Fatal("combined post-turn reply did not publish")
		}
	})

	t.Run("history conflict rejects steer echo", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":"M-other-user","role":"user"},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
			]}`, threadID, lead.InputID, steer.InputID)), nil
		}
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		h.agent.waitSteerWork()
		assert.Empty(t, h.sink.SteerResolutions())
		h.agent.mu.Lock()
		assert.Equal(t, ampSteerPending, h.agent.nativeSteers[0].state)
		h.agent.mu.Unlock()
	})

	t.Run("matchesHistoryPrefix mismatch rejects steer echo", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Live prefix."}],"stop_reason":"tool_use"}}`)
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":"asst-0","role":"assistant","content":[{"type":"text","text":"Different prefix."}],"state":{"type":"complete","stopReason":"tool_use"}},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
			]}`, threadID, lead.InputID, steer.InputID)), nil
		}
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		h.agent.waitSteerWork()
		assert.Empty(t, h.sink.SteerResolutions())
		h.agent.mu.Lock()
		assert.Equal(t, ampSteerPending, h.agent.nativeSteers[0].state)
		h.agent.mu.Unlock()
	})

	t.Run("queued second echo sees not pending and skips redundant export", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		lead, err := h.agent.SendInput("initial", nil)
		require.NoError(t, err)
		proc := h.nextProc()
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		var calls atomic.Int32
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			if calls.Add(1) == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user"},
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true}}
			]}`, threadID, lead.InputID, steer.InputID)), nil
		}

		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		ctx := testutil.DeadlineContext(t)
		select {
		case <-firstStarted:
		case <-ctx.Done():
			t.Fatal("first export did not start")
		}
		// Second echo arrives while steer is still pending, so it queues behind the first.
		h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"steer"}]}}`)
		close(releaseFirst)
		h.agent.waitSteerWork()
		assert.Equal(t, int32(1), calls.Load(), "second echo must return early when !pending without calling exportFn")
		assert.Len(t, h.sink.SteerResolutions(), 1)
	})
}

func TestAmpRecoverSteeredReplyEdgePaths(t *testing.T) {
	t.Parallel()

	t.Run("history conflict resolves echo, retires UNPROVED, and prunes steer", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		exported, err := parseThreadExport([]byte(fmt.Sprintf(`{"id":"T-1","messages":[
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
			{"protocolMessageID":"M-conflict","role":"user"}
		]}`, steer.InputID)), "T-1")
		require.NoError(t, err)

		require.True(t, h.agent.applyRecoveredSteers(exported))
		assert.Equal(t, []agent.SteerResolution{{InputID: "q-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeEchoed, Receipt: steer}}, h.sink.SteerResolutions())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			steer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, h.sink.NativeReplyRetirements())
		assert.Empty(t, h.sink.NativeReplyTurns())
		h.agent.mu.Lock()
		assert.Empty(t, h.agent.nativeSteers)
		h.agent.mu.Unlock()
	})

	t.Run("liveUnproved skips export import and retires UNPROVED", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		steer := steerReceiptFromLine(t, proc, 1, "T-1")

		h.agent.mu.Lock()
		sub := h.agent.nativeSteers[0]
		sub.nativeID = steer.InputID
		sub.state = ampSteerEchoed
		sub.liveUnproved = true
		h.agent.mu.Unlock()

		exported, err := parseThreadExport([]byte(fmt.Sprintf(`{"id":"T-1","messages":[
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
			{"protocolMessageID":"M-ans","role":"assistant","content":[{"type":"text","text":"Hidden answer."}],"state":{"type":"complete","stopReason":"end_turn"}}
		]}`, steer.InputID)), "T-1")
		require.NoError(t, err)

		require.True(t, h.agent.applyRecoveredSteers(exported))
		assert.Empty(t, h.rows(), "liveUnproved steer must not import export rows out of order")
		assert.Empty(t, h.sink.NativeReplyTurns())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			steer: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, h.sink.NativeReplyRetirements())
		h.agent.mu.Lock()
		assert.Empty(t, h.agent.nativeSteers)
		h.agent.mu.Unlock()
	})

	t.Run("frame gap before second steer's live output retires both steers UNPROVED", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "s1"})
		require.NoError(t, err)
		_, err = h.agent.SteerInput(agent.SteerRequest{InputID: "q-2", AttemptID: "att-2", Content: "s2"})
		require.NoError(t, err)
		steer1 := steerReceiptFromLine(t, proc, 1, "T-1")
		steer2 := steerReceiptFromLine(t, proc, 2, "T-1")

		frame1Digest, err := ampReplyFrameDigest([]ampBlock{{Type: "text", Text: "After s2."}})
		require.NoError(t, err)

		h.agent.mu.Lock()
		h.agent.nativeSteers[0].nativeID = steer1.InputID
		h.agent.nativeSteers[0].state = ampSteerEchoed
		// steer1 missed frame 0 (0 liveFrames), while steer2 captured frame 1 (afterFrame == 1 != 0).
		h.agent.nativeSteers[1].nativeID = steer2.InputID
		h.agent.nativeSteers[1].state = ampSteerEchoed
		h.agent.nativeSteers[1].liveFrames = []ampReplyFrame{{digest: frame1Digest, textKeys: []string{"live-key-1"}}}
		h.agent.mu.Unlock()

		exported, err := parseThreadExport([]byte(fmt.Sprintf(`{"id":"T-1","messages":[
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
			{"protocolMessageID":"M-a0","role":"assistant","content":[{"type":"text","text":"Missed frame."}],"state":{"type":"complete","stopReason":"tool_use"}},
			{"protocolMessageID":%q,"role":"user","meta":{"steer":true}},
			{"protocolMessageID":"M-a1","role":"assistant","content":[{"type":"text","text":"After s2."}],"state":{"type":"complete","stopReason":"end_turn"}}
		]}`, steer1.InputID, steer2.InputID)), "T-1")
		require.NoError(t, err)

		require.True(t, h.agent.applyRecoveredSteers(exported))
		assert.Empty(t, h.rows(), "frame gap must prevent out-of-order import")
		assert.Empty(t, h.sink.NativeReplyTurns())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			steer1: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
			steer2: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, h.sink.NativeReplyRetirements())
		h.agent.mu.Lock()
		assert.Empty(t, h.agent.nativeSteers)
		h.agent.mu.Unlock()
	})
}

func TestAmpHandleProcessExitAndStopWaitForBackgroundWork(t *testing.T) {
	t.Parallel()

	t.Run("handleProcessExit waits for steerWorkWG before completing", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")

		workStarted := make(chan struct{})
		releaseWork := make(chan struct{})
		require.True(t, h.agent.enqueueSteerWork(func() {
			close(workStarted)
			<-releaseWork
		}))
		ctx := testutil.DeadlineContext(t)
		select {
		case <-workStarted:
		case <-ctx.Done():
			t.Fatal("steer work did not start")
		}

		exitHandlerEntered := make(chan struct{})
		h.agent.stderrFn = func(*ampProcess) string {
			close(exitHandlerEntered)
			return ""
		}

		proc.exit()
		select {
		case <-exitHandlerEntered:
		case <-ctx.Done():
			close(releaseWork)
			t.Fatal("handleProcessExit did not start")
		}
		assert.True(t, proc.ending())
		select {
		case <-proc.handled:
			close(releaseWork)
			t.Fatal("handleProcessExit returned while steerWorkWG was still active")
		default:
		}

		close(releaseWork)
		proc.awaitHandled(t)
	})

	t.Run("handleProcessExit suppresses resumeAfterError when steer becomes uncertain", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")
		h.feed(proc, initLine("T-1"))
		_, err := h.agent.SteerInput(agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "steer"})
		require.NoError(t, err)
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[]}`, threadID)), nil
		}

		trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
		defer trap.Close()
		proc.exit()

		ctx := testutil.DeadlineContext(t)
		for range ampExportAttempts - 1 {
			testutil.WaitForTimer(t, ctx, trap)
			h.clock.Advance(ampExportInterval).MustWait(ctx)
		}
		proc.awaitHandled(t)

		assert.Equal(t, []agent.SteerResolution{{InputID: "q-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeUncertain}}, h.sink.SteerResolutions())
		assert.True(t, h.agent.hasUncertainSteer("T-1"))
		// Sync on sendMu in case a rogue resumeAfterError goroutine was started.
		h.agent.sendMu.Lock()
		started := h.startedThreads()
		h.agent.sendMu.Unlock()
		assert.Equal(t, []string{"T-1"}, started, "uncertain steer must suppress resumeAfterError")
	})

	t.Run("Stop waits for steerWorkWG", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")

		entered := make(chan struct{})
		release := make(chan struct{})
		require.True(t, h.agent.enqueueSteerWork(func() {
			close(entered)
			<-release
		}))
		ctx := testutil.DeadlineContext(t)
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("steer work did not start")
		}

		stopped := make(chan struct{})
		go func() {
			h.agent.Stop()
			close(stopped)
		}()
		select {
		case <-stopped:
			close(release)
			t.Fatal("Stop returned while steerWorkWG was still active")
		case <-proc.handled:
		case <-ctx.Done():
			close(release)
			t.Fatal("Stop did not handle process exit")
		}
		select {
		case <-stopped:
			close(release)
			t.Fatal("Stop returned before steerWorkWG drained")
		default:
		}
		close(release)
		select {
		case <-stopped:
		case <-ctx.Done():
			t.Fatal("Stop did not finish after steerWorkWG drained")
		}
	})

	t.Run("Stop waits for exportWG", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("initial")

		entered := make(chan struct{})
		release := make(chan struct{})
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			close(entered)
			<-release
			return nil, context.Canceled
		}

		callerCtx, cancelCaller := context.WithCancel(t.Context())
		errCh := make(chan error, 1)
		go func() {
			_, err := h.agent.readInputExport(callerCtx, "T-1")
			errCh <- err
		}()
		ctx := testutil.DeadlineContext(t)
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("export goroutine did not start")
		}
		// Cancel the caller so only exportWG keeps the background export goroutine alive.
		cancelCaller()
		require.ErrorIs(t, <-errCh, context.Canceled)

		stopped := make(chan struct{})
		go func() {
			h.agent.Stop()
			close(stopped)
		}()
		select {
		case <-stopped:
			close(release)
			t.Fatal("Stop returned while exportWG was still active")
		case <-proc.handled:
		case <-ctx.Done():
			close(release)
			t.Fatal("Stop did not handle process exit")
		}
		select {
		case <-stopped:
			close(release)
			t.Fatal("Stop returned before exportWG drained")
		default:
		}
		close(release)
		select {
		case <-stopped:
		case <-ctx.Done():
			t.Fatal("Stop did not finish after exportWG drained")
		}
	})
}
