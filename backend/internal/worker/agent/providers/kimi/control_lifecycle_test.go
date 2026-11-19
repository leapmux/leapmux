package kimi

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func publishNativeKimiApproval(t *testing.T, rig *kimiOutputRig, agentID, interactionID string) {
	t.Helper()
	raw := fmt.Sprintf(`{"type":"interaction","session_id":"session_1","agent_id":%q,"interaction_id":%q,"tool_call_id":"call","status":"pending","kind":"approval","request":{"tool_name":"Bash","action":"Run the command","tool_input_display":{"kind":"command"}}}`, agentID, interactionID)
	rig.agent.HandleOutput([]byte(raw))
}

func TestKimiNativeResolutionPreservesOtherControlIdentities(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, agent, kind, status, outcome, tool string }{
		{"another agent", "agent-0", "approval", "ended", "approved", "call"},
		{"another kind", "main", "question", "ended", "answered", "call"},
		{"another tool", "main", "approval", "ended", "approved", "other"},
		{"pending state", "main", "approval", "pending", "", "call"},
		{"unknown state", "main", "approval", "unknown", "", "call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			publishNativeKimiApproval(t, rig, "main", "approval_1")
			raw := fmt.Sprintf(`{"type":"interaction","session_id":"session_1","agent_id":%q,"interaction_id":"approval_1","kind":%q,"tool_call_id":%q,"status":%q,"outcome":%q}`, tc.agent, tc.kind, tc.tool, tc.status, tc.outcome)
			rig.agent.HandleOutput([]byte(raw))
			assert.Empty(t, rig.sink.CanceledControls())
		})
	}
}
