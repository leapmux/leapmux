package letta

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The App Server names each body in its OWN field, never under `payload`:
// `stream_delta` carries `delta`, `control_request` carries `request`,
// `update_loop_status` carries `loop_status` and `update_subagent_state`
// carries `subagents`. The frames below are verbatim from a live
// `letta server --listen` run. A handler that reads `payload` reads nothing and
// silently drops the frame.

const lettaLiveStreamDelta = `{"type":"stream_delta","delta":{"id":"ui-msg-2","date":"2026-09-25T21:18:53.273Z","agent_id":"agent-local-1","conversation_id":"local-conv-1","message_type":"assistant_message","content":[{"type":"text","text":"MOCK-REPLY-OK"}],"run_id":"local-run-1","seq_id":1,"type":"message"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":11,"emitted_at":"2026-09-25T21:18:53.273Z","idempotency_key":"stream_delta:11:x"}`

const lettaLiveTurnFinished = `{"type":"turn_finished","turn_id":"batch-direct-0ac84fc0","stop_reason":"end_turn","run_id":"local-run-1","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":16,"emitted_at":"2026-09-25T21:18:53.282Z","idempotency_key":"turn_finished:16:x"}`

const lettaLiveLoopStatus = `{"type":"update_loop_status","loop_status":{"status":"SENDING_API_REQUEST","active_run_ids":[],"executing_tool_call_ids":[]},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":5,"emitted_at":"2026-09-25T21:18:53.170Z","idempotency_key":"update_loop_status:5:x"}`

const lettaLiveSubagents = `{"type":"update_subagent_state","subagents":[],"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":6,"emitted_at":"2026-09-25T21:18:53.186Z","idempotency_key":"update_subagent_state:6:x"}`

const lettaLiveControlRequest = `{"type":"control_request","request_id":"perm-call_ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which color do you prefer?","header":"Color","options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]}]},"tool_call_id":"call_ask_1","permission_suggestions":[{"id":"save-default","text":"Yes, allow AskUserQuestion operations during this session"}],"blocked_path":null},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":15,"emitted_at":"2026-09-25T21:25:23.430Z","idempotency_key":"control_request:15:x"}`

// Protocol state and acknowledgements. Each `update_device_status` alone is
// about 3 KB. A transcript that stored them pushed the reader's own answer out
// of the virtualized chat, so the reader saw no answer at all.
const lettaLiveDeviceStatus = `{"type":"update_device_status","device_status":{"current_connection_id":"app-server","connection_name":"trustin-imac.local","is_online":true,"is_processing":true,"current_permission_mode":"unrestricted","current_working_directory":"/tmp/wd","git_context":null,"letta_code_version":"0.0.1"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":8,"emitted_at":"2026-09-25T21:18:53.186Z","idempotency_key":"update_device_status:8:x"}`

const lettaLiveUpdateQueue = `{"type":"update_queue","queue":[],"removed":[],"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":7,"emitted_at":"2026-09-25T21:18:53.186Z","idempotency_key":"update_queue:7:x"}`

const lettaLiveInputAccepted = `{"type":"input_accepted","request_id":"in-1","runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"accepted":true,"disposition":"started"}`

const lettaLiveListModelsResponse = `{"type":"list_models_response","request_id":"lm-1","success":true,"models":[]}`

const lettaLiveUsageStatistics = `{"type":"stream_delta","delta":{"message_type":"usage_statistics","prompt_tokens":10,"completion_tokens":2,"total_tokens":12},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":13,"emitted_at":"2026-09-25T21:18:53.280Z","idempotency_key":"stream_delta:13:x"}`

const lettaLiveStopReasonDelta = `{"type":"stream_delta","delta":{"message_type":"stop_reason","stop_reason":"end_turn"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":14,"emitted_at":"2026-09-25T21:18:53.281Z","idempotency_key":"stream_delta:14:x"}`

// The assistant text of a live stream_delta reaches the transcript when the
// turn ends, which is when the worker flushes the assembled generation.
func TestStreamDeltaCarriesItsBodyInTheDeltaField(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveStreamDelta))
	a.HandleOutput([]byte(lettaLiveTurnFinished))

	var found []string
	for _, m := range sink.Messages() {
		found = append(found, string(m.Content))
	}
	require.NotEmpty(t, found, "the turn produces a transcript row")
	assert.True(t, strings.Contains(strings.Join(found, "\n"), "MOCK-REPLY-OK"),
		"the assistant text reaches the transcript, rows: %v", found)
}

// A live update_loop_status arms the turn: its body names the status in
// `loop_status`, not `payload`.
func TestLoopStatusBodyLivesInTheLoopStatusField(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveLoopStatus))
	actives := sink.TurnActives()
	require.NotEmpty(t, actives, "the loop status moves the turn flag")
	assert.True(t, actives[len(actives)-1], "SENDING_API_REQUEST arms the turn")
}

// A live update_subagent_state stores a readable notification row. Reading its
// body from `payload` stores an EMPTY row, which the hub's notification
// consolidation then rejects with "unexpected end of JSON input".
func TestSubagentStateStoresAReadableNotification(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveSubagents))

	rows := sink.PersistedNotifications()
	require.NotEmpty(t, rows, "the frame produces a notification row")
	for _, row := range rows {
		assert.NotEmpty(t, row.Content, "the notification row carries the frame body")
	}
}

// A live control_request opens a question control. Its discriminator is
// `request.subtype`, its body sits in `request`, and the id sits at the frame
// root.
func TestControlRequestBodyLivesInTheRequestField(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.Mu.Lock()
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	a.HandleOutput([]byte(lettaLiveControlRequest))

	a.Mu.Lock()
	pending := len(a.controls)
	kind := lettaControlKind("")
	for _, c := range a.controls {
		kind = c.kind
	}
	a.Mu.Unlock()
	assert.Equal(t, 1, pending, "the request is held as pending")
	assert.Equal(t, lettaControlAskUser, kind, "AskUserQuestion is a question, not a permission")
}

// The published control payload spells its tool fields under the CONTRACT
// names, the ones the browser plugin reads. A payload that spelled them
// `toolName`/`toolCallId`/`input` drew a banner titled "Tool" with no command,
// and a question whose options never appeared.
func TestControlPayloadUsesTheContractFieldNames(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.Mu.Lock()
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	a.HandleOutput([]byte(lettaLiveControlRequest))

	requests := sink.PublishedControls()
	require.NotEmpty(t, requests, "the control request is published")
	var published map[string]any
	require.NoError(t, json.Unmarshal(requests[0].Payload, &published))
	assert.Equal(t, "ask_user", published["type"])
	assert.Equal(t, "AskUserQuestion", published[contracts.LettaDeltaFieldToolName], "the tool name is under tool_name")
	assert.Equal(t, "call_ask_1", published[contracts.LettaDeltaFieldToolCallID], "the call id is under tool_call_id")
	assert.NotNil(t, published[contracts.LettaDeltaFieldToolInput], "the tool input is under tool_input")
	assert.NotContains(t, published, "toolName", "no camelCase twin of tool_name")
	assert.NotContains(t, published, "input", "no bare `input` twin of tool_input")
}

// Protocol state and command acknowledgements must move NOTHING. A transcript
// that stored them drew each one as a raw JSON bubble, and the noise pushed the
// reader's own answer out of the virtualized chat: the turn ran, the model
// answered, and the reader still saw no answer.
func TestProtocolStateAndAcksMoveNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	for _, frame := range []string{
		lettaLiveDeviceStatus,
		lettaLiveUpdateQueue,
		lettaLiveInputAccepted,
		lettaLiveListModelsResponse,
		lettaLiveUsageStatistics,
		lettaLiveStopReasonDelta,
	} {
		a.HandleOutput([]byte(frame))
	}
	assert.Empty(t, sink.Messages(), "no protocol state becomes a transcript row")
	assert.Empty(t, sink.PersistedNotifications(), "no protocol state becomes a notification row")
	assert.Empty(t, sink.TurnActives(), "no protocol state moves the turn flag")
}

// The turn end still reaches the transcript: it is the result divider the
// reader sees after every turn.
func TestTurnFinishedStillReachesTheTranscript(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	rows := sink.Messages()
	require.NotEmpty(t, rows, "the turn end is persisted")
	assert.True(t, rows[len(rows)-1].TurnEnd, "the row is the turn end")
}
