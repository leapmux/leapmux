package ohmypi

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPromptThatFailsAfterItsAcknowledgementReleasesTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.agent.SendInput("hello", nil))
	id := r.commandsOfType(CommandPrompt)[0].ID

	r.emit(mustJSON(t, map[string]any{
		"type": "response", "id": id, "command": "prompt", "success": false,
		"error": "Agent is already processing. Use steer() or followUp() to queue messages, or wait for completion.",
	}))

	active, _ := r.sink.LastTurnActive()
	assert.False(t, active)
	notifications := r.sink.Notifications()
	require.NotEmpty(t, notifications)
	last := notifications[len(notifications)-1]
	assert.Equal(t, contracts.NotificationTypeAgentError, last[contracts.NotificationFieldType])
	assert.Contains(t, last[contracts.NotificationFieldError], "already processing")
}

func TestAPromptFailureWithNoMessageStatesAReason(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.agent.SendInput("hello", nil))
	id := r.commandsOfType(CommandPrompt)[0].ID

	r.emit(mustJSON(t, map[string]any{"type": "response", "id": id, "command": "prompt", "success": false}))

	notifications := r.sink.Notifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, "omp could not start the turn", notifications[0][contracts.NotificationFieldError],
		"the reader learns that the message never reached the model")
}

// A late failure that arrives after the stop reports nothing: the stop already
// ended the turn, and the tab no longer runs the process.
func TestAPromptFailureAfterAStopPersistsNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.SetStoppedForTest(true)
	r.emit(`{"type":"response","id":"leapmux-1","command":"prompt","success":false,"error":"late"}`)
	assert.Empty(t, r.sink.Notifications())
	assert.Empty(t, r.sink.TurnActives())
}

func TestAPromptFailureAfterTheRunStartedKeepsTheTurn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.agent.SendInput("hello", nil))
	id := r.commandsOfType(CommandPrompt)[0].ID
	r.emit(`{"type":"agent_start"}`)

	r.emit(mustJSON(t, map[string]any{"type": "response", "id": id, "command": "prompt", "success": false, "error": "late"}))

	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "a run that started owns the turn until its agent_end")
}

func TestPromptResultReleasesTheArm(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.agent.SendInput("/skill", nil))
	id := r.commandsOfType(CommandPrompt)[0].ID

	r.emit(mustJSON(t, map[string]any{"type": "prompt_result", "id": id, "agentInvoked": false}))

	active, _ := r.sink.LastTurnActive()
	assert.False(t, active)
}

func TestPromptResultForAnotherPromptLeavesTheArm(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.agent.SendInput("hello", nil))

	r.emit(`{"type":"prompt_result","id":"leapmux-999","agentInvoked":false}`)

	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "only the prompt that armed the turn releases it")
}

func TestAPromptResultThatStartsARunLeavesTheArm(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.agent.SendInput("hello", nil))
	id := r.commandsOfType(CommandPrompt)[0].ID
	published := len(r.sink.TurnActives())

	r.emit(
		mustJSON(t, map[string]any{"type": "prompt_result", "id": id, "agentInvoked": true}),
		`{"type":"prompt_result","id":7,"agentInvoked":false}`,
	)

	active, _ := r.sink.LastTurnActive()
	assert.True(t, active, "the run that the prompt starts takes the arm over")
	assert.Len(t, r.sink.TurnActives(), published, "a run that starts and a garbled result publish nothing")
}

func TestAReadyFrameWithoutALimitKeepsTheDefault(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"ready","protocolVersion":1,"supportedProtocolVersions":[1,2]}`)
	ready, err := r.agent.awaitReady(10 * time.Second)
	require.NoError(t, err)
	assert.True(t, ready.supports(2))
	assert.Equal(t, defaultMaxReassembledFrameBytes, r.agent.chunks.maxBytes())
}

// A ready frame that omp garbled still states that RPC mode runs, so the start
// goes on. It states no protocol version, so the handshake negotiates none.
func TestAGarbledReadyFrameStillEndsTheWait(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"ready","supportedProtocolVersions":"two","maxReassembledFrameBytes":"many"}`)
	require.NoError(t, r.agent.handshake(10*time.Second))
	assert.Empty(t, r.commandsOfType(CommandNegotiateProtocol))
	assert.Equal(t, defaultMaxReassembledFrameBytes, r.agent.chunks.maxBytes())
}

func TestNotificationFrames(t *testing.T) {
	t.Parallel()
	frames := []string{
		`{"type":"auto_compaction_start","reason":"threshold","action":"snapcompact"}`,
		`{"type":"auto_compaction_end","action":"snapcompact","result":{"summary":"s","tokensBefore":60030},"aborted":false,"willRetry":false}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":10,"delayMs":92.36,"errorMessage":"400"}`,
		`{"type":"auto_retry_end","success":true,"attempt":1}`,
		`{"type":"retry_fallback_applied","from":"a/b","to":"c/d","role":"default"}`,
		`{"type":"retry_fallback_succeeded","model":"c/d","role":"default"}`,
		`{"type":"notice","level":"info","message":"The current model has no service-tier control.","source":"priority"}`,
		`{"type":"extension_error","extensionPath":"/x","event":"agent_end","error":"boom"}`,
		`{"type":"command_output","text":"Current model: mock/mock-model"}`,
		`{"type":"todo_reminder","todos":[{"content":"Write code","status":"in_progress"}],"attempt":1,"maxAttempts":3}`,
		`{"type":"irc_message","message":{"role":"custom","customType":"irc","content":"hi"}}`,
		`{"type":"rpc_frame_error","error":"frame too large"}`,
	}
	r := newRig(t)
	r.emit(frames...)
	notifications := r.sink.PersistedNotifications()
	require.Len(t, notifications, len(frames))
	for i, notification := range notifications {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, notification.Source)
		assert.JSONEq(t, frames[i], string(notification.Content))
	}
	assert.Empty(t, r.sink.Messages())
}

func TestDroppedFrames(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"turn_start"}`,
		`{"type":"turn_end","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"tool_stream_update","toolCallId":"call_1","toolName":"edit","update":{}}`,
		`{"type":"available_commands_update","commands":[]}`,
		`{"type":"session_info_update","title":"x","sessionId":"y"}`,
		`{"type":"ttsr_triggered","rules":[]}`,
		`{"type":"advisor_cost_changed"}`,
		`{"type":"advisor_yielded"}`,
		`{"type":"config_warnings_changed"}`,
		`{"type":"todo_auto_clear"}`,
		`{"type":"host_tool_cancel","id":"h2","targetId":"h1"}`,
		`{"type":"extension_ui_request","id":"w","method":"setWidget","widgetKey":"autoresearch"}`,
		`{"type":"extension_ui_request","id":"s","method":"setStatus","statusKey":"k","statusText":"t"}`,
		`{"type":"extension_ui_request","id":"t","method":"setTitle","title":"t"}`,
		`{"type":"extension_ui_request","id":"e","method":"set_editor_text","text":"t"}`,
	)
	assert.Empty(t, r.sink.Messages())
	assert.Zero(t, r.sink.NotificationCount())
	assert.Empty(t, r.sink.TurnActives())
}

func TestAnUnknownFrameReachesTheTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"hologram_update","x":1}`)
	messages := r.sink.Messages()
	require.Len(t, messages, 1)
	assert.JSONEq(t, `{"type":"hologram_update","x":1}`, string(messages[0].Content))
}

func TestHostCallsAreRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"host_tool_call","id":"h1","toolCallId":"call_1","toolName":"leapmux_question","arguments":{}}`,
		`{"type":"host_uri_request","id":"u1","operation":"read","url":"db://x"}`,
	)
	toolResults := r.waitForCommand(CommandHostToolResult, 1)
	assert.Equal(t, "h1", toolResults[0].Payload["id"])
	assert.Equal(t, true, toolResults[0].Payload["isError"])
	uriResults := r.waitForCommand(CommandHostURIResult, 1)
	assert.Equal(t, "u1", uriResults[0].Payload["id"])
	assert.Equal(t, true, uriResults[0].Payload["isError"])
}

// A host call without an id cannot be answered: omp matches the answer by the
// id. The worker writes nothing for it.
func TestAHostCallWithoutAnIDIsNotAnswered(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		`{"type":"host_tool_call","toolCallId":"call_1","toolName":"leapmux_question","arguments":{}}`,
		`{"type":"host_uri_request","operation":"read","url":"db://x"}`,
		`{"type":"host_tool_call","id":7}`,
		// A call with an id. omp reads stdin in order, so once its answer is
		// recorded, an answer to a frame before it would be recorded too.
		`{"type":"host_tool_call","id":"h2","toolCallId":"call_2","toolName":"leapmux_question","arguments":{}}`,
	)
	toolResults := r.waitForCommand(CommandHostToolResult, 1)
	assert.Len(t, toolResults, 1)
	assert.Equal(t, "h2", toolResults[0].Payload["id"])
	assert.Empty(t, r.commandsOfType(CommandHostURIResult))
}
