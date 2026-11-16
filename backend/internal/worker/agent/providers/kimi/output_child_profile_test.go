package kimi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeAgentResultRetainsActualProfile(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"done","input":{"description":"Reviewer","prompt":"Review the diff.","subagent_type":"coder"},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nAll good.","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	row, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleChildActivity, row.Role)
	assert.Equal(t, "explore", row.Description, "the native result supplies the actual profile; requested options cannot prove it")
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status, "a parent tool result does not invent native child activity")
}
