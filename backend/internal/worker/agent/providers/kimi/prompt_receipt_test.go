package kimi

import (
	"encoding/json"
	"fmt"
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
	result, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "The hook blocks this prompt."})
	require.NoError(t, err)
	require.NotEmpty(t, result.NativeReceipt.InputID)
	assert.Equal(t, rig.sessionID(), result.NativeReceipt.SessionID)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		result.NativeReceipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
	}, rig.sink.Child(child.childAgentID).NativeReplyRetirements())
	assert.Empty(t, rig.sink.NativeReplyRetirements())
	require.NoError(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Try the next prompt."}), "blocked submission without a turn cannot retain pending admission")
	assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath(rig.sessionID(), "/prompts")), 2)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiChildLifetimeWaitsForItsAuthoritativeReply(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "queued"}
	}})
	result, err := rig.agent.SendChildInput("session_1/agent-0", agent.ChildInput{Content: "Native user prompt"})
	require.NoError(t, err)
	receipt := result.NativeReceipt
	require.NotEmpty(t, receipt.InputID)

	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	text := "Native user prompt"
	snapshot := conversationRecoveryTestTurn("child-reply", 0, "The child answer.")
	snapshot.EndedAt = "2026-11-18T00:00:01Z"
	snapshot.Items = append([]kimiConversationItem{{
		Type: contracts.KimiConversationItemKindUserMessage, ItemID: receipt.InputID,
		Status: contracts.KimiConversationStatusEnded, Outcome: contracts.KimiConversationOutcomeCompleted,
		CreatedAt: "2026-11-18T00:00:00Z", Text: &text, PromptIDs: []string{receipt.InputID}, Placement: "opener",
		Origin: json.RawMessage(`{"kind":"user"}`), Attachments: []json.RawMessage{}, Skills: []json.RawMessage{},
	}}, snapshot.Items...)
	rig.fake.reply("GET "+kimiSessionPath(rig.sessionID(), "/agents/agent-0/turns"), fakeKapReply{
		BeforeReply: func() { close(entered); <-released },
		Data:        map[string]any{"turns": []kimiConversationTurn{snapshot}, "has_more": false},
	})
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "child-reply", promptID: receipt.InputID, outcome: "completed"})
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the reply lookup did not reach the child history endpoint")
	}
	rig.agent.dispatchMu.Lock()
	assert.False(t, rig.agent.run("agent-0").childLifetime.retiring)
	rig.agent.finishDispatch()
	childSink := rig.sink.Child(child.childAgentID)
	assert.Empty(t, childSink.NativeReplyTurns())
	release()
	waitFor(t, rig.agent.transcriptRecoveryWork.Idle, "the exact reply lookup completes")
	turns := childSink.NativeReplyTurns()
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Items, 1)
	assert.Equal(t, int64(2), turns[0].Items[0].Ordinal)
	assert.Equal(t, []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}}, turns[0].Inputs)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.Empty(t, rig.sink.NativeReplyTurns())
	rig.agent.dispatchMu.Lock()
	assert.True(t, rig.agent.run("agent-0").childLifetime.retiring)
	rig.agent.finishDispatch()
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
