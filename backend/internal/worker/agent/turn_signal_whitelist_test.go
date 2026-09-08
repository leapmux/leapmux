package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The turn flag is the input queue's only dispatch guard and the client's only
// activity signal, and it LATCHES: a turn armed with nothing to end it holds
// every message the user sends -- queued, unpaused, with no visible cause --
// and runs a thinking indicator that nothing stops, until the process exits.
//
// So each provider NAMES the frames that move that flag, and everything else is
// inert. The vendors own these vocabularies and extend them between releases, so
// the rule that matters is the one about frames this build has never seen: a
// message added after it must move nothing. That is what these tables state, per
// provider, against the vocabulary each vendor ships today plus a message that
// does not exist yet.
//
// A frame that starts moving the flag is a deliberate act. It shows up here as a
// changed table entry, not as a wedged tab.

// turnFrameCase is one output frame and whether it may move the turn flag.
type turnFrameCase struct {
	name string
	line string
	// moves is true for a NAMED turn signal of this provider. Every other frame
	// must publish nothing at all.
	moves bool
}

// assertTurnFrames feeds each frame to a FRESH agent, so no case can be answered
// by state another one left.
func assertTurnFrames(t *testing.T, cases []turnFrameCase, feed func(t *testing.T, tc turnFrameCase) []bool) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			published := feed(t, tc)
			if tc.moves {
				assert.NotEmpty(t, published, "a named turn signal must move the flag")
				return
			}
			assert.Empty(t, published, "only a named turn signal may move the flag")
		})
	}
}

func TestTurnSignalWhitelist_Claude(t *testing.T) {
	t.Parallel()

	// Claude Code's `system` type carries four unrelated families under 49
	// subtypes, and only the thinking-token telemetry reports the root's own
	// work. session_state_changed is the CLI's own turn state and outranks all
	// of it (see observeTurnFromOutput).
	assertTurnFrames(t, []turnFrameCase{
		// The named signals.
		{name: "session state running", line: `{"type":"system","subtype":"session_state_changed","state":"running"}`, moves: true},
		{name: "session state requires_action", line: `{"type":"system","subtype":"session_state_changed","state":"requires_action"}`, moves: true},
		{name: "session state idle", line: `{"type":"system","subtype":"session_state_changed","state":"idle"}`, moves: true},
		{name: "thinking tokens", line: `{"type":"system","subtype":"thinking_tokens","estimated_tokens":120}`, moves: true},
		{name: "root assistant text", line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`, moves: true},
		{name: "root user echo", line: `{"type":"user","message":{"role":"user","content":"hi"}}`, moves: true},
		{name: "result", line: `{"type":"result","subtype":"success"}`, moves: true},

		// A session, not a turn.
		{name: "init", line: `{"type":"system","subtype":"init","session_id":"s-1"}`},
		{name: "session state unknown", line: `{"type":"system","subtype":"session_state_changed","state":"quiescing"}`},

		// Hooks. These fire on SessionStart, SessionEnd, ConfigChange,
		// FileChanged and TeammateIdle, none of which is a turn.
		{name: "hook started", line: `{"type":"system","subtype":"hook_started","hook_event":"SessionStart","hook_id":"h1"}`},
		{name: "hook progress", line: `{"type":"system","subtype":"hook_progress","hook_event":"SessionStart","hook_id":"h1"}`},
		{name: "hook response", line: `{"type":"system","subtype":"hook_response","hook_event":"SessionStart","hook_id":"h1","exit_code":0}`},

		// A background task outlives the turn that spawned it.
		{name: "task started", line: `{"type":"system","subtype":"task_started","task_id":"t-1","task_type":"local_bash","description":"npm test"}`},
		{name: "task progress", line: `{"type":"system","subtype":"task_progress","task_id":"t-1","description":"npm test"}`},
		{name: "task notification", line: `{"type":"system","subtype":"task_notification","task_id":"t-1","status":"completed"}`},
		{name: "task updated", line: `{"type":"system","subtype":"task_updated","task_id":"t-1","patch":{"status":"completed"}}`},
		{name: "background tasks changed", line: `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`},

		// Notifications: an event, not a live turn.
		{name: "status compacting", line: `{"type":"system","subtype":"status","status":"compacting"}`},
		{name: "status clear", line: `{"type":"system","subtype":"status","status":""}`},
		{name: "permission mode status", line: `{"type":"system","subtype":"status","status":null,"permissionMode":"plan"}`},
		{name: "api retry", line: `{"type":"system","subtype":"api_retry","attempt":2}`},
		{name: "compact boundary", line: `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto"}}`},
		{name: "microcompact boundary", line: `{"type":"system","subtype":"microcompact_boundary"}`},

		// Emitted after the turn its `result` already ended.
		{name: "turn duration", line: `{"type":"system","subtype":"turn_duration","durationMs":1200}`},
		{name: "post turn summary", line: `{"type":"system","subtype":"post_turn_summary","status_category":"done"}`},
		{name: "files persisted", line: `{"type":"system","subtype":"files_persisted","files":[]}`},

		// Bookkeeping the CLI reports whenever it changes.
		{name: "commands changed", line: `{"type":"system","subtype":"commands_changed"}`},
		{name: "vcs state changed", line: `{"type":"system","subtype":"vcs_state_changed"}`},
		{name: "elicitation complete", line: `{"type":"system","subtype":"elicitation_complete","elicitation_id":"e1"}`},
		{name: "scheduled task fire", line: `{"type":"system","subtype":"scheduled_task_fire"}`},
		{name: "memory saved", line: `{"type":"system","subtype":"memory_saved"}`},

		// This Worker's own traffic.
		{name: "control request", line: `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool"}}`},
		{name: "control response", line: `{"type":"control_response","response":{"request_id":"r1","subtype":"success"}}`},
		{name: "tool progress", line: `{"type":"tool_progress","parent_tool_use_id":"t1","heartbeat":true}`},

		// A child's frame says nothing about the root. The child's own session
		// state is the sharpest case: it reports a turn in the same words the
		// root uses, and the root may be idle while a restarted child runs on.
		{name: "subagent assistant text", line: `{"type":"assistant","parent_tool_use_id":"p1","message":{"role":"assistant","content":[{"type":"text","text":"child"}]}}`},
		{name: "subagent result", line: `{"type":"result","parent_tool_use_id":"p1","subtype":"success"}`},
		{name: "subagent session state running", line: `{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"running"}`},
		{name: "subagent session state idle", line: `{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"idle"}`},

		// The releases this build will not see.
		{name: "a subtype from a later release", line: `{"type":"system","subtype":"some_future_event","data":1}`},
		{name: "a type from a later release", line: `{"type":"some_future_frame","data":1}`},
	}, func(t *testing.T, tc turnFrameCase) []bool {
		sink := &testSink{}
		a, _ := newClaudeAgentWithStdin(sink)
		a.HandleOutput([]byte(tc.line))
		return sink.TurnActives()
	})
}

func TestTurnSignalWhitelist_Codex(t *testing.T) {
	t.Parallel()

	// Codex states both edges itself, so every other notification of its ~90 is
	// inert -- including the hook and item bookends, whose names read like turn
	// boundaries and are not.
	assertTurnFrames(t, []turnFrameCase{
		{name: "turn started", line: `{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-1"}}}`, moves: true},
		{name: "turn completed", line: `{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`, moves: true},

		{name: "item started", line: `{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"main-thread","item":{"id":"i1","itemType":"agentMessage"}}}`},
		{name: "item completed", line: `{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"main-thread","item":{"id":"i1","itemType":"agentMessage","text":"hi"}}}`},
		{name: "agent message delta", line: `{"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"i1","delta":"hi"}}`},
		{name: "hook started", line: `{"jsonrpc":"2.0","method":"hook/started","params":{"threadId":"main-thread"}}`},
		{name: "hook completed", line: `{"jsonrpc":"2.0","method":"hook/completed","params":{"threadId":"main-thread"}}`},
		{name: "turn diff updated", line: `{"jsonrpc":"2.0","method":"turn/diff/updated","params":{"threadId":"main-thread"}}`},
		{name: "turn plan updated", line: `{"jsonrpc":"2.0","method":"turn/plan/updated","params":{"threadId":"main-thread"}}`},
		{name: "token usage updated", line: `{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"main-thread","usage":{}}}`},
		{name: "rate limits updated", line: `{"jsonrpc":"2.0","method":"account/rateLimits/updated","params":{}}`},
		{name: "thread compacted", line: `{"jsonrpc":"2.0","method":"thread/compacted","params":{"threadId":"main-thread"}}`},
		{name: "fs changed", line: `{"jsonrpc":"2.0","method":"fs/changed","params":{}}`},
		{name: "warning", line: `{"jsonrpc":"2.0","method":"warning","params":{"message":"careful"}}`},
		{name: "error", line: `{"jsonrpc":"2.0","method":"error","params":{"message":"boom"}}`},

		{name: "a method from a later release", line: `{"jsonrpc":"2.0","method":"turn/futureSignal","params":{"threadId":"main-thread"}}`},
	}, func(t *testing.T, tc turnFrameCase) []bool {
		sink := &recordingControlSink{}
		a := newCodexAgentWithSink(sink)
		handleCodexOutput(a, parseLine([]byte(tc.line)))
		return sink.TurnActives()
	})
}

func TestTurnSignalWhitelist_Pi(t *testing.T) {
	t.Parallel()

	// Pi's run bookends are agent_start and agent_end. Its turn_start/turn_end
	// pair is the INNER loop -- one assistant response inside the run -- so the
	// two events whose names match "turn" are the two that must move nothing.
	assertTurnFrames(t, []turnFrameCase{
		{name: "agent start", line: `{"type":"agent_start"}`, moves: true},
		{name: "agent end", line: `{"type":"agent_end","willRetry":false}`, moves: true},

		{name: "turn start", line: `{"type":"turn_start"}`},
		{name: "turn end", line: `{"type":"turn_end","message":{"role":"assistant","content":[]}}`},
		{name: "message start", line: `{"type":"message_start","message":{"role":"assistant","content":[]}}`},
		{name: "message end", line: `{"type":"message_end","message":{"role":"assistant","content":[]}}`},
		{name: "tool execution start", line: `{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash"}`},
		{name: "tool execution end", line: `{"type":"tool_execution_end","toolCallId":"t1","toolName":"bash","isError":false}`},

		{name: "an event from a later release", line: `{"type":"agent_future_event"}`},
	}, func(t *testing.T, tc turnFrameCase) []bool {
		sink := &recordingControlSink{}
		a := newPiAgentWithSink(sink)
		handlePiOutput(a, parseLine([]byte(tc.line)))
		return sink.TurnActives()
	})
}

func TestTurnSignalWhitelist_ACP(t *testing.T) {
	t.Parallel()

	// The six ACP providers share one rule, and it needs no vocabulary at all:
	// the turn is the lifetime of the session/prompt request this Worker sent,
	// so NO notification moves the flag. These are the updates that arrive with
	// no prompt in flight, which is what makes the rule load-bearing rather than
	// incidental.
	assertTurnFrames(t, []turnFrameCase{
		{name: "agent message chunk", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`},
		{name: "agent thought chunk", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hm"}}}}`},
		{name: "tool call", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"bash","status":"pending"}}}`},
		{name: "tool call update", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}}}`},
		{name: "plan", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"plan","entries":[]}}}`},
		{name: "available commands update", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"available_commands_update","availableCommands":[]}}}`},
		{name: "current mode update", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"current_mode_update","currentModeId":"ask"}}}`},
		{name: "session info update", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"session_info_update"}}}`},
		{name: "usage update", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"usage_update","usage":{}}}}`},
		{name: "user message chunk", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"}}}}`},

		{name: "an update from a later release", line: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_future_update"}}}`},
		{name: "a method from a later release", line: `{"jsonrpc":"2.0","method":"session/futureSignal","params":{"sessionId":"session-1"}}`},
	}, func(t *testing.T, tc turnFrameCase) []bool {
		b, sink := newACPTurnBase(t, nopWriteCloser{&nopWriter{}})
		b.HandleOutput([]byte(tc.line))
		return sink.TurnActives()
	})
}

// nopWriter stands in for the agent's stdin. No ACP notification writes to it,
// which is itself part of what these cases state.
type nopWriter struct{}

func (*nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// zcodeTurnFrameCases keys each case by the event type, which is what the
// completeness test compares against the contract.
func zcodeTurnFrameCases() []turnFrameCase {
	return []turnFrameCase{
		{name: contracts.ZCodeEventTurnStarted, line: `{"turnNumber":1,"input":"hi"}`, moves: true},
		{name: contracts.ZCodeEventTurnCompleted, line: `{"toolCallCount":1}`, moves: true},
		{name: contracts.ZCodeEventTurnFailed, line: `{"error":{"message":"boom","retryable":false}}`, moves: true},

		{name: contracts.ZCodeEventTurnSteerQueued, line: `{"inputId":"i1"}`},
		{name: contracts.ZCodeEventTurnSteerDrained, line: `{"inputId":"i1"}`},
		{name: contracts.ZCodeEventSessionCreated, line: `{"sessionId":"sess-1"}`},
		{name: contracts.ZCodeEventSessionResumed, line: `{"sessionId":"sess-1"}`},
		{name: contracts.ZCodeEventSessionUpdated, line: `{"sessionId":"sess-1"}`},
		{name: contracts.ZCodeEventSessionTitleUpdated, line: `{"title":"a title"}`},
		{name: contracts.ZCodeEventSessionClosed, line: `{"sessionId":"sess-1"}`},
		{name: contracts.ZCodeEventMessageUpserted, line: `{"message":{"id":"m1","role":"assistant"}}`},
		{name: contracts.ZCodeEventMessageRemoved, line: `{"messageId":"m1"}`},
		{name: contracts.ZCodeEventPartStarted, line: `{"part":{"id":"p1","type":"text"}}`},
		{name: contracts.ZCodeEventPartDelta, line: `{"partId":"p1","delta":"hi"}`},
		{name: contracts.ZCodeEventPartUpserted, line: `{"part":{"id":"p1","type":"text","text":"hi"}}`},
		{name: contracts.ZCodeEventPartRemoved, line: `{"partId":"p1"}`},
		{name: contracts.ZCodeEventModelStreaming, line: `{"streaming":true}`},
		{name: contracts.ZCodeEventToolUpdated, line: `{"kind":"progress","toolCallId":"t1"}`},
		{name: contracts.ZCodeEventPermissionRequested, line: `{"requestId":"r1","toolCallId":"t1"}`},
		{name: contracts.ZCodeEventPermissionResolved, line: `{"requestId":"r1"}`},
		{name: contracts.ZCodeEventUserInputRequested, line: `{"requestId":"r1"}`},
		{name: contracts.ZCodeEventUserInputResolved, line: `{"requestId":"r1"}`},
		{name: contracts.ZCodeEventCheckpointCreated, line: `{"checkpointId":"c1"}`},
		{name: contracts.ZCodeEventRewindTriggered, line: `{"checkpointId":"c1"}`},
		{name: contracts.ZCodeEventStreamRecoveryUpdated, line: `{"state":"recovering"}`},

		{name: "turn.futureSignal", line: `{"turnNumber":2}`},
	}
}

func TestTurnSignalWhitelist_ZCode(t *testing.T) {
	t.Parallel()

	// ZCode is the one provider whose whole event enumeration is a contract this
	// repository owns, so the table is checked for completeness against it (see
	// the test underneath). Adding an event to the contract without an entry
	// there fails that test.
	var seq atomic.Int64
	assertTurnFrames(t, zcodeTurnFrameCases(), func(t *testing.T, tc turnFrameCase) []bool {
		sink := &testSink{}
		a := newZCodeTestAgentWithStdin(t, sink, &zcodeRecordedStdin{})
		// Each case gets its own agent, so the seq only has to rise within one.
		a.HandleOutput(zcodeEventLine(t, seq.Add(1), tc.name, tc.line))
		return sink.TurnActives()
	})
}

func TestTurnSignalWhitelist_ZCodeTableCoversTheContract(t *testing.T) {
	t.Parallel()

	// contracts/zcode-protocol.json is the vendor vocabulary this repository
	// tracks. An event listed there is one the app-server can send, so the table
	// must say whether it moves the turn flag -- and a new one arrives as a
	// failure here rather than as an unclassified frame in production.
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "contracts", "zcode-protocol.json"))
	require.NoError(t, err)
	var contract struct {
		Events map[string]string `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &contract))
	require.NotEmpty(t, contract.Events, "the contract lists the event vocabulary")

	covered := make(map[string]struct{}, len(zcodeTurnFrameCases()))
	for _, tc := range zcodeTurnFrameCases() {
		covered[tc.name] = struct{}{}
	}
	for name, eventType := range contract.Events {
		assert.Contains(t, covered, eventType,
			"contract event %s (%s) has no turn-signal case", name, eventType)
	}
}
