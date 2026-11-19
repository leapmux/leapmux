package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiControlReplayRetiresOnlyVerifiedOldInstances(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name     string
		withdraw bool
	}{
		{"absent request", true},
		{"matching pending request", false},
		{"foreign session", true},
		{"foreign agent", true},
		{"different kind", true},
		{"different tool", true},
		{"new instance", false},
		{"new request", true},
		{"answered request", false},
		{"refused confirmation", false},
		{"replaced recovery", false},
		{"replaced session", false},
		{"empty captured set", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			pending := approvalEvent("approval", map[string]any{"kind": "command", "command": "ls"})
			pending["tool_call_id"] = "call-1"
			if scenario.name != "empty captured set" {
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), pending))
			}
			rig.agent.dispatchMu.Lock()
			rig.agent.captureControlReplay(rig.sessionID())
			replay := rig.agent.controlReplay
			rig.agent.finishDispatch()
			require.NotNil(t, replay)
			accepted := true
			switch scenario.name {
			case "matching pending request":
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), pending))
			case "foreign session":
				rig.agent.HandleOutput(kimiEventFrame(t, "other-session", pending))
			case "foreign agent":
				pending["agent_id"] = "agent-9"
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), pending))
			case "different kind":
				question := questionEvent("approval")
				question["tool_call_id"] = "call-1"
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), question))
			case "different tool":
				pending["tool_call_id"] = "other-call"
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), pending))
			case "new instance":
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), map[string]any{
					"type": "interaction", "interaction_id": "approval", "kind": "approval", "agent_id": "main", "tool_call_id": "call-1", "status": "ended", "outcome": "cancelled",
				}))
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), pending))
			case "new request":
				rig.agent.HandleOutput(kimiEventFrame(t, rig.sessionID(), approvalEvent("new-request", map[string]any{"kind": "command", "command": "pwd"})))
			case "answered request":
				rig.agent.Mu.Lock()
				rig.agent.controls["approval"].answered = true
				rig.agent.Mu.Unlock()
			case "refused confirmation":
				accepted = false
			case "replaced recovery":
				rig.agent.dispatchMu.Lock()
				rig.agent.captureControlReplay(rig.sessionID())
				rig.agent.finishDispatch()
			case "replaced session":
				rig.agent.Mu.Lock()
				rig.agent.sessionID = "new-session"
				rig.agent.Mu.Unlock()
			}
			rig.agent.Mu.Lock()
			current := rig.agent.controls["approval"]
			rig.agent.Mu.Unlock()
			if scenario.name == "new instance" {
				assert.NotSame(t, replay.pending["approval"], current)
			}
			before := len(rig.sink.CanceledControls())
			rig.agent.finishControlReplay(replay, accepted)
			delta := len(rig.sink.CanceledControls()) - before
			if scenario.withdraw {
				assert.Equal(t, 1, delta)
				rig.agent.Mu.Lock()
				_, exists := rig.agent.controls["approval"]
				rig.agent.Mu.Unlock()
				assert.False(t, exists)
			} else {
				assert.Zero(t, delta)
				if scenario.name != "answered request" && scenario.name != "empty captured set" {
					rig.agent.Mu.Lock()
					assert.Same(t, current, rig.agent.controls["approval"])
					rig.agent.Mu.Unlock()
				}
			}
			if scenario.name == "new request" {
				rig.agent.Mu.Lock()
				assert.NotNil(t, rig.agent.controls["new-request"], "the initial capture grants no authority over a later request")
				rig.agent.Mu.Unlock()
			}
			after := len(rig.sink.CanceledControls())
			rig.agent.finishControlReplay(replay, accepted)
			assert.Len(t, rig.sink.CanceledControls(), after, "the same barrier cannot withdraw twice")
		})
	}
}

func TestKimiOnlyNativeInteractionsCanWithdrawALiveControl(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, oldFrame string
	}{
		{"obsolete interaction operation", `{"type":"transcript.ops","session_id":"session_1","payload":{"agent_id":"main","seq":1,"ops":[{"op":"interaction.upsert","interaction":{"interactionId":"approval_1","interactionKind":"approval","toolCallId":"call","state":"approved"}}]}}`},
		{"obsolete transcript reset", `{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"main","seq":1,"snapshot":{"items":[],"interactions":[]}}}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			publishNativeKimiApproval(t, rig, "main", "approval_1")
			require.Len(t, rig.sink.PublishedControls(), 1)
			rig.agent.HandleOutput([]byte(scenario.oldFrame))
			assert.Empty(t, rig.sink.CanceledControls(), "a retired transcript frame has no authority over a current native request")
			rig.agent.HandleOutput(kimiConversationTestPage(t, rig.sessionID(), "main"))
			assert.Empty(t, rig.sink.CanceledControls(), "conversation pages do not list pending interactions")
			rig.agent.HandleOutput([]byte(`{"type":"interaction","session_id":"session_1","agent_id":"main","interaction_id":"approval_1","tool_call_id":"call","kind":"approval","status":"ended","outcome":"cancelled"}`))
			assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
		})
	}
}
