package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kimiFinalityIdentity() kimiInvocationIdentity {
	return kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", toolCallID: "spawn"}
}

type kimiFinalityTool struct {
	outcome, output, taskID string
	background              bool
}

func (tool kimiFinalityTool) item(t *testing.T) json.RawMessage {
	t.Helper()
	ref := map[string]any{"agent_id": "agent-0", "role": "child"}
	if tool.taskID != "" {
		ref["task_id"] = tool.taskID
	}
	item := map[string]any{"type": "tool_call", "item_id": "spawn", "name": "Agent", "status": "running",
		"created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:00Z", "step_index": 1,
		"input":      map[string]any{"description": "Review", "prompt": "Review it.", "run_in_background": tool.background},
		"agent_refs": []any{ref}}
	if tool.outcome == "" {
		item["stage"] = "executing"
	} else {
		item["status"], item["outcome"] = "ended", tool.outcome
		if tool.outcome == "completed" {
			item["output"] = tool.output
		} else {
			item["error"] = tool.output
		}
	}
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	return raw
}

func (tool kimiFinalityTool) publish(t *testing.T, rig *kimiOutputRig) {
	t.Helper()
	kind := "item.started"
	if tool.outcome != "" {
		kind = "item.ended"
	}
	raw, err := json.Marshal(map[string]any{"type": kind, "turn_id": "t0", "item": tool.item(t)})
	require.NoError(t, err)
	rig.conversationFrame(t, string(raw))
}

func newKimiFinalityRig(t *testing.T, background bool) *kimiOutputRig {
	t.Helper()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	(kimiFinalityTool{background: background}).publish(t, rig)
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"turn.started","turn":{"turn_id":"independent","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
	return rig
}

func finalityOutcome(t *testing.T, rig *kimiOutputRig) bgtask.Item {
	t.Helper()
	awaitKimiTestOutput(t, rig.agent.sink)
	row, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: kimiFinalityIdentity()}, "agent-0"))
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleTaskOutcome, row.Role)
	return row
}

func assertKimiFinalityChildStillRuns(t *testing.T, rig *kimiOutputRig) {
	t.Helper()
	awaitKimiTestOutput(t, rig.agent.sink)
	marker, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
	assert.Equal(t, bgtask.StatusRunning, marker.Status)
	assert.True(t, rig.agent.ActiveChildTurnState(marker.RowKey).Active)
}

func TestKimiUnmatchedInvocationFinishesWithoutInventingAChildOutcome(t *testing.T) {
	t.Parallel()
	for _, finish := range []string{"tool done", "tool error", "parent completed", "parent interrupted", "withdrawn snapshot", "removed item", "removed turn"} {
		t.Run(finish, func(t *testing.T) {
			t.Parallel()
			rig := newKimiFinalityRig(t, false)
			switch finish {
			case "tool done":
				(kimiFinalityTool{outcome: "completed", output: "No supported child result was retained."}).publish(t, rig)
			case "tool error":
				(kimiFinalityTool{outcome: "failed", output: "Tool execution cancelled."}).publish(t, rig)
			case "parent completed", "parent interrupted":
				outcome := "completed"
				if finish == "parent interrupted" {
					outcome = "interrupted"
				}
				rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"`+outcome+`","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			case "withdrawn snapshot":
				rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "main"))
			case "removed item":
				rig.conversationFrame(t, `{"type":"item.removed","turn_id":"t0","item_id":"spawn"}`)
			case "removed turn":
				rig.conversationFrame(t, `{"type":"turns.removed","turn_ids":["t0"]}`)
			}
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status)
			assertKimiFinalityChildStillRuns(t, rig)
		})
	}
}

func TestKimiUnmatchedUnknownOutcomeAcceptsLaterKnownNativeEvidence(t *testing.T) {
	t.Parallel()
	rig := newKimiFinalityRig(t, false)
	(kimiFinalityTool{outcome: "completed", output: "No supported child result was retained."}).publish(t, rig)
	require.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status)
	(kimiFinalityTool{outcome: "completed", output: "agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nA definitive completed result."}).publish(t, rig)
	assert.Equal(t, bgtask.StatusSucceeded, finalityOutcome(t, rig).Status)
	assertKimiFinalityChildStillRuns(t, rig)
}

const kimiFinalityBackgroundResult = "task_id: task-native\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: Review"

func TestKimiExactLocalNativeStateBindsInEitherArrivalOrder(t *testing.T) {
	t.Parallel()
	for _, taskFirst := range []bool{false, true} {
		for _, ended := range []bool{false, true} {
			for _, detached := range []bool{false, true} {
				t.Run(fmt.Sprintf("task_first=%t/ended=%t/detached=%t", taskFirst, ended, detached), func(t *testing.T) {
					t.Parallel()
					rig := newKimiFinalityRig(t, detached)
					status, outcome := "running", ""
					if ended {
						status, outcome = "ended", "completed"
					}
					frame := []byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":"subagent","status":%q,"outcome":%q,"detached":%t,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`, status, outcome, detached))
					if taskFirst {
						rig.agent.HandleOutput(frame)
					}
					(kimiFinalityTool{outcome: "completed", output: kimiFinalityBackgroundResult, background: detached, taskID: "task-native"}).publish(t, rig)
					if !taskFirst {
						rig.agent.HandleOutput(frame)
					}
					awaitKimiTestOutput(t, rig.agent.sink)
					native := nativeTaskOf(t, rig.sink, "task-native")
					expected := bgtask.StatusRunning
					if ended {
						expected = bgtask.StatusSucceeded
					}
					assert.Equal(t, expected, native.Status)
					_, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: kimiFinalityIdentity()}, "agent-0"))
					assert.False(t, exists, "an exact native state already observed needs no live watch to prove this alias")
					assertKimiFinalityChildStillRuns(t, rig)
				})
			}
		}
	}
}

func TestKimiContinuingBackgroundDescriptorSurvivesParentCompletion(t *testing.T) {
	t.Parallel()
	rig := newKimiFinalityRig(t, true)
	(kimiFinalityTool{outcome: "completed", output: kimiFinalityBackgroundResult, background: true, taskID: "task-native"}).publish(t, rig)
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	assert.Equal(t, bgtask.StatusRunning, finalityOutcome(t, rig).Status, "the native background reference describes continuing coordinator work")
	assertKimiFinalityChildStillRuns(t, rig)
}

func TestKimiUnknownProvisionalOutcomeCannotEndAnIdentifiedRunningTask(t *testing.T) {
	t.Parallel()
	rig := newKimiFinalityRig(t, true)
	(kimiFinalityTool{outcome: "completed", output: "An unrecognized provisional result.", background: true}).publish(t, rig)
	require.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	(kimiFinalityTool{outcome: "completed", output: kimiFinalityBackgroundResult, background: true, taskID: "task-native"}).publish(t, rig)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "task-native").Status, "uncertainty about a parent result cannot close its now identified continuing task")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, rig.sink, "task-native").Status)
	_, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: kimiFinalityIdentity()}, "agent-0"))
	assert.False(t, exists, "a definitive native outcome can safely collapse the uncertain provisional alias")
	assertKimiFinalityChildStillRuns(t, rig)
}

func TestKimiLocalBindingRejectsContradictoryNativeMetadata(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct{ name, kind, child, parent string }{
		{"another child", "subagent", "agent-1", "spawn"},
		{"another parent call", "subagent", "agent-0", "another-call"},
		{"another task kind", "shell", "agent-0", "spawn"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiFinalityRig(t, true)
			raw := fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":%q,"status":"running","detached":true,"child_agent_id":%q,"parent_tool_call_id":%q,"command":"printf native"}`, scenario.kind, scenario.child, scenario.parent)
			rig.agent.HandleOutput([]byte(raw))
			(kimiFinalityTool{outcome: "completed", output: kimiFinalityBackgroundResult, background: true, taskID: "task-native"}).publish(t, rig)
			rig.agent.Mu.Lock()
			task := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-native"}]
			var bound *kimiChildInvocation
			if task != nil {
				bound = task.boundInvocation
			}
			rig.agent.Mu.Unlock()
			assert.Nil(t, bound)
			assert.Equal(t, bgtask.StatusRunning, finalityOutcome(t, rig).Status)
			assertKimiFinalityChildStillRuns(t, rig)
		})
	}
}

func TestKimiRecoveryBarrierPublishesWithdrawnUnmatchedOutcome(t *testing.T) {
	t.Parallel()
	connected := newKimiTestRig(t, agent.Options{})
	rig := &kimiOutputRig{agent: connected.agent, sink: connected.sink}
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	const childTurn = `{"turn_id":"independent","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}`
	(kimiFinalityTool{}).publish(t, rig)
	child := rig.agent.run("agent-0")
	waitFor(t, func() bool { return child.conversationRecovery.Load() == nil }, "the child receives its initial native page")
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"turn.started","turn":`+childTurn+`}`))
	applied := rig.agent.beginInvocationRecovery(t.Context(), rig.sessionID())
	require.NotNil(t, applied)
	assert.Equal(t, bgtask.StatusRunning, finalityOutcome(t, rig).Status)
	applied(true)
	rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "main"))
	assert.Equal(t, bgtask.StatusRunning, finalityOutcome(t, rig).Status, "the child snapshot still holds the recovery fence")
	rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "agent-0", json.RawMessage(`{
		"turn_id":"independent","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[]
	}`)))
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status, "complete native pages must also publish withdrawn outcomes")
	assertKimiFinalityChildStillRuns(t, rig)
}

func TestKimiCompletePageKeepsAnInvocationThatStillExists(t *testing.T) {
	t.Parallel()
	rig := newKimiFinalityRig(t, false)
	item := (kimiFinalityTool{}).item(t)
	turn := json.RawMessage(`{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[` + string(item) + `]}`)
	rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "main", turn))
	assert.Equal(t, bgtask.StatusRunning, finalityOutcome(t, rig).Status)
	assertKimiFinalityChildStillRuns(t, rig)
}

func TestKimiRecoveryRetainsNativeOutcomeWhenCreatorClaimsConflict(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	enteredOnce := sync.OnceFunc(func() { close(entered) })
	rig.fake.mu.Lock()
	rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
		enteredOnce()
		<-released
		return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": true, "child_agent_id": "agent-0", "result_summary": "The task definitely completed."}
	}
	rig.fake.mu.Unlock()
	native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	tool := kimiFinalityTool{outcome: "completed", output: kimiFinalityBackgroundResult, background: true, taskID: "task-native"}
	tool.publish(t, native)
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native watch did not reach the controlled response")
	}
	applied := rig.agent.beginInvocationRecovery(context.Background(), rig.sessionID())
	require.NotNil(t, applied)
	applied(true)
	var first, second map[string]any
	require.NoError(t, json.Unmarshal(tool.item(t), &first))
	require.NoError(t, json.Unmarshal(tool.item(t), &second))
	second["item_id"] = "other-call"
	turn := map[string]any{"turn_id": "t0", "ordinal": 0, "kind": "agent", "status": "running", "started_at": "2026-11-18T00:00:00Z", "steps": []any{}, "items": []any{first, second}}
	raw, err := json.Marshal(turn)
	require.NoError(t, err)
	rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "main", raw))
	release()
	waitFor(t, rig.agent.taskRecoveryWork.Idle, "the validated watch state finishes behind the creator fence")
	rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "agent-0"))
	awaitKimiTestOutput(t, rig.agent.sink)
	outcome := nativeTaskOf(t, rig.sink, "task-native")
	assert.Equal(t, bgtask.StatusSucceeded, outcome.Status, "creator ambiguity cannot discard an exact native task outcome")
	rig.agent.Mu.Lock()
	task := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-native"}]
	var bound *kimiChildInvocation
	if task != nil {
		bound = task.boundInvocation
	}
	rig.agent.Mu.Unlock()
	assert.Nil(t, bound, "neither conflicting creator gains the task")
	for _, identity := range []kimiInvocationIdentity{kimiFinalityIdentity(), {sessionID: "session_1", agentID: "main", turnID: "t0", toolCallID: "other-call"}} {
		_, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: identity}, "agent-0"))
		assert.True(t, exists, "independent creator evidence cannot be removed by an ambiguous join")
	}
}
