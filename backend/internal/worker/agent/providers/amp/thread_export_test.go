package amp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSteerWithoutAnExportBaselineStaysUnsent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("initial")
	calls := 0
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		calls++
		return nil, assert.AnError
	}
	err := h.agent.SteerInput("do not send without a baseline", nil)
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 1, calls)
	assert.Len(t, proc.stdin.lines(), 1)
	assert.Empty(t, h.agent.steers.Pending())
}

// The message shape comes from .tmp/verify-c29/amp/run2-export/export-before.json.
const recoveryHistory = `{"id":"T-1","messages":[
{"protocolMessageID":"M-user","role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"second"}]},
{"protocolMessageID":"M-answer","role":"assistant","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"thinking","thinking":"The hidden thought."},{"type":"text","text":"The hidden reply."}]}
]}`

func TestReconcileStoppedSteersPersistsTheHiddenReplyOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	h.agent.exportFn = func(context.Context, string) ([]byte, error) { return []byte(recoveryHistory), nil }
	h.agent.reconcileStoppedSteers("T-1")
	h.agent.reconcileStoppedSteers("T-1")
	require.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, h.sink.SteerEchoReports())
	var texts []string
	for _, row := range h.sink.Messages() {
		var value map[string]any
		require.NoError(t, json.Unmarshal(row.Content, &value))
		if value["type"] == contracts.AssembledMessageType {
			texts = append(texts, value["text"].(string))
			assert.Equal(t, "T-1", row.AgentSessionID)
			assert.Equal(t, "complete", value["completion"])
		}
	}
	assert.Equal(t, []string{"The hidden thought.", "The hidden reply."}, texts)
	assert.False(t, h.turnActive())
}

func TestAssociateSteersRefusesAnOlderIdenticalSteer(t *testing.T) {
	t.Parallel()
	exported, err := parseThreadExport([]byte(recoveryHistory), "T-1")
	require.NoError(t, err)
	pending := &ampSteerSubmission{key: "second", threadID: "T-1", baseline: exported.identities()}
	associateSteers([]*ampSteerSubmission{pending}, exported)
	assert.Empty(t, pending.nativeID)
}

func TestAssociateSteersRetainsAnEarlierEchoThatEntersHistoryLate(t *testing.T) {
	t.Parallel()
	exported, err := parseThreadExport([]byte(recoveryHistory), "T-1")
	require.NoError(t, err)
	first := &ampSteerSubmission{key: "second", threadID: "T-1", baseline: map[string]bool{}, echoed: true, finished: true}
	second := &ampSteerSubmission{key: "second", threadID: "T-1", baseline: map[string]bool{}}
	associateSteers([]*ampSteerSubmission{first, second}, exported)
	assert.Empty(t, first.nativeID)
	assert.Empty(t, second.nativeID, "one native receipt cannot distinguish two submissions")
	newer := exported.Messages[0]
	newer.ID = "M-new-user"
	exported.Messages = append(exported.Messages, newer)
	associateSteers([]*ampSteerSubmission{first, second}, exported)
	assert.Equal(t, "M-user", first.nativeID)
	assert.Equal(t, "M-new-user", second.nativeID)
	associateSteers([]*ampSteerSubmission{first, second}, exported)
	assert.NotEqual(t, first.nativeID, second.nativeID)
}

func TestParseThreadExportRejectsUnusableIdentities(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`null`, `{}`, `{"id":"T-2","messages":[]}`, `{"id":"T-1"}`, `{"id":"T-1","messages":[{}]}`, `{"id":"T-1","messages":[{"protocolMessageID":"same"},{"protocolMessageID":"same"}]}`, `invalid`} {
		_, err := parseThreadExport([]byte(raw), "T-1")
		require.Error(t, err, raw)
	}
	exported, err := parseThreadExport([]byte(`{"id":"T-1","messages":[]}`), "T-1")
	require.NoError(t, err)
	assert.Empty(t, exported.Messages)
}

func TestReconcileStoppedSteersMarksFailedReadsUncertain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
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
		t.Fatal("the export did not start")
	}
	h.clock.Advance(ampExportBudget).MustWait(ctx)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the recovery exceeded its total budget")
	}
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, h.sink.SteerEchoReports())
	assert.False(t, h.agent.IsStopped())
	assert.False(t, h.turnActive())
}

func TestReconcileWaitsOnTheInjectedClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	calls := 0
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"id":"T-1","messages":[]}`), nil
		}
		return []byte(recoveryHistory), nil
	}
	trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
	defer trap.Close()
	done := make(chan struct{})
	go func() { defer close(done); h.agent.reconcileStoppedSteers("T-1") }()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, ampExportInterval, testutil.WaitForTimer(t, ctx, trap))
	assert.Empty(t, h.sink.SteerEchoReports())
	h.clock.Advance(ampExportInterval).MustWait(ctx)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the recovery did not consume the next export")
	}
	assert.Equal(t, 2, calls)
	require.Len(t, h.sink.SteerEchoReports(), 1)
	assert.Equal(t, agent.SteerOutcomeEchoed, h.sink.SteerEchoReports()[0].Outcome)
}

func TestNoExportWithoutAPendingSteer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		t.Error("unexpected export")
		return nil, assert.AnError
	}
	h.agent.reconcileStoppedSteers("T-1")
}

func TestProcessForTurnRechecksAnUncertainServerDelivery(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	h.agent.mu.Lock()
	submission := h.agent.nativeSteers[0]
	submission.uncertain = true
	h.agent.mu.Unlock()
	h.agent.steers.Uncertain(submission.ticket)
	h.agent.steers.Settle(h.agent.sink)
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	proc.exit()
	proc.awaitHandled(t)
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		return []byte(`{"id":"T-1","messages":[{"protocolMessageID":"M-running","role":"assistant","state":{"type":"in-progress"},"content":[]}]}`), nil
	}
	require.ErrorIs(t, h.agent.SendInput("next", nil), agent.ErrAgentBusy)
	assert.Len(t, h.startedThreads(), 1)
}

func TestSteerRequiresTheNativeThreadIdentityBeforeItReadsHistory(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	proc := h.send("first")
	exports := 0
	h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
		exports++
		assert.Equal(t, "T-1", threadID)
		return []byte(`{"id":"T-1","messages":[]}`), nil
	}
	require.ErrorIs(t, h.agent.SteerInput("second", nil), agent.ErrSteeringUnsupported)
	assert.Zero(t, exports)
	assert.Len(t, proc.stdin.lines(), 1)
	h.feed(proc, initLine("T-1"))
	require.NoError(t, h.agent.SteerInput("second", nil))
	assert.Equal(t, 1, exports)
	assert.Len(t, proc.stdin.lines(), 2)
}

func TestSteerRechecksTheTurnAfterReadingTheBaseline(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		h.feed(proc, textLine("finished", stopReasonEndTurn))
		return []byte(`{"id":"T-1","messages":[]}`), nil
	}
	require.ErrorIs(t, h.agent.SteerInput("second", nil), agent.ErrNoActiveTurn)
	assert.Len(t, proc.stdin.lines(), 1)
	assert.Empty(t, h.agent.nativeSteers)
}

func TestAssociateSteersRefusesExtraCandidatesAndReversedOwnership(t *testing.T) {
	t.Parallel()
	exported, err := parseThreadExport([]byte(recoveryHistory), "T-1")
	require.NoError(t, err)
	copy := exported.Messages[0]
	copy.ID = "M-extra"
	exported.Messages = append(exported.Messages, copy)
	first := &ampSteerSubmission{key: "second", threadID: "T-1", baseline: map[string]bool{}}
	associateSteers([]*ampSteerSubmission{first}, exported)
	assert.Empty(t, first.nativeID)
	exported.Messages[0].Content = []json.RawMessage{json.RawMessage(`{"type":"text","text":"third"}`)}
	second := &ampSteerSubmission{key: "third", threadID: "T-1", baseline: map[string]bool{}}
	associateSteers([]*ampSteerSubmission{first, second}, exported)
	assert.Empty(t, first.nativeID)
	assert.Empty(t, second.nativeID)
}

func TestReconcileStoppedSteersKeepsALivePrefixWithoutDuplicatingIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	h.feed(proc, userEchoLine("second"))
	h.feed(proc, textLine("Already visible.", "tool_use"))
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	exported, err := parseThreadExport([]byte(recoveryHistory), "T-1")
	require.NoError(t, err)
	prefix := exported.Messages[1]
	prefix.ID = "M-prefix"
	prefix.State.StopReason = "tool_use"
	prefix.Content = []json.RawMessage{json.RawMessage(`{"type":"text","text":"Already visible."}`)}
	exported.Messages = append(exported.Messages[:1], prefix, exported.Messages[1])
	require.True(t, h.agent.applyRecoveredSteers(exported))
	require.True(t, h.agent.applyRecoveredSteers(exported))
	visible := 0
	for _, row := range h.sink.Messages() {
		if strings.Contains(string(row.Content), "Already visible.") && !row.TurnEnd {
			visible++
		}
	}
	assert.Equal(t, 1, visible)
	assert.Len(t, h.sink.SteerEchoReports(), 1)
	assert.Len(t, h.agent.importedMessages, 1)
}

func TestReconcileStoppedSteersWaitsForAFinalNativeReply(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	exported, err := parseThreadExport([]byte(recoveryHistory), "T-1")
	require.NoError(t, err)
	exported.Messages[1].State.Type = "in-progress"
	raw, err := json.Marshal(exported)
	require.NoError(t, err)
	calls := 0
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		calls++
		if calls == 1 {
			return raw, nil
		}
		return []byte(recoveryHistory), nil
	}
	trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
	defer trap.Close()
	proc.interruptSignalled.Store(true)
	proc.exit()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, ampExportInterval, testutil.WaitForTimer(t, ctx, trap))
	assert.True(t, h.turnActive())
	require.ErrorIs(t, h.agent.SendInput("next", nil), agent.ErrAgentBusy)
	assert.Len(t, h.startedThreads(), 1)
	assert.Empty(t, h.agent.importedMessages)
	h.clock.Advance(ampExportInterval).MustWait(ctx)
	proc.awaitHandled(t)
	assert.False(t, h.turnActive())
	require.NoError(t, h.agent.SendInput("next", nil))
	assert.Len(t, h.startedThreads(), 2)
}

func TestReconcileStoppedSteersRetainsRowsAndTheirSessionAfterAWriteFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.agent.sink = agent.NewModelProgressResetSink(h.agent.sink)
	h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	h.sink.PersistErr = assert.AnError
	h.agent.exportFn = func(context.Context, string) ([]byte, error) { return []byte(recoveryHistory), nil }
	h.agent.reconcileStoppedSteers("T-1")
	assert.Empty(t, h.sink.SteerEchoReports())
	h.sink.UpdateSessionID("replacement")
	h.sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(h.agent.sink))
	h.agent.reconcileStoppedSteers("T-1")
	var texts []string
	for _, row := range h.sink.Messages() {
		var value map[string]any
		require.NoError(t, json.Unmarshal(row.Content, &value))
		if value["type"] == contracts.AssembledMessageType {
			assert.Equal(t, "T-1", row.AgentSessionID)
			texts = append(texts, value["text"].(string))
		}
	}
	assert.Equal(t, []string{"The hidden thought.", "The hidden reply."}, texts)
	assert.Len(t, h.sink.SteerEchoReports(), 1)
}

func TestRecoveredSteersRespectOutputDiscard(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	exported, err := parseThreadExport([]byte(recoveryHistory), "T-1")
	require.NoError(t, err)
	h.agent.DiscardOutput()
	assert.False(t, h.agent.applyRecoveredSteers(exported))
	assert.Empty(t, h.sink.Messages())
	assert.Empty(t, h.sink.SteerEchoReports())
}

func TestReconcileStoppedSteersMarksAnAbsentReceiptUncertain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("first")
	h.agent.exportFn = func(context.Context, string) ([]byte, error) { return []byte(recoveryHistory), nil }
	require.NoError(t, h.agent.SteerInput("second", nil))
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
	defer trap.Close()
	done := make(chan struct{})
	go func() { defer close(done); h.agent.reconcileStoppedSteers("T-1") }()
	ctx := testutil.DeadlineContext(t)
	for range ampExportAttempts - 1 {
		assert.Equal(t, ampExportInterval, testutil.WaitForTimer(t, ctx, trap))
		h.clock.Advance(ampExportInterval).MustWait(ctx)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the recovery did not end after its final read")
	}
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeUncertain, Count: 1}}, h.sink.SteerEchoReports())
	assert.Empty(t, h.agent.importedMessages, "the earlier identical reply does not belong to this steer")
	assert.False(t, h.agent.IsStopped())
}

func TestStopCancelsTheNativeRecoveryRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, h.agent.SteerInput("second", nil))
	started := make(chan struct{})
	h.agent.exportFn = func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	proc.interruptSignalled.Store(true)
	proc.exit()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the recovery read did not start")
	}
	done := make(chan struct{})
	go func() { h.agent.Stop(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Stop did not cancel the recovery read")
	}
	assert.Empty(t, h.sink.SteerEchoReports())
}
