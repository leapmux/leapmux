package grok

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const grokTestLateNativeSteer = "late-native-steer"

func newGrokSteerHandoffTest(t *testing.T) (*Agent, *grokSteerRetirementSink, *quartz.Mock, string) {
	t.Helper()
	a := &Agent{}
	clock := testutil.NewQuartzMock(t)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		Ctx: t.Context(), Clock: clock, APITimeout: agent.DefaultAPITimeout,
	})
	a.SetSessionIDForTest(grokTestSession)
	receipts := &grokSteerRetirementSink{ControlSink: &agenttest.ControlSink{}}
	a.SetSinkForTest(agent.NewProviderServices(receipts))
	*a.HooksForTest() = a.configure(agent.Options{})
	old := ownPromptID(t, a)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(queueChanged(t, grokTestSession, old))
	a.steerReplies.active = &grokSteerReply{
		sessionID: grokTestSession, promptID: old, turnID: "worker-prompt",
		inputs: []grokSteerAdmission{{id: grokTestLateNativeSteer, payload: grokInterjectionPayload{text: "the steer"}}},
	}
	return a, receipts, clock, old
}

func TestGrokTransfersOnlyTheEchoedSteerToItsImmediateSuccessor(t *testing.T) {
	t.Parallel()

	const fallback = "interject-fallback-01a2060b-e397-77f1-b1ee-bc0d4e6f2121"
	for _, tc := range []struct {
		name, next, replyEcho, text, kind string
		matches                           bool
	}{
		{"matching successor", fallback, grokTestLateNativeSteer, "the steer", "prompt", true},
		{"unrelated prompt", "subagent-completed-bg-1", grokTestLateNativeSteer, "the steer", "prompt", false},
		{"missing echo", fallback, "", "the steer", "prompt", false},
		{"unrelated echo", fallback, "other-input", "the steer", "prompt", false},
		{"different text", fallback, grokTestLateNativeSteer, "another input", "prompt", false},
		{"different kind", fallback, grokTestLateNativeSteer, "the steer", "background", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, receipts, _, old := newGrokSteerHandoffTest(t)
			a.HandleOutput(turnCompleted(t, old))
			require.NotNil(t, a.steerReplies.handoff)
			assert.Empty(t, receipts.retired.InputID, "the predecessor must leave the outstanding request available")
			if tc.replyEcho != "" {
				a.HandleOutput(frame(t, map[string]any{
					"method": grokInterjectionNotification,
					"params": map[string]any{"sessionId": grokTestSession, "interjectionId": tc.replyEcho},
				}))
			}
			a.HandleOutput(frame(t, map[string]any{
				"method": grokQueueChangedMethod,
				"params": map[string]any{
					"sessionId": grokTestSession, "runningPromptId": tc.next, "runningText": tc.text, "runningKind": tc.kind,
				},
			}))
			a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
			a.steerReplies.jobs.Wait()
			assert.Nil(t, a.steerReplies.handoff)

			if !tc.matches {
				assert.Nil(t, a.steerReplies.active)
				assert.Equal(t, grokTestLateNativeSteer, receipts.retired.InputID)
				assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, receipts.outcome)
				return
			}
			a.HandleOutput(turnCompleted(t, old))
			scope := a.steerReplies.active
			require.NotNil(t, scope, "a late predecessor verdict must not finish its successor")
			assert.Equal(t, fallback, scope.promptID)
			assert.Equal(t, fallback, scope.turnID)
			assert.Equal(t, old, scope.predecessorID)
			require.Len(t, scope.inputs, 1)
			assert.Equal(t, grokTestLateNativeSteer, scope.inputs[0].id)
			assert.True(t, scope.inputs[0].echoed)
			assert.Empty(t, receipts.retired.InputID)
		})
	}
}

func TestGrokRetiresASteerWhenTheSuccessorNeverArrives(t *testing.T) {
	t.Parallel()
	a, receipts, clock, old := newGrokSteerHandoffTest(t)
	timer := clock.Trap().NewTimer("grok-interjection-handoff")
	defer timer.Close()
	ctx := testutil.DeadlineContext(t)
	a.HandleOutput(turnCompleted(t, old))
	a.HandleOutput(frame(t, map[string]any{
		"method": grokInterjectionNotification,
		"params": map[string]any{"sessionId": grokTestSession, "interjectionId": grokTestLateNativeSteer},
	}))
	delay := testutil.WaitForTimer(t, ctx, timer)
	clock.Advance(delay).MustWait(ctx)
	a.steerReplies.jobs.Wait()
	assert.Nil(t, a.steerReplies.handoff)
	assert.Equal(t, grokTestLateNativeSteer, receipts.retired.InputID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, receipts.outcome)
}

func TestGrokSuccessorResetsPredecessorAfterStream(t *testing.T) {
	t.Parallel()
	const fallback = "interject-fallback-01a2060b-e397-77f1-b1ee-bc0d4e6f2121"
	a, _, _, old := newGrokSteerHandoffTest(t)
	a.steerReplies.active.inputs[0].afterStream = 500
	a.HandleOutput(turnCompleted(t, old))
	a.HandleOutput(frame(t, map[string]any{
		"method": grokInterjectionNotification,
		"params": map[string]any{"sessionId": grokTestSession, "interjectionId": grokTestLateNativeSteer},
	}))
	a.HandleOutput(frame(t, map[string]any{
		"method": grokQueueChangedMethod,
		"params": map[string]any{
			"sessionId": grokTestSession, "runningPromptId": fallback, "runningText": "the steer", "runningKind": "prompt",
		},
	}))
	require.NotNil(t, a.steerReplies.active)
	require.Len(t, a.steerReplies.active.inputs, 1)
	assert.Zero(t, a.steerReplies.active.inputs[0].afterStream, "successor fallback prompt must not inherit predecessor stream timestamp")
}

func TestGrokPromptEndedWithoutTurnCompletedRetiresAndClearsActiveSteer(t *testing.T) {
	t.Parallel()
	a, receipts, _, _ := newGrokSteerHandoffTest(t)
	a.steerReplies.active.inputs[0].echoed = true

	// End the prompt without a preceding turn_completed notification (e.g. transport/RPC failure).
	a.FinishPromptRequestForTest(grokTestSession, nil, assert.AnError)
	a.steerReplies.jobs.Wait()

	assert.Nil(t, a.steerReplies.active, "ended prompt without turn_completed must clear active steer reply")
	assert.Equal(t, grokTestLateNativeSteer, receipts.retired.InputID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, receipts.outcome)
}

func TestGrokConcurrentFirstSteersJoinMatchingActiveBatch(t *testing.T) {
	t.Parallel()
	const sessionUUID = "0195c000-0000-7000-8000-000000000001"
	home := t.TempDir()
	workDir := filepath.Join(home, "work")
	group := grokEncodeCwd(workDir)
	journalDir := filepath.Join(home, ".grok", "sessions", group, sessionUUID)
	require.NoError(t, os.MkdirAll(journalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(journalDir, grokUpdatesJournal), []byte(grokConsumptionRow(sessionUUID, 1, 1, "prior")), 0o644))

	a, _, _ := newGrokAgent(t, agent.Options{HomeDir: home, WorkingDir: workDir}, nil)
	a.SetSessionIDForTest(sessionUUID)
	// Start an agent-initiated turn so grokSteerScope returns a non-empty (promptID, turnID).
	a.HandleOutput(queueChanged(t, sessionUUID, "goal-prompt-1"))

	r1 := agent.NativeInputReceipt{SessionID: sessionUUID, InputID: "steer-1"}
	r2 := agent.NativeInputReceipt{SessionID: sessionUUID, InputID: "steer-2"}
	first := a.admitGrokSteerWithJournal(r1, grokInterjectionPayload{text: "first"}, group, sessionstore.NewJournalCursor(nil))
	require.NotNil(t, first)
	// Second concurrent steer arrives after findJournal with no pre-fetched group while first already installed active.
	second := a.admitGrokSteerWithJournal(r2, grokInterjectionPayload{text: "second"}, "", sessionstore.JournalCursor{})
	require.Same(t, first, second)
	assert.Len(t, first.inputs, 2)
}

func TestGrokPromptResponseBeforeTurnCompletedRetainsFinalStreamKey(t *testing.T) {
	t.Parallel()
	const sessionUUID = "0195c000-0000-7000-8000-000000000002"
	home := t.TempDir()
	workDir := filepath.Join(home, "work")
	group := grokEncodeCwd(workDir)
	journalDir := filepath.Join(home, ".grok", "sessions", group, sessionUUID)
	require.NoError(t, os.MkdirAll(journalDir, 0o755))
	journalPath := filepath.Join(journalDir, grokUpdatesJournal)
	require.NoError(t, os.WriteFile(journalPath, []byte(grokConsumptionRow(sessionUUID, 3, 15, "steer text")), 0o644))

	a, _, _ := newGrokAgent(t, agent.Options{HomeDir: home, WorkingDir: workDir}, nil)
	a.SetSessionIDForTest(sessionUUID)
	replySink := &grokReplyCaptureSink{ControlSink: &agenttest.ControlSink{}}
	a.SetSinkForTest(agent.NewProviderServices(replySink))
	promptID := ownPromptID(t, a)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(queueChanged(t, sessionUUID, promptID))

	a.steerReplies.active = &grokSteerReply{
		sessionID: sessionUUID, promptID: promptID, turnID: "worker-turn", group: group,
		baseline: sessionstore.NewJournalCursor(nil),
		inputs:   []grokSteerAdmission{{id: "steer-1", payload: grokInterjectionPayload{text: "steer text"}, echoed: true, afterStream: 10}},
		streams:  []grokSteerStream{{start: 10, eventSeq: 2}, {start: 20, eventSeq: 4}},
	}

	// Simulate session/prompt response arriving before turn_completed:
	// 1. captureRootSnapshotLocked allocates the final stream's assistant text key BEFORE CaptureReplyReconciliation.
	key := a.grokAssistantTextKey(grokStreamMessageID(sessionUUID, promptID, 20))
	require.NotEmpty(t, key, "final stream must be attributable before CaptureReplyReconciliation")
	reconcile := a.captureGrokSteerReconciliation()
	require.NotNil(t, reconcile)
	// 2. turn_completed arrives on reader before reconciliation runs and publishes immediately with the pre-allocated key.
	a.HandleOutput(frame(t, map[string]any{
		"method": grokSessionUpdateMethod,
		"params": map[string]any{
			"sessionId": sessionUUID,
			"update":    map[string]any{"sessionUpdate": "turn_completed", "prompt_id": promptID, "stop_reason": "end_turn"},
		},
	}))
	// 3. Reconciliation callback runs after finishRootSnapshot and is a no-op because turn_completed already detached.
	require.NoError(t, reconcile(agent.MessageCompletionComplete, false))
	a.steerReplies.jobs.Wait()

	require.Len(t, replySink.published, 1)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: key}}, replySink.published[0].Items)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: "steer-1", AfterOrdinal: 1}}, replySink.published[0].Inputs)
}

type grokReplyCaptureSink struct {
	*agenttest.ControlSink
	published []agent.NativeReplyTurn
}

func (s *grokReplyCaptureSink) PublishNativeReplyTurn(turn agent.NativeReplyTurn) error {
	s.published = append(s.published, turn)
	return nil
}

func TestGrokUpdateMessageIDInvalidatesActiveOnMissingTextMetaOnly(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetSessionIDForTest("session-1")
	a.steerReplies.active = &grokSteerReply{sessionID: "session-1", promptID: "prompt-1", turnID: "turn-1"}

	// Non-text update without metadata does not invalidate the active batch.
	assert.Empty(t, a.grokUpdateMessageID(acp.UpdateMessageInfo{
		SessionID:  "session-1",
		UpdateType: contracts.ACPUpdateToolCall,
	}))
	assert.False(t, a.steerReplies.active.invalid)

	// Root assistant text chunk without valid outer metadata marks the batch invalid.
	assert.Empty(t, a.grokUpdateMessageID(acp.UpdateMessageInfo{
		SessionID:  "session-1",
		UpdateType: contracts.ACPUpdateAgentMessageChunk,
	}))
	assert.True(t, a.steerReplies.active.invalid)
}
