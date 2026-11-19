package kimi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeAgentResultRetainsActualProfile(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Reviewer","prompt":"Review the diff.","subagent_type":"coder"},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nAll good.","agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	row, exists := rig.sink.BackgroundTask("session_1/agent-0")
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleChildActivity, row.Role)
	assert.Equal(t, "explore", row.Description, "the native result supplies the actual profile; requested options cannot prove it")
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status, "a parent tool result does not invent native child activity")
}
