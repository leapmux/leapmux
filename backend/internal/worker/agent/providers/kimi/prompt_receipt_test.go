package kimi

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKimiChildPromptRig(t *testing.T) (*kimiTestRig, *kimiOutputRig) {
	t.Helper()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
	return rig, native
}

func TestKimiBlockedChildPromptReceiptReleasesAdmission(t *testing.T) {
	t.Parallel()
	rig, _ := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "blocked"}
	}})
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The hook blocks this prompt."}))
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Try the next prompt."}), "blocked submission without a turn cannot retain pending admission")
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiNativeChildPromptFinalityPrecedesItsLateHTTPReceipt(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"blocked", "failed", "aborted", "completed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			var sequence atomic.Int64
			sequence.Store(1)
			rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
				prompt := decodePrompt(t, request)
				native.transcriptAgentOps(t, "agent-0", sequence.Add(1), fmt.Sprintf(`{"op":"prompt.upsert","prompt":{"promptId":%q,"status":%q,"createdAt":"2026-11-16T00:00:00Z"}}`, prompt.PromptID, status))
				return map[string]any{"prompt_id": prompt.PromptID, "status": "queued"}
			}})
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "First prompt."}))
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Second prompt."}), "a late queued receipt cannot recreate a settled reservation")
			assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
			assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active, "prompt records do not publish native turn activity")
		})
	}
}

func TestKimiUnrelatedChildTurnCannotConsumePendingPromptAdmission(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The queued own prompt."}))
	native.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unrelated","state":"running","triggerPromptId":"external-prompt","origin":{"kind":"user"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unrelated","state":"completed","triggerPromptId":"external-prompt","origin":{"kind":"user"}}}`)
	require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "A second own prompt."}), agent.ErrInputPending, "another native user turn is not proof that this queued submission ran")
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 1)
}

func TestKimiOwnChildTriggerConsumesOnlyItsPendingPromptAdmission(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The queued own prompt."}))
	requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
	require.Len(t, requests, 1)
	promptID := decodePrompt(t, requests[0]).PromptID
	native.transcriptAgentOps(t, "agent-0", 2,
		fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"own","state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID),
		fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"own","state":"completed","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID))
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The next prompt."}))
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
}

func TestKimiPendingChildPromptSurvivesUnrelatedTurnCleanup(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	ready := make(chan error, 1)
	result, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "The queued prompt.", Ready: func(captured agent.CapturedTranscript) error {
		_, err := rig.sink.Child(child.childAgentID).PlanWriterFor(captured)
		ready <- err
		return nil
	}})
	require.NoError(t, err)
	assert.False(t, result.StartsTurn)
	requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
	require.Len(t, requests, 1)
	promptID := decodePrompt(t, requests[0]).PromptID
	native.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unrelated","state":"running","triggerPromptId":"external-prompt","origin":{"kind":"user"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unrelated","state":"completed","triggerPromptId":"external-prompt","origin":{"kind":"user"}}}`)
	rig.agent.dispatchMu.Lock()
	assert.False(t, rig.agent.run("agent-0").childLifetime.retiring, "pending input still needs its original readiness source")
	rig.agent.finishDispatch()
	require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The next prompt."}), agent.ErrInputPending)
	native.transcriptAgentOps(t, "agent-0", 3, fmt.Sprintf(`{"op":"prompt.upsert","prompt":{"promptId":%q,"status":"aborted","createdAt":"2026-11-16T00:00:00Z"}}`, promptID))
	select {
	case err := <-ready:
		require.NoError(t, err, "readiness must retain a current captured source through unrelated child completion")
	default:
		t.Fatal("the final prompt record did not publish readiness")
	}
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.agent.dispatchMu.Lock()
	submission := rig.agent.promptSubmissions[promptID]
	if assert.NotNil(t, submission) {
		assert.Nil(t, submission.ready, "prompt history must not retain a completed readiness callback")
		assert.Nil(t, submission.readyCapture.TranscriptWriter(), "the output queue owns the capture until readiness completes")
	}
	rig.agent.finishDispatch()
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The next prompt."}))
}

func TestKimiChildPromptWithoutReadinessRetainsNoServiceCapture(t *testing.T) {
	for _, execution := range []bool{false, true} {
		t.Run(fmt.Sprint(execution), func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
				return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
			}})
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "No readiness callback."}))
			requests := rig.fake.requestsTo("POST " + kimiSessionPath("session_1", "/prompts"))
			require.Len(t, requests, 1)
			promptID := decodePrompt(t, requests[0]).PromptID
			if execution {
				native.transcriptAgentOps(t, "agent-0", 2,
					fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"own","state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID),
					fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"own","state":"completed","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID))
			} else {
				native.transcriptAgentOps(t, "agent-0", 2, fmt.Sprintf(`{"op":"prompt.upsert","prompt":{"promptId":%q,"status":"aborted","createdAt":"2026-11-16T00:00:00Z"}}`, promptID))
			}
			rig.agent.dispatchMu.Lock()
			defer rig.agent.finishDispatch()
			submission := rig.agent.promptSubmissions[promptID]
			require.NotNil(t, submission)
			assert.Nil(t, submission.readyCapture.TranscriptWriter(), "a prompt without readiness must not retain services in its history")
		})
	}
}
