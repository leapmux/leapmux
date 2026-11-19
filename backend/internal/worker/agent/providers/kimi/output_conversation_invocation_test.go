package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationSwarmGroupsNativeItemReferences(t *testing.T) {
	t.Parallel()
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot=%t", snapshot), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			tool := json.RawMessage(`{"type":"tool_call","item_id":"swarm-call","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"AgentSwarm","stage":"executing","input":{"description":"Review both files","prompt_template":"Review {{item}}","items":["a.go","b.go"]},"agent_refs":[{"agent_id":"agent-0","role":"member","swarm_index":0},{"agent_id":"agent-1","role":"member","swarm_index":1}]}`)
			if snapshot {
				frame, err := json.Marshal(map[string]any{"type": "turns", "has_more": false, "turns": []any{map[string]any{
					"turn_id": "native-turn", "ordinal": 0, "kind": "agent", "status": "running", "started_at": "2026-11-18T00:00:00Z", "steps": []any{}, "items": []json.RawMessage{tool},
				}}})
				require.NoError(t, err)
				rig.conversationFrame(t, string(frame))
			} else {
				rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"native-turn","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
				rig.conversationFrame(t, `{"type":"item.started","turn_id":"native-turn","item":`+string(tool)+`}`)
			}
			var outcomes []bgtask.Item
			for _, row := range rig.sink.BackgroundTasks() {
				if row.Role == bgtask.RoleTaskOutcome {
					outcomes = append(outcomes, row)
				}
			}
			require.Len(t, outcomes, 2)
			assert.NotEmpty(t, outcomes[0].GroupKey)
			assert.Equal(t, outcomes[0].GroupKey, outcomes[1].GroupKey)
			assert.NotEqual(t, outcomes[0].ChildAgentID, outcomes[1].ChildAgentID)
			for _, row := range outcomes {
				assert.Equal(t, bgtask.KindWorkflow, row.Kind)
				assert.Equal(t, "Review both files", row.GroupLabel)
				assert.Equal(t, bgtask.StatusRunning, row.Status)
				assert.NotEmpty(t, row.ChildAgentID)
			}
			rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"native-turn","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			for _, previous := range outcomes {
				row, exists := rig.sink.BackgroundTask(previous.RowKey)
				require.True(t, exists)
				assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status, "the parent turn alone does not prove a child outcome")
			}
		})
	}
}

func TestKimiConversationInterruptedAgentResultRetainsStoppedOutcome(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"parent","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"parent","item":{"type":"tool_call","item_id":"spawn","status":"ended","outcome":"interrupted","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Agent","input":{"description":"Review it","prompt":"Review it."},"error":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: failed\nstop_reason: cancelled\n\nsubagent error: Cancelled.","agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	var outcomes []bgtask.Item
	for _, row := range rig.sink.BackgroundTasks() {
		if row.Role == bgtask.RoleTaskOutcome {
			outcomes = append(outcomes, row)
		}
	}
	require.Len(t, outcomes, 1)
	assert.Equal(t, bgtask.StatusStopped, outcomes[0].Status)
	child, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	activity, exists := rig.sink.BackgroundTask(child.rowKey)
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, activity.Status, "the parent result cannot supply the child's activity outcome")
}

func TestKimiConversationInvalidResumeCannotMutateAcceptedInvocation(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"parent","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"parent","item":{"type":"tool_call","item_id":"spawn","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Agent","stage":"executing","input":{"description":"Accepted assignment","prompt":"Review it."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	rig.conversationFrame(t, `{"type":"item.updated","turn_id":"parent","item_id":"spawn","set":{"input":{"description":"Contradictory assignment","resume":"agent-1"}}}`)
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"parent","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	var outcomes []bgtask.Item
	for _, row := range rig.sink.BackgroundTasks() {
		if row.Role == bgtask.RoleTaskOutcome {
			outcomes = append(outcomes, row)
		}
	}
	require.Len(t, outcomes, 1)
	assert.Equal(t, "Accepted assignment", outcomes[0].Title)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, outcomes[0].Status)
	assert.Len(t, rig.sink.ChildAgentIDs(), 1)
}

func seedKimiConversationChildInvocation(t *testing.T, rig *kimiTestRig) {
	t.Helper()
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`))
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Agent","stage":"executing","input":{"description":"Old assignment","prompt":"Review it."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`))
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Old assignment"}`))
	run := rig.agent.run("agent-0")
	waitFor(t, func() bool { return run.conversationRecovery.Load() == nil }, "the exact child initial page is complete before reconnect")
}

func waitForKimiConversationInvocationRecovery(t *testing.T, rig *kimiTestRig) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		rig.agent.dispatchMu.Lock()
		defer rig.agent.dispatchMu.Unlock()
		rig.agent.Mu.Lock()
		defer rig.agent.Mu.Unlock()
		t.Logf("invocation recovery: %+v; output pending: %v", rig.agent.invocationRecovery, agent.OutputQueueFor(rig.agent.sink).HasPending())
		for id, run := range rig.agent.runs {
			t.Logf("native run %s: conversation recovery=%+v", id, run.conversationRecovery.Load())
		}
	})
	waitFor(t, func() bool {
		rig.agent.dispatchMu.Lock()
		defer rig.agent.dispatchMu.Unlock()
		return rig.agent.invocationRecovery == nil && !agent.OutputQueueFor(rig.agent.sink).HasPending()
	}, "complete conversation pages release the invocation fence and retain queued task outcomes")
}

func seedPublicKimiChildInvocation(t *testing.T, native *kimiOutputRig) {
	t.Helper()
	native.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	native.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Old assignment","prompt":"Review it."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Old assignment"}`))
}
