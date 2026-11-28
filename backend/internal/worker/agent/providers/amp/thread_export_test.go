package amp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The message shape comes from .tmp/verify-c29/amp/run2-export/export-before.json.
func recoveryHistoryFor(steerID string) string {
	return fmt.Sprintf(`{"id":"T-1","messages":[
{"protocolMessageID":%q,"role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"second"}]},
{"protocolMessageID":"M-answer","role":"assistant","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"thinking","thinking":"The hidden thought."},{"type":"text","text":"The hidden reply."}]}
]}`, steerID)
}

func TestReconcileStoppedSteersPersistsTheHiddenReplyOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	history := recoveryHistoryFor(steer.InputID)
	h.agent.exportFn = func(context.Context, string) ([]byte, error) { return []byte(history), nil }
	h.agent.reconcileStoppedSteers("T-1")
	h.agent.reconcileStoppedSteers("T-1")
	require.Equal(t, []agent.SteerResolution{{
		InputID: "q-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeEchoed, Receipt: steer,
	}}, h.sink.SteerResolutions())
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

func TestAssociateSteersMatchesOnlyOwnedPromptIDs(t *testing.T) {
	t.Parallel()
	exported, err := parseThreadExport([]byte(`{"id":"T-1","messages":[
		{"protocolMessageID":"M-owned","role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"second"}]},
		{"protocolMessageID":"M-tool-result","role":"user","content":[{"type":"tool_result","tool_use_id":"tu-1","content":"ok"}]},
		{"protocolMessageID":"M-already","role":"user","meta":{"steer":true},"content":[{"type":"text","text":"claimed"}]},
		{"protocolMessageID":"M-answer","role":"assistant","state":{"type":"complete","stopReason":"end_turn"},"content":[{"type":"text","text":"done"}]}
	]}`), "T-1")
	require.NoError(t, err)
	owned := &ampSteerSubmission{
		threadID:   "T-1",
		resolution: agent.SteerResolution{Receipt: agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-owned"}},
	}
	absent := &ampSteerSubmission{
		threadID:   "T-1",
		resolution: agent.SteerResolution{Receipt: agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-missing"}},
	}
	assistantID := &ampSteerSubmission{
		threadID:   "T-1",
		resolution: agent.SteerResolution{Receipt: agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-answer"}},
	}
	toolResultID := &ampSteerSubmission{
		threadID:   "T-1",
		resolution: agent.SteerResolution{Receipt: agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-tool-result"}},
	}
	wrongThread := &ampSteerSubmission{
		threadID:   "T-other",
		resolution: agent.SteerResolution{Receipt: agent.NativeInputReceipt{SessionID: "T-other", InputID: "M-owned"}},
	}
	alreadyClaimed := &ampSteerSubmission{
		threadID:   "T-1",
		nativeID:   "M-already",
		resolution: agent.SteerResolution{Receipt: agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-already"}},
	}
	duplicateClaim := &ampSteerSubmission{
		threadID:   "T-1",
		resolution: agent.SteerResolution{Receipt: agent.NativeInputReceipt{SessionID: "T-1", InputID: "M-already"}},
	}
	associateSteers([]*ampSteerSubmission{owned, absent, assistantID, toolResultID, wrongThread, alreadyClaimed, duplicateClaim}, exported)
	assert.Equal(t, "M-owned", owned.nativeID)
	assert.Empty(t, absent.nativeID)
	assert.Empty(t, assistantID.nativeID, "an assistant message ID must not claim a steer")
	assert.Empty(t, toolResultID.nativeID, "a tool_result user message must not claim a steer")
	assert.Empty(t, wrongThread.nativeID, "a steer from another thread must be skipped")
	assert.Equal(t, "M-already", alreadyClaimed.nativeID)
	assert.Empty(t, duplicateClaim.nativeID, "an already claimed nativeID must not be assigned twice")
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
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
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
	assert.Equal(t, []agent.SteerResolution{{InputID: "q-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeUncertain}}, h.sink.SteerResolutions())
	assert.False(t, h.agent.IsStopped())
	assert.False(t, h.turnActive())
}

func TestReconcileWaitsOnTheInjectedClock(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	calls := 0
	history := recoveryHistoryFor(steer.InputID)
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"id":"T-1","messages":[]}`), nil
		}
		return []byte(history), nil
	}
	trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
	defer trap.Close()
	done := make(chan struct{})
	go func() { defer close(done); h.agent.reconcileStoppedSteers("T-1") }()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, ampExportInterval, testutil.WaitForTimer(t, ctx, trap))
	assert.Empty(t, h.sink.SteerResolutions())
	h.clock.Advance(ampExportInterval).MustWait(ctx)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the recovery did not consume the next export")
	}
	assert.Equal(t, 2, calls)
	require.Len(t, h.sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerOutcomeEchoed, h.sink.SteerResolutions()[0].Outcome)
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
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	h.agent.mu.Lock()
	submission := h.agent.nativeSteers[0]
	submission.state = ampSteerUncertain
	h.agent.mu.Unlock()
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	proc.exit()
	proc.awaitHandled(t)
	h.agent.exportFn = func(context.Context, string) ([]byte, error) {
		return []byte(`{"id":"T-1","messages":[{"protocolMessageID":"M-running","role":"assistant","state":{"type":"in-progress"},"content":[]}]}`), nil
	}
	require.ErrorIs(t, agenttest.SendInput(h.agent, "next", nil), agent.ErrAgentBusy)
	assert.Len(t, h.startedThreads(), 1)
}

func TestSteerRequiresTheNativeThreadIdentity(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	proc := h.send("first")
	require.ErrorIs(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}), agent.ErrSteeringUnsupported)
	assert.Len(t, proc.stdin.lines(), 1)
	h.feed(proc, initLine("T-1"))
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	assert.Len(t, proc.stdin.lines(), 2)
}

func TestReconcileStoppedSteersKeepsALivePrefixWithoutDuplicatingIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	h.agent.mu.Lock()
	h.agent.turn.reply = nil
	h.agent.mu.Unlock()
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	exported, err := parseThreadExport([]byte(recoveryHistoryFor(steer.InputID)), "T-1")
	require.NoError(t, err)
	prefix := exported.Messages[1]
	prefix.ID = "M-prefix"
	prefix.State.StopReason = "tool_use"
	prefix.Content = []json.RawMessage{json.RawMessage(`{"type":"text","text":"Already visible."}`)}
	exported.Messages = append(exported.Messages[:1], prefix, exported.Messages[1])
	echoExport, err := json.Marshal(ampThreadExport{ID: "T-1", Messages: exported.Messages[:1]})
	require.NoError(t, err)
	h.agent.exportFn = func(context.Context, string) ([]byte, error) { return echoExport, nil }

	h.feed(proc, `{"type":"user","session_id":"T-1","message":{"content":[{"type":"text","text":"second"}]}}`)
	h.agent.waitSteerWork()
	h.feed(proc, `{"type":"assistant","session_id":"T-1","message":{"content":[{"type":"text","text":"Already visible."}],"stop_reason":"tool_use"}}`)
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)

	require.True(t, h.agent.applyRecoveredSteers(exported))
	require.True(t, h.agent.applyRecoveredSteers(exported))
	visible := 0
	for _, row := range h.sink.Messages() {
		if strings.Contains(string(row.Content), "Already visible.") && !row.TurnEnd {
			visible++
		}
	}
	assert.Equal(t, 1, visible)
	assert.Len(t, h.sink.SteerResolutions(), 1)
	assert.Len(t, h.agent.importedMessages, 1)
}

func TestReconcileStoppedSteersWaitsForAFinalNativeReply(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	history := recoveryHistoryFor(steer.InputID)
	exported, err := parseThreadExport([]byte(history), "T-1")
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
		return []byte(history), nil
	}
	trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
	defer trap.Close()
	proc.interruptSignalled.Store(true)
	proc.exit()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, ampExportInterval, testutil.WaitForTimer(t, ctx, trap))
	assert.True(t, h.turnActive())
	require.ErrorIs(t, agenttest.SendInput(h.agent, "next", nil), agent.ErrAgentBusy)
	assert.Len(t, h.startedThreads(), 1)
	assert.Empty(t, h.agent.importedMessages)
	h.clock.Advance(ampExportInterval).MustWait(ctx)
	proc.awaitHandled(t)
	assert.False(t, h.turnActive())
	require.NoError(t, agenttest.SendInput(h.agent, "next", nil))
	assert.Len(t, h.startedThreads(), 2)
}

func TestReconcileStoppedSteersRetainsRowsAndTheirSessionAfterAWriteFailure(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.agent.sink = agent.NewModelProgressResetSink(h.agent.sink)
	proc := h.send("first")
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)
	h.sink.PersistErr = assert.AnError
	history := recoveryHistoryFor(steer.InputID)
	h.agent.exportFn = func(context.Context, string) ([]byte, error) { return []byte(history), nil }
	h.agent.reconcileStoppedSteers("T-1")
	assert.Empty(t, h.sink.SteerResolutions())
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
	assert.Len(t, h.sink.SteerResolutions(), 1)
}

func TestRecoveredSteersRespectOutputDiscard(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
	steer := steerReceiptFromLine(t, proc, 1, "T-1")
	exported, err := parseThreadExport([]byte(recoveryHistoryFor(steer.InputID)), "T-1")
	require.NoError(t, err)
	h.agent.DiscardOutput()
	assert.False(t, h.agent.applyRecoveredSteers(exported))
	assert.Empty(t, h.sink.Messages())
	assert.Empty(t, h.sink.SteerResolutions())
}

func TestReconcileStoppedSteersMarksAnAbsentReceiptUncertain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	h.send("first")
	h.agent.exportFn = func(context.Context, string) ([]byte, error) { return []byte(recoveryHistoryFor("M-foreign")), nil }
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
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
	assert.Equal(t, []agent.SteerResolution{{InputID: "q-1", AttemptID: "att-1", Outcome: agent.SteerOutcomeUncertain}}, h.sink.SteerResolutions())
	assert.Empty(t, h.agent.importedMessages, "the earlier identical reply does not belong to this steer")
	assert.False(t, h.agent.IsStopped())
}

func TestStopCancelsTheNativeRecoveryRead(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withResume("T-1"))
	proc := h.send("first")
	require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "q-1", AttemptID: "att-1", Content: "second"}))
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
	assert.Empty(t, h.sink.SteerResolutions())
}

func TestAmpReadInputExportEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("canceling all waiters cancels the shared export runCtx", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		started := make(chan struct{})
		runCanceled := make(chan struct{})
		h.agent.exportFn = func(ctx context.Context, _ string) ([]byte, error) {
			close(started)
			<-ctx.Done()
			close(runCanceled)
			return nil, ctx.Err()
		}

		ctx := testutil.DeadlineContext(t)
		ctx1, cancel1 := context.WithCancel(ctx)
		ctx2, cancel2 := context.WithCancel(ctx)
		err1Ch := make(chan error, 1)
		err2Ch := make(chan error, 1)
		go func() { _, err := h.agent.readInputExport(ctx1, "T-1"); err1Ch <- err }()
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("export did not start")
		}
		go func() { _, err := h.agent.readInputExport(ctx2, "T-1"); err2Ch <- err }()
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
		}, "second waiter must join in-flight export")

		cancel1()
		require.ErrorIs(t, <-err1Ch, context.Canceled)
		select {
		case <-runCanceled:
			t.Fatal("runCtx must stay alive while one waiter remains")
		default:
		}

		cancel2()
		require.ErrorIs(t, <-err2Ch, context.Canceled)
		select {
		case <-runCanceled:
		case <-ctx.Done():
			t.Fatal("canceling the final waiter did not cancel runCtx")
		}
		h.agent.exportWG.Wait()
		h.agent.exportMu.Lock()
		_, stillActive := h.agent.activeExports["T-1"]
		h.agent.exportMu.Unlock()
		assert.False(t, stillActive)
	})

	t.Run("pre-canceled context makes zero export calls", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		var calls atomic.Int32
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			calls.Add(1)
			return nil, errors.New("unexpected call")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := h.agent.readInputExport(ctx, "T-1")
		assert.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, calls.Load())
	})

	t.Run("shared export error removes slot so next call retries", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		var calls atomic.Int32
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		wantErr := errors.New("transient export failure")
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			if calls.Add(1) == 1 {
				close(firstStarted)
				<-releaseFirst
				return nil, wantErr
			}
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[]}`, threadID)), nil
		}

		ctx := testutil.DeadlineContext(t)
		err1Ch := make(chan error, 1)
		err2Ch := make(chan error, 1)
		go func() { _, err := h.agent.readInputExport(ctx, "T-1"); err1Ch <- err }()
		select {
		case <-firstStarted:
		case <-ctx.Done():
			t.Fatal("first export did not start")
		}
		go func() { _, err := h.agent.readInputExport(ctx, "T-1"); err2Ch <- err }()
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
		}, "second caller must coalesce onto failing export")

		close(releaseFirst)
		assert.ErrorIs(t, <-err1Ch, wantErr)
		assert.ErrorIs(t, <-err2Ch, wantErr)
		assert.Equal(t, int32(1), calls.Load())

		exported, err := h.agent.readInputExport(ctx, "T-1")
		require.NoError(t, err)
		assert.Equal(t, "T-1", exported.ID)
		assert.Equal(t, int32(2), calls.Load())
	})

	t.Run("distinct thread IDs do not coalesce", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		var calls atomic.Int32
		t1Started := make(chan struct{})
		releaseT1 := make(chan struct{})
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			calls.Add(1)
			if threadID == "T-1" {
				close(t1Started)
				<-releaseT1
			}
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[]}`, threadID)), nil
		}

		ctx := testutil.DeadlineContext(t)
		t1Done := make(chan error, 1)
		go func() {
			exported, err := h.agent.readInputExport(ctx, "T-1")
			if err == nil && exported.ID != "T-1" {
				err = fmt.Errorf("unexpected thread %q", exported.ID)
			}
			t1Done <- err
		}()
		select {
		case <-t1Started:
		case <-ctx.Done():
			t.Fatal("T-1 export did not start")
		}

		exported, err := h.agent.readInputExport(ctx, "T-2")
		require.NoError(t, err)
		assert.Equal(t, "T-2", exported.ID)
		assert.Equal(t, int32(2), calls.Load())

		close(releaseT1)
		require.NoError(t, <-t1Done)
	})
}

func TestAmpCheckUncertainThreadAndClearSettled(t *testing.T) {
	t.Parallel()

	seedUncertain := func(h *harness, threadID string) {
		h.agent.mu.Lock()
		h.agent.nativeSteers = append(h.agent.nativeSteers, &ampSteerSubmission{
			threadID: threadID,
			state:    ampSteerUncertain,
		})
		h.agent.mu.Unlock()
	}

	t.Run("ended assistant clears uncertain steers and subsequent check makes zero exports", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		seedUncertain(h, "T-1")
		seedUncertain(h, "T-other")
		var calls atomic.Int32
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			calls.Add(1)
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":"M-u","role":"user","meta":{"fromExecutorClient":true}},
				{"protocolMessageID":"M-a","role":"assistant","state":{"type":"complete","stopReason":"end_turn"}}
			]}`, threadID)), nil
		}

		require.NoError(t, h.agent.checkUncertainThread("T-1"))
		assert.Equal(t, int32(1), calls.Load())
		assert.False(t, h.agent.hasUncertainSteer("T-1"))
		assert.True(t, h.agent.hasUncertainSteer("T-other"), "clearing T-1 must leave other threads intact")

		require.NoError(t, h.agent.checkUncertainThread("T-1"))
		assert.Equal(t, int32(1), calls.Load(), "cleared thread must make zero exports on subsequent start")
	})

	for _, tc := range []struct {
		name     string
		messages string
	}{
		{name: "empty history clears", messages: `[]`},
		{name: "non-executor user history clears", messages: `[{"protocolMessageID":"M-u","role":"user","meta":{"fromExecutorClient":false}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withResume("T-1"))
			seedUncertain(h, "T-1")
			h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"id":%q,"messages":%s}`, threadID, tc.messages)), nil
			}
			require.NoError(t, h.agent.checkUncertainThread("T-1"))
			assert.False(t, h.agent.hasUncertainSteer("T-1"))
		})
	}

	t.Run("trailing executor user message returns ErrAgentBusy and retains steer", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		seedUncertain(h, "T-1")
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":"M-a","role":"assistant","state":{"type":"complete","stopReason":"end_turn"}},
				{"protocolMessageID":"M-u","role":"user","meta":{"fromExecutorClient":true}}
			]}`, threadID)), nil
		}
		assert.ErrorIs(t, h.agent.checkUncertainThread("T-1"), agent.ErrAgentBusy)
		assert.True(t, h.agent.hasUncertainSteer("T-1"))
	})

	t.Run("export error propagates and retains steer", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		seedUncertain(h, "T-1")
		wantErr := errors.New("network unreachable")
		h.agent.exportFn = func(context.Context, string) ([]byte, error) {
			return nil, wantErr
		}
		assert.ErrorIs(t, h.agent.checkUncertainThread("T-1"), wantErr)
		assert.True(t, h.agent.hasUncertainSteer("T-1"))
	})
}

func TestAmpReconcileStoppedSteersPostBudgetKnownUnfinishedMonitor(t *testing.T) {
	t.Parallel()

	t.Run("marks absent steer uncertain after budget and monitors confirmed unfinished steer until complete", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("first")
		require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "confirmed", AttemptID: "att-1", Content: "s1"}))
		require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "absent", AttemptID: "att-2", Content: "s2"}))
		confirmed := steerReceiptFromLine(t, proc, 1, "T-1")
		h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)

		var completed atomic.Bool
		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			state := `{"type":"in-progress"}`
			if completed.Load() {
				state = `{"type":"complete","stopReason":"end_turn"}`
			}
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"s1"}]},
				{"protocolMessageID":"M-answer","role":"assistant","state":%s,"content":[{"type":"text","text":"Late answer."}]}
			]}`, threadID, confirmed.InputID, state)), nil
		}

		trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
		defer trap.Close()
		done := make(chan struct{})
		go func() { defer close(done); h.agent.reconcileStoppedSteers("T-1") }()

		ctx := testutil.DeadlineContext(t)
		// Exhaust the initial ampExportAttempts budget while the confirmed steer is still in progress.
		for range ampExportAttempts - 1 {
			assert.Equal(t, ampExportInterval, testutil.WaitForTimer(t, ctx, trap))
			h.clock.Advance(ampExportInterval).MustWait(ctx)
		}

		// Post-budget monitor loop arms its own interval timer while keeping the turn active.
		assert.Equal(t, ampExportInterval, testutil.WaitForTimer(t, ctx, trap))
		assert.True(t, h.turnActive(), "confirmed unfinished reply must keep the turn active in monitor loop")
		assert.Equal(t, []agent.SteerResolution{
			{InputID: "confirmed", AttemptID: "att-1", Outcome: agent.SteerOutcomeEchoed, Receipt: confirmed},
			{InputID: "absent", AttemptID: "att-2", Outcome: agent.SteerOutcomeUncertain},
		}, h.sink.SteerResolutions())

		completed.Store(true)
		h.clock.Advance(ampExportInterval).MustWait(ctx)
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("post-budget monitor did not finish after native reply completed")
		}
		assert.False(t, h.turnActive())
		assert.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "T-1", TurnID: confirmed.InputID,
			Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "amp-export:T-1:M-answer:0"}},
			Inputs:  []agent.NativeReplyBoundary{{InputID: confirmed.InputID, AfterOrdinal: 1}},
		}}, h.sink.NativeReplyTurns())
	})

	t.Run("Stop exits post-budget monitor loop cleanly", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withResume("T-1"))
		proc := h.send("first")
		require.NoError(t, agenttest.SteerInput(h.agent, agent.SteerRequest{InputID: "confirmed", AttemptID: "att-1", Content: "s1"}))
		confirmed := steerReceiptFromLine(t, proc, 1, "T-1")
		h.agent.finishTurn(agent.MessageCompletionInterrupted, nil)

		h.agent.exportFn = func(_ context.Context, threadID string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"id":%q,"messages":[
				{"protocolMessageID":%q,"role":"user","meta":{"steer":true,"fromExecutorClient":true},"content":[{"type":"text","text":"s1"}]},
				{"protocolMessageID":"M-answer","role":"assistant","state":{"type":"in-progress"},"content":[]}
			]}`, threadID, confirmed.InputID)), nil
		}

		trap := h.clock.Trap().NewTimer(AmpExportIntervalTag)
		defer trap.Close()
		done := make(chan struct{})
		go func() { defer close(done); h.agent.reconcileStoppedSteers("T-1") }()

		ctx := testutil.DeadlineContext(t)
		for range ampExportAttempts - 1 {
			testutil.WaitForTimer(t, ctx, trap)
			h.clock.Advance(ampExportInterval).MustWait(ctx)
		}
		// Enter post-budget monitor wait.
		testutil.WaitForTimer(t, ctx, trap)

		var stopWG sync.WaitGroup
		stopWG.Add(1)
		go func() { defer stopWG.Done(); h.agent.Stop() }()
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("Stop did not exit post-budget monitor loop")
		}
		stopWG.Wait()
	})
}

func TestAmpForgetUnconsumedSteerBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      ampSteerState
		nativeID   string
		wantForget bool
	}{
		{name: "pending without nativeID is removed", state: ampSteerPending, nativeID: "", wantForget: true},
		{name: "pending with nativeID is retained", state: ampSteerPending, nativeID: "M-1", wantForget: false},
		{name: "echoed is retained", state: ampSteerEchoed, nativeID: "", wantForget: false},
		{name: "finished is retained", state: ampSteerFinished, nativeID: "", wantForget: false},
		{name: "uncertain is retained", state: ampSteerUncertain, nativeID: "", wantForget: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withResume("T-1"))
			sub := &ampSteerSubmission{threadID: "T-1", state: tc.state, nativeID: tc.nativeID}
			h.agent.mu.Lock()
			h.agent.nativeSteers = []*ampSteerSubmission{sub}
			h.agent.mu.Unlock()

			assert.Equal(t, tc.wantForget, h.agent.forgetUnconsumedSteer(sub))
			h.agent.mu.Lock()
			remaining := len(h.agent.nativeSteers)
			h.agent.mu.Unlock()
			if tc.wantForget {
				assert.Zero(t, remaining)
			} else {
				assert.Equal(t, 1, remaining)
			}
		})
	}
}
