package claude

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestTurnSignalWhitelist_Claude(t *testing.T) {
	t.Parallel()

	// Claude Code's `system` type carries four unrelated families under 49
	// subtypes, and only the thinking-token telemetry reports the root's own
	// work. session_state_changed is the CLI's own turn state and outranks all
	// of it (see observeTurnFromOutput).
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		// The named signals.
		{Name: "session state running", Line: `{"type":"system","subtype":"session_state_changed","state":"running"}`, Moves: true},
		{Name: "session state requires_action", Line: `{"type":"system","subtype":"session_state_changed","state":"requires_action"}`, Moves: true},
		{Name: "session state idle", Line: `{"type":"system","subtype":"session_state_changed","state":"idle"}`, Moves: true},
		{Name: "thinking tokens", Line: `{"type":"system","subtype":"thinking_tokens","estimated_tokens":120}`, Moves: true},
		{Name: "root assistant text", Line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`, Moves: true},
		{Name: "root user echo", Line: `{"type":"user","message":{"role":"user","content":"hi"}}`, Moves: true},
		{Name: "result", Line: `{"type":"result","subtype":"success"}`, Moves: true},

		// A session, not a turn.
		{Name: "init", Line: `{"type":"system","subtype":"init","session_id":"s-1"}`},
		{Name: "session state unknown", Line: `{"type":"system","subtype":"session_state_changed","state":"quiescing"}`},

		// Hooks. These fire on SessionStart, SessionEnd, ConfigChange,
		// FileChanged and TeammateIdle, none of which is a turn.
		{Name: "hook started", Line: `{"type":"system","subtype":"hook_started","hook_event":"SessionStart","hook_id":"h1"}`},
		{Name: "hook progress", Line: `{"type":"system","subtype":"hook_progress","hook_event":"SessionStart","hook_id":"h1"}`},
		{Name: "hook response", Line: `{"type":"system","subtype":"hook_response","hook_event":"SessionStart","hook_id":"h1","exit_code":0}`},

		// A background task outlives the turn that spawned it.
		{Name: "task started", Line: `{"type":"system","subtype":"task_started","task_id":"t-1","task_type":"local_bash","description":"npm test"}`},
		{Name: "task progress", Line: `{"type":"system","subtype":"task_progress","task_id":"t-1","description":"npm test"}`},
		{Name: "task notification", Line: `{"type":"system","subtype":"task_notification","task_id":"t-1","status":"completed"}`},
		{Name: "task updated", Line: `{"type":"system","subtype":"task_updated","task_id":"t-1","patch":{"status":"completed"}}`},
		{Name: "background tasks changed", Line: `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`},

		// Notifications: an event, not a live turn.
		{Name: "status compacting", Line: `{"type":"system","subtype":"status","status":"compacting"}`},
		{Name: "status clear", Line: `{"type":"system","subtype":"status","status":""}`},
		{Name: "permission mode status", Line: `{"type":"system","subtype":"status","status":null,"permissionMode":"plan"}`},
		{Name: "api retry", Line: `{"type":"system","subtype":"api_retry","attempt":2}`},
		{Name: "compact boundary", Line: `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto"}}`},
		{Name: "microcompact boundary", Line: `{"type":"system","subtype":"microcompact_boundary"}`},

		// Emitted after the turn its `result` already ended.
		{Name: "turn duration", Line: `{"type":"system","subtype":"turn_duration","durationMs":1200}`},
		{Name: "post turn summary", Line: `{"type":"system","subtype":"post_turn_summary","status_category":"done"}`},
		{Name: "files persisted", Line: `{"type":"system","subtype":"files_persisted","files":[]}`},

		// Bookkeeping the CLI reports whenever it changes.
		{Name: "commands changed", Line: `{"type":"system","subtype":"commands_changed"}`},
		{Name: "vcs state changed", Line: `{"type":"system","subtype":"vcs_state_changed"}`},
		{Name: "elicitation complete", Line: `{"type":"system","subtype":"elicitation_complete","elicitation_id":"e1"}`},
		{Name: "scheduled task fire", Line: `{"type":"system","subtype":"scheduled_task_fire"}`},
		{Name: "memory saved", Line: `{"type":"system","subtype":"memory_saved"}`},

		// This Worker's own traffic.
		{Name: "control request", Line: `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool"}}`},
		{Name: "control response", Line: `{"type":"control_response","response":{"request_id":"r1","subtype":"success"}}`},
		{Name: "tool progress", Line: `{"type":"tool_progress","parent_tool_use_id":"t1","heartbeat":true}`},

		// A child's frame says nothing about the root. The child's own session
		// state is the sharpest case: it reports a turn in the same words the
		// root uses, and the root may be idle while a restarted child runs on.
		{Name: "subagent assistant text", Line: `{"type":"assistant","parent_tool_use_id":"p1","message":{"role":"assistant","content":[{"type":"text","text":"child"}]}}`},
		{Name: "subagent result", Line: `{"type":"result","parent_tool_use_id":"p1","subtype":"success"}`},
		{Name: "subagent session state running", Line: `{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"running"}`},
		{Name: "subagent session state idle", Line: `{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"idle"}`},

		// The releases this build will not see.
		{Name: "a subtype from a later release", Line: `{"type":"system","subtype":"some_future_event","data":1}`},
		{Name: "a type from a later release", Line: `{"type":"some_future_frame","data":1}`},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		sink := &agenttest.Sink{}
		a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActives()
	})
}
