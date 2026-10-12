package letta

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// Letta Code 0.34 made AskUserQuestion asynchronous. The tool belongs to no
// built-in toolset any more: a client that wants it names it in
// `client_toolset.include` on each `create_message` input, and the server
// answers "Tool not found: AskUserQuestion" otherwise. The tool posts its
// questions and returns a receipt at once. No `control_request` frame carries
// the question, and no tool call waits for the answer. The answer arrives
// later, as an ordinary user message that holds a `<task-notification>`.

// The three frames below are verbatim from a live `letta server --listen` run of
// Letta Code 0.34.2 in Standard mode: the tool start, the empty tool end, and
// the receipt.
const (
	lettaLiveQuestionStart  = `{"type":"stream_delta","delta":{"id":"letta-msg-1","date":"2026-10-04T21:28:36.123Z","message_type":"client_tool_start","run_id":"local-run-1","tool_call_id":"ask-1","tool_name":"AskUserQuestion","tool_args":"{\"questions\":[{\"question\":\"Which color do you prefer?\",\"header\":\"Color\",\"options\":[{\"label\":\"Blue\",\"description\":\"The color blue\"},{\"label\":\"Red\",\"description\":\"The color red\"}]}]}"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":18,"emitted_at":"2026-10-04T21:28:36.123Z","idempotency_key":"stream_delta:18:6497e878-f08a-4384-98d0-8a241a3bf654"}`
	lettaLiveQuestionEnd    = `{"type":"stream_delta","delta":{"id":"letta-msg-1","date":"2026-10-04T21:28:36.125Z","message_type":"client_tool_end","run_id":"local-run-1","tool_call_id":"ask-1","status":"success"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":19,"emitted_at":"2026-10-04T21:28:36.125Z","idempotency_key":"stream_delta:19:6873b077-fc09-4445-90b4-98aa80e29c2e"}`
	lettaLiveQuestionReturn = `{"type":"stream_delta","delta":{"type":"message","message_type":"tool_return_message","id":"synthetic-tool-return-106c4f93-c4ce-40b8-a64d-c7acf4988a83","date":"2026-10-04T21:28:36.125Z","run_id":"local-run-1","status":"success","tool_call_id":"ask-1","tool_return":"{\"type\":\"ask_user_question\",\"version\":2,\"toolCallId\":\"ask-1\",\"questions\":[{\"question\":\"Which color do you prefer?\",\"header\":\"Color\",\"options\":[{\"label\":\"Blue\",\"description\":\"The color blue\"},{\"label\":\"Red\",\"description\":\"The color red\"}]}],\"message\":\"Questions posted. Answers or dismissal will arrive later in a task notification. You may continue working; do not assume an answer.\"}","tool_returns":[{"tool_call_id":"ask-1","status":"success","tool_return":"{\"type\":\"ask_user_question\",\"version\":2,\"toolCallId\":\"ask-1\",\"questions\":[{\"question\":\"Which color do you prefer?\",\"header\":\"Color\",\"options\":[{\"label\":\"Blue\",\"description\":\"The color blue\"},{\"label\":\"Red\",\"description\":\"The color red\"}]}],\"message\":\"Questions posted. Answers or dismissal will arrive later in a task notification. You may continue working; do not assume an answer.\"}"}]},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":20,"emitted_at":"2026-10-04T21:28:36.125Z","idempotency_key":"stream_delta:20:64e4c395-0e70-4499-ae63-04b096756fc9"}`
)

// lettaColorQuestions is the question list of the frames above.
const lettaColorQuestions = `[{"question":"Which color do you prefer?","header":"Color","options":[{"label":"Blue","description":"The color blue"},{"label":"Red","description":"The color red"}]}]`

// lettaEscapedColorQuestions is lettaColorQuestions inside a JSON string.
const lettaEscapedColorQuestions = `[{\"question\":\"Which color do you prefer?\",\"header\":\"Color\",\"options\":[{\"label\":\"Blue\",\"description\":\"The color blue\"},{\"label\":\"Red\",\"description\":\"The color red\"}]}]`

// questionAgent returns an agent of the conversation of the frames above, with a
// sink that records each published control request.
func questionAgent(t *testing.T) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	return a, sink
}

// receiptFrames is the frame sequence of one accepted question call.
func receiptFrames() []string {
	return []string{lettaLiveQuestionStart, lettaLiveQuestionEnd, lettaLiveQuestionReturn}
}

// questionRequest runs the receipt frames and returns the one control request
// that they publish.
func questionRequest(t *testing.T) agenttest.ControlRequestRecord {
	t.Helper()
	a, sink := questionAgent(t)
	for _, frame := range receiptFrames() {
		a.HandleOutput([]byte(frame))
	}
	published := sink.PublishedControls()
	require.Len(t, published, 1, "the receipt posts one question request")
	return published[0]
}

// questionAnswer is the neutral envelope that the browser sends for a question.
// An answer folds the answers into the input of the question call, as the shared
// question control does for every provider.
func questionAnswer(t *testing.T, requestID, behavior string, answers map[string]string) []byte {
	t.Helper()
	inner := map[string]any{"behavior": behavior}
	if answers != nil {
		var questions any
		require.NoError(t, json.Unmarshal([]byte(lettaColorQuestions), &questions))
		inner["updatedInput"] = map[string]any{"questions": questions, "answers": answers}
	}
	raw, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": inner}})
	require.NoError(t, err)
	return raw
}

// resolveQuestion resolves one browser answer against the request that the
// receipt published.
func resolveQuestion(t *testing.T, request agenttest.ControlRequestRecord, behavior string, answers map[string]string) agent.ControlResponseResolution {
	t.Helper()
	return lettaProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       request.RequestID,
		RequestPayload:  request.Payload,
		ResponseContent: questionAnswer(t, request.RequestID, behavior, answers),
	})
}

// openedAgent returns an agent that is connected to a fake server, with the
// runtime identity of the frames above.
func openedAgent(t *testing.T) (*fakeAppServer, *Agent) {
	t.Helper()
	return openedAgentWithSink(t, &agenttest.Sink{})
}

// openedAgentWithSink is openedAgent for a test that reads the rows that the
// agent stores.
func openedAgentWithSink(t *testing.T, sink *agenttest.Sink) (*fakeAppServer, *Agent) {
	t.Helper()
	fake, a := newFakeAppServerWithSink(t, sink)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()
	return fake, a
}

// inputPayload returns the payload of the next input command.
func inputPayload(t *testing.T, fake *fakeAppServer) map[string]any {
	t.Helper()
	command := fake.nextCommand(t)
	require.Equal(t, "input", command["type"])
	payload, ok := command["payload"].(map[string]any)
	require.True(t, ok, "the input carries a payload")
	return payload
}

func TestEveryUserMessageOptsIntoTheQuestionTool(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)

	require.NoError(t, a.SendInput("Ask me a question.", nil))

	payload := inputPayload(t, fake)
	assert.Equal(t, "create_message", payload["kind"])
	assert.Equal(t, map[string]any{"include": []any{"AskUserQuestion"}}, payload["client_toolset"],
		"no built-in toolset holds AskUserQuestion since Letta Code 0.34, so each message asks for it")
}

// sentClientMessageID returns the `client_message_id` of the one message that an
// input payload carries, or "" when the message states none.
func sentClientMessageID(t *testing.T, payload map[string]any) string {
	t.Helper()
	messages, ok := payload["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok)
	id, _ := message["client_message_id"].(string)
	return id
}

// The App Server gives the echo of a queued message the `client_message_id` of
// that message as its `otid` (see TestEchoOfAQueuedMessageTheReaderSentIsNotARow).
// The echo therefore names the message that it repeats only when the worker
// stated an id on the way in, and each message needs an id of its own: the App
// Server drops an input whose id it accepted already.
func TestEveryUserMessageStatesItsOwnClientMessageID(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)

	require.NoError(t, a.SendInput("First message.", nil))
	first := sentClientMessageID(t, inputPayload(t, fake))
	// The turn ends, so the agent takes the next message.
	a.HandleOutput([]byte(`{"type":"update_loop_status","loop_status":{"status":"WAITING_ON_INPUT","active_run_ids":[],"executing_tool_call_ids":[]},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"}}`))
	require.NoError(t, a.SendInput("Second message.", nil))
	second := sentClientMessageID(t, inputPayload(t, fake))

	assert.NotEmpty(t, first, "the first message states no client_message_id")
	assert.NotEmpty(t, second, "the second message states no client_message_id")
	assert.NotEqual(t, first, second, "two messages share one client_message_id, and the App Server drops the second as a repeat")
}

func TestQuestionReceiptPostsAQuestionRequest(t *testing.T) {
	t.Parallel()
	request := questionRequest(t)

	var published struct {
		Type       string `json:"type"`
		RequestID  string `json:"requestId"`
		ToolName   string `json:"tool_name"`
		ToolCallID string `json:"tool_call_id"`
		ToolInput  struct {
			Questions json.RawMessage `json:"questions"`
		} `json:"tool_input"`
	}
	require.NoError(t, json.Unmarshal(request.Payload, &published))
	assert.Equal(t, "ask_user", published.Type)
	assert.NotEmpty(t, published.RequestID)
	assert.Equal(t, request.RequestID, published.RequestID)
	assert.Equal(t, "AskUserQuestion", published.ToolName)
	assert.Equal(t, "ask-1", published.ToolCallID)
	assert.JSONEq(t, lettaColorQuestions, string(published.ToolInput.Questions), "the request asks the questions that the receipt posted")
	assert.Equal(t, "local-conv-1", request.AgentSessionID)
}

func TestQuestionReceiptPostsOneRequestForRepeatedFrames(t *testing.T) {
	t.Parallel()
	a, sink := questionAgent(t)
	for _, frame := range receiptFrames() {
		a.HandleOutput([]byte(frame))
	}
	// The server can send the composite result twice. The call closed on the first.
	a.HandleOutput([]byte(lettaLiveQuestionReturn))

	assert.Len(t, sink.PublishedControls(), 1)
}

func TestQuestionReceiptOfAnotherToolPostsNothing(t *testing.T) {
	t.Parallel()
	a, sink := questionAgent(t)
	// The same receipt bytes that a Bash call returns are command output, not a question.
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"client_tool_start","run_id":"local-run-1","tool_call_id":"ask-1","tool_name":"Bash","tool_args":"{\"command\":\"cat receipt.json\"}"}}`))
	a.HandleOutput([]byte(lettaLiveQuestionReturn))

	assert.Empty(t, sink.PublishedControls())
}

func TestQuestionReceiptOfAChildPostsNothing(t *testing.T) {
	t.Parallel()
	a, sink := questionAgent(t)
	// A subagent runs headless: no client answers its questions.
	a.HandleOutput(lettaChildStateFrame(t, "running"))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{"message_type": "client_tool_start", "run_id": "local-run-9", "tool_call_id": "ask-1", "tool_name": "AskUserQuestion", "tool_args": "{}"}))
	a.HandleOutput(lettaChildDeltaFrame(t, map[string]any{
		"message_type": "tool_return_message", "run_id": "local-run-9", "status": "success", "tool_call_id": "ask-1",
		"tool_return": `{"type":"ask_user_question","version":2,"toolCallId":"ask-1","questions":` + lettaColorQuestions + `}`,
	}))

	assert.Empty(t, sink.PublishedControls())
}

func TestQuestionReturnThatIsNotAReceiptPostsNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"another call":      `{\"type\":\"ask_user_question\",\"version\":2,\"toolCallId\":\"ask-2\",\"questions\":` + lettaEscapedColorQuestions + `}`,
		"another version":   `{\"type\":\"ask_user_question\",\"version\":1,\"toolCallId\":\"ask-1\",\"questions\":` + lettaEscapedColorQuestions + `}`,
		"another type":      `{\"type\":\"ask_user_question_response\",\"version\":2,\"toolCallId\":\"ask-1\",\"questions\":` + lettaEscapedColorQuestions + `}`,
		"no questions":      `{\"type\":\"ask_user_question\",\"version\":2,\"toolCallId\":\"ask-1\",\"questions\":[]}`,
		"a blank question":  `{\"type\":\"ask_user_question\",\"version\":2,\"toolCallId\":\"ask-1\",\"questions\":[{\"question\":\" \",\"header\":\"H\",\"options\":[]}]}`,
		"a repeated prompt": `{\"type\":\"ask_user_question\",\"version\":2,\"toolCallId\":\"ask-1\",\"questions\":[{\"question\":\"Same?\"},{\"question\":\"Same?\"}]}`,
		"plain text":        `Questions posted.`,
	}
	for name, toolReturn := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := questionAgent(t)
			a.HandleOutput([]byte(lettaLiveQuestionStart))
			a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"tool_return_message","run_id":"local-run-1","status":"success","tool_call_id":"ask-1","tool_return":"` + toolReturn + `"}}`))

			assert.Empty(t, sink.PublishedControls())
		})
	}
}

func TestFailedQuestionCallPostsNothing(t *testing.T) {
	t.Parallel()
	a, sink := questionAgent(t)
	a.HandleOutput([]byte(lettaLiveQuestionStart))
	// The native text of a call that Letta Code 0.34.2 refused.
	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"tool_return_message","run_id":"local-run-1","status":"error","tool_call_id":"ask-1","tool_return":"Tool not found: AskUserQuestion. Available tools: Bash, Read"}}`))

	assert.Empty(t, sink.PublishedControls())
}

func TestQuestionAnswerBecomesTheNativeResponse(t *testing.T) {
	t.Parallel()
	request := questionRequest(t)

	resolution := resolveQuestion(t, request, "allow", map[string]string{"Which color do you prefer?": "Red"})

	require.NoError(t, resolution.Refusal())
	assert.JSONEq(t, `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":`+lettaColorQuestions+`,"status":"answered","answers":{"Which color do you prefer?":"Red"}}`, string(resolution.Content))
}

func TestQuestionRejectionBecomesTheNativeDismissal(t *testing.T) {
	t.Parallel()
	request := questionRequest(t)

	resolution := resolveQuestion(t, request, "deny", nil)

	require.NoError(t, resolution.Refusal())
	assert.JSONEq(t, `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":`+lettaColorQuestions+`,"status":"dismissed"}`, string(resolution.Content))
}

func TestQuestionAnswerThatLettaWouldRefuseIsWithheld(t *testing.T) {
	t.Parallel()
	request := questionRequest(t)
	// Letta Code accepts an answered response only when it answers every
	// question, by the exact text of the question, with nonempty text.
	for name, answers := range map[string]map[string]string{
		"no answer":       nil,
		"a blank answer":  {"Which color do you prefer?": "  "},
		"another prompt":  {"Which shape?": "Round"},
		"an extra prompt": {"Which color do you prefer?": "Red", "Which shape?": "Round"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := resolveQuestion(t, request, "allow", answers)

			assert.True(t, resolution.Withhold)
			assert.Error(t, resolution.Refusal())
		})
	}
}

func TestQuestionResponseTravelsAsATaskNotification(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)

	// The text holds the three characters that Letta Code escapes in XML, and quotes that it keeps.
	response := `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":[{"question":"Use <b> & \"x\"?","header":"Tag","options":[{"label":"Yes","description":"Use it"},{"label":"No","description":"Skip it"}]}],"status":"answered","answers":{"Use <b> & \"x\"?":"Yes"}}`
	require.NoError(t, a.SendRawInput([]byte(response), agent.StopContext{}))

	command := fake.nextCommand(t)
	require.Equal(t, "input", command["type"])
	assert.Equal(t, map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"}, command["runtime"])
	payload, ok := command["payload"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "create_message", payload["kind"])
	assert.Equal(t, map[string]any{"include": []any{"AskUserQuestion"}}, payload["client_toolset"])
	// This is exactly what `prepareAskUserQuestionNotif` of Letta Code 0.34.2 writes.
	want := "<task-notification>\n<task-id>ask-1</task-id>\n<summary>User answered your questions.</summary>\n" +
		`<ask-user-question-response>{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":[{"question":"Use &lt;b&gt; &amp; \"x\"?","header":"Tag","options":[{"label":"Yes","description":"Use it"},{"label":"No","description":"Skip it"}]}],"status":"answered","answers":{"Use &lt;b&gt; &amp; \"x\"?":"Yes"}}</ask-user-question-response>` +
		"\n</task-notification>"
	// The answer is a message too, so it states the id that the worker knows its echo by.
	id := sentClientMessageID(t, payload)
	assert.True(t, isLeapMuxClientMessageID(id), "the answer states a LeapMux id: %q", id)
	assert.Equal(t, []any{map[string]any{"role": "user", "client_message_id": id, "content": []any{map[string]any{"type": "text", "text": want}}}}, payload["messages"])
}

func TestQuestionDismissalTravelsAsATaskNotification(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)

	require.NoError(t, a.SendRawInput([]byte(`{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":`+lettaColorQuestions+`,"status":"dismissed"}`), agent.StopContext{}))

	payload := inputPayload(t, fake)
	messages, ok := payload["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	content, ok := messages[0].(map[string]any)["content"].([]any)
	require.True(t, ok)
	text := content[0].(map[string]any)["text"]
	assert.Equal(t, "<task-notification>\n<task-id>ask-1</task-id>\n<summary>User dismissed your questions.</summary>\n"+
		`<ask-user-question-response>{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":`+lettaColorQuestions+`,"status":"dismissed"}</ask-user-question-response>`+
		"\n</task-notification>", text)
}

func TestQuestionResponseThatLettaWouldRefuseIsNotSent(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)

	for name, response := range map[string]string{
		"answered with no answers": `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":` + lettaColorQuestions + `,"status":"answered"}`,
		"dismissed with answers":   `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":` + lettaColorQuestions + `,"status":"dismissed","answers":{"Which color do you prefer?":"Red"}}`,
		"another status":           `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":` + lettaColorQuestions + `,"status":"postponed"}`,
		"another version":          `{"type":"ask_user_question_response","version":1,"toolCallId":"ask-1","questions":` + lettaColorQuestions + `,"status":"dismissed"}`,
		"no call":                  `{"type":"ask_user_question_response","version":2,"questions":` + lettaColorQuestions + `,"status":"dismissed"}`,
	} {
		assert.Error(t, a.SendRawInput([]byte(response), agent.StopContext{}), name)
	}
	select {
	case command := <-fake.commands:
		t.Fatalf("a refused response still wrote a command: %v", command)
	default:
	}
}

// A `can_use_tool` request asks whether the tool may RUN. In Strict mode Letta
// Code 0.34.2 still sends one for AskUserQuestion, and the questions follow as
// a receipt only after the reader allows the call. The request is an ordinary
// permission: it carries no answer.
func TestQuestionToolPermissionIsAPermission(t *testing.T) {
	t.Parallel()
	a, sink := questionAgent(t)

	a.HandleOutput([]byte(lettaLiveControlRequest))

	published := sink.PublishedControls()
	require.Len(t, published, 1)
	var payload struct {
		Type     string `json:"type"`
		ToolName string `json:"tool_name"`
	}
	require.NoError(t, json.Unmarshal(published[0].Payload, &payload))
	assert.Equal(t, "permission", payload.Type)
	assert.Equal(t, "AskUserQuestion", payload.ToolName)
}

// A running turn queues the answer, and Letta Code echoes the queued message as a
// `user_message` when it starts. This is a verbatim frame of Letta Code 0.34.2 from
// before the worker stated a message id, so its `otid` is random: it stands for
// an answer that another sender wrote. TestQueuedQuestionAnswerEchoIsNotARow sets
// the `otid` to the id of an answer that LeapMux sent.
const lettaLiveQueuedAnswerEcho = `{"type":"stream_delta","delta":{"type":"message","id":"user-msg-672d25e1-1deb-4e8e-808d-962d54007e3f","date":"2026-10-04T21:59:12.431Z","message_type":"user_message","content":[{"type":"text","text":"<task-notification>\n<task-id>ask-1</task-id>\n<summary>User answered your questions.</summary>\n<ask-user-question-response>{\"type\":\"ask_user_question_response\",\"version\":2,\"toolCallId\":\"ask-1\",\"questions\":[{\"question\":\"Which color do you prefer?\",\"header\":\"Color\",\"options\":[{\"label\":\"Blue\",\"description\":\"The color blue\"},{\"label\":\"Red\",\"description\":\"The color red\"}]}],\"status\":\"answered\",\"answers\":{\"Which color do you prefer?\":\"Red\"}}</ask-user-question-response>\n</task-notification>"}],"otid":"aa72b91d-0de8-4bde-bada-4d070e2a13f1"},"runtime":{"agent_id":"agent-local-1","conversation_id":"local-conv-1"},"event_seq":35,"emitted_at":"2026-10-04T21:59:12.431Z","idempotency_key":"stream_delta:35:308b7153-f5ea-4bb8-b211-4e7f61cf96c4"}`

// lettaLiveQueuedAnswerEchoOtid is the random `otid` of lettaLiveQueuedAnswerEcho.
const lettaLiveQueuedAnswerEchoOtid = "aa72b91d-0de8-4bde-bada-4d070e2a13f1"

// sentMessageText returns the text of the one message that an input payload carries.
func sentMessageText(t *testing.T, payload map[string]any) string {
	t.Helper()
	messages, ok := payload["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	content, ok := messages[0].(map[string]any)["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}

// queuedEchoFrame returns the `user_message` echo that Letta Code writes when it
// dequeues a message of this text and this client message id.
func queuedEchoFrame(t *testing.T, text, otid string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"type": "stream_delta",
		"delta": map[string]any{
			"type": "message", "id": "user-msg-1", "message_type": "user_message",
			"content": []any{map[string]any{"type": "text", "text": text}},
			"otid":    otid,
		},
		"runtime": map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
	})
	require.NoError(t, err)
	return frame
}

// The echo of the answer that LeapMux sent is no row: the answer is already a
// control response row. The echo states the id of the answer, so the echo of the
// verbatim frame is skipped by that id and by nothing else.
func TestQueuedQuestionAnswerEchoIsNotARow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	fake, a := openedAgentWithSink(t, sink)

	require.NoError(t, a.SendRawInput([]byte(`{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":`+lettaColorQuestions+`,"status":"answered","answers":{"Which color do you prefer?":"Red"}}`), agent.StopContext{}))
	payload := inputPayload(t, fake)
	id := sentClientMessageID(t, payload)
	require.True(t, isLeapMuxClientMessageID(id), "the answer states a LeapMux id: %q", id)

	// The verbatim frame holds the very text that LeapMux sent, with the id of the answer.
	echo := strings.Replace(lettaLiveQueuedAnswerEcho, lettaLiveQueuedAnswerEchoOtid, id, 1)
	require.Contains(t, echo, id)
	var frame struct {
		Delta struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"delta"`
	}
	require.NoError(t, json.Unmarshal([]byte(echo), &frame))
	require.Equal(t, sentMessageText(t, payload), frame.Delta.Content[0].Text, "the verbatim echo repeats the text that LeapMux sent")

	a.HandleOutput([]byte(echo))
	assert.Empty(t, sink.Messages(), "the echo of a question answer is not a transcript row")

	a.HandleOutput([]byte(`{"type":"stream_delta","delta":{"message_type":"user_message","content":[{"type":"text","text":"Please continue."}]}}`))
	assert.Len(t, sink.Messages(), 1, "a user message of any other text stays a row")
}

// Letta Code echoes each message of a queued batch with the `otid` of that message
// (emitDequeuedUserMessage runs for each message of the merged turn), so the id
// of the message is the only thing that the worker needs. This test sends each
// kind of answer that LeapMux writes and echoes it with the text that was sent.
func TestEchoOfEveryAnswerLeapMuxSendsIsNoRow(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"answered":  `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":` + lettaColorQuestions + `,"status":"answered","answers":{"Which color do you prefer?":"Red"}}`,
		"dismissed": `{"type":"ask_user_question_response","version":2,"toolCallId":"ask-2","questions":` + lettaColorQuestions + `,"status":"dismissed"}`,
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			fake, a := openedAgentWithSink(t, sink)

			require.NoError(t, a.SendRawInput([]byte(response), agent.StopContext{}))
			payload := inputPayload(t, fake)
			id := sentClientMessageID(t, payload)
			text := sentMessageText(t, payload)
			require.True(t, strings.HasPrefix(text, lettaNotificationOpen), "the answer travels as a task notification")

			a.HandleOutput(queuedEchoFrame(t, text, id))

			assert.Empty(t, sink.Messages(), "the echo of the answer is no row")
		})
	}
}

// An answer that another sender wrote is not an answer that LeapMux stored. Its
// echo states an id of Letta Code's own, so the reader sees it as a message.
func TestEchoOfAnAnswerFromAnotherSenderStaysARow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	_, a := openedAgentWithSink(t, sink)

	a.HandleOutput([]byte(lettaLiveQueuedAnswerEcho))

	assert.Len(t, sink.Messages(), 1, "an answer that LeapMux did not send is a row")
}

// The answer to a question is a message too. Its echo names the id of the
// answer, so the identity skips it whatever its text is.
func TestEchoOfAQuestionAnswerStatesTheIDOfTheAnswer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	fake, a := openedAgentWithSink(t, sink)

	require.NoError(t, a.SendRawInput([]byte(`{"type":"ask_user_question_response","version":2,"toolCallId":"ask-1","questions":`+lettaColorQuestions+`,"status":"dismissed"}`), agent.StopContext{}))
	id := sentClientMessageID(t, inputPayload(t, fake))
	require.True(t, isLeapMuxClientMessageID(id), "the answer states a LeapMux id: %q", id)

	a.HandleOutput([]byte(fmt.Sprintf(lettaLiveQueuedMessageEcho, id)))

	assert.Empty(t, sink.Messages(), "the echo of the answer is no row, although its text is no task notification")
}

func TestQuestionDismissalCarriesTheTypedReasonAsFeedback(t *testing.T) {
	t.Parallel()
	request := questionRequest(t)
	cases := []struct {
		name         string
		behavior     string
		answers      map[string]string
		message      string
		wantFeedback string
	}{
		{"a dismissal with a reason", "deny", nil, "Ask me again tomorrow.", "Ask me again tomorrow."},
		{"a dismissal with the placeholder reason", "deny", nil, agent.ControlRejectedByUserMessage, ""},
		{"a dismissal with no reason", "deny", nil, "", ""},
		{"an answer", "allow", map[string]string{"Which color do you prefer?": "Red"}, "Thanks.", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := map[string]any{"behavior": tc.behavior, "message": tc.message}
			if tc.answers != nil {
				inner["updatedInput"] = map[string]any{"answers": tc.answers}
			}
			content, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": request.RequestID, "response": inner}})
			require.NoError(t, err)

			resolution := lettaProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestID: request.RequestID, RequestPayload: request.Payload, ResponseContent: content,
			})

			require.NoError(t, resolution.Refusal())
			assert.Equal(t, tc.wantFeedback, resolution.Feedback, "Letta Code reads a dismissal without a reason, so the reason follows as a message")
		})
	}
}

func TestQuestionRequestThatLacksAQuestionIsWithheld(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string]string{
		"no call":      `{"type":"ask_user","requestId":"letta-question-ask-1","tool_input":{"questions":` + lettaColorQuestions + `}}`,
		"no question":  `{"type":"ask_user","requestId":"letta-question-ask-1","tool_call_id":"ask-1","tool_input":{}}`,
		"an empty set": `{"type":"ask_user","requestId":"letta-question-ask-1","tool_call_id":"ask-1","tool_input":{"questions":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution := lettaProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestID:       "letta-question-ask-1",
				RequestPayload:  []byte(payload),
				ResponseContent: questionAnswer(t, "letta-question-ask-1", "deny", nil),
			})

			assert.True(t, resolution.Withhold)
			assert.Equal(t, agent.RefusalUnreadableRequest, resolution.Refusal().Error())
		})
	}
}

// A permission answer reads no tool input, so a request whose input is no object
// stays answerable. Only a question request reads its input.
func TestPermissionWithAnInputThatIsNoObjectStaysAnswerable(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{"text": `"just text"`, "a list": `["a","b"]`, "null": `null`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			payload := `{"type":"permission","requestId":"perm-1","tool_name":"Custom","tool_call_id":"call-1","tool_input":` + input + `}`
			content, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": "perm-1", "response": map[string]any{"behavior": "allow"}}})
			require.NoError(t, err)

			resolution := lettaProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestID: "perm-1", RequestPayload: []byte(payload), ResponseContent: content,
			})

			require.NoError(t, resolution.Refusal())
			assert.JSONEq(t, `{"kind":"approval_response","request_id":"perm-1","decision":{"behavior":"allow","message":""}}`, string(resolution.Content))
		})
	}
}

// Each input builds its own toolset, so a change to one input leaves the next one whole.
func TestEachInputBuildsItsOwnClientToolset(t *testing.T) {
	t.Parallel()
	first := lettaClientToolset()
	first["include"] = []string{"Bash"}
	first["base"] = "codex"

	assert.Equal(t, map[string]any{"include": []string{"AskUserQuestion"}}, lettaClientToolset())
}
