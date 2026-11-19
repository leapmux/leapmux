package kimi

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeSwarmMetadataPreservesMemberAndGroupLabels(t *testing.T) {
	t.Parallel()
	for _, taskFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("task_first=%t", taskFirst), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
			parent := `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"call_swarm","step_index":1,"name":"AgentSwarm","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Audit the packages"},"agent_refs":[{"agent_id":"agent-1","role":"member","swarm_index":1}]}}`
			task := []byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-1","parent_tool_call_id":"call_swarm","swarm_index":1,"description":"Audit #1"}]}`)
			if taskFirst {
				rig.agent.HandleOutput(task)
			}
			rig.conversationFrame(t, parent)
			rig.agent.HandleOutput(task)
			rig.conversationFrame(t, parent)
			marker, exists := rig.sink.BackgroundTask("session_1/agent-1")
			require.True(t, exists)
			assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
			assert.False(t, marker.Status.IsWorking())
			row := nativeTaskOf(t, rig.sink, "swarm-member")
			assert.Equal(t, "Audit #1", row.Title)
			assert.Equal(t, bgtask.KindWorkflow, row.Kind)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.Empty(t, row.GroupKey, "the bare parent call ID cannot identify a complete invocation")
			identity := kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", toolCallID: "call_swarm"}
			invocation := rig.agent.run("main").childInvocations[identity]
			require.NotNil(t, invocation)
			outcome, exists := rig.sink.BackgroundTask(kimiInvocationOutcomeKey(invocation, "agent-1"))
			require.True(t, exists)
			assert.Equal(t, bgtask.RoleTaskOutcome, outcome.Role)
			assert.Equal(t, bgtask.KindWorkflow, outcome.Kind)
			assert.Equal(t, "Audit the packages", outcome.GroupLabel)
			assert.Equal(t, kimiInvocationGroupKey(identity), outcome.GroupKey)
			assert.Equal(t, marker.ChildAgentID, row.ChildAgentID)
			assert.Equal(t, marker.ChildAgentID, outcome.ChildAgentID)
			assert.Len(t, rig.sink.BackgroundTasks(), 3)
			span, err := rig.sink.ChildSpawnSpan(row.ChildAgentID)
			require.NoError(t, err)
			rows := rig.sink.Messages()
			require.Len(t, rows, 1)
			require.NotEmpty(t, rows[0].SpanID)
			assert.Equal(t, rows[0].SpanID+"#agent-1", span)
		})
	}
}

func TestKimiTaskFirstChildAdoptionAttachesTheCanonicalParentSpan(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ tool, role, member string }{
		{"Agent", "child", ""},
		{"AgentSwarm", "member", `,"swarm_index":1`},
	} {
		for _, priorCall := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prior_call=%t", test.tool, priorCall), func(t *testing.T) {
				t.Parallel()
				rig := newKimiTestRig(t, agent.Options{})
				feed := func(raw string) { rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "main", raw)) }
				if priorCall {
					feed(`{"type":"turn.started","turn":{"turn_id":"previous","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
					feed(fmt.Sprintf(`{"type":"item.ended","turn_id":"previous","item":{"type":"tool_call","item_id":"spawn","name":%q,"status":"ended","outcome":"failed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{},"error":"No child was started."}}`, test.tool))
					feed(`{"type":"turn.ended","turn":{"turn_id":"previous","ordinal":0,"kind":"agent","status":"ended","outcome":"failed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
				}
				rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Reviewer"%s}]}`, test.member)))
				awaitKimiTestOutput(t, rig.agent.sink)
				row, exists := rig.sink.BackgroundTask("session_1/agent-0")
				require.True(t, exists)
				require.NotEmpty(t, row.ChildAgentID)
				firstChildID := row.ChildAgentID
				span, err := rig.sink.ChildSpawnSpan(firstChildID)
				require.NoError(t, err)
				assert.Empty(t, span, "the bare task call ID does not identify its native parent turn")
				require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
				before := len(rig.sink.Messages())
				feed(`{"type":"turn.started","turn":{"turn_id":"current","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
				feed(fmt.Sprintf(`{"type":"item.started","turn_id":"current","item":{"type":"tool_call","item_id":"spawn","name":%q,"status":"running","stage":"executing","created_at":"2026-11-18T00:00:01Z","started_at":"2026-11-18T00:00:01Z","input":{},"agent_refs":[{"agent_id":"agent-0","role":%q}]}}`, test.tool, test.role))
				awaitKimiTestOutput(t, rig.agent.sink)
				messages := rig.sink.Messages()
				require.Len(t, messages, before+1)
				want := messages[len(messages)-1].SpanID
				require.NotEmpty(t, want)
				if test.role == "member" {
					want += "#agent-0"
				}
				span, err = rig.sink.ChildSpawnSpan(firstChildID)
				require.NoError(t, err)
				assert.Equal(t, want, span)
				row, exists = rig.sink.BackgroundTask("session_1/agent-0")
				require.True(t, exists)
				assert.Equal(t, firstChildID, row.ChildAgentID)
				_, linked := rig.agent.children.get("agent-0")
				require.True(t, linked)
				assert.False(t, row.Status.IsWorking(), "attaching a parent span does not invent a running child turn")
				task := nativeTaskOf(t, rig.sink, "task-child")
				assert.Equal(t, bgtask.StatusRunning, task.Status)
				assert.Equal(t, firstChildID, task.ChildAgentID)
			})
		}
	}
}
