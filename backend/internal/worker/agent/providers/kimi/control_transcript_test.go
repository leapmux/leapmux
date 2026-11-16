package kimi

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func publishNativeKimiApproval(t *testing.T, rig *kimiOutputRig, agentID, interactionID string) {
	t.Helper()
	raw := fmt.Sprintf(`{"type":"interaction","session_id":"session_1","agent_id":%q,"interaction_id":%q,"tool_call_id":"call","status":"pending","kind":"approval","request":{"tool_name":"Bash","action":"Run the command","tool_input_display":{"kind":"command"}}}`, agentID, interactionID)
	rig.agent.HandleOutput([]byte(raw))
}

func TestKimiTranscriptResolutionRetiresItsLiveControl(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID("session_1")
	publishNativeKimiApproval(t, rig, "main", "approval_1")
	require.Len(t, rig.sink.PublishedControls(), 1)
	rig.transcriptOps(t, 1, `{"op":"interaction.upsert","interaction":{"interactionId":"approval_1","interactionKind":"approval","toolCallId":"call","state":"approved","request":{"toolName":"Bash","action":"Run the command"},"response":{"decision":"approved"}}}`)
	assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
}

func TestKimiTranscriptResetRetiresOnlyItsOwnAgentControls(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID("session_1")
	publishNativeKimiApproval(t, rig, "main", "main_approval")
	publishNativeKimiApproval(t, rig, "agent-0", "child_approval")
	require.Len(t, rig.sink.PublishedControls(), 2)
	rig.transcriptReset(t, 1)
	assert.Equal(t, []string{"main_approval"}, rig.sink.CanceledControls())
	rig.agent.Mu.Lock()
	assert.Contains(t, rig.agent.controls, "child_approval")
	rig.agent.Mu.Unlock()
}

func TestKimiTranscriptResolutionRetiresAnUnlinkedChildControl(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.sink.UpdateSessionID("session_1")
	publishNativeKimiApproval(t, rig, "agent-0", "child_approval")
	require.Len(t, rig.sink.PublishedControls(), 1)
	rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"interaction.upsert","interaction":{"interactionId":"child_approval","interactionKind":"approval","toolCallId":"call","state":"cancelled"}}`)
	assert.Equal(t, []string{"child_approval"}, rig.sink.CanceledControls())
}

func TestKimiTranscriptResolutionPreservesOtherControlIdentities(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, agent, kind, state, tool string }{
		{"another agent", "agent-0", "approval", "approved", "call"},
		{"another kind", "main", "question", "answered", "call"},
		{"another tool", "main", "approval", "approved", "other"},
		{"pending state", "main", "approval", "pending", "call"},
		{"unknown state", "main", "approval", "unknown", "call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			publishNativeKimiApproval(t, rig, "main", "approval_1")
			op := fmt.Sprintf(`{"op":"interaction.upsert","interaction":{"interactionId":"approval_1","interactionKind":%q,"toolCallId":%q,"state":%q}}`, tc.kind, tc.tool, tc.state)
			rig.transcriptAgentOps(t, tc.agent, 1, op)
			assert.Empty(t, rig.sink.CanceledControls())
		})
	}
}

func TestKimiTranscriptControlSnapshotRequiresACompleteValidList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, entries string
		retired       bool
	}{
		{"absent", "", false},
		{"null", "null", false},
		{"empty", "[]", true},
		{"pending", `[{"interactionId":"approval_1","interactionKind":"approval","toolCallId":"call","state":"pending"}]`, false},
		{"resolved", `[{"interactionId":"approval_1","interactionKind":"approval","toolCallId":"call","state":"rejected"}]`, true},
		{"wrong tool", `[{"interactionId":"approval_1","interactionKind":"approval","toolCallId":"other","state":"rejected"}]`, false},
		{"invalid entry", `[{}]`, false},
		{"duplicate identity", `[{"interactionId":"approval_1","interactionKind":"approval","state":"pending"},{"interactionId":"approval_1","interactionKind":"approval","state":"approved"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			publishNativeKimiApproval(t, rig, "main", "approval_1")
			extra := ""
			if tc.entries != "" {
				extra = `,"interactions":` + tc.entries
			}
			raw := fmt.Sprintf(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":1,"snapshot":{"items":[]%s}}}`, extra)
			rig.agent.HandleOutput([]byte(raw))
			if tc.retired {
				assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
			} else {
				assert.Empty(t, rig.sink.CanceledControls())
			}
		})
	}
}

func TestKimiEmbeddedResetValidatesBeforeItWithdrawsControls(t *testing.T) {
	t.Parallel()
	for _, items := range []string{
		`[{"kind":"turn","turnId":"bad","ordinal":1,"state":"invalid","steps":[]}]`,
		`[{"kind":"turn","turnId":"bad","ordinal":1.5,"state":"running","steps":[]}]`,
		`[{"kind":"turn","turnId":"bad","state":"running","steps":[]},{"kind":"turn","turnId":"bad","state":"running","steps":[]}]`,
	} {
		t.Run(items, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			publishNativeKimiApproval(t, rig, "main", "approval_1")
			rig.transcriptOps(t, 1, `{"op":"reset","agentId":"main","snapshot":{"items":`+items+`,"interactions":[]}}`)
			assert.Empty(t, rig.sink.CanceledControls())
		})
	}
}
