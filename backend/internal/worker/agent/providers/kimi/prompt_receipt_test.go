package kimi

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKimiChildPromptRig(t *testing.T) (*kimiTestRig, *kimiOutputRig) {
	t.Helper()
	rig := newKimiTestRig(t, agent.Options{})
	seedKimiConversationChildInvocation(t, rig)
	return rig, &kimiOutputRig{agent: rig.agent, sink: rig.sink}
}

type kimiTestChildPromptTurn struct {
	id        string
	ordinal   int64
	promptID  string
	outcome   string
	endReason string
}

// deliverKimiChildPromptTurn consumes an exact prompt through the public feed.
// Prompt records and triggerPromptId are absent from the current native protocol.
func deliverKimiChildPromptTurn(t *testing.T, native *kimiOutputRig, turn kimiTestChildPromptTurn) {
	t.Helper()
	dispatch := func(frame map[string]any) {
		raw, err := json.Marshal(frame)
		require.NoError(t, err)
		native.agent.HandleOutput(kimiConversationTestFrame(t, native.sessionID(), "agent-0", string(raw)))
	}
	header := map[string]any{"turn_id": turn.id, "ordinal": turn.ordinal, "kind": "agent", "status": "running", "started_at": "2026-11-18T00:00:00Z", "steps": []any{}}
	dispatch(map[string]any{"type": "turn.started", "turn": header})
	dispatch(map[string]any{"type": "item.ended", "turn_id": turn.id, "item": map[string]any{
		"type": "user_message", "item_id": turn.promptID, "status": "ended", "outcome": "completed", "created_at": "2026-11-18T00:00:00Z",
		"text": "Native user prompt", "prompt_ids": []string{turn.promptID}, "placement": "opener", "origin": map[string]any{"kind": "user"}, "attachments": []any{}, "skills": []any{},
	}})
	header["status"], header["outcome"], header["ended_at"] = "ended", turn.outcome, "2026-11-18T00:00:01Z"
	if turn.endReason != "" {
		header["end_reason"] = turn.endReason
	}
	dispatch(map[string]any{"type": "turn.ended", "turn": header})
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

func TestKimiBlockedChildPromptReceiptPublishesReadinessBeforeCleanup(t *testing.T) {
	t.Parallel()
	rig, _ := newKimiChildPromptRig(t)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "blocked"}
	}})
	ready := make(chan error, 1)
	_, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "A blocked prompt.", Ready: func(captured agent.CapturedTranscript) error {
		_, err := rig.sink.Child(child.childAgentID).PlanWriterFor(captured)
		ready <- err
		return nil
	}})
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-ready:
		require.NoError(t, err, "readiness must use a current child capture before cleanup retires it")
	case <-ctx.Done():
		t.Fatal("the blocked receipt did not publish readiness")
	}
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.agent.dispatchMu.Lock()
	assert.Empty(t, rig.agent.promptSubmissions, "a refused prompt that never ran retains no submission or readiness capture")
	rig.agent.finishDispatch()
}

func TestKimiNativeChildPromptFinalityPrecedesItsLateHTTPReceipt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct{ name, outcome, reason string }{
		{"blocked", "failed", "blocked"}, {"failed", "failed", "error"}, {"aborted", "interrupted", "aborted"}, {"completed", "completed", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			var ordinal atomic.Int64
			rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
				prompt := decodePrompt(t, request)
				index := ordinal.Add(1)
				deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: fmt.Sprintf("own-%d", index), ordinal: index, promptID: prompt.PromptID, outcome: scenario.outcome, endReason: scenario.reason})
				return map[string]any{"prompt_id": prompt.PromptID, "status": "queued"}
			}})
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "First prompt."}))
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Second prompt."}), "a late queued receipt cannot recreate a settled reservation")
			assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
			assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active, "the explicit native completion ends the child turn")
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
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "unrelated", ordinal: 1, promptID: "external-prompt", outcome: "completed"})
	require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "A second own prompt."}), agent.ErrInputPending, "another native user turn is not proof that this queued submission ran")
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 1)
}

func TestKimiOwnChildPromptItemConsumesOnlyItsPendingAdmission(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The queued own prompt."}))
	requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
	require.Len(t, requests, 1)
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "own", ordinal: 1, promptID: decodePrompt(t, requests[0]).PromptID, outcome: "completed"})
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
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "unrelated", ordinal: 1, promptID: "external-prompt", outcome: "completed"})
	rig.agent.dispatchMu.Lock()
	assert.False(t, rig.agent.run("agent-0").childLifetime.retiring, "pending input still needs its original readiness source")
	rig.agent.finishDispatch()
	require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "The next prompt."}), agent.ErrInputPending)
	select {
	case err := <-ready:
		t.Fatalf("an unrelated prompt released this submission's readiness: %v", err)
	default:
	}
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "own", ordinal: 2, promptID: promptID, outcome: "completed"})
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-ready:
		require.NoError(t, err, "readiness must retain a current captured source through unrelated child completion")
	case <-ctx.Done():
		t.Fatal("the exact native prompt consumption did not publish readiness")
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
			status := "blocked"
			if execution {
				status = "queued"
			}
			rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
				return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": status}
			}})
			require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "No readiness callback."}))
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			require.Len(t, requests, 1)
			promptID := decodePrompt(t, requests[0]).PromptID
			if execution {
				deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "own", ordinal: 1, promptID: promptID, outcome: "completed"})
			}
			rig.agent.dispatchMu.Lock()
			defer rig.agent.finishDispatch()
			submission := rig.agent.promptSubmissions[promptID]
			if !execution {
				assert.Nil(t, submission, "a stated blocked receipt without execution retains no submission or service capture")
				return
			}
			require.NotNil(t, submission)
			assert.Nil(t, submission.readyCapture.TranscriptWriter(), "a prompt without readiness must not retain services in its history")
		})
	}
}
