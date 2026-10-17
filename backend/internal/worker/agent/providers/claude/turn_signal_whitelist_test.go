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
		{Name: "api metrics", Line: `{"type":"system","subtype":"api_metrics","models":[]}`},

		// Bookkeeping the CLI reports whenever it changes.
		{Name: "commands changed", Line: `{"type":"system","subtype":"commands_changed","commands":[{"name":"review"}]}`},
		{Name: "vcs state changed", Line: `{"type":"system","subtype":"vcs_state_changed"}`},
		{Name: "code change published", Line: `{"type":"system","subtype":"code_change_published","kind":"push","branch":"main"}`},
		{Name: "elicitation complete", Line: `{"type":"system","subtype":"elicitation_complete","elicitation_id":"e1"}`},
		{Name: "scheduled task fire", Line: `{"type":"system","subtype":"scheduled_task_fire"}`},
		{Name: "memory saved", Line: `{"type":"system","subtype":"memory_saved"}`},
		{Name: "dev intent", Line: `{"type":"system","subtype":"dev_intent"}`},
		{Name: "session title changed", Line: `{"type":"system","subtype":"session_title_changed","title":"T"}`},
		{Name: "per turn effort changed", Line: `{"type":"system","subtype":"per_turn_effort_changed","effortLevel":"high"}`},
		{Name: "feedback draft queued", Line: `{"type":"system","subtype":"feedback_draft_queued"}`},
		{Name: "peer message hold", Line: `{"type":"system","subtype":"peer_message_hold"}`},
		{Name: "local command", Line: `{"type":"system","subtype":"local_command","command":"/cost"}`},
		{Name: "local command output", Line: `{"type":"system","subtype":"local_command_output","output":"total"}`},
		{Name: "permission retry", Line: `{"type":"system","subtype":"permission_retry"}`},
		{Name: "stop hook summary", Line: `{"type":"system","subtype":"stop_hook_summary"}`},
		{Name: "away summary", Line: `{"type":"system","subtype":"away_summary"}`},

		// Live state and one-row notices: events, never a live turn.
		{Name: "permission check status", Line: `{"type":"system","subtype":"permission_check_status","tool_use_id":"t1","status":"checking"}`},
		{Name: "permission denied", Line: `{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"t1"}`},
		{Name: "notification", Line: `{"type":"system","subtype":"notification","key":"k","text":"hi","priority":"low"}`},
		{Name: "informational", Line: `{"type":"system","subtype":"informational","content":"hi","level":"warning"}`},
		{Name: "memory recall", Line: `{"type":"system","subtype":"memory_recall","mode":"select","memories":[]}`},
		{Name: "model fallback", Line: `{"type":"system","subtype":"model_fallback","trigger":"overloaded","fallback_model":"m2","content":"Switched to m2"}`},
		{Name: "model refusal fallback", Line: `{"type":"system","subtype":"model_refusal_fallback","scope":"session","fallback_model":"m2","content":"Switched to m2"}`},
		{Name: "model consent fallback", Line: `{"type":"system","subtype":"model_consent_fallback","fallback_model":"m2","content":"Switched to m2"}`},
		{Name: "model refusal no fallback", Line: `{"type":"system","subtype":"model_refusal_no_fallback"}`},
		{Name: "ui log", Line: `{"type":"system","subtype":"ui_log","plugin":"p","text":"t"}`},
		{Name: "ui toast", Line: `{"type":"system","subtype":"ui_toast","plugin":"p","text":"t"}`},
		{Name: "ui status", Line: `{"type":"system","subtype":"ui_status","plugin":"p"}`},
		{Name: "ui invalidate", Line: `{"type":"system","subtype":"ui_invalidate","plugin":"p"}`},
		{Name: "ui panes", Line: `{"type":"system","subtype":"ui_panes","plugin":"p"}`},
		{Name: "ui scroll", Line: `{"type":"system","subtype":"ui_scroll","plugin":"p"}`},
		{Name: "ui focus", Line: `{"type":"system","subtype":"ui_focus","plugin":"p"}`},

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
