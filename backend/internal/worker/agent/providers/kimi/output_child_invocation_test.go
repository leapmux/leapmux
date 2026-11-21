package kimi

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func invocationOutcomeOf(t *testing.T, sink *agenttest.ControlSink, identity kimiInvocationIdentity, childID string) bgtask.Item {
	t.Helper()
	row, exists := sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: identity}, childID))
	require.True(t, exists, "the full parent invocation retains its independent outcome")
	assert.Equal(t, bgtask.RoleTaskOutcome, row.Role)
	return row
}

func TestKimiChildInputReservationSettlesItsReceipt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		uncertain, nativeUser bool
	}{
		{name: "definite refusal"},
		{name: "lost receipt", uncertain: true},
		{name: "new native user survives refusal", nativeUser: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			entered, released := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			defer release()
			rig.fake.reply("POST "+kimiSessionPath("session_1", "/prompts"), fakeKapReply{Code: 40001, Msg: "not allowed", Drop: tc.uncertain, BeforeReply: func() { close(entered); <-released }})
			done := make(chan error, 1)
			go func() {
				done <- agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Again."})
			}()
			ctx := testutil.DeadlineContext(t)
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("the pending child input did not reach its native request")
			}
			require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "Concurrent input."}), agent.ErrInputPending)
			assert.Len(t, rig.fake.requestsTo("POST "+kimiSessionPath("session_1", "/prompts")), 1)
			if tc.nativeUser {
				requests := rig.fake.requestsTo("POST " + kimiSessionPath("session_1", "/prompts"))
				require.Len(t, requests, 1)
				promptID := decodePrompt(t, requests[0]).PromptID
				native.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"turn.started","turn":{"turn_id":"user","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
				item, err := json.Marshal(map[string]any{"type": "item.ended", "turn_id": "user", "item": map[string]any{
					"type": "user_message", "item_id": promptID, "status": "ended", "outcome": "completed", "created_at": "2026-11-18T00:00:00Z", "text": "Again.", "prompt_ids": []string{promptID}, "placement": "opener", "origin": map[string]any{"kind": "user"}, "attachments": []any{}, "skills": []any{},
				}})
				require.NoError(t, err)
				native.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", string(item)))
			}
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			release()
			select {
			case err := <-done:
				require.Error(t, err)
				assert.Equal(t, tc.uncertain, errors.Is(err, agent.ErrDeliveryUncertain))
			case <-ctx.Done():
				t.Fatal("the native child receipt did not settle")
			}
			row, _ := childOf(t, rig.sink, "session_1/agent-0")
			want := bgtask.StatusEndedWithUnknownOutcome
			if tc.nativeUser {
				want = bgtask.StatusRunning
			}
			assert.Equal(t, want, row.Status)
			assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, rig.sink, "old-task").Status)
			child, linked := rig.agent.children.get("agent-0")
			require.True(t, linked)
			assert.Equal(t, tc.uncertain && !tc.nativeUser, child.pendingPrompt != nil)
			if tc.nativeUser {
				assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
				native.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"turn.ended","turn":{"turn_id":"user","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","ended_at":"2026-11-18T00:00:01Z","steps":[]}}`))
				row, _ = childOf(t, rig.sink, "session_1/agent-0")
				assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			}
		})
	}
}

type kimiTestInvocationCall struct {
	id, prompt, description string
	resumeExisting          bool
	outcome, output         string
}

func (call kimiTestInvocationCall) item() map[string]any {
	input := map[string]any{"prompt": call.prompt}
	if call.description != "" {
		input["description"] = call.description
	}
	if call.resumeExisting {
		input["resume"] = "agent-0"
	}
	item := map[string]any{
		"type": "tool_call", "item_id": call.id, "name": "Agent", "step_index": 1,
		"created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:00Z",
		"input": input, "agent_refs": []any{map[string]any{"agent_id": "agent-0", "role": "child"}},
	}
	if call.outcome == "" {
		item["status"], item["stage"] = "running", "executing"
	} else {
		item["status"], item["outcome"] = "ended", call.outcome
		if call.outcome == "failed" {
			item["error"] = call.output
		} else {
			item["output"] = call.output
		}
	}
	return item
}

func (call kimiTestInvocationCall) deliver(t *testing.T, native *kimiOutputRig, turnID string) {
	t.Helper()
	frameType := "item.started"
	if call.outcome != "" {
		frameType = "item.ended"
	}
	raw, err := json.Marshal(map[string]any{"type": frameType, "turn_id": turnID, "item": call.item()})
	require.NoError(t, err)
	native.conversationFrame(t, string(raw))
}

func TestKimiIndependentChildTurnDoesNotReuseAnOldTaskStopAddress(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	native.startPublicTurn(t, "agent-0", "first", 0)
	native.endPublicTurn(t, "agent-0", "first", 0, "completed")
	native.startPublicTurn(t, "agent-0", "user", 1)
	native.publicOpener(t, "agent-0", "user", `{"kind":"user"}`)
	require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
	rig.fake.mu.Lock()
	assert.Empty(t, rig.fake.taskCommands, "a taskless user turn must not stop an earlier parent task")
	rig.fake.mu.Unlock()
}

func TestKimiNewParentInvocationRetainsItsOwnOutcome(t *testing.T) {
	t.Parallel()
	for _, userBefore := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent_user_turn=%t", userBefore), func(t *testing.T) {
			t.Parallel()
			rig, native := newKimiChildPromptRig(t)
			native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			if userBefore {
				deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "user", ordinal: 0, promptID: "independent", outcome: "completed"})
			}
			native.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
			require.NoError(t, native.agent.SendInput("Resume the child.", nil))
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			require.Len(t, requests, 1)
			promptID := decodePrompt(t, requests[0]).PromptID
			before, _ := childOf(t, native.sink, "session_1/agent-0")
			require.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, native.sink, "old-task").Status)
			require.False(t, before.Status.IsWorking())
			native.startPublicTurn(t, kimiMainAgentID, "t1", 1)
			native.conversationFrame(t, `{"type":"item.ended","turn_id":"t1","item":`+kimiPublicPromptItem(t, promptID, "opener", promptID)+`}`)
			call := kimiTestInvocationCall{id: "resume", prompt: "Review again.", description: "New assignment", resumeExisting: true}
			call.deliver(t, native, "t1")
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, before.Status, row.Status, "a new parent operation cannot revive child activity")
			identity := kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t1", toolCallID: "resume"}
			assert.Equal(t, bgtask.StatusRunning, invocationOutcomeOf(t, native.sink, identity, "agent-0").Status)
			call.outcome = "failed"
			call.output = "agent_id: agent-0\nactual_subagent_type: explore\nstatus: failed\nstop_reason: provider_error\n\nsubagent error: New provider failure."
			call.deliver(t, native, "t1")
			row, _ = childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, before.Status, row.Status)
			assert.Equal(t, bgtask.StatusFailed, invocationOutcomeOf(t, native.sink, identity, "agent-0").Status)
			assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, native.sink, "old-task").Status)
		})
	}
}

func TestKimiDelayedParentStartCannotSupersedeACompletedUserTurn(t *testing.T) {
	t.Parallel()
	native := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, native)
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "user", ordinal: 0, promptID: "independent", outcome: "completed"})
	native.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
	native.startPublicTurn(t, kimiMainAgentID, "t1", 1)
	call := kimiTestInvocationCall{id: "resume", prompt: "An older resume.", description: "Delayed assignment", resumeExisting: true}
	call.deliver(t, native, "t1")
	row, _ := childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "frame arrival does not prove the invocation follows the user's work")
	call.outcome = "failed"
	call.output = "agent_id: agent-0\nactual_subagent_type: explore\nstatus: failed\nstop_reason: provider_error\n\nsubagent error: Old failure."
	call.deliver(t, native, "t1")
	row, _ = childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestKimiConcurrentInvocationKeepsThePreviousNativeTaskOutcome(t *testing.T) {
	t.Parallel()
	native := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, native)
	// Both calls target this child in the same step. Their ordinals cannot establish ownership.
	call := kimiTestInvocationCall{id: "other", prompt: "Another assignment.", resumeExisting: true}
	call.deliver(t, native, "t0")
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","error":"The previous task failed."}`))
	row, _ := childOf(t, native.sink, "session_1/agent-0")
	assert.False(t, row.Status.IsWorking(), "parent operations do not invent native child activity")
	assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, native.sink, "old-task").Status)
	current := invocationOutcomeOf(t, native.sink, kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", toolCallID: "other"}, "agent-0")
	assert.Equal(t, bgtask.StatusRunning, current.Status, "the independent unresolved invocation retains its own state")
}

func TestKimiDefiniteForegroundResultClosesBeforeTaskEndMetadata(t *testing.T) {
	t.Parallel()
	native := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, native)
	call := kimiTestInvocationCall{id: "spawn", prompt: "Review it.", description: "Old assignment", outcome: "completed",
		output: "agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nThe foreground assignment finished."}
	call.deliver(t, native, "t0")
	row := invocationOutcomeOf(t, native.sink, kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", toolCallID: "spawn"}, "agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "the fixed foreground result proves this invocation completed")
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, native.sink, "old-task").Status, "a bare call ID cannot merge the native task")
}

func TestKimiDelayedIntermediateInvocationCannotCloseANewerUnjoinedTurn(t *testing.T) {
	t.Parallel()
	native := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, native)
	for ordinal, turn := range []string{"first", "middle"} {
		native.startPublicTurn(t, "agent-0", turn, ordinal)
		native.endPublicTurn(t, "agent-0", turn, ordinal, "completed")
	}
	native.startPublicTurn(t, "agent-0", "latest", 2)
	native.startPublicTurn(t, kimiMainAgentID, "parent-middle", 1)
	call := kimiTestInvocationCall{id: "middle-call", prompt: "Middle assignment.", resumeExisting: true, outcome: "completed",
		output: "agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nThe middle assignment finished."}
	call.deliver(t, native, "parent-middle")
	row, _ := childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "middle is newer than first, but that does not identify latest")
	assert.True(t, native.agent.ActiveChildTurnState(row.RowKey).Active)
	assert.Equal(t, "latest", native.agent.run("agent-0").conversation.activeTurnID)
}

func TestKimiNativeOutcomeRemainsKnownWhenUnmatchedParentMetadataArrives(t *testing.T) {
	t.Parallel()
	native := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, native)
	(kimiTestInvocationCall{id: "other", prompt: "Another assignment.", resumeExisting: true}).deliver(t, native, "t0")
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"new-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"new-call"}`))
	native.startPublicTurn(t, kimiMainAgentID, "new-parent", 1)
	(kimiTestInvocationCall{id: "new-call", prompt: "New assignment.", resumeExisting: true}).deliver(t, native, "new-parent")
	row := nativeTaskOf(t, native.sink, "new-task")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "parent metadata cannot replace a known native task outcome")
	current := invocationOutcomeOf(t, native.sink, kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "new-parent", toolCallID: "new-call"}, "agent-0")
	assert.Equal(t, bgtask.StatusRunning, current.Status)
}

func TestKimiCompletedChildSnapshotUsesItsOwnAssignment(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{"user", "system"} {
		for _, previousEnded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/previous_ended=%t", origin, previousEnded), func(t *testing.T) {
				t.Parallel()
				native := newKimiConversationRig(t)
				seedPublicKimiChildInvocation(t, native)
				native.startPublicTurn(t, "agent-0", "first", 0)
				native.endPublicTurn(t, "agent-0", "first", 0, "completed")
				if previousEnded {
					native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
				}
				_, child := childOf(t, native.sink, "session_1/agent-0")
				activeBefore := child.TurnActives()
				const answer = `{"type":"assistant_message","item_id":"answer","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"Recovered completed assignment."}`
				opener := fmt.Sprintf(`{"type":"user_message","item_id":"missed-prompt","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","prompt_ids":[],"placement":"opener","origin":{"kind":%q},"text":"Review the missing assignment.","attachments":[],"skills":[]}`, origin)
				native.conversationAgentFrame(t, "agent-0", `{"type":"turns","has_more":false,"turns":[{"turn_id":"missed","ordinal":1,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+opener+`,`+answer+`]}]}`)
				awaitKimiTestOutput(t, native.agent.sink)
				row, _ := childOf(t, native.sink, "session_1/agent-0")
				want := bgtask.StatusEndedWithUnknownOutcome
				if origin == "user" {
					want = bgtask.StatusSucceeded
				}
				assert.Equal(t, want, row.Status)
				assert.False(t, native.agent.ActiveChildTurnState(row.RowKey).Active)
				assert.NotContains(t, child.TurnActives()[len(activeBefore):], true, "completed history never starts a live turn")
				rows := child.Messages()
				require.GreaterOrEqual(t, len(rows), 3)
				view, err := decodeKimiConversationRecord(rows[len(rows)-2].Content)
				require.NoError(t, err)
				require.NotNil(t, view.item)
				require.NotNil(t, view.item.Text)
				assert.Equal(t, "Recovered completed assignment.", *view.item.Text)
				assert.Equal(t, "missed", view.record.TurnID)
				assert.True(t, rows[len(rows)-1].TurnEnd)
			})
		}
	}
}

func TestKimiChildInputProtectsItsPendingNativeAdmission(t *testing.T) {
	t.Parallel()
	rig, native := newKimiChildPromptRig(t)
	ctx := testutil.DeadlineContext(t)
	entered := make(chan string, 1)
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.fake.reply("POST "+kimiSessionPath(rig.sessionID(), "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		promptID := decodePrompt(t, request).PromptID
		entered <- promptID
		select {
		case <-released:
		case <-ctx.Done():
		}
		return map[string]any{"prompt_id": promptID, "status": "running"}
	}})
	done := make(chan error, 1)
	go func() {
		done <- agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "One more thing."})
	}()
	var promptID string
	select {
	case promptID = <-entered:
	case err := <-done:
		t.Fatalf("the child prompt returned before reaching the native request: %v", err)
	case <-ctx.Done():
		t.Fatal("the child prompt did not reach its native request")
	}
	require.NotEmpty(t, promptID)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	awaitKimiTestOutput(t, rig.agent.sink)
	row, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.False(t, row.Status.IsWorking(), "a pending prompt has not started a native child turn")
	assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, rig.sink, "old-task").Status)
	pending, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	require.NotNil(t, pending.pendingPrompt)
	assert.Equal(t, "agent-0", pending.pendingPrompt.receiver)
	assert.Equal(t, promptID, pending.pendingPrompt.id)
	require.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "A competing prompt."}), agent.ErrInputPending)
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the child prompt did not finish")
	}
	deliverKimiChildPromptTurn(t, native, kimiTestChildPromptTurn{id: "user", promptID: promptID, outcome: "completed"})
	awaitKimiTestOutput(t, rig.agent.sink)
	row, _ = childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	pending, linked = rig.agent.children.get("agent-0")
	require.True(t, linked)
	assert.Nil(t, pending.pendingPrompt, "the exact public prompt consumes the reservation")
}

func TestKimiChildInvocationFenceDefersStateUntilParentReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	seedKimiConversationChildInvocation(t, rig)
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	enteredOnce := sync.OnceFunc(func() { close(entered) })
	page := kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","has_more":false,"turns":[{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[],"items":[{"type":"tool_call","item_id":"resume","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","started_at":"2026-11-18T00:00:01Z","name":"Agent","input":{"resume":"agent-0","prompt":"Again."},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nDone.","agent_refs":[{"agent_id":"agent-0","role":"child"}]}]}]}`)
	rig.fake.mu.Lock()
	rig.fake.beforeReplay = func(fakeKapSubscribe) { enteredOnce(); <-released }
	rig.fake.sessions[rig.sessionID()].ReplayFrames = []any{json.RawMessage(page)}
	rig.fake.mu.Unlock()
	require.NoError(t, rig.fake.disconnectClients())
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native resubscription did not reach its conversation page")
	}
	rig.agent.dispatchMu.Lock()
	assert.NotNil(t, rig.agent.invocationRecovery, "the fence must start before inbound restoration")
	rig.agent.finishDispatch()
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "old-task").Status)
	release()
	waitForKimiConversationInvocationRecovery(t, rig)
	assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, rig.sink, "old-task").Status)
	newer := invocationOutcomeOf(t, rig.sink, kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t1", toolCallID: "resume"}, "agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, newer.Status)
}

func TestKimiChildInvocationFenceCompletesWithEmptyConversationPages(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	seedKimiConversationChildInvocation(t, rig)
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	enteredOnce := sync.OnceFunc(func() { close(entered) })
	rig.fake.mu.Lock()
	rig.fake.beforeReplay = func(fakeKapSubscribe) { enteredOnce(); <-released }
	rig.fake.mu.Unlock()
	require.NoError(t, rig.fake.disconnectClients())
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the reconnect did not reach its initial pages")
	}
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "old-task").Status)
	release()
	waitForKimiConversationInvocationRecovery(t, rig)
	assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, rig.sink, "old-task").Status)
}

func TestKimiChildInvocationRefusesMalformedSnapshotAtomically(t *testing.T) {
	t.Parallel()
	for _, malformed := range []struct{ name, raw string }{
		{"fractional turn ordinal", `{"turn_id":"bad","ordinal":1.5,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}`},
		{"duplicate turn identity", `{"turn_id":"t1","ordinal":2,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}`},
		{"nonnumeric step ordinal", `{"turn_id":"bad","ordinal":2,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[{"index":"later","outcome":"completed","started_at":"2026-11-18T00:00:00Z"}],"items":[]}`},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			t.Parallel()
			native := newKimiConversationRig(t)
			seedPublicKimiChildInvocation(t, native)
			call := kimiTestInvocationCall{id: "resume", prompt: "Again.", resumeExisting: true, outcome: "failed",
				output: "agent_id: agent-0\nactual_subagent_type: explore\nstatus: failed\nstop_reason: provider_error\n\nsubagent error: Failed."}
			item, err := json.Marshal(call.item())
			require.NoError(t, err)
			awaitKimiTestOutput(t, native.agent.sink)
			before := native.sink.BackgroundTasks()
			native.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+string(item)+`]},`+malformed.raw+`]}`)
			awaitKimiTestOutput(t, native.agent.sink)
			assert.Equal(t, before, native.sink.BackgroundTasks(), "a malformed later turn cannot admit earlier outcome changes")
			assert.Equal(t, "t0", native.agent.run(kimiMainAgentID).conversation.activeTurnID)
		})
	}
}

func TestKimiChildInvocationRejectsAnOlderRecoveredTask(t *testing.T) {
	t.Parallel()
	for _, repeatedCall := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			t.Run(fmt.Sprintf("repeated_call=%t/newer_running=%t", repeatedCall, running), func(t *testing.T) {
				t.Parallel()
				rig, native := newKimiChildPromptRig(t)
				native.startPublicTurn(t, "agent-0", "first", 0)
				native.endPublicTurn(t, "agent-0", "first", 0, "completed")
				entered, released := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				defer release()
				enter := sync.OnceFunc(func() { close(entered) })
				rig.fake.mu.Lock()
				rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
					assert.Equal(t, "old-task", command.TaskID)
					enter()
					<-released
					return map[string]any{"task_id": "old-task", "kind": "subagent", "status": "ended", "outcome": "failed", "error": "Old assignment failed.", "detached": false, "child_agent_id": "agent-0", "parent_tool_call_id": "spawn", "description": "Old assignment"}
				}
				rig.fake.mu.Unlock()
				rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
				ctx := testutil.DeadlineContext(t)
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("the old task watch did not reach the server")
				}
				call := kimiTestInvocationCall{id: "new-spawn", prompt: "Review the next change.", description: "New assignment", resumeExisting: true}
				if repeatedCall {
					call.id = "spawn"
				}
				if !running {
					call.outcome = "completed"
					call.output = "agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nNew assignment completed."
				}
				item, err := json.Marshal(call.item())
				require.NoError(t, err)
				native.conversationFrame(t, `{"type":"turns","has_more":false,"turns":[{"turn_id":"t1","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+string(item)+`]}]}`)
				childStatus := `"status":"ended","outcome":"completed"`
				if running {
					childStatus = `"status":"running"`
				}
				native.conversationAgentFrame(t, "agent-0", `{"type":"turns","has_more":false,"turns":[{"turn_id":"next","ordinal":1,"kind":"agent",`+childStatus+`,"started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]}]}`)
				release()
				waitFor(t, rig.agent.taskRecoveryWork.Idle, "the older task response finishes applying")
				awaitKimiTestOutput(t, rig.agent.sink)
				row, _ := childOf(t, rig.sink, "session_1/agent-0")
				want := bgtask.StatusEndedWithUnknownOutcome
				if running {
					want = bgtask.StatusRunning
				}
				assert.Equal(t, want, row.Status, "the older task watch cannot supply a newer child turn outcome")
				assert.Equal(t, running, rig.agent.ActiveChildTurnState(row.RowKey).Active)
				assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, rig.sink, "old-task").Status,
					"the old task outcome remains retained at its native address")
			})
		}
	}
}
