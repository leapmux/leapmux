package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kimiFinalityIdentity() kimiTranscriptToolIdentity {
	return kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "spawn", toolCallID: "spawn"}
}

func kimiFinalityOperation(t *testing.T, state, output string, background bool) string {
	t.Helper()
	frame := map[string]any{"kind": "tool", "frameId": "spawn", "toolCallId": "spawn", "name": "Agent", "state": state,
		"input":     map[string]any{"description": "Review", "prompt": "Review it.", "run_in_background": background},
		"agentRefs": []any{map[string]any{"agentId": "agent-0", "role": "child"}}}
	if state != "running" {
		frame["output"] = output
	}
	raw, err := json.Marshal(map[string]any{"op": "frame.upsert", "turnId": "t0", "stepId": "t0.1", "frame": frame})
	require.NoError(t, err)
	return string(raw)
}

func newKimiFinalityRig(t *testing.T, background bool) *kimiOutputRig {
	t.Helper()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		kimiFinalityOperation(t, "running", "", background))
	rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"independent","state":"running","origin":{"kind":"user"}}}`)
	return rig
}

func finalityOutcome(t *testing.T, rig *kimiOutputRig) bgtask.Item {
	t.Helper()
	row, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: kimiFinalityIdentity()}, "agent-0"))
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleTaskOutcome, row.Role)
	return row
}

func assertKimiFinalityChildStillRuns(t *testing.T, rig *kimiOutputRig) {
	t.Helper()
	marker, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
	assert.Equal(t, bgtask.StatusRunning, marker.Status)
	assert.True(t, rig.agent.ActiveChildTurnState(marker.RowKey).Active)
}

func TestKimiUnmatchedInvocationFinishesWithoutInventingAChildOutcome(t *testing.T) {
	t.Parallel()
	for _, finish := range []string{"tool done", "tool error", "parent completed", "parent cancelled", "withdrawn snapshot"} {
		t.Run(finish, func(t *testing.T) {
			t.Parallel()
			rig := newKimiFinalityRig(t, false)
			switch finish {
			case "tool done":
				rig.transcriptOps(t, 2, kimiFinalityOperation(t, "done", "No supported child result was retained.", false))
			case "tool error":
				rig.transcriptOps(t, 2, kimiFinalityOperation(t, "error", "Tool execution cancelled.", false))
			case "parent completed":
				rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
			case "parent cancelled":
				rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"user"}}}`)
			case "withdrawn snapshot":
				rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "main", 2))
			}
			assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status)
			assertKimiFinalityChildStillRuns(t, rig)
		})
	}
}

func TestKimiUnmatchedUnknownOutcomeAcceptsLaterKnownNativeEvidence(t *testing.T) {
	t.Parallel()
	rig := newKimiFinalityRig(t, false)
	rig.transcriptOps(t, 2, kimiFinalityOperation(t, "done", "No supported child result was retained.", false))
	require.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status)
	rig.transcriptOps(t, 3, kimiFinalityOperation(t, "done", "agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nA definitive completed result.", false))
	assert.Equal(t, bgtask.StatusSucceeded, finalityOutcome(t, rig).Status)
	assertKimiFinalityChildStillRuns(t, rig)
}

const kimiFinalityBackgroundResult = "task_id: task-native\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: Review"

func TestKimiExactLocalNativeStateBindsInEitherArrivalOrder(t *testing.T) {
	t.Parallel()
	for _, taskFirst := range []bool{false, true} {
		for _, ended := range []bool{false, true} {
			t.Run(fmt.Sprintf("task_first=%t/ended=%t", taskFirst, ended), func(t *testing.T) {
				t.Parallel()
				rig := newKimiFinalityRig(t, true)
				status, outcome := "running", ""
				if ended {
					status, outcome = "ended", "completed"
				}
				frame := []byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":"subagent","status":%q,"outcome":%q,"detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`, status, outcome))
				if taskFirst {
					rig.agent.HandleOutput(frame)
				}
				rig.transcriptOps(t, 2, kimiFinalityOperation(t, "done", kimiFinalityBackgroundResult, true))
				if !taskFirst {
					rig.agent.HandleOutput(frame)
				}
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

func TestKimiContinuingBackgroundDescriptorSurvivesParentCompletion(t *testing.T) {
	t.Parallel()
	rig := newKimiFinalityRig(t, true)
	rig.transcriptOps(t, 2, kimiFinalityOperation(t, "done", kimiFinalityBackgroundResult, true),
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
	assert.Equal(t, bgtask.StatusRunning, finalityOutcome(t, rig).Status, "the native background result describes continuing coordinator work")
	assertKimiFinalityChildStillRuns(t, rig)
}

func TestKimiUnknownProvisionalOutcomeCannotEndAnIdentifiedRunningTask(t *testing.T) {
	t.Parallel()
	rig := newKimiFinalityRig(t, true)
	rig.transcriptOps(t, 2, kimiFinalityOperation(t, "done", "An unrecognized provisional result.", true))
	require.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	rig.transcriptOps(t, 3, kimiFinalityOperation(t, "done", kimiFinalityBackgroundResult, true))
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "task-native").Status, "uncertainty about a parent result cannot close its now identified continuing task")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, rig.sink, "task-native").Status)
	_, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: kimiFinalityIdentity()}, "agent-0"))
	assert.False(t, exists, "a definitive native outcome can safely collapse the uncertain provisional alias")
	assertKimiFinalityChildStillRuns(t, rig)
}

func TestKimiLocalBindingRejectsContradictoryNativeMetadata(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, kind, child, parent string
		detached                  bool
	}{
		{"foreground task", "subagent", "agent-0", "spawn", false},
		{"another child", "subagent", "agent-1", "spawn", true},
		{"another parent call", "subagent", "agent-0", "another-call", true},
		{"another task kind", "shell", "agent-0", "spawn", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiFinalityRig(t, true)
			raw := fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-native","kind":%q,"status":"running","detached":%t,"child_agent_id":%q,"parent_tool_call_id":%q,"command":"printf native"}`, test.kind, test.detached, test.child, test.parent)
			rig.agent.HandleOutput([]byte(raw))
			rig.transcriptOps(t, 2, kimiFinalityOperation(t, "done", kimiFinalityBackgroundResult, true))
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
	rig := newKimiFinalityRig(t, false)
	applied := rig.agent.beginInvocationRecovery(context.Background(), rig.sessionID())
	require.NotNil(t, applied)
	rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "main", 2))
	assert.Equal(t, bgtask.StatusRunning, finalityOutcome(t, rig).Status, "the incomplete recovery fence delays projection")
	applied(true)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, finalityOutcome(t, rig).Status, "the applied barrier must also publish withdrawn outcomes")
	assertKimiFinalityChildStillRuns(t, rig)
}

func TestKimiRecoveryRetainsNativeOutcomeWhenCreatorClaimsConflict(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.fake.mu.Lock()
	rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
		close(entered)
		<-released
		return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": true, "child_agent_id": "agent-0", "result_summary": "The task definitely completed."}
	}
	rig.fake.mu.Unlock()
	native.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		kimiFinalityOperation(t, "done", kimiFinalityBackgroundResult, true))
	ctx := testutil.DeadlineContext(t)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the native watch did not reach the controlled response")
	}
	applied := rig.agent.beginInvocationRecovery(context.Background(), rig.sessionID())
	require.NotNil(t, applied)
	defer applied(false)
	firstRaw := kimiFinalityOperation(t, "done", kimiFinalityBackgroundResult, true)
	secondRaw := strings.Replace(strings.Replace(firstRaw, `"frameId":"spawn"`, `"frameId":"other"`, 1), `"toolCallId":"spawn"`, `"toolCallId":"other-call"`, 1)
	var first, second struct {
		Frame map[string]any `json:"frame"`
	}
	require.NoError(t, json.Unmarshal([]byte(firstRaw), &first))
	require.NoError(t, json.Unmarshal([]byte(secondRaw), &second))
	turn := map[string]any{"kind": "turn", "turnId": "t0", "state": "running", "ordinal": 0, "origin": map[string]any{"kind": "user"}, "steps": []any{map[string]any{"kind": "step", "stepId": "t0.1", "ordinal": 1, "state": "running", "frames": []any{first.Frame, second.Frame}}}}
	rig.agent.HandleOutput(nativeKimiRecoveryReset(t, rig.sessionID(), "main", 2, turn))
	release()
	waitFor(t, rig.agent.taskRecoveryWork.Idle, "the validated watch state finishes behind the creator fence")
	applied(true)
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
	for _, identity := range []kimiTranscriptToolIdentity{kimiFinalityIdentity(), {sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "other", toolCallID: "other-call"}} {
		_, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(&kimiChildInvocation{identity: identity}, "agent-0"))
		assert.True(t, exists, "independent creator evidence cannot be removed by an ambiguous join")
	}
}
