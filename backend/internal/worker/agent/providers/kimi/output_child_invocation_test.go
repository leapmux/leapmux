package kimi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func invocationOutcomeOf(t *testing.T, sink *agenttest.ControlSink, identity kimiTranscriptToolIdentity, childID string) bgtask.Item {
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
			rig := newKimiTestRig(t, agent.Options{})
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			seedKimiChildInvocation(t, native)
			native.transcriptAgentOps(t, "agent-0", 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
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
				native.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`)
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
				native.transcriptAgentOps(t, "agent-0", 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"completed","origin":{"kind":"user"}}}`)
				row, _ = childOf(t, rig.sink, "session_1/agent-0")
				assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			}
		})
	}
}

func TestKimiIndependentChildTurnDoesNotReuseAnOldTaskStopAddress(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`)
	require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
	rig.fake.mu.Lock()
	assert.Empty(t, rig.fake.taskCommands, "a taskless user turn must not stop an earlier parent task")
	rig.fake.mu.Unlock()
}

func seedKimiChildInvocation(t *testing.T, native *kimiOutputRig) {
	t.Helper()
	native.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Old assignment","prompt":"Review it."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Old assignment"}`))
}

func TestKimiNewParentInvocationRetainsItsOwnOutcome(t *testing.T) {
	t.Parallel()
	for _, userBefore := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent_user_turn=%t", userBefore), func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			seedKimiChildInvocation(t, native)
			native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			if userBefore {
				native.transcriptAgentOps(t, "agent-0", 1,
					`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`,
					`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"completed","origin":{"kind":"user"}}}`)
			}
			native.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"user"}}}`)
			require.NoError(t, native.agent.SendInput("Resume the child.", nil))
			requests := rig.fake.requestsTo("POST " + kimiSessionPath(rig.sessionID(), "/prompts"))
			require.Len(t, requests, 1)
			promptID := decodePrompt(t, requests[0]).PromptID
			before, _ := childOf(t, native.sink, "session_1/agent-0")
			require.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, native.sink, "old-task").Status)
			require.False(t, before.Status.IsWorking())
			native.transcriptOps(t, 3,
				fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":%q,"origin":{"kind":"user"}}}`, promptID),
				`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"t1.1","ordinal":1,"state":"running"}}`,
				`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"new","toolCallId":"resume","name":"Agent","state":"running","input":{"resume":"agent-0","prompt":"Review again.","description":"New assignment"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, before.Status, row.Status, "a new parent operation cannot revive child activity")
			started := invocationOutcomeOf(t, native.sink, kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t1", stepID: "t1.1", frameID: "new", toolCallID: "resume"}, "agent-0")
			assert.Equal(t, bgtask.StatusRunning, started.Status)
			native.transcriptOps(t, 4, `{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"new","toolCallId":"resume","name":"Agent","state":"error","input":{"resume":"agent-0","prompt":"Review again.","description":"New assignment"},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: failed\nstop_reason: provider_error\n\nsubagent error: New provider failure.","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			row, _ = childOf(t, native.sink, "session_1/agent-0")
			assert.Equal(t, before.Status, row.Status)
			finished := invocationOutcomeOf(t, native.sink, kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t1", stepID: "t1.1", frameID: "new", toolCallID: "resume"}, "agent-0")
			assert.Equal(t, bgtask.StatusFailed, finished.Status, "the new invocation keeps its known outcome")
			assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, native.sink, "old-task").Status)
		})
	}
}

func TestKimiDelayedParentStartCannotSupersedeACompletedUserTurn(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	seedKimiChildInvocation(t, native)
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"completed","origin":{"kind":"user"}}}`)
	// A native dispatcher can delay the first start frame until after a different agent's user turn ends.
	native.transcriptOps(t, 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","triggerPromptId":"unowned-prompt","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t1","step":{"kind":"step","stepId":"t1.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"delayed","toolCallId":"resume","name":"Agent","state":"running","input":{"resume":"agent-0","prompt":"An older resume.","description":"Delayed assignment"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	row, _ := childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "frame arrival does not prove the invocation follows the user's work")
	native.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"delayed","toolCallId":"resume","name":"Agent","state":"error","input":{"resume":"agent-0","prompt":"An older resume.","description":"Delayed assignment"},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: failed\nstop_reason: provider_error\n\nsubagent error: Old failure.","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	row, _ = childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestKimiConcurrentInvocationKeepsThePreviousNativeTaskOutcome(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	seedKimiChildInvocation(t, native)
	// Both calls target this child in the same step. Their ordinals cannot establish ownership.
	native.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"other","toolCallId":"other","name":"Agent","state":"running","input":{"resume":"agent-0","prompt":"Another assignment."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","error":"The previous task failed."}`))
	row, _ := childOf(t, native.sink, "session_1/agent-0")
	assert.False(t, row.Status.IsWorking(), "parent operations do not invent native child activity")
	assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, native.sink, "old-task").Status)
	current := invocationOutcomeOf(t, native.sink, kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "other", toolCallID: "other"}, "agent-0")
	assert.Equal(t, bgtask.StatusRunning, current.Status, "the independent unresolved invocation retains its own state")
}

func TestKimiDefiniteForegroundResultClosesBeforeTaskEndMetadata(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	seedKimiChildInvocation(t, native)
	native.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"done","input":{"description":"Old assignment","prompt":"Review it."},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nThe foreground assignment finished.","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	row := invocationOutcomeOf(t, native.sink, kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "spawn", toolCallID: "spawn"}, "agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "the fixed foreground result proves this invocation completed")
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, native.sink, "old-task").Status, "a bare call ID cannot merge the native task")
}

func TestKimiDelayedIntermediateInvocationCannotCloseANewerUnjoinedTurn(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
	native.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"middle","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"middle","state":"completed","origin":{"kind":"other"}}}`)
	middleTurn := native.agent.run("agent-0").transcript.activeTurn
	native.transcriptAgentOps(t, "agent-0", 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"latest","state":"running","origin":{"kind":"other"}}}`)
	latestTurn := native.agent.run("agent-0").transcript.activeTurn
	assert.NotEqual(t, middleTurn, latestTurn, "the new native turn has its own identity")
	native.transcriptOps(t, 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"parent-middle","ordinal":1,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"parent-middle","step":{"kind":"step","stepId":"middle.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"parent-middle","stepId":"middle.1","frame":{"kind":"tool","frameId":"middle-call","toolCallId":"middle-call","name":"Agent","state":"done","input":{"resume":"agent-0","prompt":"Middle assignment."},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nThe middle assignment finished.","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	row, _ := childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "middle is newer than first, but that does not identify latest")
	assert.True(t, native.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiNativeOutcomeRemainsKnownWhenUnmatchedParentMetadataArrives(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	seedKimiChildInvocation(t, native)
	native.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"other","toolCallId":"other","name":"Agent","state":"running","input":{"resume":"agent-0","prompt":"Another assignment."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"new-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"new-call"}`))
	native.transcriptOps(t, 3,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"new-parent","ordinal":1,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"new-parent","step":{"kind":"step","stepId":"new.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"new-parent","stepId":"new.1","frame":{"kind":"tool","frameId":"new-call","toolCallId":"new-call","name":"Agent","state":"running","input":{"resume":"agent-0","prompt":"New assignment."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	row := nativeTaskOf(t, native.sink, "new-task")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status, "parent metadata cannot replace a known native task outcome")
	current := invocationOutcomeOf(t, native.sink, kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "new-parent", stepID: "new.1", frameID: "new-call", toolCallID: "new-call"}, "agent-0")
	assert.Equal(t, bgtask.StatusRunning, current.Status)
}

func TestKimiCompletedChildSnapshotUsesItsOwnAssignment(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		origin        string
		previousEnded bool
	}{{"user", false}, {"other", false}, {"user", true}, {"other", true}} {
		t.Run(fmt.Sprintf("%s/previous_ended=%t", scenario.origin, scenario.previousEnded), func(t *testing.T) {
			origin := scenario.origin
			t.Parallel()
			native := newKimiOutputRig(t)
			seedKimiChildInvocation(t, native)
			native.transcriptAgentOps(t, "agent-0", 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
			if scenario.previousEnded {
				native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			}
			_, child := childOf(t, native.sink, "session_1/agent-0")
			activeBefore := child.TurnActives()
			native.agent.Mu.Lock()
			endedAt := native.agent.attachedAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
			native.agent.Mu.Unlock()
			turn := map[string]any{"kind": "turn", "turnId": "missed", "ordinal": 1, "state": "completed", "endedAt": endedAt, "origin": map[string]any{"kind": origin}, "steps": []any{
				map[string]any{"kind": "step", "stepId": "missed.1", "ordinal": 1, "state": "completed", "frames": []any{
					map[string]any{"kind": "text", "frameId": "answer", "role": "assistant", "text": "Recovered completed assignment."},
				}},
			}}
			native.agent.HandleOutput(nativeKimiRecoveryReset(t, native.sessionID(), "agent-0", 3, turn))
			row, _ := childOf(t, native.sink, "session_1/agent-0")
			want := bgtask.StatusEndedWithUnknownOutcome
			if origin == "user" {
				want = bgtask.StatusSucceeded
			}
			assert.Equal(t, want, row.Status)
			assert.False(t, native.agent.ActiveChildTurnState("session_1/agent-0").Active)
			for _, active := range child.TurnActives()[len(activeBefore):] {
				assert.False(t, active, "completed history must not publish a new active turn")
			}
			var retained bool
			for _, message := range child.Messages() {
				if strings.Contains(string(message.Content), "Recovered completed assignment.") {
					retained = true
				}
			}
			assert.True(t, retained)
		})
	}
}

func TestKimiChildInputProtectsItsPendingNativeAdmission(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.fake.reply("POST "+kimiSessionPath("session_1", "/prompts"), fakeKapReply{DataFor: func(request fakeKapRequest) any {
		return map[string]any{"prompt_id": decodePrompt(t, request).PromptID, "status": "running"}
	}, BeforeReply: func() { close(entered); <-released }})
	done := make(chan error, 1)
	go func() {
		done <- agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "One more thing."})
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the child prompt did not reach its native request")
	}
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	row, _ := childOf(t, rig.sink, "session_1/agent-0")
	assert.False(t, row.Status.IsWorking(), "a pending prompt has not started a native child turn")
	assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, rig.sink, "old-task").Status)
	pending, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	require.NotNil(t, pending.pendingPrompt)
	assert.Equal(t, "agent-0", pending.pendingPrompt.receiver)
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the child prompt did not finish")
	}
	native.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"completed","origin":{"kind":"user"}}}`)
	row, _ = childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
}

func TestKimiChildInvocationFenceDefersStateUntilParentReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	seedKimiChildInvocation(t, native)
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.fake.mu.Lock()
	rig.fake.beforeReplay = func(fakeKapSubscribe) { close(entered); <-released }
	rig.fake.mu.Unlock()
	parent := map[string]any{"kind": "turn", "turnId": "t1", "ordinal": 1, "state": "running", "origin": map[string]any{"kind": "user"}, "steps": []any{map[string]any{"kind": "step", "stepId": "t1.1", "ordinal": 1, "state": "running", "frames": []any{
		map[string]any{"kind": "tool", "frameId": "new", "toolCallId": "resume", "name": "Agent", "state": "done", "input": map[string]any{"resume": "agent-0", "prompt": "Again."}, "output": "agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nDone.", "agentRefs": []any{map[string]any{"agentId": "agent-0", "role": "child"}}},
	}}}}
	finished := beginNativeKimiReconnect(t, rig, nativeKimiRecoveryReset(t, rig.sessionID(), "main", 20, parent))
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native resubscription did not reach replay")
	}
	rig.agent.dispatchMu.Lock()
	assert.NotNil(t, rig.agent.invocationRecovery, "the fence must start before inbound restoration")
	rig.agent.finishDispatch()
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	row := nativeTaskOf(t, rig.sink, "old-task")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	release()
	waitFor(t, finished, "the parent snapshot and applied marker finish")
	waitFor(t, func() bool {
		rig.agent.dispatchMu.Lock()
		defer rig.agent.dispatchMu.Unlock()
		return rig.agent.invocationRecovery == nil
	}, "the ordered native barrier releases the projection fence")
	row = nativeTaskOf(t, rig.sink, "old-task")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	newer := invocationOutcomeOf(t, rig.sink, kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t1", stepID: "t1.1", frameID: "new", toolCallID: "resume"}, "agent-0")
	assert.Equal(t, bgtask.StatusSucceeded, newer.Status)
}

func TestKimiChildInvocationFenceCompletesWithoutTranscriptOperations(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	seedKimiChildInvocation(t, native)
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.fake.mu.Lock()
	rig.fake.beforeReplay = func(fakeKapSubscribe) { close(entered); <-released }
	rig.fake.mu.Unlock()
	rig.fake.closeConnections()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the reconnect did not reach its native state sample")
	}
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	release()
	waitFor(t, func() bool {
		rig.agent.dispatchMu.Lock()
		defer rig.agent.dispatchMu.Unlock()
		return rig.agent.invocationRecovery == nil
	}, "an acknowledgement-only catchup still completes its applied barrier")
	row := nativeTaskOf(t, rig.sink, "old-task")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestKimiChildInvocationRefusesMalformedSnapshotAtomically(t *testing.T) {
	t.Parallel()
	for _, malformed := range []struct{ name, raw string }{
		{"fractional turn ordinal", `{"kind":"turn","turnId":"bad","ordinal":1.5,"state":"running","steps":[]}`},
		{"duplicate turn identity", `{"kind":"turn","turnId":"t1","ordinal":2,"state":"running","steps":[]}`},
		{"nonnumeric step ordinal", `{"kind":"turn","turnId":"bad","ordinal":2,"state":"running","steps":[{"kind":"step","stepId":"bad.1","ordinal":"later","state":"running","frames":[]}]}`},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			t.Parallel()
			native := newKimiOutputRig(t)
			seedKimiChildInvocation(t, native)
			turn := json.RawMessage(`{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t1.1","ordinal":1,"state":"running","frames":[{"kind":"tool","frameId":"new","toolCallId":"resume","name":"Agent","state":"error","input":{"resume":"agent-0","prompt":"Again."},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: failed\nstop_reason: provider_error\n\nsubagent error: Failed.","agentRefs":[{"agentId":"agent-0","role":"child"}]}]}]}`)
			var validItem, invalidItem map[string]any
			require.NoError(t, json.Unmarshal(turn, &validItem))
			require.NoError(t, json.Unmarshal([]byte(malformed.raw), &invalidItem))
			before := native.sink.BackgroundTasks()
			native.agent.HandleOutput(nativeKimiRecoveryReset(t, native.sessionID(), "main", 20, validItem, invalidItem))
			assert.Equal(t, before, native.sink.BackgroundTasks(), "a malformed later item cannot admit earlier outcome mutations")
		})
	}
}

func TestKimiChildInvocationRejectsAnOlderRecoveredTask(t *testing.T) {
	t.Parallel()
	for _, repeatedCall := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			t.Run(fmt.Sprintf("repeated_call=%t/newer_running=%t", repeatedCall, running), func(t *testing.T) {
				t.Parallel()
				rig := newKimiTestRig(t, agent.Options{})
				native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
				native.transcriptOps(t, 1,
					`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
					`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
					`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Old assignment","prompt":"Review it."},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Old assignment"}`))
				native.transcriptAgentOps(t, "agent-0", 1,
					`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
					`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
				entered, released := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				defer release()
				var enteredOnce sync.Once
				rig.fake.mu.Lock()
				rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
					assert.Equal(t, "old-task", command.TaskID)
					enteredOnce.Do(func() { close(entered) })
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
				callID := "new-spawn"
				if repeatedCall {
					callID = "spawn"
				}
				frame := map[string]any{"kind": "tool", "frameId": "new-frame", "toolCallId": callID, "name": "Agent", "state": "running", "input": map[string]any{"resume": "agent-0", "prompt": "Review the next change.", "description": "New assignment"}, "agentRefs": []any{map[string]any{"agentId": "agent-0", "role": "child"}}}
				if !running {
					frame["state"] = "done"
					frame["output"] = "agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nNew assignment completed."
				}
				parent := map[string]any{"kind": "turn", "turnId": "t1", "ordinal": 1, "state": "running", "origin": map[string]any{"kind": "user"}, "steps": []any{map[string]any{"kind": "step", "stepId": "t1.1", "ordinal": 1, "state": "running", "frames": []any{frame}}}}
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "main", 20, parent))
				state := "completed"
				if running {
					state = "running"
				}
				child := map[string]any{"kind": "turn", "turnId": "next", "ordinal": 1, "state": state, "origin": map[string]any{"kind": "other"}, "endedAt": time.Now().UTC().Format(time.RFC3339Nano), "steps": []any{}}
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "agent-0", 20, child))
				release()
				waitFor(t, rig.agent.taskRecoveryWork.Idle, "the older task response finishes applying")
				row, _ := childOf(t, rig.sink, "session_1/agent-0")
				want := bgtask.StatusEndedWithUnknownOutcome
				if running {
					want = bgtask.StatusRunning
				}
				assert.Equal(t, want, row.Status, "the older task watch cannot supply a newer child turn outcome")
				assert.Equal(t, running, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
				rig.agent.Mu.Lock()
				old := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "old-task"}]
				rig.agent.Mu.Unlock()
				require.NotNil(t, old)
				assert.True(t, old.ended, "the old task outcome remains retained at its native address")
			})
		}
	}
}
