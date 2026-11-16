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
			rig := newKimiOutputRig(t)
			parent := `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"swarm","toolCallId":"call_swarm","name":"AgentSwarm","state":"running","input":{"description":"Audit the packages"},"agentRefs":[{"agentId":"agent-1","role":"member"}]}}`
			task := []byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"swarm-member","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-1","parent_tool_call_id":"call_swarm","swarm_index":1,"description":"Audit #1"}]}`)
			if taskFirst {
				rig.agent.HandleOutput(task)
			}
			rig.transcriptOps(t, 1, parent)
			rig.agent.HandleOutput(task)
			rig.transcriptOps(t, 2, parent)
			marker, exists := rig.sink.BackgroundTask("session_1/agent-1")
			require.True(t, exists)
			assert.Equal(t, bgtask.RoleChildActivity, marker.Role)
			assert.False(t, marker.Status.IsWorking())
			row := nativeTaskOf(t, rig.sink, "swarm-member")
			assert.Equal(t, "Audit #1", row.Title)
			assert.Equal(t, bgtask.KindWorkflow, row.Kind)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.Empty(t, row.GroupKey, "the bare parent call ID cannot identify a complete invocation")
			identity := kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "swarm", toolCallID: "call_swarm"}
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
			assert.Equal(t, `session_1/kimi/"main"/"t0"/"t0.1"/"swarm"#agent-1`, span)
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
				native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
				if priorCall {
					native.transcriptOps(t, 1, fmt.Sprintf(`{"op":"frame.upsert","turnId":"previous","stepId":"previous.1","frame":{"kind":"tool","frameId":"previous.spawn","toolCallId":"spawn","name":%q,"state":"error","input":{},"output":"no child was started"}}`, test.tool))
				}
				rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Reviewer"%s}]}`, test.member)))
				row, exists := rig.sink.BackgroundTask("session_1/agent-0")
				require.True(t, exists)
				require.NotEmpty(t, row.ChildAgentID)
				firstChildID := row.ChildAgentID
				span, err := rig.sink.ChildSpawnSpan(firstChildID)
				require.NoError(t, err)
				assert.Empty(t, span, "the task supplies no native turn, step, or frame identity")
				require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
				before := len(rig.sink.Messages())
				native.transcriptOps(t, 2, fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":%q,"state":"running","input":{},"agentRefs":[{"agentId":"agent-0","role":%q}]}}`, test.tool, test.role))
				messages := rig.sink.Messages()
				require.Len(t, messages, before+1)
				want := messages[len(messages)-1].SpanID
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
