package grok

import (
	"encoding/json"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
